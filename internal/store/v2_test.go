package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tejasa97/vidstow/internal/jobmodel"
)

type replacerFunc func(string, string) replaceResult

func (f replacerFunc) Replace(temp, target string) replaceResult { return f(temp, target) }

func TestOpenV2CreatesPrivateStateAndLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, status, err := OpenV2(path)
	if err != nil || !status.Healthy() || s == nil {
		t.Fatalf("OpenV2 = %v, %#v, %v", s, status, err)
	}
	if got := s.Snapshot(); got.Version != jobmodel.StateVersion || got.Settings.DownloadConcurrency != 2 {
		t.Fatalf("initial state = %#v", got)
	}
	for _, name := range []string{path, path + ".lock"} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o, want 0600", name, info.Mode().Perm())
		}
	}
}

func TestOpenV2FailsClosedForCorruptUnknownAndUnsafeState(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		reason     RecoveryReason
	}{
		{"corrupt", "{", RecoveryCorruptState},
		{"unknown-version", `{"version":99}`, RecoveryUnsupportedVersion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			s, status, err := OpenV2(path)
			if err != nil || s != nil || status.Mode != StartupRecoveryRequired || status.Reason != tc.reason {
				t.Fatalf("OpenV2 = %v, %#v, %v", s, status, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != tc.body {
				t.Fatalf("state was altered: %q, %v", got, err)
			}
		})
	}
	if runtime.GOOS != "windows" {
		path := filepath.Join(t.TempDir(), "state.json")
		if err := os.WriteFile(path, mustJSON(t, defaultStateV2()), 0o644); err != nil {
			t.Fatal(err)
		}
		s, status, err := OpenV2(path)
		if err != nil || s != nil || status.Reason != RecoveryUnsafePermissions {
			t.Fatalf("unsafe state = %v, %#v, %v", s, status, err)
		}
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		s, status, err = OpenV2(filepath.Join(dir, "state.json"))
		if err != nil || s == nil || !status.Healthy() {
			t.Fatalf("owner-owned readable directory was not tightened: %v, %#v, %v", s, status, err)
		}
		if info, statErr := os.Stat(dir); statErr != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("tightened directory mode = %v, %v", info, statErr)
		}
		dir = t.TempDir()
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		s, status, err = OpenV2(filepath.Join(dir, "state.json"))
		if err != nil || s != nil || status.Reason != RecoveryUnsafePermissions {
			t.Fatalf("unsafe directory mutation mode = %v, %#v, %v", s, status, err)
		}
	}
}

func TestOpenV2StrictlyRejectsUnknownSchemaFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state := defaultStateV2()
	data := string(mustJSON(t, state))
	data = strings.TrimSuffix(data, "}") + `,"surprise":true}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	s, status, err := OpenV2(path)
	if err != nil || s != nil || status.Reason != RecoveryCorruptState {
		t.Fatalf("unknown-field load = %v, %#v, %v", s, status, err)
	}
}

func TestOpenV2UsesNoFollowStateRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native reparse-point fixture runs in Windows security matrix")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "real.json")
	if err := os.WriteFile(target, mustJSON(t, defaultStateV2()), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if s, status, err := OpenV2(path); err != nil || s != nil || status.Reason != RecoveryUnsafePermissions {
		t.Fatalf("symlink state = %v %#v %v", s, status, err)
	}
}

func TestOpenV2RejectsUnsafeExistingLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, mustJSON(t, defaultStateV2()), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("ACL mutation fixture runs in the native Windows security test")
	}
	if err := os.WriteFile(path+".lock", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s, status, err := OpenV2(path)
	if err != nil || s != nil || status.Reason != RecoveryUnsafePermissions {
		t.Fatalf("unsafe lock = %v, %#v, %v", s, status, err)
	}
}

func TestOpenV2RejectsOversizedAndInvalidInvariantImages(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*jobmodel.State)
	}{
		{"ordinal-not-below-next", func(s *jobmodel.State) {
			j := testJob()
			j.QueueOrdinal = s.NextQueueOrdinal
			s.Jobs = []jobmodel.DurableJob{j}
		}},
		{"duplicate-attempt", func(s *jobmodel.State) {
			a, b := testJob(), testJob()
			a.ID, b.ID, a.QueueOrdinal, b.QueueOrdinal, b.AttemptID = "a", "b", 1, 2, a.AttemptID
			s.NextQueueOrdinal = 3
			s.Jobs = []jobmodel.DurableJob{a, b}
		}},
		{"root-identity-mismatch", func(s *jobmodel.State) {
			j := testJob()
			j.QueueOrdinal = 1
			j.Reservation.Directory.Identity = "other"
			s.NextQueueOrdinal = 2
			s.Jobs = []jobmodel.DurableJob{j}
		}},
		{"portable-basename", func(s *jobmodel.State) {
			j := testJob()
			j.QueueOrdinal = 1
			j.Reservation.Artifacts[0].Basename = "CON.txt"
			s.NextQueueOrdinal = 2
			s.Jobs = []jobmodel.DurableJob{j}
		}},
		{"unsafe-source-url", func(s *jobmodel.State) {
			j := testJob()
			j.QueueOrdinal = 1
			j.Request.SourceURL = "https://evil.example/watch?v=abc12345678"
			s.NextQueueOrdinal = 2
			s.Jobs = []jobmodel.DurableJob{j}
		}},
		{"duplicate-reservation-claim", func(s *jobmodel.State) {
			a, b := testJob(), testJob()
			a.ID, b.ID, a.AttemptID, b.AttemptID, a.QueueOrdinal, b.QueueOrdinal = "a", "b", "attempt-a", "attempt-b", 1, 2
			s.NextQueueOrdinal = 3
			s.Jobs = []jobmodel.DurableJob{a, b}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			state := defaultStateV2()
			tc.mutate(&state)
			if err := os.WriteFile(path, mustJSON(t, state), 0o600); err != nil {
				t.Fatal(err)
			}
			s, status, err := OpenV2(path)
			if err != nil || s != nil || status.Reason != RecoveryCorruptState {
				t.Fatalf("invalid image = %v %#v %v", s, status, err)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), maxStateBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, status, err := OpenV2(path); err != nil || s != nil || status.Reason != RecoveryCorruptState {
		t.Fatalf("oversized image = %v %#v %v", s, status, err)
	}
	tooManyJobs := make([]any, maxJobs+1)
	data, err := json.Marshal(map[string]any{"version": jobmodel.StateVersion, "jobs": tooManyJobs})
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if s, status, err := OpenV2(path); err != nil || s != nil || status.Reason != RecoveryCorruptState {
		t.Fatalf("oversized jobs collection = %v %#v %v", s, status, err)
	}
	state := defaultStateV2()
	job := testJob()
	job.Reservation.Artifacts = make([]jobmodel.ReservedArtifact, maxArtifacts+1)
	for i := range job.Reservation.Artifacts {
		job.Reservation.Artifacts[i] = jobmodel.ReservedArtifact{Kind: fmt.Sprintf("kind-%d", i), Identity: fmt.Sprintf("identity-%d", i), Basename: fmt.Sprintf("artifact-%d.bin", i)}
	}
	state.Jobs = []jobmodel.DurableJob{job}
	state.NextQueueOrdinal = 2
	data = mustJSON(t, state)
	path = filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if s, status, err := OpenV2(path); err != nil || s != nil || status.Reason != RecoveryCorruptState {
		t.Fatalf("oversized artifact collection = %v %#v %v", s, status, err)
	}
}

func TestV2PersistedUnicodeControlsAreRejected(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*jobmodel.DurableJob)
	}{
		{"job-id", func(job *jobmodel.DurableJob) { job.ID = "job-\u0085" }},
		{"title", func(job *jobmodel.DurableJob) { job.Request.Title = "Title\u0085" }},
		{"output-path", func(job *jobmodel.DurableJob) {
			job.OutputRoot.CanonicalPath = "/safe/\u0085root"
			job.Reservation.Directory.CanonicalPath = job.OutputRoot.CanonicalPath
		}},
		{"artifact-basename", func(job *jobmodel.DurableJob) { job.Reservation.Artifacts[0].Basename = "clip\u0085.mp4" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			state := defaultStateV2()
			job := testJob()
			tc.mutate(&job)
			state.Jobs = []jobmodel.DurableJob{job}
			state.NextQueueOrdinal = 2
			if err := os.WriteFile(path, mustJSON(t, state), 0o600); err != nil {
				t.Fatal(err)
			}
			if s, status, err := OpenV2(path); err != nil || s != nil || status.Reason != RecoveryCorruptState {
				t.Fatalf("control image = %v %#v %v", s, status, err)
			}
		})
	}
}

func TestV2PersistedBasenamesMatchPortableReservationContract(t *testing.T) {
	invalid := []string{
		"CLOCK$.txt", "CONIN$.txt", "CONOUT$.txt", "clock$.txt", "COM¹.txt", "COM²", "COM³.log", "LPT¹.txt", "LPT²", "LPT³.csv",
		"CON.txt", "a/b.txt", "clip.", "clip ", "clip\u0085.mp4", strings.Repeat("a", maxBasenameBytes+1),
	}
	for _, basename := range invalid {
		t.Run(basename, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			state := defaultStateV2()
			job := testJob()
			job.Reservation.Artifacts[0].Basename = basename
			state.Jobs = []jobmodel.DurableJob{job}
			state.NextQueueOrdinal = 2
			if err := os.WriteFile(path, mustJSON(t, state), 0o600); err != nil {
				t.Fatal(err)
			}
			if s, status, err := OpenV2(path); err != nil || s != nil || status.Reason != RecoveryCorruptState {
				t.Fatalf("basename %q accepted: %v %#v %v", basename, s, status, err)
			}
		})
	}
}

func TestV2PreconditionRejectsUnknownLifecycle(t *testing.T) {
	err := validatePreconditionsInput([]JobPrecondition{{
		ID:         "job-1",
		Revision:   1,
		Lifecycle:  jobmodel.Lifecycle("not-a-lifecycle"),
		SessionID:  "0123456789abcdef0123456789abcdef",
		OutputRoot: testJob().OutputRoot,
	}})
	if err == nil {
		t.Fatal("unknown precondition lifecycle was accepted")
	}
}

func TestV2TemporalInvariantsRequireUTCAndMonotonicTimestamps(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*jobmodel.State)
	}{
		{"job-non-utc", func(state *jobmodel.State) {
			job := testJob()
			zone := time.FixedZone("IST", 5*60*60+30*60)
			job.CreatedAt = time.Date(2026, 8, 11, 10, 0, 0, 0, zone)
			job.UpdatedAt = job.CreatedAt
			state.Jobs = []jobmodel.DurableJob{job}
			state.NextQueueOrdinal = 2
		}},
		{"job-updated-before-created", func(state *jobmodel.State) {
			job := testJob()
			job.CreatedAt = time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
			job.UpdatedAt = job.CreatedAt.Add(-time.Second)
			state.Jobs = []jobmodel.DurableJob{job}
			state.NextQueueOrdinal = 2
		}},
		{"tombstone-updated-before-created", func(state *jobmodel.State) {
			job := testJob()
			tombstone := jobmodel.CleanupTombstone{JobID: "cleanup-1", SessionID: "fedcba9876543210fedcba9876543210", OutputRoot: job.OutputRoot, Reservation: job.Reservation, State: jobmodel.CleanupPending, CreatedAt: time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 8, 11, 9, 59, 59, 0, time.UTC)}
			tombstone.Reservation.GroupID = tombstone.JobID
			state.Cleanup = []jobmodel.CleanupTombstone{tombstone}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			state := defaultStateV2()
			tc.mutate(&state)
			if err := os.WriteFile(path, mustJSON(t, state), 0o600); err != nil {
				t.Fatal(err)
			}
			if s, status, err := OpenV2(path); err != nil || s != nil || status.Reason != RecoveryCorruptState {
				t.Fatalf("temporal image = %v %#v %v", s, status, err)
			}
		})
	}
}

func TestV2ReservationAndLifecycleAxesFollowStructuralRules(t *testing.T) {
	now := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	state := defaultStateV2()
	canceled := testJob()
	canceled.ID, canceled.Lifecycle, canceled.Desired = "canceled-job", jobmodel.LifecycleCanceled, jobmodel.DesiredRunning
	canceled.Reservation.GroupID = canceled.ID
	tomb := jobmodel.CleanupTombstone{JobID: canceled.ID, SessionID: canceled.SessionID, OutputRoot: canceled.OutputRoot, Reservation: canceled.Reservation, State: jobmodel.CleanupPending, CreatedAt: now, UpdatedAt: now}
	state.Jobs = []jobmodel.DurableJob{canceled}
	state.Cleanup = []jobmodel.CleanupTombstone{tomb}
	state.NextQueueOrdinal = 2
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, mustJSON(t, state), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, status, err := OpenV2(path); err != nil || !status.Healthy() || s == nil {
		t.Fatalf("coherent canceled tombstone = %v %#v %v", s, status, err)
	}

	state = defaultStateV2()
	failed := testJob()
	failed.ID, failed.Lifecycle, failed.Desired = "failed-job", jobmodel.LifecycleFailed, jobmodel.DesiredRunning
	failed.Reservation.GroupID = failed.ID
	state.Jobs = []jobmodel.DurableJob{failed}
	state.NextQueueOrdinal = 2
	path = filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, mustJSON(t, state), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, status, err := OpenV2(path); err != nil || !status.Healthy() || s == nil {
		t.Fatalf("orthogonal failed state = %v %#v %v", s, status, err)
	}

	state = defaultStateV2()
	caseDistinct := testJob()
	caseDistinct.ID = "case-job"
	caseDistinct.Reservation.GroupID = caseDistinct.ID
	caseDistinct.Reservation.Artifacts = append(caseDistinct.Reservation.Artifacts, jobmodel.ReservedArtifact{Kind: "sidecar", Identity: "metadata", Basename: "ORIGINAL.mp4"})
	state.Jobs = []jobmodel.DurableJob{caseDistinct}
	state.NextQueueOrdinal = 2
	path = filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, mustJSON(t, state), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, status, err := OpenV2(path); err != nil || !status.Healthy() || s == nil {
		t.Fatalf("case-distinct structural names = %v %#v %v", s, status, err)
	}

	needsAdmission := testJob()
	needsAdmission.ID = "needs-admission"
	needsAdmission.Lifecycle = jobmodel.LifecycleActionRequired
	needsAdmission.Desired = jobmodel.DesiredRunning
	needsAdmission.Phase = jobmodel.PhasePreparing
	needsAdmission.ActionRequiredCode = "migration-reanalysis-required"
	needsAdmission.OutputRoot = jobmodel.OutputRootRef{}
	needsAdmission.Reservation = jobmodel.ReservationSet{}
	state = defaultStateV2()
	state.Jobs = []jobmodel.DurableJob{needsAdmission}
	state.NextQueueOrdinal = 2
	if err := validateState(state); err != nil {
		t.Fatalf("orthogonal action-required state rejected: %v", err)
	}
	needsAdmission.Phase = jobmodel.PhaseReadyToPublish
	state.Jobs[0] = needsAdmission
	if err := validateState(state); err == nil {
		t.Fatal("action-required row without staged reservation was marked publish-ready")
	}
}

func TestV2RetryEscalationCountersAreBounded(t *testing.T) {
	valid := func() jobmodel.State {
		state := defaultStateV2()
		job := testJob()
		job.LastFailureCommittedBytes = 3145728
		job.ZeroProgressResumes = 1
		job.SessionRestarts = 2
		state.Jobs = []jobmodel.DurableJob{job}
		state.NextQueueOrdinal = 2
		return state
	}
	if err := validateState(valid()); err != nil {
		t.Fatalf("in-range escalation counters rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*jobmodel.DurableJob)
	}{
		{"negative committed bytes", func(job *jobmodel.DurableJob) { job.LastFailureCommittedBytes = -1 }},
		{"negative zero-progress resumes", func(job *jobmodel.DurableJob) { job.ZeroProgressResumes = -1 }},
		{"zero-progress resumes above bound", func(job *jobmodel.DurableJob) { job.ZeroProgressResumes = maxRetryEscalationCounter + 1 }},
		{"negative session restarts", func(job *jobmodel.DurableJob) { job.SessionRestarts = -1 }},
		{"session restarts above bound", func(job *jobmodel.DurableJob) { job.SessionRestarts = maxRetryEscalationCounter + 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := valid()
			test.mutate(&state.Jobs[0])
			if err := validateState(state); err == nil {
				t.Fatal("out-of-range escalation counters were accepted")
			}
		})
	}
}

func TestV2CleanupTombstoneCannotOverlapLivePendingJob(t *testing.T) {
	now := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	state := defaultStateV2()
	job := testJob()
	job.Lifecycle = jobmodel.LifecyclePending
	job.Desired = jobmodel.DesiredRunning
	retired := "fedcba9876543210fedcba9876543210"
	tomb := jobmodel.CleanupTombstone{
		JobID: job.ID, SessionID: retired, OutputRoot: job.OutputRoot, Reservation: job.Reservation,
		State: jobmodel.CleanupPending, CreatedAt: now, UpdatedAt: now,
	}
	state.Jobs = []jobmodel.DurableJob{job}
	state.Cleanup = []jobmodel.CleanupTombstone{tomb}
	state.NextQueueOrdinal = 2
	if err := validateState(state); err == nil {
		t.Fatal("retired-session tombstone overlapping a live pending job was accepted")
	}
}

func TestOpenV2MigratesV1AndBacksUpOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := map[string]any{
		"version":  1,
		"settings": map[string]any{"downloadFolder": "downloads", "downloadConcurrency": 4, "restoreInterruptedJobs": false, "perVideoSubfolder": true},
		"history":  []any{},
		"jobs": []any{map[string]any{"snapshot": map[string]any{
			"id": "old-job", "url": "https://www.youtube.com/watch?v=abc12345678&token=private", "videoID": "abc12345678", "title": "Title", "quality": "best", "outputDir": "downloads", "filename": "Title.mp4", "status": "active", "createdAt": "2026-01-02T03:04:05Z",
		}, "privateSelector": "bv*+ba/b[height<=1080]"}},
	}
	original := mustJSON(t, legacy)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	s, status, err := OpenV2(path)
	if err != nil || !status.Healthy() {
		t.Fatalf("OpenV2 migration: %v, %#v, %v", s, status, err)
	}
	backup, err := os.ReadFile(path + ".pre-v2.bak")
	if err != nil || string(backup) != string(original) {
		t.Fatalf("backup = %q, %v", backup, err)
	}
	state := s.Snapshot()
	if state.Version != 2 || state.Settings.DownloadConcurrency != 4 || len(state.Jobs) != 1 {
		t.Fatalf("migrated state = %#v", state)
	}
	job := state.Jobs[0]
	if job.Lifecycle != jobmodel.LifecycleActionRequired || job.Desired != jobmodel.DesiredPaused || job.RetryMode != jobmodel.RetryModeRestartNewSession {
		t.Fatalf("migrated job = %#v", job)
	}
	if job.Request.SourceURL != "https://www.youtube.com/watch?v=abc12345678" || job.Lifecycle != jobmodel.LifecycleActionRequired || len(job.Reservation.Artifacts) != 0 {
		t.Fatalf("unsafe or missing migration fields: %#v", job)
	}
	if job.Plan.PrivateSelector != "bv*+ba/b[height<=1080]" || len(job.SessionID) != 32 || strings.ToLower(job.SessionID) != job.SessionID {
		t.Fatalf("private selector/session migration = %#v", job.Plan)
	}
	if job.AttemptID == "" || job.SessionID == "" || job.QueueOrdinal != 1 {
		t.Fatalf("missing migration identities: %#v", job)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path + ".pre-v2.bak")
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("backup mode = %v, %v", info, err)
		}
	}
	current, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(current), "restoreInterruptedJobs") {
		t.Fatalf("v2 settings retained legacy restore toggle: %q, %v", current, err)
	}
}

func TestV2MigrationNormalizesLegacyTimestampsToUTC(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := map[string]any{
		"version":  1,
		"settings": map[string]any{},
		"history":  []any{map[string]any{"id": "history-1", "completedAt": "2026-08-11T10:00:00+05:30"}},
		"jobs": []any{map[string]any{"snapshot": map[string]any{
			"id": "old-job", "url": "https://www.youtube.com/watch?v=abc12345678", "videoID": "abc12345678", "title": "Title", "quality": "best", "createdAt": "2026-08-11T10:00:00+05:30",
		}}},
	}
	if err := os.WriteFile(path, mustJSON(t, legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, status, err := OpenV2(path)
	if err != nil || !status.Healthy() {
		t.Fatalf("timestamp migration = %v %#v %v", s, status, err)
	}
	state := s.Snapshot()
	job := state.Jobs[0]
	if job.CreatedAt.Location() != time.UTC || job.UpdatedAt.Location() != time.UTC || job.UpdatedAt.Before(job.CreatedAt) {
		t.Fatalf("migrated job timestamps = %v %v", job.CreatedAt, job.UpdatedAt)
	}
	completed, err := time.Parse(time.RFC3339Nano, state.History[0].CompletedAt)
	if err != nil || completed.Location() != time.UTC {
		t.Fatalf("migrated history timestamp = %q (%v)", state.History[0].CompletedAt, err)
	}
}

func TestOpenV2MigratesVersionZeroAndRejectsUnknownLegacyFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := []byte(`{"version":0,"settings":{"downloadConcurrency":2},"history":[],"jobs":[]}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if s, status, err := OpenV2(path); err != nil || !status.Healthy() || s.Snapshot().Version != jobmodel.StateVersion {
		t.Fatalf("version zero migration = %v %#v", s, status)
	}
	path = filepath.Join(t.TempDir(), "state.json")
	legacy = []byte(`{"version":1,"settings":{},"history":[],"jobs":[],"unknownLegacyField":true}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if s, status, err := OpenV2(path); err != nil || s != nil || status.Reason != RecoveryMigrationFailed {
		t.Fatalf("unknown legacy field = %v %#v %v", s, status, err)
	}
}

func TestMigrationSourceIdentityMatchesVideoID(t *testing.T) {
	videoID := "abc12345678"
	cases := []struct{ name, raw, want string }{
		{"watch", "https://www.youtube.com/watch?v=" + videoID + "&utm_source=tracking", "https://www.youtube.com/watch?v=" + videoID},
		{"short-host", "https://youtu.be/" + videoID + "?si=tracking", "https://www.youtube.com/watch?v=" + videoID},
		{"watch-mismatch", "https://www.youtube.com/watch?v=other123456", ""},
		{"short-mismatch", "https://youtu.be/other123456", ""},
		{"watch-missing-v", "https://www.youtube.com/watch", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := safeSourceURL(tc.raw, videoID); got != tc.want {
				t.Fatalf("safeSourceURL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestV2MigrationBackupIsIdempotentAndConflictsFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := []byte(`{"version":1,"settings":{},"history":[],"jobs":[]}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".pre-v2.bak", legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if s, status, err := OpenV2(path); err != nil || !status.Healthy() || s == nil {
		t.Fatalf("identical backup retry = %v %#v %v", s, status, err)
	}
	path = filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".pre-v2.bak", []byte("different"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, status, err := OpenV2(path); err != nil || s != nil || status.Reason != RecoveryMigrationFailed {
		t.Fatalf("conflicting backup = %v %#v %v", s, status, err)
	}
}

func TestOpenV2MigratesUnverifiableDestinationToActionRequired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := []byte(`{"version":1,"settings":{"downloadConcurrency":2},"history":[],"jobs":[{"snapshot":{"id":"old","url":"https://www.youtube.com/watch?v=abc12345678","videoID":"abc12345678","title":"Title","quality":"best","outputDir":"out","status":"pending"}}]}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	s, status, err := OpenV2(path)
	if err != nil || !status.Healthy() {
		t.Fatalf("OpenV2: %v %#v %v", s, status, err)
	}
	job := s.Snapshot().Jobs[0]
	if job.Lifecycle != jobmodel.LifecycleActionRequired || job.ActionRequiredCode != "migration-reanalysis-required" || job.Phase == jobmodel.PhaseReadyToPublish {
		t.Fatalf("job = %#v", job)
	}
}

func TestV2TransactionDeepCloneAndStaleRevision(t *testing.T) {
	s := openV2TestStore(t)
	var retained *jobmodel.State
	if err := s.Transaction(nil, func(state *jobmodel.State) error {
		job := testJob()
		job.QueueOrdinal = state.NextQueueOrdinal
		state.NextQueueOrdinal++
		state.Jobs = append(state.Jobs, job)
		retained = state
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := s.Snapshot()
	if err := s.Transaction([]JobPrecondition{{ID: "job-1", Revision: 1, Lifecycle: jobmodel.LifecyclePaused, SessionID: "0123456789abcdef0123456789abcdef", OutputRoot: before.Jobs[0].OutputRoot}}, func(state *jobmodel.State) error {
		state.Jobs[0].Revision++
		state.Jobs[0].Reservation.Artifacts[0].Basename = "changed.mp4"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if before.Jobs[0].Reservation.Artifacts[0].Basename != "original.mp4" {
		t.Fatalf("snapshot aliased transaction clone: %#v", before)
	}
	retained.Settings.FFmpegPath = "caller-retained"
	if got := s.Snapshot().Settings.FFmpegPath; got != "" {
		t.Fatalf("committed memory retained caller pointer: %q", got)
	}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			retained.Settings.FFmpegPath = "caller-race"
		}
		close(done)
	}()
	for i := 0; i < 1000; i++ {
		_ = s.Snapshot()
	}
	<-done
	stale := s.Snapshot()
	err := s.Transaction([]JobPrecondition{{ID: "job-1", Revision: 1, Lifecycle: jobmodel.LifecyclePaused, SessionID: "0123456789abcdef0123456789abcdef", OutputRoot: stale.Jobs[0].OutputRoot}}, func(*jobmodel.State) error { return nil })
	if !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale transaction error = %v", err)
	}
}

func TestV2TransactionCommitOutcomeSemantics(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		result                   replaceResult
		committed, indeterminate bool
	}{
		{"pre-commit", replaceResult{err: errors.New("temp failure")}, false, false},
		{"post-commit", replaceResult{err: errors.New("directory sync failure"), committed: true}, true, false},
		{"indeterminate", replaceResult{err: errors.New("replace authority unknown"), indeterminate: true}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openV2TestStore(t)
			old := s.Snapshot().StoreRevision
			s.replacer = replacerFunc(func(_, _ string) replaceResult { return tc.result })
			err := s.Transaction(nil, func(state *jobmodel.State) error { state.Settings.FFmpegPath = "new"; return nil })
			var outcome CommitError
			if !errors.As(err, &outcome) || outcome.Committed() != tc.committed || outcome.Indeterminate() != tc.indeterminate {
				t.Fatalf("outcome = %v (%T)", err, err)
			}
			got := s.Snapshot()
			if tc.committed {
				if got.Settings.FFmpegPath != "new" || !s.Status().Healthy() || s.Status().Warning != WarningDurabilityUncertain {
					t.Fatalf("committed error hid known authority: %#v / %#v", got, s.Status())
				}
			} else if tc.indeterminate {
				if got.Version != 0 || s.Status().Mode != StartupRecoveryRequired {
					t.Fatalf("indeterminate state exposed as authority: %#v / %#v", got, s.Status())
				}
			} else if got.Settings.FFmpegPath != "" || got.StoreRevision != old {
				t.Fatalf("pre-commit mutated state: %#v", got)
			}
			if tc.indeterminate && s.Status().Mode != StartupRecoveryRequired {
				t.Fatalf("status after indeterminate = %#v", s.Status())
			}
		})
	}
}

func TestV2KnownCommittedWarningExposesSnapshotAndBlocksMutation(t *testing.T) {
	s := openV2TestStore(t)
	replaceCalls := 0
	s.replacer = replacerFunc(func(_, _ string) replaceResult {
		replaceCalls++
		return replaceResult{err: errors.New("parent durability warning"), committed: true}
	})
	if err := s.Transaction(nil, func(state *jobmodel.State) error {
		state.Settings.FFmpegPath = "committed-image"
		return nil
	}); err == nil {
		t.Fatal("known committed transaction unexpectedly succeeded without outcome")
	} else {
		var outcome CommitError
		if !errors.As(err, &outcome) || !outcome.Committed() || outcome.Indeterminate() {
			t.Fatalf("known committed outcome = %v / %#v", err, outcome)
		}
	}
	if got := s.Snapshot().Settings.FFmpegPath; got != "committed-image" || !s.Status().Healthy() || s.Status().Warning != WarningDurabilityUncertain {
		t.Fatalf("known committed authority = %#v / %#v", s.Snapshot(), s.Status())
	}
	if err := s.Transaction(nil, func(state *jobmodel.State) error {
		state.Settings.FFmpegPath = "must-not-overwrite"
		return nil
	}); !errors.Is(err, ErrDurabilityUncertain) {
		t.Fatalf("blocked follow-up mutation = %v, want ErrDurabilityUncertain", err)
	}
	if replaceCalls != 1 || s.Snapshot().Settings.FFmpegPath != "committed-image" {
		t.Fatalf("follow-up mutation changed authority: calls=%d state=%#v", replaceCalls, s.Snapshot())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, status, err := OpenV2(s.path); err != nil || status.Reason != RecoveryIndeterminate {
		t.Fatalf("known-commit evidence restart = %#v %v", status, err)
	}
}

func TestV2StartupReleaseFailureWarnsAndBlocksMutation(t *testing.T) {
	releaseErr := errors.New("startup unlock failure")
	oldRelease := releaseStateLock
	releaseStateLock = func(*stateLock) error { return releaseErr }
	s, status, err := OpenV2(filepath.Join(t.TempDir(), "state.json"))
	releaseStateLock = oldRelease
	if err != nil || s == nil || !status.Healthy() || status.Warning != WarningDurabilityUncertain {
		t.Fatalf("startup release failure = %v %#v %v", s, status, err)
	}
	if s.Snapshot().Version != jobmodel.StateVersion {
		t.Fatalf("startup release failure hid known state: %#v", s.Snapshot())
	}
	if err := s.Transaction(nil, func(state *jobmodel.State) error {
		state.Settings.FFmpegPath = "must-not-mutate"
		return nil
	}); !errors.Is(err, ErrDurabilityUncertain) {
		t.Fatalf("startup release follow-up = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestV2TransactionReleaseFailuresAreClassified(t *testing.T) {
	t.Run("precommit", func(t *testing.T) {
		s := openV2TestStore(t)
		releaseErr := errors.New("precommit unlock failure")
		oldRelease := releaseStateLock
		releaseStateLock = func(*stateLock) error { return releaseErr }
		defer func() { releaseStateLock = oldRelease }()
		mutationErr := errors.New("mutation rejected")
		err := s.Transaction(nil, func(*jobmodel.State) error { return mutationErr })
		var outcome CommitError
		if !errors.As(err, &outcome) || outcome.Committed() || !outcome.Indeterminate() || s.Status().Reason != RecoveryIndeterminate {
			t.Fatalf("precommit release = %v %#v status=%#v", err, outcome, s.Status())
		}
		if s.Snapshot().Version != 0 {
			t.Fatalf("precommit release exposed state: %#v", s.Snapshot())
		}
		releaseStateLock = oldRelease
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("known-commit", func(t *testing.T) {
		s := openV2TestStore(t)
		releaseErr := errors.New("commit unlock failure")
		oldRelease := releaseStateLock
		releaseStateLock = func(*stateLock) error { return releaseErr }
		defer func() { releaseStateLock = oldRelease }()
		err := s.Transaction(nil, func(state *jobmodel.State) error {
			state.Settings.FFmpegPath = "committed-before-unlock"
			return nil
		})
		var outcome CommitError
		if !errors.As(err, &outcome) || !outcome.Committed() || outcome.Indeterminate() || !s.Status().Healthy() || s.Status().Warning != WarningDurabilityUncertain {
			t.Fatalf("known commit release = %v %#v status=%#v", err, outcome, s.Status())
		}
		if s.Snapshot().Settings.FFmpegPath != "committed-before-unlock" {
			t.Fatalf("known commit release hid state: %#v", s.Snapshot())
		}
		releaseStateLock = oldRelease
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestV2StateLockCloseIsIdempotentAndQuarantineCloseRetries(t *testing.T) {
	s := openV2TestStore(t)
	s.replacer = replacerFunc(func(_, _ string) replaceResult {
		return replaceResult{err: errors.New("authority unknown"), indeterminate: true}
	})
	if err := s.Transaction(nil, func(*jobmodel.State) error { return nil }); err == nil {
		t.Fatal("indeterminate transaction unexpectedly succeeded")
	}
	releaseErr := errors.New("quarantine unlock failure")
	oldRelease := releaseStateLock
	releaseStateLock = func(*stateLock) error { return releaseErr }
	if err := s.Close(); !errors.Is(err, releaseErr) {
		t.Fatalf("quarantine close failure = %v", err)
	}
	releaseStateLock = oldRelease
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("idempotent quarantine close = %v", err)
	}

	lock, err := acquireStateLock(s.lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("idempotent state lock close = %v", err)
	}
}

func TestV2IndeterminateRetainsCrossProcessLockUntilClose(t *testing.T) {
	s := openV2TestStore(t)
	s.replacer = replacerFunc(func(_, _ string) replaceResult {
		return replaceResult{err: errors.New("authority lost"), indeterminate: true}
	})
	err := s.Transaction(nil, func(state *jobmodel.State) error { state.Settings.FFmpegPath = "unproved"; return nil })
	var outcome CommitError
	if !errors.As(err, &outcome) || !outcome.Indeterminate() || s.quarantine == nil {
		t.Fatalf("indeterminate transaction = %v, quarantine=%v", err, s.quarantine)
	}
	if s.evidencePath == "" {
		t.Fatal("indeterminate image evidence was not retained")
	}
	if _, err := os.Stat(s.evidencePath); err != nil {
		t.Fatalf("indeterminate evidence missing: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		lock, err := acquireStateLock(s.lockPath)
		if lock != nil {
			lock.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("recovery lock was released: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("recovery lock remained after Close")
	}
}

func TestV2IndeterminateMarkerBlocksRestartWithOrWithoutTemp(t *testing.T) {
	s := openV2TestStore(t)
	s.replacer = replacerFunc(func(_, _ string) replaceResult {
		return replaceResult{err: errors.New("target authority unknown"), indeterminate: true}
	})
	if err := s.Transaction(nil, func(state *jobmodel.State) error { state.Settings.FFmpegPath = "candidate"; return nil }); err == nil {
		t.Fatal("indeterminate transaction unexpectedly succeeded")
	}
	temp := s.evidencePath
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, status, err := OpenV2(s.path); err != nil || status.Reason != RecoveryIndeterminate {
		t.Fatalf("restart with temp = %#v, %v", status, err)
	}
	if err := os.Remove(temp); err != nil {
		t.Fatal(err)
	}
	if _, status, err := OpenV2(s.path); err != nil || status.Reason != RecoveryIndeterminate {
		t.Fatalf("restart without temp = %#v, %v", status, err)
	}
}

func TestV2MarkerWriteFailurePreventsReplacement(t *testing.T) {
	s := openV2TestStore(t)
	called := false
	s.markerWriter = func(string, recoveryMarker) markerWriteResult {
		return markerWriteResult{err: errors.New("marker pre-commit failure")}
	}
	s.replacer = replacerFunc(func(_, _ string) replaceResult { called = true; return replaceResult{} })
	err := s.Transaction(nil, func(state *jobmodel.State) error { state.Settings.FFmpegPath = "must-not-commit"; return nil })
	var outcome CommitError
	if !errors.As(err, &outcome) || outcome.Committed() || outcome.Indeterminate() || called || s.Status().Mode != StartupHealthy {
		t.Fatalf("marker failure = %v called=%v status=%#v", err, called, s.Status())
	}
	if got := s.Snapshot().Settings.FFmpegPath; got != "" {
		t.Fatalf("marker failure changed memory: %q", got)
	}
}

func TestV2ExistingMarkerIsUnsafeAndBlocksRestart(t *testing.T) {
	s := openV2TestStore(t)
	if err := os.WriteFile(s.markerPath, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	s.replacer = replacerFunc(func(_, _ string) replaceResult { called = true; return replaceResult{} })
	err := s.Transaction(nil, func(state *jobmodel.State) error { state.Settings.FFmpegPath = "must-not-replace"; return nil })
	var outcome CommitError
	if !errors.As(err, &outcome) || !outcome.Indeterminate() || called || s.Status().Mode != StartupRecoveryRequired {
		t.Fatalf("existing marker = %v called=%v status=%#v", err, called, s.Status())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, status, err := OpenV2(s.path); err != nil || status.Reason != RecoveryIndeterminate {
		t.Fatalf("corrupt marker restart = %#v %v", status, err)
	}
}

func TestV2RecoveryMarkerReadUsesDedicatedBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path+".recovery", bytes.Repeat([]byte("x"), maxRecoveryMarkerBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, status, err := OpenV2(path); err != nil || s != nil || status.Reason != RecoveryIndeterminate {
		t.Fatalf("oversized marker = %v %#v %v", s, status, err)
	}
}

func TestV2MarkerRemovalUncertaintyQuarantinesDeterministicAndCommittedOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		result    replaceResult
		committed bool
	}{
		{"deterministic-replace-failure", replaceResult{err: errors.New("replace failed")}, false},
		{"successful-replace-marker-cleanup", replaceResult{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openV2TestStore(t)
			oldSync := syncPrivateParent
			calls := 0
			syncPrivateParent = func(path string) error {
				calls++
				if calls >= 2 {
					return errors.New("parent sync injected")
				}
				return oldSync(path)
			}
			defer func() { syncPrivateParent = oldSync }()
			s.replacer = replacerFunc(func(_, _ string) replaceResult { return tc.result })
			err := s.Transaction(nil, func(state *jobmodel.State) error { state.Settings.FFmpegPath = "candidate"; return nil })
			var outcome CommitError
			if !errors.As(err, &outcome) || outcome.Committed() != tc.committed || outcome.Indeterminate() != !tc.committed {
				t.Fatalf("cleanup uncertainty = %v outcome=%#v status=%#v", err, outcome, s.Status())
			}
			if tc.committed {
				if !s.Status().Healthy() || s.Status().Warning != WarningDurabilityUncertain || s.Snapshot().Settings.FFmpegPath != "candidate" {
					t.Fatalf("known commit was hidden: %#v / %#v", s.Snapshot(), s.Status())
				}
			} else if s.Status().Reason != RecoveryIndeterminate {
				t.Fatalf("deterministic uncertainty status = %#v", s.Status())
			}
			if s.evidencePath == "" {
				t.Fatal("cleanup uncertainty dropped evidence path")
			}
			if _, err := os.Stat(s.markerPath); err != nil {
				t.Fatalf("cleanup uncertainty dropped marker: %v", err)
			}
		})
	}
}

func TestV2WriteInitialHonorsAtomicOutcomesAndMarkerCleanup(t *testing.T) {
	s := openV2TestStore(t)
	s.replacer = replacerFunc(func(_, _ string) replaceResult { return replaceResult{err: errors.New("pre-install failure")} })
	if err := s.writeInitial(defaultStateV2()); err == nil {
		t.Fatal("pre-install write unexpectedly succeeded")
	}
	if _, err := os.Stat(s.markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-install marker = %v", err)
	}

	s.replacer = replacerFunc(func(_, _ string) replaceResult {
		return replaceResult{err: errors.New("install authority unknown"), indeterminate: true}
	})
	if err := s.writeInitial(defaultStateV2()); err == nil {
		t.Fatal("indeterminate install unexpectedly succeeded")
	}
	marker, exists, err := readRecoveryMarker(s.markerPath)
	if err != nil || !exists {
		t.Fatalf("indeterminate install marker = %#v %v %v", marker, exists, err)
	}
	if _, err := os.Stat(marker.TempPath); err != nil {
		t.Fatalf("indeterminate install evidence = %v", err)
	}
	if err := removeRecoveryMarker(s.markerPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(marker.TempPath); err != nil {
		t.Fatal(err)
	}

	committed := openV2TestStore(t)
	committed.replacer = replacerFunc(func(_, _ string) replaceResult {
		return replaceResult{err: errors.New("post-install sync failure"), committed: true}
	})
	err = committed.writeInitial(defaultStateV2())
	var outcome CommitError
	if !errors.As(err, &outcome) || !outcome.Committed() || outcome.Indeterminate() || !committed.Status().Healthy() || committed.Status().Warning != WarningDurabilityUncertain || committed.Snapshot().Version != jobmodel.StateVersion {
		t.Fatalf("committed initial outcome = %v outcome=%#v status=%#v", err, outcome, committed.Status())
	}
	if _, exists, markerErr := readRecoveryMarker(committed.markerPath); markerErr != nil || !exists {
		t.Fatalf("committed initial marker = %v %v", markerErr, exists)
	}

	for _, tc := range []struct {
		name      string
		result    replaceResult
		committed bool
	}{
		{name: "deterministic", result: replaceResult{err: errors.New("replace failed")}},
		{name: "successful", result: replaceResult{}, committed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openV2TestStore(t)
			oldSync := syncPrivateParent
			calls := 0
			syncPrivateParent = func(path string) error {
				calls++
				if calls >= 2 {
					return errors.New("initial marker cleanup sync failure")
				}
				return oldSync(path)
			}
			defer func() { syncPrivateParent = oldSync }()
			s.replacer = replacerFunc(func(_, _ string) replaceResult { return tc.result })
			err := s.writeInitial(defaultStateV2())
			var outcome CommitError
			if !errors.As(err, &outcome) || outcome.Committed() != tc.committed || outcome.Indeterminate() != !tc.committed {
				t.Fatalf("initial cleanup outcome = %v outcome=%#v status=%#v", err, outcome, s.Status())
			}
			if tc.committed {
				if !s.Status().Healthy() || s.Status().Warning != WarningDurabilityUncertain || s.Snapshot().Version != jobmodel.StateVersion {
					t.Fatalf("initial known commit was hidden: %#v / %#v", s.Snapshot(), s.Status())
				}
			} else if s.Status().Reason != RecoveryIndeterminate {
				t.Fatalf("initial deterministic uncertainty status = %#v", s.Status())
			}
			if _, statErr := os.Stat(s.evidencePath); statErr != nil {
				t.Fatalf("initial cleanup evidence = %v", statErr)
			}
			if _, statErr := os.Stat(s.markerPath); statErr != nil {
				t.Fatalf("initial cleanup marker = %v", statErr)
			}
		})
	}
}

func TestV2LockAcquisitionFailureIsPreCommit(t *testing.T) {
	s := openV2TestStore(t)
	s.lockPath = filepath.Join(t.TempDir(), "missing", "state.json.lock")
	err := s.Transaction(nil, func(state *jobmodel.State) error { state.Settings.FFmpegPath = "must-not-write"; return nil })
	var outcome CommitError
	if errors.As(err, &outcome) || s.Status().Mode != StartupHealthy {
		t.Fatalf("lock acquisition incorrectly classified as indeterminate: %v / %#v", err, s.Status())
	}
}

func TestV2CrossProcessLockSerializesTransaction(t *testing.T) {
	if os.Getenv("VIDSTOW_V2_LOCK_HELPER") == "1" {
		path := os.Getenv("VIDSTOW_V2_LOCK_PATH")
		lock, err := acquireStateLock(path + ".lock")
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		fmt.Fprintln(os.Stdout, "locked")
		time.Sleep(350 * time.Millisecond)
		return
	}
	s := openV2TestStore(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestV2CrossProcessLockSerializesTransaction$", "-test.v")
	cmd.Env = append(os.Environ(), "VIDSTOW_V2_LOCK_HELPER=1", "VIDSTOW_V2_LOCK_PATH="+s.path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	locked := false
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "locked" {
			locked = true
			break
		}
	}
	if err := scanner.Err(); err != nil || !locked {
		t.Fatalf("lock helper did not acquire state lock: %v", err)
	}
	start := time.Now()
	err = s.Transaction(nil, func(state *jobmodel.State) error { state.Settings.FFmpegPath = "serialized"; return nil })
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed < 200*time.Millisecond {
		t.Fatalf("transaction did not wait for cross-process lock: %s", elapsed)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestV2StoreRereadsLatestImageUnderLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	first, status, err := OpenV2(path)
	if err != nil || !status.Healthy() {
		t.Fatalf("first OpenV2: %v, %#v", err, status)
	}
	second, status, err := OpenV2(path)
	if err != nil || !status.Healthy() {
		t.Fatalf("second OpenV2: %v, %#v", err, status)
	}
	if err := first.Transaction(nil, func(state *jobmodel.State) error { state.Settings.FFmpegPath = "first"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := second.Transaction(nil, func(state *jobmodel.State) error { state.Settings.ConfirmBeforeDownload = true; return nil }); err != nil {
		t.Fatal(err)
	}
	got := second.Snapshot()
	if got.Settings.FFmpegPath != "first" || !got.Settings.ConfirmBeforeDownload {
		t.Fatalf("lost cross-store update: %#v", got.Settings)
	}
}

func openV2TestStore(t *testing.T) *V2Store {
	t.Helper()
	s, status, err := OpenV2(filepath.Join(t.TempDir(), "state.json"))
	if err != nil || !status.Healthy() {
		t.Fatalf("OpenV2: %v %#v", err, status)
	}
	return s
}

func testJob() jobmodel.DurableJob {
	now := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	root := jobmodel.OutputRootRef{CanonicalPath: "/safe/root", Identity: "root"}
	return jobmodel.DurableJob{ID: "job-1", Revision: 1, AttemptID: "attempt-1", SessionID: "0123456789abcdef0123456789abcdef", QueueOrdinal: 1, Lifecycle: jobmodel.LifecyclePaused, Phase: jobmodel.PhasePreparing, Desired: jobmodel.DesiredPaused, Request: jobmodel.PersistedRequest{SourceURL: "https://www.youtube.com/watch?v=abc12345678", VideoID: "abc12345678", Title: "Title", Quality: "best"}, OutputRoot: root, Reservation: jobmodel.ReservationSet{GroupID: "job-1", Directory: root, Artifacts: []jobmodel.ReservedArtifact{{Kind: "primary", Identity: "primary", Basename: "original.mp4"}}}, RetryMode: jobmodel.RetryModeNone, CreatedAt: now, UpdatedAt: now}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
