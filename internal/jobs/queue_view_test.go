package jobs

import (
	"errors"
	"math"
	"testing"

	"github.com/tejasa97/vidstow/internal/jobmodel"
)

type queueTestCommitOutcome struct {
	err           error
	committed     bool
	indeterminate bool
}

func (e queueTestCommitOutcome) Error() string       { return e.err.Error() }
func (e queueTestCommitOutcome) Unwrap() error       { return e.err }
func (e queueTestCommitOutcome) Committed() bool     { return e.committed }
func (e queueTestCommitOutcome) Indeterminate() bool { return e.indeterminate }

func TestQueueViewLifecycleCapabilitiesAndAggregates(t *testing.T) {
	m := New(nil, nil)
	defer m.Close()
	m.mu.Lock()
	m.concurrency = 2
	m.all = map[string]*jobState{
		"pending":    {snap: JobSnapshot{ID: "pending", Status: StatusPending, Lifecycle: jobmodel.LifecyclePending}, commandToken: "pending-token"},
		"active":     {snap: JobSnapshot{ID: "active", Status: StatusActive, Lifecycle: jobmodel.LifecycleActive, CanPause: true}, commandToken: "active-token"},
		"processing": {snap: JobSnapshot{ID: "processing", Status: StatusActive, Lifecycle: jobmodel.LifecycleActive, Processing: true}, commandToken: "processing-token"},
		"pausing":    {snap: JobSnapshot{ID: "pausing", Status: StatusPausing, Lifecycle: jobmodel.LifecyclePausing}, commandToken: "pausing-token"},
		"paused":     {snap: JobSnapshot{ID: "paused", Status: StatusPaused, Lifecycle: jobmodel.LifecyclePaused}, commandToken: "paused-token"},
		"failed":     {snap: JobSnapshot{ID: "failed", Status: StatusFailed, Lifecycle: jobmodel.LifecycleFailed}, commandToken: "failed-token"},
		"canceled":   {snap: JobSnapshot{ID: "canceled", Status: StatusCanceled, Lifecycle: jobmodel.LifecycleCanceled}, commandToken: "canceled-token"},
		"completed":  {snap: JobSnapshot{ID: "completed", Status: StatusComplete, Lifecycle: jobmodel.LifecycleCompleted, AbsolutePath: "/safe/file"}, commandToken: "completed-token"},
		"action":     {snap: JobSnapshot{ID: "action", Status: StatusActionRequired, Lifecycle: jobmodel.LifecycleActionRequired, Phase: jobmodel.PhaseReadyToPublish}, commandToken: "action-token"},
	}
	m.active = map[string]*worker{"active": {}, "processing": {}, "pausing": {}}
	m.order = []string{"pending"}
	m.queueCommandToken = "queue-token"
	view := m.queueViewLocked()
	m.mu.Unlock()

	if got, want := view.Summary, (QueueSummary{TotalJobs: 9, RunningJobs: 3, OccupiedSlots: 3, SlotLimit: 2, ProcessingOccupied: 1, ProcessingLimit: MaxProcessingConcurrency, WaitingJobs: 1, PausedJobs: 1}); got != want {
		t.Fatalf("summary = %#v; want %#v", got, want)
	}
	rows := map[string]QueueRow{}
	for _, row := range view.Rows {
		rows[row.ID] = row
	}
	if !rows["pending"].Capabilities.Pause || !rows["pending"].Capabilities.Cancel || rows["pending"].QueuePosition != 1 {
		t.Fatal("pending row did not retain backend capability/order facts")
	}
	if !rows["active"].Capabilities.Pause || !rows["active"].OccupiesSlot {
		t.Fatal("active row capability/occupancy incorrect")
	}
	if rows["processing"].Capabilities.Pause || !rows["processing"].OccupiesSlot {
		t.Fatal("processing row derived unsafe pause capability")
	}
	if !rows["paused"].Capabilities.Resume || !rows["paused"].Capabilities.Cancel {
		t.Fatal("paused row capabilities incorrect")
	}
	if !rows["failed"].Capabilities.Retry || rows["failed"].Capabilities.DownloadAgain {
		t.Fatal("failed row capability incorrect")
	}
	if rows["canceled"].Capabilities.DownloadAgain || !rows["canceled"].Capabilities.Remove {
		t.Fatal("canceled row must fail closed until persisted-plan re-admission exists")
	}
	if !rows["completed"].Capabilities.Open || !rows["completed"].Capabilities.Remove {
		t.Fatal("completed row capability incorrect")
	}
	if rows["action"].Capabilities != (QueueJobCapabilities{}) {
		t.Fatal("action-required row must fail closed without destination review facade")
	}
	if !view.Capabilities.PauseAll || !view.Capabilities.ClearCompleted {
		t.Fatal("queue-wide capabilities not backend authored")
	}
}

func TestQueueViewAuthorsCollectionCapabilitiesAndConsumesParentToken(t *testing.T) {
	store, _, _ := newV2TestStore(t, "one", "two")
	state := store.Snapshot()
	now := state.Jobs[0].CreatedAt
	state.Collections = []jobmodel.DurableCollection{{
		ID: "collection", Revision: 1, PlaylistID: "PLfixture", SourceURL: "https://www.youtube.com/playlist?list=PLfixture",
		Title: "Fixture playlist", Channel: "Creator", Policy: "video:1080p", ChildJobIDs: []string{"one", "two"}, CreatedAt: now, UpdatedAt: now,
	}}
	for index := range state.Jobs {
		state.Jobs[index].CollectionID = "collection"
		state.Jobs[index].CollectionIndex = index + 1
	}
	store.state = state
	m := New(nil, nil)
	defer m.Close()
	if err := m.SetStateStore(store); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	for _, durable := range store.Snapshot().Jobs {
		job := &jobState{snap: JobSnapshot{ID: durable.ID, Status: StatusPending, Lifecycle: jobmodel.LifecyclePending, Desired: jobmodel.DesiredRunning}, durable: durable, fromStateV2: true, done: make(chan struct{}), commandToken: durable.ID + "-token"}
		m.all[durable.ID] = job
		m.order = append(m.order, durable.ID)
	}
	m.emitQueueLocked()
	m.mu.Unlock()

	view := m.QueueView()
	if len(view.Collections) != 1 || len(view.Collections[0].ChildJobIDs) != 2 || !view.Collections[0].Capabilities.Pause || !view.Collections[0].Capabilities.Cancel || view.Collections[0].CommandToken == "" {
		t.Fatalf("collection view = %#v", view.Collections)
	}
	for index, row := range view.Rows {
		if row.CollectionID != "collection" || row.CollectionIndex != index+1 {
			t.Fatalf("child row[%d] = %#v", index, row)
		}
	}
	count, err := m.QueuePauseCollection("collection", view.Collections[0].CommandToken)
	if err != nil || count != 2 {
		t.Fatalf("Pause collection = %d, %v", count, err)
	}
	if _, err := m.QueuePauseCollection("collection", view.Collections[0].CommandToken); err == nil {
		t.Fatal("consumed collection token was reused")
	}
	for _, durable := range store.Snapshot().Jobs {
		if durable.Lifecycle != jobmodel.LifecyclePaused {
			t.Fatalf("child %s lifecycle = %s", durable.ID, durable.Lifecycle)
		}
	}
	pausedView := m.QueueView()
	if len(pausedView.Collections) != 1 || !pausedView.Collections[0].Capabilities.Resume || pausedView.Collections[0].Capabilities.Pause {
		t.Fatalf("paused collection capabilities = %#v", pausedView.Collections)
	}
}

func TestQueueTelemetryLabelsAreSafeAndHumanReadable(t *testing.T) {
	for _, test := range []struct {
		name string
		in   float64
		want string
	}{
		{"zero", 0, ""}, {"negative", -1, ""}, {"NaN", math.NaN(), ""}, {"infinity", math.Inf(1), ""},
		{"bytes", 1023, "1023 B/s"}, {"kibibyte", 1024, "1.0 KiB/s"}, {"mebibyte", 1024 * 1024, "1.0 MiB/s"}, {"gibibyte", 1024 * 1024 * 1024, "1.0 GiB/s"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := queueSpeedLabel(test.in); got != test.want {
				t.Fatalf("queueSpeedLabel(%v) = %q; want %q", test.in, got, test.want)
			}
		})
	}
	for _, test := range []struct {
		name string
		in   float64
		want string
	}{
		{"zero", 0, ""}, {"negative", -1, ""}, {"NaN", math.NaN(), ""}, {"infinity", math.Inf(1), ""},
		{"seconds", 1, "0:01"}, {"minute boundary", 60, "1:00"}, {"hour boundary", 3600, "1:00:00"}, {"long ETA", 27*60*60 + 5*60 + 9, "27:05:09"}, {"bounded ETA", math.MaxFloat64, "99:59:59"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := queueETALabel(test.in); got != test.want {
				t.Fatalf("queueETALabel(%v) = %q; want %q", test.in, got, test.want)
			}
		})
	}
}

func TestQueueCommandsFailClosedForPersistenceAndUseExactCompletedBatch(t *testing.T) {
	store, _, _ := newV2TestStore(t, "completed", "failed")
	state := store.Snapshot()
	state.Jobs[0].Lifecycle = jobmodel.LifecycleCompleted
	state.Jobs[0].Desired = jobmodel.DesiredRunning
	state.Jobs[1].Lifecycle = jobmodel.LifecycleFailed
	store.state = state
	m := New(nil, nil)
	defer m.Close()
	if err := m.SetStateStore(store); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	for _, durable := range store.Snapshot().Jobs {
		job, err := stateFromDurable(durable)
		if err != nil {
			t.Fatal(err)
		}
		m.all[durable.ID] = job
	}
	m.emitQueueLocked()
	m.mu.Unlock()

	view := m.QueueView()
	if !view.Capabilities.ClearCompleted {
		t.Fatal("completed row should authorize Clear completed")
	}
	store.failNext = errors.New("disk full")
	if err := m.QueueClearCompleted(view.Capabilities.CommandToken); err == nil {
		t.Fatal("clear completed accepted a failed durable batch")
	}
	if got := len(store.Snapshot().Jobs); got != 2 {
		t.Fatalf("failed clear changed durable rows: %d", got)
	}
	view = m.QueueView()
	if !view.Capabilities.ClearCompleted || view.Capabilities.CommandToken == "" {
		t.Fatal("failed batch did not restore fresh clear authority")
	}
	if err := m.QueueClearCompleted(view.Capabilities.CommandToken); err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot().Jobs; len(got) != 1 || got[0].ID != "failed" {
		t.Fatalf("clear completed scope = %#v", got)
	}

	m.persistStatus = PersistenceStatus{Available: true, Healthy: false}
	view = m.QueueView()
	if view.Capabilities.ClearCompleted || view.Rows[0].Capabilities != (QueueJobCapabilities{}) {
		t.Fatal("unhealthy persistence exposed queue authority")
	}
	m.persistStatus = PersistenceStatus{Available: false, Healthy: false}
	view = m.QueueView()
	if view.Capabilities.ClearCompleted || view.Rows[0].Capabilities != (QueueJobCapabilities{}) {
		t.Fatal("unavailable persistence exposed queue authority")
	}
}

func TestQueuePauseAllConsumesExactSnapshotToken(t *testing.T) {
	store, _, _ := newV2TestStore(t, "one", "two")
	m := New(nil, nil)
	defer m.Close()
	if err := m.SetStateStore(store); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	for _, durable := range store.Snapshot().Jobs {
		job := &jobState{snap: JobSnapshot{ID: durable.ID, Status: StatusPending, Lifecycle: jobmodel.LifecyclePending, Desired: jobmodel.DesiredRunning}, durable: durable, fromStateV2: true, done: make(chan struct{}), commandToken: durable.ID + "-token"}
		m.all[durable.ID] = job
		m.order = append(m.order, durable.ID)
	}
	m.emitQueueLocked()
	m.mu.Unlock()
	view := m.QueueView()
	count, err := m.QueuePauseAll(view.Capabilities.CommandToken)
	if err != nil || count != 2 {
		t.Fatalf("Pause All = %d, %v; want 2 accepted", count, err)
	}
	if _, err := m.QueuePauseAll(view.Capabilities.CommandToken); err == nil {
		t.Fatal("Pause All reused a consumed token")
	}
	for _, durable := range store.Snapshot().Jobs {
		if durable.Lifecycle != jobmodel.LifecyclePaused {
			t.Fatalf("%s lifecycle = %s", durable.ID, durable.Lifecycle)
		}
	}
}

func TestQueueViewRotatesRowTokensAndRejectsStaleAuthority(t *testing.T) {
	m := New(nil, nil)
	defer m.Close()
	m.mu.Lock()
	state := &jobState{snap: JobSnapshot{ID: "paused", Status: StatusPaused, Lifecycle: jobmodel.LifecyclePaused}, commandToken: "first-token"}
	m.all = map[string]*jobState{"paused": state}
	m.queueCommandToken = "queue-token"
	first := m.queueViewLocked()
	state.snap.Status = StatusPending
	state.snap.Lifecycle = jobmodel.LifecyclePending
	m.emitQueueLocked() // represents the next accepted lifecycle/queue snapshot.
	second := m.queueViewLocked()
	m.mu.Unlock()
	if first.Rows[0].CommandToken == second.Rows[0].CommandToken {
		t.Fatal("row token was reused after queue snapshot")
	}
	if err := m.QueueResume("paused", first.Rows[0].CommandToken); err == nil {
		t.Fatal("stale row token was accepted after lifecycle snapshot changed")
	}
}

func TestIntermediateJobUpdateRejectsPriorRowAndQueueAuthority(t *testing.T) {
	events := make(chan Event, 8)
	m := New(nil, func(event Event) { events <- event })
	defer m.Close()
	m.mu.Lock()
	state := &jobState{snap: JobSnapshot{ID: "job", Status: StatusPending, Lifecycle: jobmodel.LifecyclePending}, commandToken: "seed"}
	m.all = map[string]*jobState{"job": state}
	m.emitQueueLocked()
	m.mu.Unlock()
	<-events // initial queue view
	old := m.QueueView()
	oldRowToken := old.Rows[0].CommandToken
	oldQueueToken := old.Capabilities.CommandToken

	m.mu.Lock()
	state.snap.Status = StatusComplete
	state.snap.Lifecycle = jobmodel.LifecycleCompleted
	m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	m.mu.Unlock()
	intermediate := <-events
	if intermediate.Name != EventJobUpdate || intermediate.QueueView == nil {
		t.Fatal("job update did not carry QueueView")
	}
	if got := intermediate.QueueView.Rows[0].CommandToken; got == oldRowToken {
		t.Fatal("job:update retained pending-row token after completed capabilities were published")
	}
	if got := intermediate.QueueView.Capabilities.CommandToken; got == oldQueueToken {
		t.Fatal("job:update retained queue token after action candidates changed")
	}
	if err := m.QueuePause("job", oldRowToken); err == nil {
		t.Fatal("pending-row token authorized completed Pause")
	}
	if !intermediate.QueueView.Capabilities.ClearCompleted {
		t.Fatal("completed job did not publish Clear completed authority")
	}
	if err := m.QueueClearCompleted(oldQueueToken); err == nil {
		t.Fatal("Pause All token authorized newly available Clear completed")
	}
	if err := m.QueueRemove("job", intermediate.QueueView.Rows[0].CommandToken); err != nil {
		t.Fatalf("current completed token was not usable: %v", err)
	}
}

func TestRowAuthorizationRefreshesAnUnseenCapabilityChange(t *testing.T) {
	m := New(nil, nil)
	defer m.Close()
	m.mu.Lock()
	state := &jobState{snap: JobSnapshot{ID: "job", Status: StatusPending, Lifecycle: jobmodel.LifecyclePending}}
	m.all = map[string]*jobState{"job": state}
	m.order = []string{"job"}
	m.emitQueueLocked()
	m.mu.Unlock()
	before := m.QueueView()

	// Model a backend state change that has occurred but whose UI event has not
	// yet been observed. Authorization must refresh the signature itself.
	m.mu.Lock()
	state.snap.Status = StatusComplete
	state.snap.Lifecycle = jobmodel.LifecycleCompleted
	m.mu.Unlock()
	if err := m.QueuePause("job", before.Rows[0].CommandToken); err == nil {
		t.Fatal("unseen capability change accepted a pending Pause token")
	}
	if err := m.QueueClearCompleted(before.Capabilities.CommandToken); err == nil {
		t.Fatal("unseen capability change accepted a Pause All queue token")
	}
}

func TestQueueWideAuthorizationRefreshesUnseenCapabilityChanges(t *testing.T) {
	for _, test := range []struct {
		name        string
		initial     Status
		initialLife jobmodel.Lifecycle
		next        Status
		nextLife    jobmodel.Lifecycle
		command     func(*Manager, string) error
	}{
		{
			name: "Pause All token cannot clear completed", initial: StatusPending, initialLife: jobmodel.LifecyclePending,
			next: StatusComplete, nextLife: jobmodel.LifecycleCompleted,
			command: func(m *Manager, token string) error { return m.QueueClearCompleted(token) },
		},
		{
			name: "Clear completed token cannot pause all", initial: StatusComplete, initialLife: jobmodel.LifecycleCompleted,
			next: StatusPending, nextLife: jobmodel.LifecyclePending,
			command: func(m *Manager, token string) error { _, err := m.QueuePauseAll(token); return err },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := New(nil, nil)
			defer m.Close()
			m.mu.Lock()
			state := &jobState{snap: JobSnapshot{ID: "job", Status: test.initial, Lifecycle: test.initialLife}}
			m.all = map[string]*jobState{"job": state}
			m.order = []string{"job"}
			m.emitQueueLocked()
			m.mu.Unlock()
			before := m.QueueView()
			if before.Capabilities.CommandToken == "" {
				t.Fatal("initial queue action had no token")
			}
			m.mu.Lock()
			state.snap.Status = test.next
			state.snap.Lifecycle = test.nextLife
			m.mu.Unlock()
			if err := test.command(m, before.Capabilities.CommandToken); err == nil {
				t.Fatal("unseen capability change accepted an old queue-wide token")
			}
		})
	}
}

func TestProgressOnlyJobUpdatePreservesUsableAuthority(t *testing.T) {
	events := make(chan Event, 8)
	m := New(nil, func(event Event) { events <- event })
	defer m.Close()
	m.mu.Lock()
	state := &jobState{snap: JobSnapshot{ID: "job", Status: StatusPending, Lifecycle: jobmodel.LifecyclePending}, commandToken: "seed"}
	m.all = map[string]*jobState{"job": state}
	m.order = []string{"job"}
	m.emitQueueLocked()
	m.mu.Unlock()
	<-events
	before := m.QueueView()

	m.mu.Lock()
	state.snap.Progress = 0.5
	state.snap.SpeedBps = 1024
	state.snap.ETASeconds = 61
	m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	m.mu.Unlock()
	event := <-events
	if event.QueueView == nil {
		t.Fatal("progress job:update did not carry QueueView")
	}
	after := event.QueueView
	if after.Rows[0].CommandToken != before.Rows[0].CommandToken || after.Capabilities.CommandToken != before.Capabilities.CommandToken {
		t.Fatal("progress-only update rotated authority")
	}
	if after.Rows[0].SpeedLabel != "1.0 KiB/s" || after.Rows[0].ETALabel != "1:01" {
		t.Fatalf("progress labels = %#v", after.Rows[0])
	}
	if err := m.QueuePause("job", before.Rows[0].CommandToken); err != nil {
		t.Fatalf("progress-preserved row token was not usable: %v", err)
	}
}

func TestCommitOutcomeRevokesQueueAuthorityButStaleErrorsDoNot(t *testing.T) {
	for _, test := range []struct {
		name    string
		err     error
		revoked bool
	}{
		{"stale precondition", errors.New("stale revision"), false},
		{"pre-commit failure", queueTestCommitOutcome{err: errors.New("disk full")}, true},
		{"committed warning", queueTestCommitOutcome{err: errors.New("durability warning"), committed: true}, true},
		{"indeterminate", queueTestCommitOutcome{err: errors.New("authority unknown"), indeterminate: true}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _, _ := newV2TestStore(t, "job")
			m := New(nil, nil)
			defer m.Close()
			if err := m.SetStateStore(store); err != nil {
				t.Fatal(err)
			}
			durable := store.Snapshot().Jobs[0]
			durable.Lifecycle = jobmodel.LifecyclePaused
			durable.Desired = jobmodel.DesiredPaused
			store.state.Jobs[0] = durable
			state := &jobState{snap: JobSnapshot{ID: durable.ID, Status: StatusPaused, Lifecycle: jobmodel.LifecyclePaused, Desired: jobmodel.DesiredPaused}, durable: durable, fromStateV2: true, done: make(chan struct{})}
			m.mu.Lock()
			m.all[durable.ID] = state
			m.order = []string{durable.ID}
			m.emitQueueLocked()
			m.mu.Unlock()
			view := m.QueueView()
			store.failNext = test.err
			if err := m.QueueResume(durable.ID, view.Rows[0].CommandToken); err == nil {
				t.Fatal("injected transaction failure was accepted")
			}
			view = m.QueueView()
			if got := !(view.Persistence.Available && view.Persistence.Healthy); got != test.revoked {
				t.Fatalf("persistence revoked = %t; want %t (%#v)", got, test.revoked, view.Persistence)
			}
			if test.revoked && (view.Capabilities.PauseAll || view.Rows[0].Capabilities != (QueueJobCapabilities{})) {
				t.Fatal("uncertain commit left queue authority enabled")
			}
			if !test.revoked && !view.Rows[0].Capabilities.Resume {
				t.Fatal("ordinary stale error poisoned persistence authority")
			}
		})
	}
}
