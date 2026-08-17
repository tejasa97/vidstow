package recovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tejasa97/vidstow/internal/jobmodel"
	"github.com/tejasa97/vidstow/internal/reservationfs"
	"github.com/tejasa97/youtube_dlp/engine"
)

const (
	DefaultCleanupInterval = 5 * time.Second
	OrphanAge              = 7 * 24 * time.Hour
)

// CleanupPass reports bounded, path-free maintenance outcomes. A pass never
// treats an unsafe or indeterminate session as collected.
type CleanupPass struct {
	DurableChanges        int
	RemovedTombstones     int
	RetriedTombstones     int
	QuarantinedTombstones int
	CollectedOrphans      int
	PendingOrphans        int
	ReconciliationOrphans int
	SkippedRoots          int
}

// RunCleanupOnce retries durable cancellation tombstones and then performs a
// bounded orphan scan for every known output root. State must already have
// passed startup reconciliation; an unavailable/invalid State image is a
// hard error and causes no filesystem cleanup.
func RunCleanupOnce(ctx context.Context, stateStore StateStore) (CleanupPass, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if stateStore == nil {
		return CleanupPass{}, errors.New("recovery: nil State v2 store")
	}
	state := stateStore.Snapshot()
	if state.Version != jobmodel.StateVersion {
		return CleanupPass{}, errors.New("recovery: cleanup requires a healthy State v2 snapshot")
	}

	pass := CleanupPass{}
	tombstones := append([]jobmodel.CleanupTombstone(nil), state.Cleanup...)
	sort.Slice(tombstones, func(i, j int) bool { return tombstones[i].UpdatedAt.Before(tombstones[j].UpdatedAt) })
	for _, tombstone := range tombstones {
		if err := ctx.Err(); err != nil {
			return pass, err
		}
		if tombstone.State == jobmodel.CleanupQuarantined {
			// Quarantine is durable evidence that automatic cleanup could not
			// prove safety. Leave it untouched until an explicit recovery path.
			pass.QuarantinedTombstones++
			continue
		}
		outcome, err := cleanTombstone(ctx, tombstone)
		if err != nil {
			var update tombstoneUpdate
			if errors.As(err, &update) {
				if markErr := markTombstone(stateStore, tombstone, update.code, update.state); markErr != nil {
					return pass, fmt.Errorf("recovery: record tombstone outcome %q: %w", tombstone.JobID, markErr)
				}
				pass.DurableChanges++
				if update.state == jobmodel.CleanupQuarantined {
					pass.QuarantinedTombstones++
				} else {
					pass.RetriedTombstones++
				}
				continue
			}
			return pass, fmt.Errorf("recovery: cleanup tombstone %q: %w", tombstone.JobID, err)
		}
		switch outcome {
		case tombstoneRemoved:
			if err := removeTombstone(stateStore, tombstone); err != nil {
				return pass, err
			}
			pass.RemovedTombstones++
			pass.DurableChanges++
		case tombstoneRetried:
			pass.RetriedTombstones++
		case tombstoneQuarantined:
			pass.QuarantinedTombstones++
		}
	}

	// Re-read after tombstone settlement. A concurrent lifecycle transaction
	// may have added another live reference while the pass was inspecting.
	state = stateStore.Snapshot()
	roots := liveRoots(state)
	cutoff := time.Now().UTC().Add(-OrphanAge)
	for _, rootInfo := range roots {
		if err := ctx.Err(); err != nil {
			return pass, err
		}
		root, err := reservationfs.OpenRoot(rootInfo.path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				pass.SkippedRoots++
				continue
			}
			return pass, fmt.Errorf("recovery: validate orphan root: %w", err)
		}
		facts := root.Facts()
		if facts.Volume.CanonicalPath != rootInfo.ref.CanonicalPath || (rootInfo.ref.Identity != "" && facts.Volume.Identity != rootInfo.ref.Identity) {
			_ = root.Close()
			if rootIsQuarantined(state, rootInfo) {
				pass.ReconciliationOrphans++
				continue
			}
			return pass, errors.New("recovery: output root identity changed during orphan collection")
		}
		result, collectErr := engine.CollectResumeOrphans(ctx, engineRootRef(rootInfo.ref, facts.Volume.Identity), rootInfo.live, cutoff)
		closeErr := root.Close()
		if collectErr != nil {
			if closeErr != nil {
				return pass, errors.Join(collectErr, closeErr)
			}
			if strings.Contains(collectErr.Error(), "workspace unavailable") {
				pass.SkippedRoots++
				continue
			}
			return pass, collectErr
		}
		if closeErr != nil {
			return pass, closeErr
		}
		pass.CollectedOrphans += len(result.CollectedSessionIDs)
		pass.PendingOrphans += len(result.CleanupPendingSessionIDs)
		pass.ReconciliationOrphans += len(result.ReconciliationSessionIDs)
	}
	return pass, nil
}

type tombstoneOutcome uint8

const (
	tombstoneRemoved tombstoneOutcome = iota + 1
	tombstoneRetried
	tombstoneQuarantined
)

func cleanTombstone(ctx context.Context, tombstone jobmodel.CleanupTombstone) (tombstoneOutcome, error) {
	root, err := reservationfs.OpenRoot(tombstone.OutputRoot.CanonicalPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tombstoneRetried, updateTombstoneUnavailable(tombstone)
		}
		return tombstoneQuarantined, updateTombstoneError(tombstone, "output-root-unsafe")
	}
	facts := root.Facts()
	if facts.Volume.CanonicalPath != tombstone.OutputRoot.CanonicalPath || facts.Volume.Identity != tombstone.OutputRoot.Identity {
		_ = root.Close()
		return tombstoneQuarantined, updateTombstoneError(tombstone, "output-root-identity-changed")
	}
	closeRoot := func() error { return root.Close() }

	summary, inspectErr := engine.InspectResumeState(ctx, engineRootRef(tombstone.OutputRoot, facts.Volume.Identity), tombstone.SessionID)
	if inspectErr != nil {
		_ = closeRoot()
		return tombstoneQuarantined, updateTombstoneError(tombstone, "cleanup-reconciliation-required")
	}
	if IsRootUnavailable(summary) {
		if err := closeRoot(); err != nil {
			return tombstoneRetried, err
		}
		return tombstoneRemoved, nil
	}
	if summary.LeaseContended || hasUncertainEvidence(summary) {
		_ = closeRoot()
		return tombstoneQuarantined, updateTombstoneError(tombstone, evidenceActionCode(summary))
	}
	handle, prepareErr := engine.PrepareResumeDiscard(ctx, engineRootRef(tombstone.OutputRoot, facts.Volume.Identity), tombstone.SessionID)
	if prepareErr != nil {
		_ = closeRoot()
		if errors.Is(prepareErr, os.ErrNotExist) {
			return tombstoneRetried, updateTombstoneUnavailable(tombstone)
		}
		return tombstoneQuarantined, updateTombstoneError(tombstone, "cleanup-reconciliation-required")
	}
	if handle == nil {
		_ = closeRoot()
		return tombstoneQuarantined, updateTombstoneError(tombstone, "cleanup-invalid-handle")
	}
	result, discardErr := handle.Discard(ctx)
	closeErr := closeRoot()
	if discardErr != nil {
		if closeErr != nil {
			return tombstoneRetried, errors.Join(discardErr, closeErr)
		}
		return tombstoneRetried, updateTombstoneError(tombstone, "cleanup")
	}
	if closeErr != nil {
		return tombstoneRetried, closeErr
	}
	switch result.Disposition {
	case engine.ResumeDiscarded:
		return tombstoneRemoved, nil
	case engine.ResumeDiscardCleanupPending:
		return tombstoneRetried, updateTombstoneError(tombstone, "cleanup")
	default:
		return tombstoneQuarantined, updateTombstoneError(tombstone, "cleanup-reconciliation-required")
	}
}

// The helper errors are consumed by the worker's State transaction. Keeping
// them as values rather than silently mutating State from cleanTombstone makes
// retries idempotent and keeps one writer boundary.
type tombstoneUpdate struct {
	code  string
	state jobmodel.CleanupState
}

func (e tombstoneUpdate) Error() string { return e.code }

func updateTombstoneError(_ jobmodel.CleanupTombstone, code string) error {
	return tombstoneUpdate{code: code, state: jobmodel.CleanupQuarantined}
}

func updateTombstoneUnavailable(_ jobmodel.CleanupTombstone) error {
	return tombstoneUpdate{code: "output-root-unavailable", state: jobmodel.CleanupPending}
}

func removeTombstone(stateStore StateStore, tombstone jobmodel.CleanupTombstone) error {
	return stateStore.Transaction(nil, func(state *jobmodel.State) error {
		for index, candidate := range state.Cleanup {
			if candidate.JobID == tombstone.JobID && candidate.SessionID == tombstone.SessionID && candidate.UpdatedAt.Equal(tombstone.UpdatedAt) {
				state.Cleanup = append(state.Cleanup[:index], state.Cleanup[index+1:]...)
				return nil
			}
		}
		return nil
	})
}

func markTombstone(stateStore StateStore, tombstone jobmodel.CleanupTombstone, code string, cleanupState jobmodel.CleanupState) error {
	now := time.Now().UTC()
	return stateStore.Transaction(nil, func(state *jobmodel.State) error {
		for index := range state.Cleanup {
			candidate := &state.Cleanup[index]
			if candidate.JobID != tombstone.JobID || candidate.SessionID != tombstone.SessionID || !candidate.UpdatedAt.Equal(tombstone.UpdatedAt) {
				continue
			}
			candidate.State = cleanupState
			candidate.LastErrorCode = code
			candidate.UpdatedAt = now
			return nil
		}
		return nil
	})
}

func rootIsQuarantined(state jobmodel.State, root rootLiveSet) bool {
	for _, tombstone := range state.Cleanup {
		if tombstone.State != jobmodel.CleanupQuarantined || tombstone.OutputRoot.CanonicalPath != root.ref.CanonicalPath {
			continue
		}
		if root.ref.Identity == "" || tombstone.OutputRoot.Identity == root.ref.Identity {
			return true
		}
	}
	return false
}

type rootLiveSet struct {
	path string
	ref  jobmodel.OutputRootRef
	live map[string]struct{}
}

func liveRoots(state jobmodel.State) []rootLiveSet {
	byIdentity := make(map[string]*rootLiveSet)
	add := func(root jobmodel.OutputRootRef, sessionID string) {
		if root.CanonicalPath == "" || root.Identity == "" || sessionID == "" {
			return
		}
		key := root.Identity + "\x00" + root.CanonicalPath
		current := byIdentity[key]
		if current == nil {
			current = &rootLiveSet{path: root.CanonicalPath, ref: root, live: make(map[string]struct{})}
			byIdentity[key] = current
		}
		current.live[sessionID] = struct{}{}
	}
	for _, job := range state.Jobs {
		add(job.OutputRoot, job.SessionID)
	}
	for _, tombstone := range state.Cleanup {
		add(tombstone.OutputRoot, tombstone.SessionID)
	}
	if state.Settings.DownloadFolder != "" {
		if path, err := filepath.Abs(state.Settings.DownloadFolder); err == nil {
			path = filepath.Clean(path)
			key := "\x00" + path
			if _, exists := byIdentity[key]; !exists {
				byIdentity[key] = &rootLiveSet{
					path: path,
					ref:  jobmodel.OutputRootRef{CanonicalPath: path},
					live: make(map[string]struct{}),
				}
			}
		}
	}
	result := make([]rootLiveSet, 0, len(byIdentity))
	for _, root := range byIdentity {
		result = append(result, *root)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].path < result[j].path })
	return result
}

func engineRootRef(root jobmodel.OutputRootRef, observedReservationIdentity string) engine.OutputRootRef {
	identity := root.EngineIdentity
	if identity == "" {
		// Settings-only and older State rows do not carry the engine token.
		// Derive it from the already-opened path; the reservation identity is
		// only a last-resort value that will fail closed in the engine facade.
		if validated, err := engine.ValidateOutputRoot(root.CanonicalPath); err == nil {
			identity = validated.Identity
		} else {
			identity = observedReservationIdentity
		}
	}
	return engine.OutputRootRef{CanonicalPath: root.CanonicalPath, Identity: identity}
}

// StartCleanupWorker performs one immediate pass and retries periodically.
// The returned channel closes when the supplied context is canceled. The
// worker never starts until the caller has completed startup reconciliation.
func StartCleanupWorker(ctx context.Context, stateStore StateStore, interval time.Duration) <-chan struct{} {
	return StartCleanupWorkerWithReport(ctx, stateStore, interval, nil)
}

// StartCleanupWorkerWithReport is StartCleanupWorker with a bounded outcome
// callback. The callback lets the desktop refresh its backend-authored queue
// after maintenance changes durable cleanup authority.
func StartCleanupWorkerWithReport(ctx context.Context, stateStore StateStore, interval time.Duration, report func(CleanupPass)) <-chan struct{} {
	done := make(chan struct{})
	if ctx == nil {
		ctx = context.Background()
	}
	if interval <= 0 {
		interval = DefaultCleanupInterval
	}
	go func() {
		defer close(done)
		run := func() {
			pass, err := RunCleanupOnce(ctx, stateStore)
			if err == nil && report != nil {
				report(pass)
			}
		}
		run()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
	return done
}
