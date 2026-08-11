package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tejasa97/vidstow/internal/jobmodel"
)

type StartupMode string

const (
	StartupHealthy          StartupMode = "healthy"
	StartupRecoveryRequired StartupMode = "recovery-required"
)

type RecoveryReason string

const (
	RecoveryCorruptState       RecoveryReason = "corrupt-state"
	RecoveryUnsupportedVersion RecoveryReason = "unsupported-version"
	RecoveryMigrationFailed    RecoveryReason = "migration-failed"
	RecoveryUnsafePermissions  RecoveryReason = "unsafe-permissions"
	RecoveryIndeterminate      RecoveryReason = "indeterminate-commit"
)

// StartupStatus is safe to pass to startup UI code. It intentionally omits
// paths and raw I/O errors, which may contain user-sensitive data.
type StartupStatus struct {
	Mode   StartupMode    `json:"mode"`
	Reason RecoveryReason `json:"reason,omitempty"`
}

func (s StartupStatus) Healthy() bool { return s.Mode == StartupHealthy }

type JobPrecondition struct {
	ID         string
	Revision   uint64
	Lifecycle  jobmodel.Lifecycle
	SessionID  string
	OutputRoot jobmodel.OutputRootRef
}

var ErrStaleRevision = errors.New("store: job precondition did not match")

// CommitError describes whether a failed transaction crossed its atomic
// replacement commit point. Callers must not retry indeterminate outcomes.
type CommitError interface {
	error
	Committed() bool
	Indeterminate() bool
}

type commitError struct {
	err           error
	committed     bool
	indeterminate bool
}

func (e *commitError) Error() string       { return e.err.Error() }
func (e *commitError) Unwrap() error       { return e.err }
func (e *commitError) Committed() bool     { return e.committed }
func (e *commitError) Indeterminate() bool { return e.indeterminate }

type replaceResult struct {
	err           error
	committed     bool
	indeterminate bool
}

type atomicReplacer interface {
	Replace(tempPath, targetPath string) replaceResult
}

type V2Store struct {
	path     string
	lockPath string
	mu       sync.RWMutex
	state    jobmodel.State
	status   StartupStatus
	replacer atomicReplacer
}

// OpenV2 loads State v2 under its permanent sibling lock. Missing state starts
// empty; corrupt, unsupported, or unsafe state is never rewritten and returns
// a recovery-required status with no usable store.
func OpenV2(path string) (*V2Store, StartupStatus, error) {
	if path == "" {
		return nil, StartupStatus{}, errors.New("store: empty path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, StartupStatus{}, fmt.Errorf("store: create v2 dir: %w", err)
	}
	if err := validateStateDirectory(filepath.Dir(path)); err != nil {
		return nil, recoveryStatus(err), nil
	}
	s := &V2Store{path: path, lockPath: path + ".lock", status: StartupStatus{Mode: StartupHealthy}, replacer: osAtomicReplacer{}}
	lock, err := acquireStateLock(s.lockPath)
	if err != nil {
		return nil, recoveryStatus(err), nil
	}
	defer lock.Close()

	state, missing, err := readStateV2(path)
	if err != nil {
		return nil, recoveryStatus(err), nil
	}
	if missing {
		state = decodedState{State: defaultStateV2()}
		if err := s.writeInitial(state.State); err != nil {
			return nil, recoveryStatus(err), nil
		}
	} else if state.Version == 1 {
		migrated, err := migrateV1(path, state.raw)
		if err != nil {
			return nil, StartupStatus{Mode: StartupRecoveryRequired, Reason: RecoveryMigrationFailed}, nil
		}
		state = decodedState{State: migrated}
		if err := s.writeInitial(migrated); err != nil {
			return nil, StartupStatus{Mode: StartupRecoveryRequired, Reason: RecoveryMigrationFailed}, nil
		}
	}
	if state.Version != jobmodel.StateVersion {
		return nil, StartupStatus{Mode: StartupRecoveryRequired, Reason: RecoveryUnsupportedVersion}, nil
	}
	if err := validateState(state.State); err != nil {
		return nil, recoveryStatus(err), nil
	}
	s.state = state.State
	return s, s.status, nil
}

func recoveryStatus(err error) StartupStatus {
	reason := RecoveryCorruptState
	if errors.Is(err, errUnsafePermissions) {
		reason = RecoveryUnsafePermissions
	}
	return StartupStatus{Mode: StartupRecoveryRequired, Reason: reason}
}

// Snapshot returns a deep, independent state image.
func (s *V2Store) Snapshot() jobmodel.State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return jobmodel.CloneState(s.state)
}

func (s *V2Store) Status() StartupStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

// Transaction rereads the current disk image while holding the process and
// cross-process locks, applies a mutation to a clone, then atomically replaces
// the full document. The live manager intentionally does not use this in V0.
func (s *V2Store) Transaction(preconditions []JobPrecondition, mutate func(*jobmodel.State) error) error {
	if mutate == nil {
		return errors.New("store: nil v2 mutation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.status.Healthy() {
		return &commitError{err: errors.New("store: recovery required"), indeterminate: true}
	}
	lock, err := acquireStateLock(s.lockPath)
	if err != nil {
		return s.indeterminate(err)
	}
	defer lock.Close()
	decoded, missing, err := readStateV2(s.path)
	if err != nil || missing || decoded.Version != jobmodel.StateVersion || validateState(decoded.State) != nil {
		if err == nil {
			err = errors.New("store: disk state changed to invalid image")
		}
		return s.indeterminate(err)
	}
	next := jobmodel.CloneState(decoded.State)
	if err := checkPreconditions(next, preconditions); err != nil {
		return err
	}
	if err := mutate(&next); err != nil {
		return err
	}
	next.Version = jobmodel.StateVersion
	next.StoreRevision++
	if err := validateState(next); err != nil {
		return err
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return &commitError{err: fmt.Errorf("store: marshal v2: %w", err)}
	}
	temp, err := writeTempFile(s.path, data)
	if err != nil {
		return &commitError{err: err}
	}
	defer os.Remove(temp)
	result := s.replacer.Replace(temp, s.path)
	if result.err != nil {
		if result.indeterminate {
			s.state = next // New image is safest in-memory authority after uncertainty.
			return s.indeterminate(result.err)
		}
		if result.committed {
			s.state = next
			return &commitError{err: result.err, committed: true}
		}
		return &commitError{err: result.err}
	}
	s.state = next
	return nil
}

func (s *V2Store) indeterminate(err error) error {
	s.status = StartupStatus{Mode: StartupRecoveryRequired, Reason: RecoveryIndeterminate}
	return &commitError{err: fmt.Errorf("store: indeterminate commit authority: %w", err), indeterminate: true}
}

func (s *V2Store) writeInitial(state jobmodel.State) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("store: marshal initial v2: %w", err)
	}
	temp, err := writeTempFile(s.path, data)
	if err != nil {
		return err
	}
	defer os.Remove(temp)
	result := s.replacer.Replace(temp, s.path)
	if result.err != nil {
		return result.err
	}
	return nil
}

type decodedState struct {
	jobmodel.State
	raw []byte
}

func readStateV2(path string) (decodedState, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return decodedState{}, true, nil
	}
	if err != nil {
		return decodedState{}, false, fmt.Errorf("store: read v2: %w", err)
	}
	if err := validatePrivateRegularFile(path); err != nil {
		return decodedState{}, false, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return decodedState{}, false, errors.New("store: empty state")
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return decodedState{}, false, fmt.Errorf("store: invalid state JSON: %w", err)
	}
	var version int
	if raw, ok := probe["version"]; !ok || json.Unmarshal(raw, &version) != nil {
		return decodedState{}, false, errors.New("store: state has no valid version")
	}
	if version == 1 {
		return decodedState{State: jobmodel.State{Version: 1}, raw: data}, false, nil
	}
	var state jobmodel.State
	if err := decodeStrict(data, &state); err != nil {
		return decodedState{}, false, fmt.Errorf("store: invalid v2 state: %w", err)
	}
	return decodedState{State: state, raw: data}, false, nil
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func defaultStateV2() jobmodel.State {
	return jobmodel.State{
		Version:          jobmodel.StateVersion,
		NextQueueOrdinal: 1,
		Settings: jobmodel.Settings{
			DownloadFolder:      defaultDownloadDir(),
			WindowWidth:         1180,
			WindowHeight:        760,
			DownloadConcurrency: 2,
			PerVideoSubfolder:   true,
		},
		Jobs:    []jobmodel.DurableJob{},
		History: []jobmodel.HistoryEntry{},
		Cleanup: []jobmodel.CleanupTombstone{},
	}
}

func checkPreconditions(state jobmodel.State, conditions []JobPrecondition) error {
	for _, want := range conditions {
		found := false
		for _, job := range state.Jobs {
			if job.ID != want.ID {
				continue
			}
			found = true
			if job.Revision != want.Revision || job.Lifecycle != want.Lifecycle || job.SessionID != want.SessionID || job.OutputRoot != want.OutputRoot {
				return ErrStaleRevision
			}
			break
		}
		if !found {
			return ErrStaleRevision
		}
	}
	return nil
}

func validateState(state jobmodel.State) error {
	if state.Version != jobmodel.StateVersion {
		return errors.New("store: invalid state version")
	}
	if state.NextQueueOrdinal == 0 {
		return errors.New("store: next queue ordinal is zero")
	}
	if state.Settings.DownloadConcurrency < 1 || state.Settings.DownloadConcurrency > 10 {
		return errors.New("store: invalid download concurrency")
	}
	seen := make(map[string]struct{}, len(state.Jobs))
	for _, job := range state.Jobs {
		if job.ID == "" || job.AttemptID == "" || job.SessionID == "" || job.Revision == 0 || job.CreatedAt.IsZero() || job.UpdatedAt.IsZero() {
			return errors.New("store: incomplete durable job")
		}
		if _, duplicate := seen[job.ID]; duplicate {
			return errors.New("store: duplicate job id")
		}
		seen[job.ID] = struct{}{}
		if !validLifecycle(job.Lifecycle) || !validPhase(job.Phase) || !validDesired(job.Desired) || !validRetryMode(job.RetryMode) {
			return errors.New("store: invalid durable job enum")
		}
		if job.Lifecycle == jobmodel.LifecycleActionRequired && job.ActionRequiredCode == "migration-destination-unverified" && len(job.Reservation.Artifacts) == 0 {
			// V1 has no engine-rendered artifact declaration. Preserve such rows
			// for user repair rather than inventing an unsafe reservation.
		} else if err := validateReservation(job.OutputRoot, job.Reservation); err != nil {
			return err
		}
	}
	for _, tombstone := range state.Cleanup {
		if tombstone.JobID == "" || tombstone.SessionID == "" || tombstone.CreatedAt.IsZero() || tombstone.UpdatedAt.IsZero() || (tombstone.State != jobmodel.CleanupPending && tombstone.State != jobmodel.CleanupQuarantined) {
			return errors.New("store: invalid cleanup tombstone")
		}
		if err := validateReservation(tombstone.OutputRoot, tombstone.Reservation); err != nil {
			return err
		}
	}
	return nil
}

func validateReservation(root jobmodel.OutputRootRef, reservation jobmodel.ReservationSet) error {
	if root.CanonicalPath == "" || reservation.Directory.CanonicalPath != root.CanonicalPath || reservation.GroupID == "" || len(reservation.Artifacts) == 0 {
		return errors.New("store: incomplete reservation")
	}
	for _, artifact := range reservation.Artifacts {
		if artifact.Kind == "" || artifact.Identity == "" || artifact.Basename == "" || filepath.Base(artifact.Basename) != artifact.Basename {
			return errors.New("store: invalid reservation artifact")
		}
	}
	return nil
}

func validLifecycle(v jobmodel.Lifecycle) bool {
	switch v {
	case jobmodel.LifecyclePending, jobmodel.LifecycleActive, jobmodel.LifecyclePausing, jobmodel.LifecyclePaused, jobmodel.LifecycleCanceling, jobmodel.LifecycleFailed, jobmodel.LifecycleCanceled, jobmodel.LifecycleCompleted, jobmodel.LifecycleActionRequired:
		return true
	}
	return false
}
func validPhase(v jobmodel.Phase) bool {
	switch v {
	case jobmodel.PhasePreparing, jobmodel.PhaseDownloading, jobmodel.PhaseWaitingForProcessing, jobmodel.PhaseFinalizing, jobmodel.PhaseReadyToPublish, jobmodel.PhasePublishing, jobmodel.PhaseCleaningUp:
		return true
	}
	return false
}
func validDesired(v jobmodel.DesiredState) bool {
	return v == jobmodel.DesiredRunning || v == jobmodel.DesiredPaused || v == jobmodel.DesiredCanceled
}
func validRetryMode(v jobmodel.RetryMode) bool {
	return v == jobmodel.RetryModeNone || v == jobmodel.RetryModeResumeValidated || v == jobmodel.RetryModeRestartNewSession || v == jobmodel.RetryModePublishOnly
}

func writeTempFile(target string, data []byte) (string, error) {
	temp, err := os.CreateTemp(filepath.Dir(target), ".state-v2-*")
	if err != nil {
		return "", fmt.Errorf("store: create temp: %w", err)
	}
	name := temp.Name()
	if err := temp.Chmod(0o600); err == nil {
		_, err = temp.Write(data)
	}
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("store: write temp: %w", err)
	}
	return name, nil
}

// utcNow exists so migration tests can reason about the only generated time.
var utcNow = func() time.Time { return time.Now().UTC() }
