package store

import (
	"testing"

	"github.com/tejasa97/vidstow/internal/jobmodel"
)

func collectionState(kind jobmodel.CollectionKind) jobmodel.State {
	state := defaultStateV2()
	job := testJob()
	job.CollectionID = "collection-1"
	job.CollectionIndex = 1
	state.Jobs = []jobmodel.DurableJob{job}
	state.NextQueueOrdinal = 2
	state.Collections = []jobmodel.DurableCollection{{
		ID: "collection-1", Revision: 1, Kind: kind,
		Title: "Collection", Policy: "video:1080p", ChildJobIDs: []string{job.ID},
		CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
	}}
	return state
}

func TestNormalizeStateV2TreatsLegacyCollectionsAsPlaylists(t *testing.T) {
	state := collectionState("")
	state.Collections[0].PlaylistID = "PLfixture"
	state.Collections[0].SourceURL = "https://www.youtube.com/playlist?list=PLfixture"
	normalizeStateV2(&state)
	if state.Collections[0].Kind != jobmodel.CollectionKindPlaylist {
		t.Fatalf("normalized kind = %q", state.Collections[0].Kind)
	}
	if err := validateState(state); err != nil {
		t.Fatalf("normalized legacy collection rejected: %v", err)
	}
}

func TestValidateStateAcceptsSourceFreeBatchCollections(t *testing.T) {
	state := collectionState(jobmodel.CollectionKindBatch)
	if err := validateState(state); err != nil {
		t.Fatalf("batch collection rejected: %v", err)
	}
	state.Collections[0].SourceURL = "https://private.invalid/pasted-input"
	if err := validateState(state); err == nil {
		t.Fatal("batch collection retained pasted source metadata")
	}
	state = collectionState("unknown")
	if err := validateState(state); err == nil {
		t.Fatal("unknown collection kind was accepted")
	}
}
