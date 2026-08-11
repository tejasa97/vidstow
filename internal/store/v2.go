package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

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

type StatusWarning string

const (
	WarningDurabilityUncertain StatusWarning = "durability-uncertain"
)

// StartupStatus is safe to pass to startup UI code. It intentionally omits
// paths and raw I/O errors, which may contain user-sensitive data.
type StartupStatus struct {
	Mode    StartupMode    `json:"mode"`
	Reason  RecoveryReason `json:"reason,omitempty"`
	Warning StatusWarning  `json:"warning,omitempty"`
}

func (s StartupStatus) Healthy() bool { return s.Mode == StartupHealthy }

// JobPrecondition remains exported from store for the V1 admission seam.
// The canonical definition lives in jobmodel so the jobs package can depend
// on the transaction contract without introducing a store import cycle.
type JobPrecondition = jobmodel.JobPrecondition

var ErrStaleRevision = errors.New("store: job precondition did not match")
var ErrDurabilityUncertain = errors.New("store: durability evidence unresolved")

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
	path            string
	lockPath        string
	mu              sync.RWMutex
	state           jobmodel.State
	status          StartupStatus
	replacer        atomicReplacer
	quarantine      *stateLock
	evidencePath    string
	markerPath      string
	markerWriter    func(string, recoveryMarker) markerWriteResult
	mutationBlocked bool
}

// OpenV2 loads State v2 under its permanent sibling lock. Missing state starts
// empty. Corrupt or indeterminate state returns recovery-required with no
// usable store; a known committed image with unresolved durability returns a
// usable snapshot plus a warning and mutation block.
func OpenV2(path string) (store *V2Store, status StartupStatus, returnErr error) {
	if path == "" {
		return nil, StartupStatus{}, errors.New("store: empty path")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, StartupStatus{}, err
	}
	if !validPath(absPath) {
		return nil, recoveryStatus(errors.New("store: invalid state path")), nil
	}
	if err := ensureStateDirectory(filepath.Dir(absPath)); err != nil {
		return nil, recoveryStatus(err), nil
	}
	canonicalPath, canonicalErr := canonicalizeStatePath(absPath)
	if canonicalErr != nil {
		return nil, recoveryStatus(canonicalErr), nil
	}
	path = canonicalPath
	if !validPath(path) || !validPath(path+".lock") || !validPath(path+".recovery") {
		return nil, recoveryStatus(errors.New("store: state path exceeds limit")), nil
	}
	s := &V2Store{path: path, lockPath: path + ".lock", markerPath: path + ".recovery", status: StartupStatus{Mode: StartupHealthy}, replacer: osAtomicReplacer{}, markerWriter: writeRecoveryMarker}
	lock, err := acquireStateLock(s.lockPath)
	if err != nil {
		return nil, recoveryStatus(err), nil
	}
	keepStartupLock := false
	defer func() {
		if keepStartupLock {
			return
		}
		if releaseErr := releaseStateLock(lock); releaseErr != nil {
			if store != nil {
				store.markReleaseFailure(lock, true)
				status = store.status
				returnErr = nil
				return
			}
			status = StartupStatus{Mode: StartupRecoveryRequired, Reason: RecoveryIndeterminate}
			returnErr = nil
		}
	}()
	if _, exists, markerErr := readRecoveryMarker(s.markerPath); markerErr != nil || exists {
		if markerErr != nil && errors.Is(markerErr, errUnsafePermissions) {
			return nil, StartupStatus{Mode: StartupRecoveryRequired, Reason: RecoveryUnsafePermissions}, nil
		}
		return nil, StartupStatus{Mode: StartupRecoveryRequired, Reason: RecoveryIndeterminate}, nil
	}

	state, missing, err := readStateV2(path)
	if err != nil {
		return nil, recoveryStatus(err), nil
	}
	if missing {
		state = decodedState{State: defaultStateV2()}
		if err := s.writeInitial(state.State); err != nil {
			var outcome CommitError
			if errors.As(err, &outcome) && outcome.Committed() && !outcome.Indeterminate() {
				s.quarantine = lock
				keepStartupLock = true
				return s, s.status, nil
			}
			return nil, startupWriteStatus(err), nil
		}
	} else if state.Version == 0 || state.Version == 1 {
		migrated, err := migrateV1(path, state.raw)
		if err != nil {
			return nil, StartupStatus{Mode: StartupRecoveryRequired, Reason: RecoveryMigrationFailed}, nil
		}
		if err := validateState(migrated); err != nil {
			return nil, StartupStatus{Mode: StartupRecoveryRequired, Reason: RecoveryMigrationFailed}, nil
		}
		state = decodedState{State: migrated}
		if err := s.writeInitial(migrated); err != nil {
			var outcome CommitError
			if errors.As(err, &outcome) && outcome.Committed() && !outcome.Indeterminate() {
				s.quarantine = lock
				keepStartupLock = true
				return s, s.status, nil
			}
			return nil, startupWriteStatus(err), nil
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
	if !s.status.Healthy() {
		return jobmodel.State{}
	}
	return jobmodel.CloneState(s.state)
}

// Close releases only a retained recovery quarantine lock. Normal V0 stores
// have no persistent process lock between transactions.
func (s *V2Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.quarantine == nil {
		return nil
	}
	err := releaseStateLock(s.quarantine)
	if err == nil {
		s.quarantine = nil
	}
	return err
}

func startupWriteStatus(err error) StartupStatus {
	var outcome CommitError
	if errors.As(err, &outcome) && (outcome.Committed() || outcome.Indeterminate()) {
		return StartupStatus{Mode: StartupRecoveryRequired, Reason: RecoveryIndeterminate}
	}
	return recoveryStatus(err)
}

func (s *V2Store) Status() StartupStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

// Transaction rereads the current disk image while holding the process and
// cross-process locks, applies a mutation to a clone, then atomically replaces
// the full document. The live manager intentionally does not use this in V0.
func (s *V2Store) Transaction(preconditions []JobPrecondition, mutate func(*jobmodel.State) error) (returnErr error) {
	if mutate == nil {
		return errors.New("store: nil v2 mutation")
	}
	if err := validatePreconditionsInput(preconditions); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.status.Healthy() || s.mutationBlocked {
		err := errors.New("store: recovery required")
		if s.status.Warning == WarningDurabilityUncertain {
			err = ErrDurabilityUncertain
		}
		return &commitError{err: err, indeterminate: true}
	}
	lock, err := acquireStateLock(s.lockPath)
	if err != nil {
		if errors.Is(err, errUnsafePermissions) {
			s.enterRecovery(RecoveryUnsafePermissions)
		}
		return fmt.Errorf("store: acquire transaction lock: %w", err)
	}
	keepLock := false
	knownCommitted := false
	defer func() {
		if !keepLock {
			if releaseErr := releaseStateLock(lock); releaseErr != nil {
				s.markReleaseFailure(lock, knownCommitted)
				committed := knownCommitted
				indeterminate := !knownCommitted
				if prior, ok := s.commitOutcome(returnErr); ok {
					committed = committed || prior.Committed()
					if !knownCommitted {
						indeterminate = true
					}
				}
				releaseErr = fmt.Errorf("store: release transaction lock: %w", releaseErr)
				if returnErr != nil {
					releaseErr = errors.Join(returnErr, releaseErr)
				}
				returnErr = &commitError{err: releaseErr, committed: committed, indeterminate: indeterminate}
			}
		}
	}()
	decoded, missing, err := readStateV2(s.path)
	if err != nil || missing || decoded.Version != jobmodel.StateVersion || validateState(decoded.State) != nil {
		if err == nil {
			err = errors.New("store: disk state changed to invalid image")
		}
		s.enterRecovery(RecoveryCorruptState)
		return fmt.Errorf("store: unsafe disk state before transaction: %w", err)
	}
	next := jobmodel.CloneState(decoded.State)
	if err := checkPreconditions(next, preconditions); err != nil {
		return err
	}
	if err := mutate(&next); err != nil {
		return err
	}
	next.Version = jobmodel.StateVersion
	if next.StoreRevision == maxDurableCounter {
		return &commitError{err: errors.New("store: store revision exhausted")}
	}
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
	keepTemp := false
	defer func() {
		if !keepTemp {
			_ = os.Remove(temp)
		}
	}()
	markerResult := s.markerWriter(s.markerPath, recoveryMarker{Version: recoveryMarkerVersion, TargetPath: s.path, TempPath: temp, StoreRevision: next.StoreRevision, CreatedAt: utcNow().UTC()})
	if markerResult.err != nil {
		if markerResult.present {
			s.state = jobmodel.State{}
			s.quarantine = lock
			s.evidencePath = temp
			keepTemp = true
			keepLock = true
			s.enterRecovery(RecoveryIndeterminate)
			return &commitError{err: markerResult.err, indeterminate: true}
		}
		return &commitError{err: markerResult.err}
	}
	result := s.replacer.Replace(temp, s.path)
	if result.err != nil {
		if result.indeterminate {
			s.state = jobmodel.State{}
			s.quarantine = lock
			s.evidencePath = temp
			keepTemp = true
			keepLock = true
			return s.indeterminate(result.err, result.committed)
		}
		if result.committed {
			s.state = jobmodel.CloneState(next)
			s.quarantine = lock
			s.evidencePath = temp
			keepTemp = true
			keepLock = true
			knownCommitted = true
			s.markDurabilityWarning(lock)
			return &commitError{err: result.err, committed: true}
		}
		if err := removeRecoveryMarker(s.markerPath); err != nil {
			s.state = jobmodel.State{}
			s.quarantine = lock
			s.evidencePath = temp
			keepTemp = true
			keepLock = true
			s.enterRecovery(RecoveryIndeterminate)
			return &commitError{err: err, indeterminate: true}
		}
		return &commitError{err: result.err}
	}
	s.state = jobmodel.CloneState(next)
	knownCommitted = true
	if err := removeRecoveryMarker(s.markerPath); err != nil {
		s.quarantine = lock
		s.evidencePath = temp
		keepTemp = true
		keepLock = true
		s.markDurabilityWarning(lock)
		return &commitError{err: err, committed: true}
	}
	return nil
}

func (s *V2Store) indeterminate(err error, committed bool) error {
	s.enterRecovery(RecoveryIndeterminate)
	return &commitError{err: fmt.Errorf("store: indeterminate commit authority: %w", err), committed: committed, indeterminate: true}
}

func (s *V2Store) commitOutcome(err error) (CommitError, bool) {
	if err == nil {
		return nil, false
	}
	var outcome CommitError
	return outcome, errors.As(err, &outcome)
}

func (s *V2Store) markDurabilityWarning(lock *stateLock) {
	s.status = StartupStatus{Mode: StartupHealthy, Warning: WarningDurabilityUncertain}
	s.mutationBlocked = true
	if s.quarantine == nil {
		s.quarantine = lock
	}
}

func (s *V2Store) markReleaseFailure(lock *stateLock, knownCommitted bool) {
	if knownCommitted {
		s.markDurabilityWarning(lock)
		return
	}
	s.quarantine = lock
	s.enterRecovery(RecoveryIndeterminate)
}

func (s *V2Store) enterRecovery(reason RecoveryReason) {
	s.status = StartupStatus{Mode: StartupRecoveryRequired, Reason: reason}
	s.state = jobmodel.State{}
	s.mutationBlocked = true
}

func (s *V2Store) writeInitial(state jobmodel.State) error {
	if err := validateState(state); err != nil {
		return fmt.Errorf("store: invalid initial v2 state: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("store: marshal initial v2: %w", err)
	}
	temp, err := writeTempFile(s.path, data)
	if err != nil {
		return err
	}
	keepTemp := false
	defer func() {
		if !keepTemp {
			_ = os.Remove(temp)
		}
	}()
	markerResult := s.markerWriter(s.markerPath, recoveryMarker{Version: recoveryMarkerVersion, TargetPath: s.path, TempPath: temp, StoreRevision: state.StoreRevision, CreatedAt: utcNow().UTC()})
	if markerResult.err != nil {
		if markerResult.present {
			s.enterRecovery(RecoveryIndeterminate)
			s.evidencePath = temp
			keepTemp = true
			return &commitError{err: markerResult.err, indeterminate: true}
		}
		return &commitError{err: markerResult.err}
	}
	result := s.replacer.Replace(temp, s.path)
	if result.err != nil {
		if result.committed || result.indeterminate {
			if result.committed && !result.indeterminate {
				s.state = jobmodel.CloneState(state)
				s.evidencePath = temp
				s.status = StartupStatus{Mode: StartupHealthy, Warning: WarningDurabilityUncertain}
				s.mutationBlocked = true
				keepTemp = true
				return &commitError{err: result.err, committed: true}
			}
			s.enterRecovery(RecoveryIndeterminate)
			s.evidencePath = temp
			keepTemp = true
			return &commitError{err: result.err, committed: result.committed, indeterminate: result.indeterminate}
		}
		if markerErr := removeRecoveryMarker(s.markerPath); markerErr != nil {
			s.enterRecovery(RecoveryIndeterminate)
			s.evidencePath = temp
			keepTemp = true
			return &commitError{err: markerErr, indeterminate: true}
		}
		return &commitError{err: result.err}
	}
	if err := removeRecoveryMarker(s.markerPath); err != nil {
		s.state = jobmodel.CloneState(state)
		s.evidencePath = temp
		keepTemp = true
		s.status = StartupStatus{Mode: StartupHealthy, Warning: WarningDurabilityUncertain}
		s.mutationBlocked = true
		return &commitError{err: err, committed: true}
	}
	return nil
}

type decodedState struct {
	jobmodel.State
	raw []byte
}

func readStateV2(path string) (decodedState, bool, error) {
	f, err := openPrivateRead(path)
	if errors.Is(err, os.ErrNotExist) {
		return decodedState{}, true, nil
	}
	if err != nil {
		return decodedState{}, false, fmt.Errorf("store: read v2: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxStateBytes+1))
	if err != nil {
		return decodedState{}, false, fmt.Errorf("store: read v2: %w", err)
	}
	if len(data) > maxStateBytes {
		return decodedState{}, false, errors.New("store: state exceeds size limit")
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
	for _, field := range []struct {
		name string
		max  int
	}{
		{name: "jobs", max: maxJobs},
		{name: "history", max: maxHistory},
		{name: "cleanup", max: maxCleanup},
	} {
		if err := preflightJSONArray(probe[field.name], field.max); err != nil {
			return decodedState{}, false, fmt.Errorf("store: %s exceeds structural limit: %w", field.name, err)
		}
	}
	if err := preflightJobArtifactArrays(probe["jobs"]); err != nil {
		return decodedState{}, false, fmt.Errorf("store: job artifacts exceed structural limit: %w", err)
	}
	if version == 0 || version == 1 {
		return decodedState{State: jobmodel.State{Version: 1}, raw: data}, false, nil
	}
	var state jobmodel.State
	if err := decodeStrict(data, &state); err != nil {
		return decodedState{}, false, fmt.Errorf("store: invalid v2 state: %w", err)
	}
	return decodedState{State: state, raw: data}, false, nil
}

// preflightJSONArray counts one top-level JSON array without materializing its
// elements. State collections are bounded before decoding them into Go slices.
func preflightJSONArray(raw json.RawMessage, max int) error {
	if len(raw) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return nil
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '[' {
		return errors.New("must be an array")
	}
	count := 0
	for decoder.More() {
		if count >= max {
			return fmt.Errorf("contains more than %d elements", max)
		}
		if err := skipJSONValue(decoder); err != nil {
			return err
		}
		count++
	}
	if _, err := decoder.Token(); err != nil {
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

func skipJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '[' && delim != '{' {
		return nil
	}
	for decoder.More() {
		if delim == '{' {
			if _, err := decoder.Token(); err != nil {
				return err
			}
		}
		if err := skipJSONValue(decoder); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func preflightJobArtifactArrays(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return nil
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '[' {
		return errors.New("jobs must be an array")
	}
	for decoder.More() {
		var job map[string]json.RawMessage
		if err := decoder.Decode(&job); err != nil {
			return err
		}
		reservationRaw, ok := job["reservation"]
		if !ok {
			continue
		}
		var reservation map[string]json.RawMessage
		if err := json.Unmarshal(reservationRaw, &reservation); err != nil {
			return err
		}
		if err := preflightJSONArray(reservation["artifacts"], maxArtifacts); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
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
	if len(conditions) > maxPreconditions {
		return errors.New("store: too many job preconditions")
	}
	for _, want := range conditions {
		found := false
		for _, job := range state.Jobs {
			if job.ID != want.ID {
				continue
			}
			found = true
			if job.Revision != want.Revision || job.Lifecycle != want.Lifecycle || (want.AttemptID != "" && job.AttemptID != want.AttemptID) || job.SessionID != want.SessionID || job.OutputRoot != want.OutputRoot {
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
	if state.NextQueueOrdinal == 0 || state.NextQueueOrdinal > maxDurableCounter || state.StoreRevision > maxDurableCounter {
		return errors.New("store: next queue ordinal is zero")
	}
	if !validSettings(state.Settings) || state.Settings.DownloadConcurrency < 1 || state.Settings.DownloadConcurrency > 10 {
		return errors.New("store: invalid download concurrency")
	}
	if len(state.Jobs) > maxJobs || len(state.History) > maxHistory || len(state.Cleanup) > maxCleanup {
		return errors.New("store: state collection exceeds limit")
	}
	seen, attempts, sessions, ordinals, cleanupJobs := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}, map[uint64]struct{}{}, map[string]struct{}{}
	jobsByID := map[string]jobmodel.DurableJob{}
	reservationClaims := map[string]string{}
	for _, job := range state.Jobs {
		if !validID(job.ID) || !validID(job.AttemptID) || !validSessionID(job.SessionID) || job.Revision == 0 || job.Revision > maxDurableCounter || job.QueueOrdinal == 0 || job.QueueOrdinal >= state.NextQueueOrdinal || !validTimestampPair(job.CreatedAt, job.UpdatedAt) {
			return errors.New("store: incomplete durable job")
		}
		if _, duplicate := seen[job.ID]; duplicate {
			return errors.New("store: duplicate job id")
		}
		if _, duplicate := attempts[job.AttemptID]; duplicate {
			return errors.New("store: duplicate attempt id")
		}
		if _, duplicate := sessions[job.SessionID]; duplicate {
			return errors.New("store: duplicate session id")
		}
		if _, duplicate := ordinals[job.QueueOrdinal]; duplicate {
			return errors.New("store: duplicate queue ordinal")
		}
		seen[job.ID] = struct{}{}
		jobsByID[job.ID] = job
		attempts[job.AttemptID], sessions[job.SessionID], ordinals[job.QueueOrdinal] = struct{}{}, struct{}{}, struct{}{}
		if !validLifecycle(job.Lifecycle) || !validPhase(job.Phase) || !validDesired(job.Desired) || !validRetryMode(job.RetryMode) {
			return errors.New("store: invalid durable job enum")
		}
		if !validRequest(job.Request) || !validPlan(job.Plan) || !validText(job.ActionRequiredCode, maxShortText, job.Lifecycle == jobmodel.LifecycleActionRequired) || (job.Lifecycle != jobmodel.LifecycleActionRequired && job.ActionRequiredCode != "") || !validText(job.LastErrorCode, maxShortText, false) {
			return errors.New("store: unsafe persisted request or plan")
		}
		if job.Lifecycle == jobmodel.LifecycleActionRequired && (job.ActionRequiredCode == "migration-reanalysis-required" || job.ActionRequiredCode == "migration-private-plan-unverified") && job.Phase == jobmodel.PhasePreparing && job.OutputRoot == (jobmodel.OutputRootRef{}) && job.Reservation.GroupID == "" && job.Reservation.Directory == (jobmodel.OutputRootRef{}) && len(job.Reservation.Artifacts) == 0 {
			// V1 has no engine-rendered artifact declaration. Preserve such rows
			// for user repair rather than inventing an unsafe reservation.
		} else if !validText(job.OutputRoot.EngineIdentity, maxVolumeIdentityBytes, false) {
			return errors.New("store: invalid engine output root identity")
		} else if err := validateReservation(job.OutputRoot, job.Reservation); err != nil {
			return err
		} else if job.Reservation.GroupID != job.ID {
			return errors.New("store: reservation group does not match job")
		} else if lifecycleHoldsReservation(job) {
			for _, artifact := range job.Reservation.Artifacts {
				key := reservationKey(job.OutputRoot, artifact.Basename)
				if owner, exists := reservationClaims[key]; exists && owner != job.ID {
					return errors.New("store: duplicate live reservation claim")
				}
				reservationClaims[key] = job.ID
			}
		}
	}
	for _, tombstone := range state.Cleanup {
		if !validID(tombstone.JobID) || !validSessionID(tombstone.SessionID) || !validText(tombstone.LastErrorCode, maxShortText, false) || !validTimestampPair(tombstone.CreatedAt, tombstone.UpdatedAt) || (tombstone.State != jobmodel.CleanupPending && tombstone.State != jobmodel.CleanupQuarantined) {
			return errors.New("store: invalid cleanup tombstone")
		}
		if !validText(tombstone.OutputRoot.EngineIdentity, maxVolumeIdentityBytes, false) {
			return errors.New("store: invalid engine output root identity")
		}
		if err := validateReservation(tombstone.OutputRoot, tombstone.Reservation); err != nil {
			return err
		}
		if tombstone.Reservation.GroupID != tombstone.JobID {
			return errors.New("store: cleanup group does not match job")
		}
		if live, liveExists := jobsByID[tombstone.JobID]; liveExists {
			if live.Lifecycle != jobmodel.LifecycleCanceled || live.SessionID != tombstone.SessionID || live.OutputRoot != tombstone.OutputRoot || !reflect.DeepEqual(live.Reservation, tombstone.Reservation) {
				return errors.New("store: cleanup tombstone overlaps incompatible live job")
			}
		}
		if _, duplicate := cleanupJobs[tombstone.JobID]; duplicate {
			return errors.New("store: duplicate cleanup job")
		}
		cleanupJobs[tombstone.JobID] = struct{}{}
		if _, duplicate := sessions[tombstone.SessionID]; duplicate && jobsByID[tombstone.JobID].Lifecycle != jobmodel.LifecycleCanceled {
			return errors.New("store: duplicate cleanup session")
		}
		sessions[tombstone.SessionID] = struct{}{}
		for _, artifact := range tombstone.Reservation.Artifacts {
			key := reservationKey(tombstone.OutputRoot, artifact.Basename)
			if _, exists := reservationClaims[key]; exists {
				return errors.New("store: duplicate cleanup reservation claim")
			}
			reservationClaims[key] = tombstone.JobID
		}
	}
	for _, history := range state.History {
		if !validHistory(history) {
			return errors.New("store: invalid history")
		}
	}
	return nil
}

func validateReservation(root jobmodel.OutputRootRef, reservation jobmodel.ReservationSet) error {
	if !validOutputPath(root.CanonicalPath) || !validText(root.Identity, maxVolumeIdentityBytes, true) || reservation.Directory.CanonicalPath != root.CanonicalPath || reservation.Directory.Identity != root.Identity || !validIDBounded(reservation.GroupID, maxGroupIDBytes, true) || len(reservation.Artifacts) == 0 || len(reservation.Artifacts) > maxArtifacts {
		return errors.New("store: incomplete reservation")
	}
	seenNames, seenIdentities := map[string]struct{}{}, map[string]struct{}{}
	for _, artifact := range reservation.Artifacts {
		if !validText(artifact.Kind, maxKindBytes, true) || !validText(artifact.Identity, maxIdentityBytes, true) || !validBasename(artifact.Basename) {
			return errors.New("store: invalid reservation artifact")
		}
		name := artifact.Basename
		if _, ok := seenNames[name]; ok {
			return errors.New("store: duplicate reservation basename")
		}
		artifactKey := artifact.Kind + "\x00" + artifact.Identity
		if _, ok := seenIdentities[artifactKey]; ok {
			return errors.New("store: duplicate reservation identity")
		}
		seenNames[name], seenIdentities[artifactKey] = struct{}{}, struct{}{}
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
	if len(data) > maxStateBytes {
		return "", errors.New("store: serialized state exceeds size limit")
	}
	temp, err := createPrivateTemp(filepath.Dir(target), ".state-v2-")
	if err != nil {
		return "", fmt.Errorf("store: create temp: %w", err)
	}
	name := temp.Name()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		_ = os.Remove(name)
		return "", fmt.Errorf("store: set temp permissions: %w", err)
	}
	var written int
	if written, err = temp.Write(data); err == nil && written != len(data) {
		err = io.ErrShortWrite
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

const (
	maxStateBytes          = 8 << 20
	maxJobs                = 10_000
	maxHistory             = 10_000
	maxCleanup             = 10_000
	maxArtifacts           = 32
	maxPreconditions       = 10_000
	maxDurableCounter      = uint64(1<<63 - 1)
	maxIDBytes             = 128
	maxGroupIDBytes        = 128
	maxKindBytes           = 64
	maxIdentityBytes       = 256
	maxCanonicalPathBytes  = 4096
	maxVolumeIdentityBytes = 256
	maxBasenameBytes       = 255
	maxShortText           = 256
	maxText                = 4096
	maxPathBytes           = 32767
)

func validText(value string, max int, required bool) bool {
	if required && value == "" {
		return false
	}
	if len(value) > max || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func validID(value string) bool {
	return validText(value, maxIDBytes, true) && !strings.ContainsAny(value, `\\/`)
}
func validIDBounded(value string, max int, required bool) bool {
	return validText(value, max, required) && !strings.ContainsAny(value, `\\/`)
}
func validSessionID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func validPath(value string) bool {
	return validText(value, maxPathBytes, true) && filepath.IsAbs(value) && filepath.Clean(value) == value
}
func validOutputPath(value string) bool {
	return validText(value, maxCanonicalPathBytes, true) && filepath.IsAbs(value) && filepath.Clean(value) == value
}
func validBasename(value string) bool {
	if !validText(value, maxBasenameBytes, true) || value == "." || value == ".." || filepath.Base(value) != value || filepath.VolumeName(value) != "" || strings.ContainsAny(value, `\\/:*?"<>|`) || strings.HasSuffix(value, ".") || strings.HasSuffix(value, " ") {
		return false
	}
	base := strings.TrimRight(value, " .")
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	base = strings.TrimRight(base, " .")
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL", "CLOCK$", "CONIN$", "CONOUT$", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9", "COM¹", "COM²", "COM³", "LPT¹", "LPT²", "LPT³":
		return false
	}
	return true
}
func validTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.Year() >= 2000 && value.Year() <= 2100
}
func validTimestampPair(created, updated time.Time) bool {
	return validTime(created) && validTime(updated) && !updated.Before(created)
}
func validRequest(r jobmodel.PersistedRequest) bool {
	sourceOK := r.SourceURL == "" && safeVideoID(r.VideoID) || validSourceURL(r.SourceURL, r.VideoID)
	return sourceOK && validText(r.VideoID, maxIDBytes, true) && validText(r.Title, maxText, true) && validText(r.Channel, maxText, false) && validText(r.Quality, maxShortText, true) && validIDBounded(r.PlanID, maxIDBytes, false) && validText(r.Duration, maxShortText, false)
}
func validPlan(p jobmodel.PersistedPlan) bool {
	return (p.ID == "" || validID(p.ID)) && validText(p.Kind, maxShortText, false) && validText(p.Label, maxText, false) && validText(p.Container, maxShortText, false) && validText(p.VideoCodec, maxShortText, false) && validText(p.AudioCodec, maxShortText, false) && validPrivateSelector(p.PrivateSelector)
}
func validPrivateSelector(value string) bool {
	if value == "" {
		return true
	}
	if !validText(value, maxText, false) {
		return false
	}
	for _, r := range value {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("*+/_.,()[]<>!=-", r)) {
			return false
		}
	}
	return true
}
func validSourceURL(value, videoID string) bool {
	return value == "https://www.youtube.com/watch?v="+videoID && safeVideoID(videoID)
}
func safeVideoID(value string) bool {
	if len(value) < 6 || len(value) > 32 {
		return false
	}
	for _, r := range value {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func lifecycleHoldsReservation(job jobmodel.DurableJob) bool {
	return job.Lifecycle != jobmodel.LifecycleCanceled && job.Lifecycle != jobmodel.LifecycleCompleted
}
func reservationKey(root jobmodel.OutputRootRef, basename string) string {
	return root.Identity + "\x00" + root.CanonicalPath + "\x00" + basename
}
func validHistory(h jobmodel.HistoryEntry) bool {
	return validID(h.ID) && validText(h.VideoID, maxIDBytes, false) && validText(h.Title, maxText, false) && validText(h.Channel, maxText, false) && validText(h.Quality, maxShortText, false) && validText(h.Container, maxShortText, false) && validText(h.VideoCodec, maxShortText, false) && validText(h.AudioCodec, maxShortText, false) && (h.Filename == "" || validBasename(h.Filename)) && validText(h.AbsolutePath, maxPathBytes, false) && (h.AbsolutePath == "" || filepath.IsAbs(h.AbsolutePath) && filepath.Clean(h.AbsolutePath) == h.AbsolutePath) && validTimestampText(h.CompletedAt, false) && validText(h.DurationLabel, maxShortText, false) && h.SizeBytes >= 0
}

func validSettings(s jobmodel.Settings) bool {
	return validText(s.DownloadFolder, maxPathBytes, false) && validText(s.FFmpegPath, maxPathBytes, false) && s.WindowWidth >= 0 && s.WindowWidth <= 10000 && s.WindowHeight >= 0 && s.WindowHeight <= 10000
}
func validatePreconditionsInput(values []JobPrecondition) error {
	if len(values) > maxPreconditions {
		return errors.New("store: too many job preconditions")
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, duplicate := seen[value.ID]; duplicate {
			return errors.New("store: duplicate job precondition")
		}
		seen[value.ID] = struct{}{}
		if !validID(value.ID) || !validLifecycle(value.Lifecycle) || (value.AttemptID != "" && !validID(value.AttemptID)) || !validSessionID(value.SessionID) || value.Revision == 0 || value.Revision > maxDurableCounter || !validOutputPath(value.OutputRoot.CanonicalPath) || !validText(value.OutputRoot.Identity, maxVolumeIdentityBytes, true) || !validText(value.OutputRoot.EngineIdentity, maxVolumeIdentityBytes, false) {
			return errors.New("store: invalid job precondition")
		}
	}
	return nil
}

func validTimestampText(value string, required bool) bool {
	if !validText(value, maxShortText, required) {
		return false
	}
	if value == "" {
		return !required
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && validTime(parsed)
}

// utcNow exists so migration tests can reason about the only generated time.
var utcNow = func() time.Time { return time.Now().UTC() }
