package reservation

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
)

func TestSelectChoosesFirstFreeWholeSetSuffix(t *testing.T) {
	t.Parallel()
	root := testVolume(t)
	probe := &recordingProbe{occupied: map[string]bool{"Title.info (2).json": true}}
	selector := testSelector(t, ExactNames{}, probe)
	set, err := selector.Select(context.Background(), SelectionRequest{
		GroupID:   "job-1",
		Directory: root,
		Artifacts: []ArtifactDeclaration{
			{Kind: "primary", Identity: "video", ProposedBasename: "Title.mp4"},
			{Kind: "sidecar", Identity: "metadata", ProposedBasename: "Title.info.json"},
		},
	}, []ReservationSet{{
		GroupID:   "existing-job",
		Directory: root,
		Artifacts: []ReservedArtifact{{Kind: "primary", Identity: "old-video", Basename: "Title.mp4"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	assertBasenames(t, set, "Title (3).mp4", "Title.info (3).json")
}

func TestSelectPreservesExistingNumericTitle(t *testing.T) {
	t.Parallel()
	root := testVolume(t)
	selector := testSelector(t, ExactNames{}, &recordingProbe{occupied: map[string]bool{"Title (2).mp4": true}})
	set, err := selector.Select(context.Background(), singleRequest(root, "Title (2).mp4"), nil)
	if err != nil {
		t.Fatal(err)
	}
	assertBasenames(t, set, "Title (2) (2).mp4")
}

func TestSelectHonoursCaseAndVolumePolicies(t *testing.T) {
	t.Parallel()
	root := testVolume(t)
	selector := testSelector(t, FoldedNames{}, &recordingProbe{})
	request := singleRequest(root, "Title.mp4")
	set, err := selector.Select(context.Background(), request, []ReservationSet{{
		GroupID:   "existing-job",
		Directory: Volume{CanonicalPath: filepath.Join(root.CanonicalPath, "alias"), Identity: root.Identity},
		Artifacts: []ReservedArtifact{{Kind: "primary", Identity: "old-video", Basename: "title.MP4"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	assertBasenames(t, set, "Title (2).mp4")

	set, err = selector.Select(context.Background(), request, []ReservationSet{{
		GroupID:   "other-volume-job",
		Directory: Volume{CanonicalPath: root.CanonicalPath, Identity: "different-volume"},
		Artifacts: []ReservedArtifact{{Kind: "primary", Identity: "old-video", Basename: "Title.mp4"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	assertBasenames(t, set, "Title.mp4")
}

func TestFoldedNamesHasExplicitUnicodeCaseAndNormalizationSemantics(t *testing.T) {
	t.Parallel()
	policy := FoldedNames{}
	if !policy.Equal("Δownload.MP4", "δOWNLOAD.mp4") {
		t.Fatal("FoldedNames did not apply Unicode case folding")
	}
	for _, pair := range [][2]string{{"Σ", "σ"}, {"Σ", "ς"}, {"σ", "ς"}} {
		if !policy.Equal(pair[0], pair[1]) || policy.Key(pair[0]) != policy.Key(pair[1]) {
			t.Fatalf("simple-fold equivalence inconsistent for %q and %q", pair[0], pair[1])
		}
	}
	composed := "Café.mp4"
	decomposed := "Cafe\u0301.mp4"
	if policy.Equal(composed, decomposed) {
		t.Fatal("FoldedNames silently normalized Unicode; a volume policy must state that behavior")
	}
}

func TestActiveIndexRejectsDuplicateGroupsAndDestinationClaims(t *testing.T) {
	t.Parallel()
	root := testVolume(t)
	probe := &recordingProbe{}
	selector := testSelector(t, FoldedNames{}, probe)
	callback, err := selector.Callback(singleRequest(root, "Title.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	duplicateGroup := []ReservationSet{
		{GroupID: "same", Directory: root, Artifacts: []ReservedArtifact{{Kind: "primary", Identity: "one", Basename: "One.mp4"}}},
		{GroupID: "same", Directory: root, Artifacts: []ReservedArtifact{{Kind: "primary", Identity: "two", Basename: "Two.mp4"}}},
	}
	if _, err := callback(context.Background(), duplicateGroup); !errors.Is(err, ErrInvalidReservation) {
		t.Fatalf("duplicate group error = %v, want ErrInvalidReservation", err)
	}
	duplicateClaim := []ReservationSet{
		{GroupID: "one", Directory: root, Artifacts: []ReservedArtifact{{Kind: "primary", Identity: "one", Basename: "Title.mp4"}}},
		{GroupID: "two", Directory: root, Artifacts: []ReservedArtifact{{Kind: "primary", Identity: "two", Basename: "title.MP4"}}},
	}
	if _, err := callback(context.Background(), duplicateClaim); !errors.Is(err, ErrInvalidReservation) {
		t.Fatalf("duplicate claim error = %v, want ErrInvalidReservation", err)
	}
	if _, err := callback(context.Background(), []ReservationSet{{
		GroupID:   "job",
		Directory: root,
		Artifacts: []ReservedArtifact{{Kind: "primary", Identity: "active", Basename: "Other.mp4"}},
	}}); !errors.Is(err, ErrInvalidReservation) {
		t.Fatalf("existing request group error = %v, want ErrInvalidReservation", err)
	}
	if probe.calls != 0 {
		t.Fatalf("invalid active/request group input reached probe %d times", probe.calls)
	}
}

func TestCandidateBasenameCollisionFailsBeforeProbing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		policy NameComparison
	}{
		{name: "exact", policy: ExactNames{}},
		{name: "folded", policy: FoldedNames{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := testVolume(t)
			prefix := strings.Repeat("p", 240)
			caseA, caseB := "A", "A"
			if tc.name == "folded" {
				caseB = "a"
			}
			first := prefix + caseA + "sharedX.mp4"
			second := prefix + caseB + "sharedY.mp4"
			probe := &recordingProbe{}
			selector := testSelector(t, tc.policy, probe)
			callback, err := selector.Callback(SelectionRequest{
				GroupID:   "new-job",
				Directory: root,
				Artifacts: []ArtifactDeclaration{{Kind: "primary", Identity: "one", ProposedBasename: first}, {Kind: "sidecar", Identity: "two", ProposedBasename: second}},
			})
			if err != nil {
				t.Fatal(err)
			}
			active := []ReservationSet{{
				GroupID:   "old-job",
				Directory: root,
				Artifacts: []ReservedArtifact{{Kind: "primary", Identity: "one", Basename: first}, {Kind: "sidecar", Identity: "two", Basename: second}},
			}}
			if _, err := callback(context.Background(), active); !errors.Is(err, ErrNoAvailableName) {
				t.Fatalf("collision error = %v, want ErrNoAvailableName", err)
			}
			if probe.calls != 0 {
				t.Fatalf("candidate collision reached probe %d times", probe.calls)
			}
		})
	}
}

func TestSelectionHasBoundedProbeWorkAndHonoursCancellation(t *testing.T) {
	t.Parallel()
	root := testVolume(t)
	occupied := map[string]bool{"Second.mp4": true}
	for suffix := uint64(2); suffix <= 4; suffix++ {
		occupied[suffixedBasenameForTest("Second.mp4", suffix)] = true
	}
	probe := &recordingProbe{occupied: occupied}
	selector, err := NewSelector(Options{
		Policies:  Policies{Names: ExactNames{}, Volumes: CanonicalVolumes{}},
		Probe:     probe,
		MaxSuffix: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := SelectionRequest{
		GroupID:   "bounded",
		Directory: root,
		Artifacts: []ArtifactDeclaration{{Kind: "primary", Identity: "one", ProposedBasename: "First.mp4"}, {Kind: "primary", Identity: "two", ProposedBasename: "Second.mp4"}},
	}
	if _, err := selector.Select(context.Background(), request, nil); !errors.Is(err, ErrNoAvailableName) {
		t.Fatalf("bounded selection error = %v, want ErrNoAvailableName", err)
	}
	if probe.calls != 8 {
		t.Fatalf("probe calls = %d, want exactly maxSuffix*artifactCount = 8", probe.calls)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancelProbe := &cancelingProbe{cancel: cancel}
	selector = testSelector(t, ExactNames{}, cancelProbe)
	_, err = selector.Select(ctx, request, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled selection error = %v, want context.Canceled", err)
	}
	if cancelProbe.calls != 1 {
		t.Fatalf("canceled selection probes = %d, want one", cancelProbe.calls)
	}
}

func TestSuffixFitsPortableByteLimitWithoutBreakingUTF8(t *testing.T) {
	t.Parallel()
	root := testVolume(t)
	cases := []struct {
		name string
		want string
	}{
		{name: strings.Repeat("a", 251) + ".mp4", want: ".mp4"},
		{name: "aa" + strings.Repeat("界", 83) + ".mp4", want: ".mp4"},
		{name: ".profile", want: ".profile (2)"},
		{name: "Title.tar.gz", want: "Title.tar (2).gz"},
	}
	for _, tc := range cases {
		selector := testSelector(t, ExactNames{}, &recordingProbe{occupied: map[string]bool{tc.name: true}})
		set, err := selector.Select(context.Background(), singleRequest(root, tc.name), nil)
		if err != nil {
			t.Fatalf("%q: %v", tc.name, err)
		}
		got := set.Artifacts[0].Basename
		if !utf8.ValidString(got) || len(got) > MaxBasenameBytes {
			t.Fatalf("%q selected invalid fitted basename %q (%d bytes)", tc.name, got, len(got))
		}
		if !strings.HasSuffix(got, tc.want) && got != tc.want {
			t.Fatalf("%q selected %q, want suffix/filename %q", tc.name, got, tc.want)
		}
		if tc.name == ".profile" || strings.Contains(tc.name, ".tar.") {
			if got != tc.want {
				t.Fatalf("%q selected %q, want %q", tc.name, got, tc.want)
			}
		}
	}
}

func TestInjectedProbeBlocksSymlinkAndNonRegularTargets(t *testing.T) {
	t.Parallel()
	root := testVolume(t)
	for _, target := range []string{"symlink.mp4", "directory.mp4"} {
		selector := testSelector(t, ExactNames{}, &recordingProbe{occupied: map[string]bool{target: true}})
		set, err := selector.Select(context.Background(), singleRequest(root, target), nil)
		if err != nil {
			t.Fatal(err)
		}
		if set.Artifacts[0].Basename == target {
			t.Fatalf("injected probe allowed existing %s target", target)
		}
	}
}

func TestNewSelectorRequiresRootBoundProbeAndPolicies(t *testing.T) {
	t.Parallel()
	if _, err := NewSelector(Options{}); err == nil {
		t.Fatal("NewSelector accepted absent policies and probe")
	}
	if _, err := NewSelector(Options{Policies: Policies{Names: ExactNames{}, Volumes: CanonicalVolumes{}}}); err == nil {
		t.Fatal("NewSelector accepted an absent root-bound probe")
	}
	if _, err := NewSelector(Options{Policies: Policies{Names: ExactNames{}, Volumes: CanonicalVolumes{}}, Probe: &recordingProbe{}, MaxSuffix: ^uint64(0)}); err == nil {
		t.Fatal("NewSelector accepted an unbounded suffix limit")
	}
	selector, err := NewSelector(Options{Policies: Policies{Names: ExactNames{}, Volumes: CanonicalVolumes{}}, Probe: &recordingProbe{occupied: map[string]bool{"Title.mp4": true}}, MaxSuffix: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := selector.Select(context.Background(), singleRequest(testVolume(t), "Title.mp4"), nil); !errors.Is(err, ErrNoAvailableName) {
		t.Fatalf("bounded suffix selection error = %v, want ErrNoAvailableName", err)
	}
}

func TestNewSelectorRejectsTypedNilDependencies(t *testing.T) {
	t.Parallel()
	var nilNames *typedNilNames
	var nilVolumes *typedNilVolumes
	var nilProbe *recordingProbe
	validProbe := &recordingProbe{}
	validPolicies := Policies{Names: ExactNames{}, Volumes: CanonicalVolumes{}}
	if _, err := NewSelector(Options{Policies: Policies{Names: nilNames, Volumes: CanonicalVolumes{}}, Probe: validProbe}); err == nil {
		t.Fatal("NewSelector accepted typed-nil NameComparison")
	}
	if _, err := NewSelector(Options{Policies: Policies{Names: ExactNames{}, Volumes: nilVolumes}, Probe: validProbe}); err == nil {
		t.Fatal("NewSelector accepted typed-nil VolumeComparison")
	}
	if _, err := NewSelector(Options{Policies: validPolicies, Probe: nilProbe}); err == nil {
		t.Fatal("NewSelector accepted typed-nil AvailabilityProbe")
	}
}

func TestRootReplacementProbeSeamFailsClosed(t *testing.T) {
	root := testVolume(t)
	outside := t.TempDir()
	probe := &rootBoundProbe{identity: root.Identity}
	selector := testSelector(t, ExactNames{}, probe)
	if _, err := selector.Select(context.Background(), singleRequest(root, "Title.mp4"), nil); err != nil {
		t.Fatalf("initial selection: %v", err)
	}
	oldRoot := root.CanonicalPath + ".old"
	if err := os.Rename(root.CanonicalPath, oldRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root.CanonicalPath); err != nil {
		if errors.Is(err, os.ErrPermission) {
			t.Skip("symlink creation is not permitted")
		}
		t.Fatal(err)
	}
	_, err := selector.Select(context.Background(), singleRequest(root, "Title.mp4"), nil)
	if !errors.Is(err, errRootChanged) {
		t.Fatalf("selection through swapped root error = %v, want %v", err, errRootChanged)
	}
}

func TestCallbackErrorRollsBackAndReleasesStoreTransaction(t *testing.T) {
	t.Parallel()
	root := testVolume(t)
	probeErr := errors.New("probe failure")
	selector := testSelector(t, ExactNames{}, &recordingProbe{err: probeErr})
	callback, err := selector.Callback(singleRequest(root, "Title.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	store := &callbackStore{}
	_, err = store.Transact(context.Background(), callback, nil)
	if !errors.Is(err, probeErr) {
		t.Fatalf("transaction error = %v, want %v", err, probeErr)
	}
	if len(store.sets) != 0 || store.commits != 0 {
		t.Fatalf("callback error committed %+v (%d commits)", store.sets, store.commits)
	}

	finished := make(chan error, 1)
	go func() {
		_, err := store.Transact(context.Background(), func(_ context.Context, _ []ReservationSet) (ReservationSet, error) {
			return ReservationSet{}, errors.New("second callback")
		}, nil)
		finished <- err
	}()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("callback error retained the transaction lock")
	}
}

func TestStoreOwnedCallbackCommitAndLateConflictDoNotRetry(t *testing.T) {
	t.Parallel()
	root := testVolume(t)
	probe := &recordingProbe{}
	selector := testSelector(t, ExactNames{}, probe)
	callback, err := selector.Callback(singleRequest(root, "Title.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	commitErr := errors.New("durable write failed")
	store := &callbackStore{commitErr: commitErr}
	_, err = store.Transact(context.Background(), callback, nil)
	if !errors.Is(err, commitErr) || store.commits != 1 || len(store.sets) != 0 {
		t.Fatalf("commit failure: err=%v commits=%d sets=%v", err, store.commits, store.sets)
	}

	lateConflict := errors.New("stale durable reservation conflict")
	store = &callbackStore{commitErr: lateConflict}
	_, err = store.Transact(context.Background(), callback, nil)
	if !errors.Is(err, lateConflict) {
		t.Fatalf("late conflict error = %v, want %v", err, lateConflict)
	}
	if store.callbackCalls != 1 || store.commits != 1 || len(store.sets) != 0 || probe.calls != 2 {
		t.Fatalf("late conflict retried selection or committed: callbacks=%d commits=%d sets=%d probes=%d", store.callbackCalls, store.commits, len(store.sets), probe.calls)
	}

	store = &callbackStore{}
	jobMutationErr := errors.New("job clone mutation failed")
	_, err = store.Transact(context.Background(), callback, func(ReservationSet) error { return jobMutationErr })
	if !errors.Is(err, jobMutationErr) || store.commits != 0 || len(store.sets) != 0 {
		t.Fatalf("job mutation failure was not rolled back: err=%v commits=%d sets=%v", err, store.commits, store.sets)
	}
}

func TestConcurrentStoreTransactionsCannotDuplicateAReservation(t *testing.T) {
	root := testVolume(t)
	selector := testSelector(t, ExactNames{}, &recordingProbe{})
	const workers = 32
	callbacks := make([]SelectionCallback, workers)
	for i := range callbacks {
		request := singleRequest(root, "Title.mp4")
		request.GroupID = fmt.Sprintf("job-%d", i)
		callback, err := selector.Callback(request)
		if err != nil {
			t.Fatal(err)
		}
		callbacks[i] = callback
	}
	store := &callbackStore{}
	start := make(chan struct{})
	results := make(chan string, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			set, err := store.Transact(context.Background(), callbacks[index], nil)
			if err != nil {
				errs <- err
				return
			}
			results <- set.Artifacts[0].Basename
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	seen := map[string]struct{}{}
	for name := range results {
		if _, exists := seen[name]; exists {
			t.Errorf("duplicate committed reservation %q", name)
		}
		seen[name] = struct{}{}
	}
	if len(seen) != workers {
		t.Fatalf("committed %d reservations, want %d", len(seen), workers)
	}
	if _, exists := seen["Title.mp4"]; !exists {
		t.Fatalf("base reservation missing: %v", seen)
	}
	if _, exists := seen["Title (32).mp4"]; !exists {
		t.Fatalf("last deterministic reservation missing: %v", seen)
	}
}

func TestValidationBoundsBeforeProbe(t *testing.T) {
	t.Parallel()
	root := testVolume(t)
	probe := &recordingProbe{}
	selector := testSelector(t, ExactNames{}, probe)
	tooMany := make([]ArtifactDeclaration, MaxArtifactsPerSet+1)
	for i := range tooMany {
		tooMany[i] = ArtifactDeclaration{Kind: "primary", Identity: fmt.Sprintf("id-%d", i), ProposedBasename: fmt.Sprintf("Title-%d.mp4", i)}
	}
	invalidRequests := []SelectionRequest{
		{GroupID: "", Directory: root, Artifacts: []ArtifactDeclaration{{Kind: "primary", Identity: "video", ProposedBasename: "Title.mp4"}}},
		{GroupID: strings.Repeat("g", MaxGroupIDBytes+1), Directory: root, Artifacts: []ArtifactDeclaration{{Kind: "primary", Identity: "video", ProposedBasename: "Title.mp4"}}},
		{GroupID: "job", Directory: Volume{CanonicalPath: root.CanonicalPath, Identity: strings.Repeat("i", MaxVolumeIdentityBytes+1)}, Artifacts: []ArtifactDeclaration{{Kind: "primary", Identity: "video", ProposedBasename: "Title.mp4"}}},
		{GroupID: "job", Directory: Volume{CanonicalPath: string([]byte{0xff}), Identity: root.Identity}, Artifacts: []ArtifactDeclaration{{Kind: "primary", Identity: "video", ProposedBasename: "Title.mp4"}}},
		{GroupID: "job", Directory: Volume{CanonicalPath: "/" + strings.Repeat("p", MaxCanonicalPathBytes), Identity: root.Identity}, Artifacts: []ArtifactDeclaration{{Kind: "primary", Identity: "video", ProposedBasename: "Title.mp4"}}},
		{GroupID: "job", Directory: root, Artifacts: []ArtifactDeclaration{{Kind: strings.Repeat("k", MaxKindBytes+1), Identity: "video", ProposedBasename: "Title.mp4"}}},
		{GroupID: "job", Directory: root, Artifacts: []ArtifactDeclaration{{Kind: "primary", Identity: strings.Repeat("i", MaxIdentityBytes+1), ProposedBasename: "Title.mp4"}}},
		{GroupID: "job", Directory: root, Artifacts: tooMany},
		{GroupID: "job", Directory: root, Artifacts: []ArtifactDeclaration{{Kind: "primary", Identity: string([]byte{0xff}), ProposedBasename: "Title.mp4"}}},
	}
	for _, request := range invalidRequests {
		if _, err := selector.Callback(request); !errors.Is(err, ErrInvalidDeclaration) {
			t.Errorf("Callback(%+v) error = %v, want ErrInvalidDeclaration", request, err)
		}
	}
	if probe.calls != 0 {
		t.Fatalf("invalid static input reached probe %d times", probe.calls)
	}

	callback, err := selector.Callback(singleRequest(root, "Title.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	tooManyActive := make([]ReservationSet, MaxActiveReservationSets+1)
	if _, err := callback(context.Background(), tooManyActive); !errors.Is(err, ErrInvalidReservation) {
		t.Fatalf("too many active sets error = %v, want ErrInvalidReservation", err)
	}
	tooManyArtifacts := make([]ReservedArtifact, MaxArtifactsPerActiveSet+1)
	for i := range tooManyArtifacts {
		tooManyArtifacts[i] = ReservedArtifact{Kind: "primary", Identity: fmt.Sprintf("id-%d", i), Basename: fmt.Sprintf("Title-%d.mp4", i)}
	}
	if _, err := callback(context.Background(), []ReservationSet{{GroupID: "old", Directory: root, Artifacts: tooManyArtifacts}}); !errors.Is(err, ErrInvalidReservation) {
		t.Fatalf("too many active artifacts error = %v, want ErrInvalidReservation", err)
	}
	if _, err := callback(context.Background(), []ReservationSet{{GroupID: "", Directory: root, Artifacts: []ReservedArtifact{{Kind: "primary", Identity: "old-video", Basename: "Title.mp4"}}}}); !errors.Is(err, ErrInvalidReservation) {
		t.Fatalf("invalid active group ID error = %v, want ErrInvalidReservation", err)
	}
	if probe.calls != 0 {
		t.Fatalf("invalid active input reached probe %d times", probe.calls)
	}
}

func TestValidateBasenameRejectsPortablePathWindowsAndEncodingHazards(t *testing.T) {
	t.Parallel()
	invalid := []string{"", ".", "..", "nested/file.mp4", `nested\\file.mp4`, "C:clip.mp4", "CON", "aux.txt", "CON .txt", "NUL .", "CONIN$", "CONOUT$.txt", "CLOCK$", "COM¹", "COM².txt", "COM³", "LPT¹", "LPT².log", "LPT³", "clip.", "clip ", "clip?.mp4", "clip\x00.mp4", string([]byte{0xff}), "clip\u0085.mp4", strings.Repeat("a", MaxBasenameBytes+1)}
	for _, name := range invalid {
		if err := ValidateBasename(name); !errors.Is(err, ErrInvalidDeclaration) {
			t.Errorf("ValidateBasename(%q) error = %v, want ErrInvalidDeclaration", name, err)
		}
	}
	for _, name := range []string{"東京 — résumé.mp4", "emoji 🎬.mkv", "Title (2).ext", ".profile"} {
		if err := ValidateBasename(name); err != nil {
			t.Errorf("ValidateBasename(%q) = %v", name, err)
		}
	}
}

func TestSelectionPropertyReturnsFirstFreeSuffixForEveryArtifact(t *testing.T) {
	t.Parallel()
	root := testVolume(t)
	rng := rand.New(rand.NewPCG(21, 34))
	for i := 0; i < 200; i++ {
		occupied := make(map[string]bool)
		free := uint64(0)
		for candidate := uint64(1); candidate <= 12; candidate++ {
			if rng.IntN(3) != 0 {
				occupied[suffixedBasenameForTest("Title.mp4", candidate)] = true
			} else if free == 0 {
				free = candidate
			}
		}
		if free == 0 {
			free = 13
		}
		for candidate := uint64(1); candidate < free; candidate++ {
			occupied[suffixedBasenameForTest("Title.mp4", candidate)] = true
		}
		selector := testSelector(t, ExactNames{}, &recordingProbe{occupied: occupied})
		set, err := selector.Select(context.Background(), SelectionRequest{
			GroupID:   "job",
			Directory: root,
			Artifacts: []ArtifactDeclaration{
				{Kind: "primary", Identity: "video", ProposedBasename: "Title.mp4"},
				{Kind: "sidecar", Identity: "metadata", ProposedBasename: "Title.json"},
			},
		}, nil)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if got := set.Artifacts[0].Basename; got != suffixedBasenameForTest("Title.mp4", free) {
			t.Fatalf("iteration %d: selected %q, want suffix %d", i, got, free)
		}
		for _, artifact := range set.Artifacts {
			if err := ValidateBasename(artifact.Basename); err != nil {
				t.Fatalf("iteration %d: invalid selected basename %q: %v", i, artifact.Basename, err)
			}
		}
	}
}

func FuzzValidateBasenameKeepsAcceptedValuesToOnePortableComponent(f *testing.F) {
	for _, seed := range []string{"Title.mp4", "東京.mp4", "nested/file", "CON", "CON .txt", "emoji 🎬.mkv", string([]byte{0xff})} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		if err := ValidateBasename(name); err != nil {
			return
		}
		if !utf8.ValidString(name) || strings.ContainsAny(name, "/\\\x00") || filepath.Base(name) != name {
			t.Fatalf("accepted non-basename %q", name)
		}
		for _, r := range name {
			if unicode.IsControl(r) {
				t.Fatalf("accepted control character in %q", name)
			}
		}
	})
}

func FuzzCallbackRejectsInvalidStaticInputBeforeProbe(f *testing.F) {
	f.Add("job", "primary", "video", "Title.mp4")
	f.Add(string([]byte{0xff}), "primary", "video", "Title.mp4")
	f.Fuzz(func(t *testing.T, groupID, kind, identity, basename string) {
		root := testVolume(t)
		probe := &recordingProbe{}
		selector := testSelector(t, ExactNames{}, probe)
		_, err := selector.Callback(SelectionRequest{GroupID: groupID, Directory: root, Artifacts: []ArtifactDeclaration{{Kind: kind, Identity: identity, ProposedBasename: basename}}})
		if err != nil && probe.calls != 0 {
			t.Fatalf("invalid input reached probe %d times", probe.calls)
		}
	})
}

type recordingProbe struct {
	occupied map[string]bool
	err      error
	calls    int
}

type cancelingProbe struct {
	cancel context.CancelFunc
	calls  int
}

func (p *cancelingProbe) Probe(_ context.Context, _ Volume, _ string) (Availability, error) {
	p.calls++
	p.cancel()
	return Available, nil
}

type typedNilNames struct{}

func (*typedNilNames) Equal(a, b string) bool { return a == b }
func (*typedNilNames) Key(name string) string { return name }

type typedNilVolumes struct{}

func (*typedNilVolumes) Equal(a, b Volume) bool   { return a.Identity == b.Identity }
func (*typedNilVolumes) Key(volume Volume) string { return volume.Identity }

func (p *recordingProbe) Probe(_ context.Context, _ Volume, basename string) (Availability, error) {
	p.calls++
	if p.err != nil {
		return Occupied, p.err
	}
	if p.occupied[basename] {
		return Occupied, nil
	}
	return Available, nil
}

var errRootChanged = errors.New("output root was replaced")

type rootBoundProbe struct {
	identity string
}

func (p *rootBoundProbe) Probe(_ context.Context, volume Volume, _ string) (Availability, error) {
	if volume.Identity != p.identity {
		return Occupied, errRootChanged
	}
	info, err := os.Lstat(volume.CanonicalPath)
	if err != nil {
		return Occupied, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Occupied, errRootChanged
	}
	return Available, nil
}

// callbackStore models the future State adapter contract: it holds one lock,
// invokes the selector against its current clone, applies future job mutation,
// and commits the clone only when every step succeeds.
type callbackStore struct {
	mu            sync.Mutex
	sets          []ReservationSet
	commitErr     error
	commits       int
	callbackCalls int
}

func (s *callbackStore) Transact(ctx context.Context, callback SelectionCallback, mutate func(ReservationSet) error) (ReservationSet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := append([]ReservationSet(nil), s.sets...)
	s.callbackCalls++
	set, err := callback(ctx, active)
	if err != nil {
		return ReservationSet{}, err
	}
	if mutate != nil {
		if err := mutate(set); err != nil {
			return ReservationSet{}, err
		}
	}
	s.commits++
	if s.commitErr != nil {
		return ReservationSet{}, s.commitErr
	}
	s.sets = append(s.sets, set)
	return set, nil
}

func testSelector(t *testing.T, names NameComparison, probe AvailabilityProbe) *Selector {
	t.Helper()
	selector, err := NewSelector(Options{Policies: Policies{Names: names, Volumes: CanonicalVolumes{}}, Probe: probe, MaxSuffix: 100})
	if err != nil {
		t.Fatal(err)
	}
	return selector
}

func testVolume(t *testing.T) Volume {
	t.Helper()
	path := t.TempDir()
	return Volume{CanonicalPath: path, Identity: fmt.Sprintf("test:%s", path)}
}

func singleRequest(root Volume, basename string) SelectionRequest {
	return SelectionRequest{GroupID: "job", Directory: root, Artifacts: []ArtifactDeclaration{{Kind: "primary", Identity: "video", ProposedBasename: basename}}}
}

func assertBasenames(t *testing.T, set ReservationSet, want ...string) {
	t.Helper()
	got := make([]string, len(set.Artifacts))
	for i, artifact := range set.Artifacts {
		got[i] = artifact.Basename
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("basenames = %v, want %v", got, want)
	}
}

func suffixedBasenameForTest(basename string, suffix uint64) string {
	if suffix == 1 {
		return basename
	}
	return strings.TrimSuffix(basename, filepath.Ext(basename)) + fmt.Sprintf(" (%d)%s", suffix, filepath.Ext(basename))
}
