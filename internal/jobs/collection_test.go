package jobs

import (
	"testing"
	"time"

	"github.com/tejasa97/vidstow/internal/jobmodel"
)

func TestRemoveCollectionChildPreservesOriginalIndexesAndDropsEmptyParent(t *testing.T) {
	now := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	state := jobmodel.State{
		Collections: []jobmodel.DurableCollection{{ID: "collection-1", Revision: 1, ChildJobIDs: []string{"job-1", "job-2"}}},
	}
	first := jobmodel.DurableJob{ID: "job-1", CollectionID: "collection-1", CollectionIndex: 1}
	second := jobmodel.DurableJob{ID: "job-2", CollectionID: "collection-1", CollectionIndex: 2}
	if err := removeCollectionChild(&state, first, now); err != nil {
		t.Fatal(err)
	}
	if len(state.Collections) != 1 || state.Collections[0].Revision != 2 || state.Collections[0].UpdatedAt != now || len(state.Collections[0].ChildJobIDs) != 1 || state.Collections[0].ChildJobIDs[0] != "job-2" {
		t.Fatalf("collection after first removal = %#v", state.Collections)
	}
	if err := removeCollectionChild(&state, second, now); err != nil {
		t.Fatal(err)
	}
	if len(state.Collections) != 0 {
		t.Fatalf("collections after final removal = %#v", state.Collections)
	}
}

func TestRemoveCollectionChildFailsClosedOnMembershipMismatch(t *testing.T) {
	state := jobmodel.State{Collections: []jobmodel.DurableCollection{{ID: "collection-1", Revision: 1, ChildJobIDs: []string{"other"}}}}
	job := jobmodel.DurableJob{ID: "job-1", CollectionID: "collection-1", CollectionIndex: 1}
	if err := removeCollectionChild(&state, job, time.Now()); err == nil {
		t.Fatal("removeCollectionChild error = nil")
	}
	if len(state.Collections[0].ChildJobIDs) != 1 || state.Collections[0].ChildJobIDs[0] != "other" {
		t.Fatalf("mismatched collection mutated = %#v", state.Collections[0])
	}
}
