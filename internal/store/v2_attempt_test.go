package store

import (
	"testing"

	"github.com/tejasa97/vidstow/internal/jobmodel"
)

func TestCheckPreconditionsRejectsStaleAttemptID(t *testing.T) {
	root := jobmodel.OutputRootRef{CanonicalPath: "/private/vidstow", Identity: "volume"}
	state := jobmodel.State{Jobs: []jobmodel.DurableJob{{
		ID: "job-1", Revision: 2, AttemptID: "attempt-current", SessionID: "0123456789abcdef0123456789abcdef",
		Lifecycle: jobmodel.LifecycleActive, OutputRoot: root,
	}}}
	err := checkPreconditions(state, []JobPrecondition{{
		ID: "job-1", Revision: 2, Lifecycle: jobmodel.LifecycleActive, AttemptID: "attempt-stale",
		SessionID: "0123456789abcdef0123456789abcdef", OutputRoot: root,
	}})
	if err != ErrStaleRevision {
		t.Fatalf("stale attempt precondition error = %v; want ErrStaleRevision", err)
	}
}
