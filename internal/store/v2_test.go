package store

import (
	"bufio"
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
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		s, status, err = OpenV2(filepath.Join(dir, "state.json"))
		if err != nil || s != nil || status.Reason != RecoveryUnsafePermissions {
			t.Fatalf("unsafe directory = %v, %#v, %v", s, status, err)
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

func TestOpenV2RejectsUnsafeExistingLock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows ACL validation is native-integration work")
	}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, mustJSON(t, defaultStateV2()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".lock", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s, status, err := OpenV2(path)
	if err != nil || s != nil || status.Reason != RecoveryUnsafePermissions {
		t.Fatalf("unsafe lock = %v, %#v, %v", s, status, err)
	}
}

func TestOpenV2MigratesV1AndBacksUpOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := map[string]any{
		"version":  1,
		"settings": map[string]any{"downloadFolder": "downloads", "downloadConcurrency": 4, "restoreInterruptedJobs": false, "perVideoSubfolder": true},
		"history":  []any{},
		"jobs": []any{map[string]any{"snapshot": map[string]any{
			"id": "old-job", "url": "https://www.youtube.com/watch?v=abc&token=private", "videoID": "abc", "title": "Title", "quality": "best", "outputDir": "downloads", "filename": "Title.mp4", "status": "active", "createdAt": "2026-01-02T03:04:05Z",
		}}},
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
	if job.Lifecycle != jobmodel.LifecyclePaused || job.Desired != jobmodel.DesiredPaused || job.RetryMode != jobmodel.RetryModeRestartNewSession {
		t.Fatalf("migrated job = %#v", job)
	}
	if job.Request.SourceURL != "https://www.youtube.com/watch" || job.Reservation.Artifacts[0].Basename != "Title.mp4" {
		t.Fatalf("unsafe or missing migration fields: %#v", job)
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

func TestOpenV2MigratesUnverifiableDestinationToActionRequired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := []byte(`{"version":1,"settings":{"downloadConcurrency":2},"history":[],"jobs":[{"snapshot":{"id":"old","url":"https://example.test/v","outputDir":"out","status":"pending"}}]}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	s, status, err := OpenV2(path)
	if err != nil || !status.Healthy() {
		t.Fatalf("OpenV2: %v %#v %v", s, status, err)
	}
	job := s.Snapshot().Jobs[0]
	if job.Lifecycle != jobmodel.LifecycleActionRequired || job.ActionRequiredCode != "migration-destination-unverified" {
		t.Fatalf("job = %#v", job)
	}
}

func TestV2TransactionDeepCloneAndStaleRevision(t *testing.T) {
	s := openV2TestStore(t)
	if err := s.Transaction(nil, func(state *jobmodel.State) error { state.Jobs = append(state.Jobs, testJob()); return nil }); err != nil {
		t.Fatal(err)
	}
	before := s.Snapshot()
	if err := s.Transaction([]JobPrecondition{{ID: "job-1", Revision: 1, Lifecycle: jobmodel.LifecyclePaused, SessionID: "session-1", OutputRoot: before.Jobs[0].OutputRoot}}, func(state *jobmodel.State) error {
		state.Jobs[0].Revision++
		state.Jobs[0].Reservation.Artifacts[0].Basename = "changed.mp4"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if before.Jobs[0].Reservation.Artifacts[0].Basename != "original.mp4" {
		t.Fatalf("snapshot aliased transaction clone: %#v", before)
	}
	stale := s.Snapshot()
	err := s.Transaction([]JobPrecondition{{ID: "job-1", Revision: 1, Lifecycle: jobmodel.LifecyclePaused, SessionID: "session-1", OutputRoot: stale.Jobs[0].OutputRoot}}, func(*jobmodel.State) error { return nil })
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
			if tc.committed || tc.indeterminate {
				if got.Settings.FFmpegPath != "new" || got.StoreRevision != old+1 {
					t.Fatalf("new image not adopted: %#v", got)
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
	return jobmodel.DurableJob{ID: "job-1", Revision: 1, AttemptID: "attempt-1", SessionID: "session-1", QueueOrdinal: 1, Lifecycle: jobmodel.LifecyclePaused, Phase: jobmodel.PhasePreparing, Desired: jobmodel.DesiredPaused, OutputRoot: root, Reservation: jobmodel.ReservationSet{GroupID: "job-1", Directory: root, Artifacts: []jobmodel.ReservedArtifact{{Kind: "primary", Identity: "primary", Basename: "original.mp4"}}}, RetryMode: jobmodel.RetryModeNone, CreatedAt: now, UpdatedAt: now}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
