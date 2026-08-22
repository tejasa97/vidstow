package admission

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/tejasa97/vidstow/internal/jobmodel"
	"github.com/tejasa97/vidstow/internal/jobs"
	"github.com/tejasa97/vidstow/internal/outputplan"
	"github.com/tejasa97/vidstow/internal/reservationfs"
	"github.com/tejasa97/vidstow/internal/store"
	"github.com/tejasa97/youtube_dlp/engine/value"
)

type recordingQueue struct {
	store   *store.V2Store
	ids     []string
	jobs    []jobs.Request
	plans   []outputplan.Plan
	outputs []jobs.AdmittedOutput
	err     error
}

type staticPlanResolver struct {
	plan outputplan.Plan
	err  error
}

func (r staticPlanResolver) ResolvePlan(string, string) (outputplan.Plan, error) {
	return r.plan, r.err
}

func (q *recordingQueue) SubmitAdmitted(id string, request jobs.Request, plan *outputplan.Plan, output jobs.AdmittedOutput) (string, error) {
	if q.err != nil {
		return "", q.err
	}
	state := q.store.Snapshot()
	found := false
	for _, job := range state.Jobs {
		if job.ID == id {
			found = true
			break
		}
	}
	if !found {
		return "", errors.New("durable job was not committed before FIFO admission")
	}
	q.ids = append(q.ids, id)
	q.jobs = append(q.jobs, request)
	if plan != nil {
		q.plans = append(q.plans, *plan)
	}
	q.outputs = append(q.outputs, output)
	return id, nil
}

func TestCoordinatorCommitsReservationBeforeFIFOAdmission(t *testing.T) {
	outputDir := t.TempDir()
	root, err := reservationfs.OpenRoot(outputDir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer root.Close()

	statePath := filepath.Join(t.TempDir(), "state.json")
	state, status, err := store.OpenV2(statePath)
	if err != nil || !status.Healthy() {
		t.Fatalf("OpenV2: %v, %#v", err, status)
	}
	defer state.Close()

	plan := outputplan.Plan{
		ID:             "video-1080-mp4",
		Kind:           outputplan.KindVideo,
		Label:          "1080p",
		Container:      "MP4",
		VideoCodec:     "H.264",
		AudioCodec:     "AAC",
		RequiresFFmpeg: true,
		Selector:       "bv1+ba1",
	}
	queue := &recordingQueue{store: state}
	ids := [][3]string{
		{"job-1", "attempt-1", "0123456789abcdef0123456789abcdef"},
		{"job-2", "attempt-2", "abcdef0123456789abcdef0123456789"},
	}
	next := 0
	coordinator, err := NewCoordinator(Dependencies{
		Store:    state,
		Resolver: staticPlanResolver{plan: plan},
		Queue:    queue,
		Now: func() time.Time {
			return time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
		},
		NewIDs: func() (string, string, string) {
			current := ids[next]
			next++
			return current[0], current[1], current[2]
		},
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	request := Request{
		Queue: jobs.Request{
			URL:       "https://www.youtube.com/watch?v=abc123",
			VideoID:   "abc123",
			Title:     "A title",
			Channel:   "A channel",
			Quality:   jobs.Quality1080p,
			PlanID:    plan.ID,
			OutputDir: outputDir,
			Duration:  "1:23",
		},
		Metadata: value.NewInfo(value.NewObject()),
	}

	first, err := coordinator.Admit(context.Background(), root, request)
	if err != nil {
		t.Fatalf("first Admit: %v", err)
	}
	second, err := coordinator.Admit(context.Background(), root, request)
	if err != nil {
		t.Fatalf("second Admit: %v", err)
	}

	if len(queue.ids) != 2 || queue.ids[0] != first.Job.ID || queue.ids[1] != second.Job.ID {
		t.Fatalf("FIFO admissions = %#v, want %#v then %#v", queue.ids, first.Job.ID, second.Job.ID)
	}
	if len(first.Artifacts) != 1 || len(second.Artifacts) != 1 {
		t.Fatalf("artifact declarations = %#v, %#v", first.Artifacts, second.Artifacts)
	}
	if len(queue.outputs) != 2 || queue.outputs[0].Basename != first.Reservation.Artifacts[0].Basename || queue.outputs[1].Basename != second.Reservation.Artifacts[0].Basename {
		t.Fatalf("runtime targets = %#v, want one exact target per committed reservation", queue.outputs)
	}
	if got, want := first.Artifacts[0].ProposedBasename, "A title [abc123] [1080p].mp4"; got != want {
		t.Fatalf("first proposed basename = %q, want %q", got, want)
	}
	if got, want := first.Reservation.Artifacts[0].Basename, first.Artifacts[0].ProposedBasename; got != want {
		t.Fatalf("first reservation basename = %q, want %q", got, want)
	}
	if got, want := queue.outputs[1].Basename, second.Reservation.Artifacts[0].Basename; got != want {
		t.Fatalf("second FIFO target = %q, want committed basename %q", got, want)
	}
	if got, want := second.Reservation.Artifacts[0].Basename, "A title [abc123] [1080p] (2).mp4"; got != want {
		t.Fatalf("second reservation basename = %q, want %q", got, want)
	}
	if first.Job.QueueOrdinal != 1 || second.Job.QueueOrdinal != 2 {
		t.Fatalf("queue ordinals = %d, %d; want 1, 2", first.Job.QueueOrdinal, second.Job.QueueOrdinal)
	}
	if first.Job.Lifecycle != "pending" || first.Job.Reservation.GroupID != first.Job.ID {
		t.Fatalf("first durable job = %#v", first.Job)
	}
	if got := state.Snapshot().NextQueueOrdinal; got != 3 {
		t.Fatalf("next queue ordinal = %d, want 3", got)
	}
}

func TestCoordinatorRejectsMismatchedOutputRoot(t *testing.T) {
	rootPath := t.TempDir()
	otherPath := t.TempDir()
	root, err := reservationfs.OpenRoot(rootPath)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer root.Close()
	state, status, err := store.OpenV2(filepath.Join(t.TempDir(), "state.json"))
	if err != nil || !status.Healthy() {
		t.Fatalf("OpenV2: %v, %#v", err, status)
	}
	defer state.Close()
	plan := outputplan.Plan{ID: "audio-mp3", Kind: outputplan.KindAudio, Label: "MP3", Container: "MP3", Selector: "ba1"}
	coordinator, err := NewCoordinator(Dependencies{
		Store:    state,
		Resolver: staticPlanResolver{plan: plan},
		Queue:    &recordingQueue{store: state},
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	_, err = coordinator.Admit(context.Background(), root, Request{
		Queue: jobs.Request{URL: "https://www.youtube.com/watch?v=abc123", VideoID: "abc123", Title: "Title", PlanID: plan.ID, OutputDir: otherPath},
	})
	if err == nil {
		t.Fatal("Admit() error = nil, want root mismatch")
	}
	if got := len(state.Snapshot().Jobs); got != 0 {
		t.Fatalf("state jobs = %d, want 0", got)
	}
}

func TestCoordinatorConcurrentAdmissionsChooseDistinctWholeSetNames(t *testing.T) {
	outputDir := t.TempDir()
	root, err := reservationfs.OpenRoot(outputDir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer root.Close()
	state, status, err := store.OpenV2(filepath.Join(t.TempDir(), "state.json"))
	if err != nil || !status.Healthy() {
		t.Fatalf("OpenV2: %v, %#v", err, status)
	}
	defer state.Close()

	plan := outputplan.Plan{ID: "audio-mp3", Kind: outputplan.KindAudio, Label: "MP3", Container: "MP3", Selector: "ba1"}
	makeRequest := func() Request {
		return Request{
			Queue: jobs.Request{
				URL:       "https://www.youtube.com/watch?v=abc123",
				VideoID:   "abc123",
				Title:     "Concurrent title",
				Quality:   jobs.QualityAudioOnly,
				PlanID:    plan.ID,
				OutputDir: outputDir,
			},
		}
	}
	ids := [][3]string{
		{"job-a", "attempt-a", "0123456789abcdef0123456789abcdef"},
		{"job-b", "attempt-b", "abcdef0123456789abcdef0123456789"},
	}
	queues := []*recordingQueue{{store: state}, {store: state}}
	coordinators := make([]*Coordinator, 2)
	for i := range coordinators {
		index := i
		coordinator, coordinatorErr := NewCoordinator(Dependencies{
			Store:    state,
			Resolver: staticPlanResolver{plan: plan},
			Queue:    queues[i],
			Now: func() time.Time {
				return time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
			},
			NewIDs: func() (string, string, string) {
				current := ids[index]
				return current[0], current[1], current[2]
			},
		})
		if coordinatorErr != nil {
			t.Fatalf("NewCoordinator[%d]: %v", i, coordinatorErr)
		}
		coordinators[i] = coordinator
	}

	var wait sync.WaitGroup
	errorsCh := make(chan error, len(coordinators))
	for i := range coordinators {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, admitErr := coordinators[index].Admit(context.Background(), root, makeRequest())
			errorsCh <- admitErr
		}(i)
	}
	wait.Wait()
	close(errorsCh)
	for admitErr := range errorsCh {
		if admitErr != nil {
			t.Fatalf("concurrent Admit: %v", admitErr)
		}
	}

	stateSnapshot := state.Snapshot()
	if len(stateSnapshot.Jobs) != 2 {
		t.Fatalf("durable jobs = %d, want 2", len(stateSnapshot.Jobs))
	}
	seen := make(map[string]struct{}, 2)
	for _, job := range stateSnapshot.Jobs {
		if len(job.Reservation.Artifacts) != 1 {
			t.Fatalf("job reservation = %#v", job.Reservation)
		}
		name := job.Reservation.Artifacts[0].Basename
		if _, duplicate := seen[name]; duplicate {
			t.Fatalf("duplicate concurrent reservation %q", name)
		}
		seen[name] = struct{}{}
	}
}

func TestCoordinatorManagerFailureLeavesDurablePendingRow(t *testing.T) {
	plan := outputplan.Plan{ID: "audio-mp3", Kind: outputplan.KindAudio, Label: "MP3", Container: "MP3", Selector: "ba1"}
	coordinator, root, state, request, queue := admissionFixture(t, plan)
	sentinel := errors.New("manager admission failed")
	queue.err = sentinel

	result, err := coordinator.Admit(context.Background(), root, request)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Admit() error = %v, want manager error", err)
	}
	if result.Job.ID == "" || result.Job.Lifecycle != jobmodel.LifecyclePending {
		t.Fatalf("result after manager failure = %#v", result.Job)
	}
	snapshot := state.Snapshot()
	if len(snapshot.Jobs) != 1 || snapshot.Jobs[0].ID != result.Job.ID || snapshot.Jobs[0].Lifecycle != jobmodel.LifecyclePending {
		t.Fatalf("durable state after manager failure = %#v, want one pending row", snapshot.Jobs)
	}
	if len(queue.outputs) != 0 {
		t.Fatalf("FIFO outputs after manager failure = %#v, want none", queue.outputs)
	}
}

func TestCoordinatorRejectsServerPlanWithoutSelector(t *testing.T) {
	plan := outputplan.Plan{ID: "audio-mp3", Kind: outputplan.KindAudio, Label: "MP3", Container: "MP3"}
	coordinator, root, state, request, _ := admissionFixture(t, plan)
	_, err := coordinator.Admit(context.Background(), root, request)
	if err == nil {
		t.Fatal("Admit() error = nil, want incomplete server plan")
	}
	if got := len(state.Snapshot().Jobs); got != 0 {
		t.Fatalf("state jobs = %d, want 0 after rejected server plan", got)
	}
}

func TestCoordinatorClosedRootFailsTransactionWithoutChangingState(t *testing.T) {
	plan := outputplan.Plan{ID: "audio-mp3", Kind: outputplan.KindAudio, Label: "MP3", Container: "MP3", Selector: "ba1"}
	coordinator, root, state, request, _ := admissionFixture(t, plan)
	before := state.Snapshot()
	if err := root.Close(); err != nil {
		t.Fatalf("Close root: %v", err)
	}

	_, err := coordinator.Admit(context.Background(), root, request)
	if err == nil {
		t.Fatal("Admit() error = nil, want closed-root transaction failure")
	}
	if after := state.Snapshot(); !reflect.DeepEqual(after, before) {
		t.Fatalf("State changed after failed transaction: before=%#v after=%#v", before, after)
	}
}

func TestCoordinatorClaimsActiveAndCleanupTombstoneBeforeSelectingSuffix(t *testing.T) {
	plan := outputplan.Plan{ID: "audio-mp3", Kind: outputplan.KindAudio, Label: "MP3", Container: "MP3", Selector: "ba1"}
	coordinator, root, state, request, queue := admissionFixture(t, plan)
	facts := root.Facts()
	rootRef := jobmodel.OutputRootRef{CanonicalPath: facts.Volume.CanonicalPath, Identity: facts.Volume.Identity}
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	base := "Title [abc123] [MP3].mp3"
	second := "Title [abc123] [MP3] (2).mp3"
	if err := state.Transaction(nil, func(snapshot *jobmodel.State) error {
		activeReservation := jobmodel.ReservationSet{
			GroupID:   "existing-job",
			Directory: rootRef,
			Artifacts: []jobmodel.ReservedArtifact{{Kind: "primary", Identity: "primary", Basename: base}},
		}
		snapshot.Jobs = append(snapshot.Jobs, jobmodel.DurableJob{
			ID:           "existing-job",
			Revision:     1,
			AttemptID:    "existing-attempt",
			SessionID:    "11111111111111111111111111111111",
			QueueOrdinal: 1,
			Lifecycle:    jobmodel.LifecyclePaused,
			Phase:        jobmodel.PhasePreparing,
			Desired:      jobmodel.DesiredPaused,
			Request: jobmodel.PersistedRequest{
				SourceURL: request.Queue.URL,
				VideoID:   request.Queue.VideoID,
				Title:     request.Queue.Title,
				Quality:   string(request.Queue.Quality),
				PlanID:    request.Queue.PlanID,
			},
			Plan: jobmodel.PersistedPlan{
				ID: request.Queue.PlanID, Kind: string(plan.Kind), Label: plan.Label,
				Container: plan.Container, PrivateSelector: plan.Selector,
			},
			OutputRoot: rootRef, Reservation: activeReservation, RetryMode: jobmodel.RetryModeNone,
			CreatedAt: now, UpdatedAt: now,
		})
		snapshot.Cleanup = append(snapshot.Cleanup, jobmodel.CleanupTombstone{
			JobID: "cleanup-job", SessionID: "22222222222222222222222222222222", OutputRoot: rootRef,
			Reservation: jobmodel.ReservationSet{
				GroupID: "cleanup-job", Directory: rootRef,
				Artifacts: []jobmodel.ReservedArtifact{{Kind: "primary", Identity: "primary", Basename: second}},
			},
			State: jobmodel.CleanupPending, CreatedAt: now, UpdatedAt: now,
		})
		snapshot.NextQueueOrdinal = 2
		return nil
	}); err != nil {
		t.Fatalf("seed claims: %v", err)
	}

	result, err := coordinator.Admit(context.Background(), root, request)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if len(result.Reservation.Artifacts) != 1 || len(queue.outputs) != 1 {
		t.Fatalf("runtime target count = reservation=%d queue=%d, want one each", len(result.Reservation.Artifacts), len(queue.outputs))
	}
	if got, want := result.Reservation.Artifacts[0].Basename, "Title [abc123] [MP3] (3).mp3"; got != want {
		t.Fatalf("selected basename = %q, want %q", got, want)
	}
	if got, want := queue.outputs[0].Basename, result.Reservation.Artifacts[0].Basename; got != want {
		t.Fatalf("runtime target = %q, want committed basename %q", got, want)
	}
}

func TestActiveReservationsPreserveActionRequiredDestinationAndDeduplicateCleanup(t *testing.T) {
	root := jobmodel.OutputRootRef{CanonicalPath: "/tmp/downloads", Identity: "volume-1"}
	reservationSet := jobmodel.ReservationSet{
		GroupID: "job-action", Directory: root,
		Artifacts: []jobmodel.ReservedArtifact{{Kind: "primary", Identity: "primary", Basename: "Video.mp4"}},
	}
	state := jobmodel.State{
		Jobs:    []jobmodel.DurableJob{{ID: "job-action", Lifecycle: jobmodel.LifecycleActionRequired, OutputRoot: root, Reservation: reservationSet}},
		Cleanup: []jobmodel.CleanupTombstone{{JobID: "job-action", SessionID: "0123456789abcdef0123456789abcdef", OutputRoot: root, Reservation: reservationSet, State: jobmodel.CleanupPending}},
	}
	claims := activeReservations(state)
	if len(claims) != 1 || claims[0].Artifacts[0].Basename != "Video.mp4" {
		t.Fatalf("active reservations = %#v; want one preserved destination claim", claims)
	}
}

func admissionFixture(t *testing.T, plan outputplan.Plan) (*Coordinator, *reservationfs.Root, *store.V2Store, Request, *recordingQueue) {
	t.Helper()
	outputDir := t.TempDir()
	root, err := reservationfs.OpenRoot(outputDir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	state, status, err := store.OpenV2(filepath.Join(t.TempDir(), "state.json"))
	if err != nil || !status.Healthy() {
		root.Close()
		t.Fatalf("OpenV2: %v, %#v", err, status)
	}
	queue := &recordingQueue{store: state}
	coordinator, err := NewCoordinator(Dependencies{
		Store: state, Resolver: staticPlanResolver{plan: plan}, Queue: queue,
	})
	if err != nil {
		root.Close()
		state.Close()
		t.Fatalf("NewCoordinator: %v", err)
	}
	t.Cleanup(func() {
		root.Close()
		state.Close()
	})
	return coordinator, root, state, Request{
		Queue: jobs.Request{
			URL: "https://www.youtube.com/watch?v=abc123", VideoID: "abc123", Title: "Title",
			Quality: jobs.QualityAudioOnly, PlanID: plan.ID, OutputDir: outputDir,
		},
	}, queue
}
