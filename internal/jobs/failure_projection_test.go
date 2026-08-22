package jobs

import (
	"fmt"
	"os"
	"syscall"
	"testing"

	"github.com/tejasa97/vidstow/internal/jobmodel"
)

func TestQueueFailureProjectionAndCapabilities(t *testing.T) {
	tests := []struct {
		name       string
		code       string
		category   string
		retry      bool
		startAgain bool
		openSource bool
		messageKey string
	}{
		{name: "network", code: "network", category: "network_interrupted", retry: true, messageKey: "queue.failure.network_interrupted"},
		{name: "authentication", code: "authentication", category: "authentication_required", openSource: true, messageKey: "queue.failure.authentication_required"},
		{name: "unavailable", code: "unsupported", category: "resource_unavailable", openSource: true, messageKey: "queue.failure.resource_unavailable"},
		{name: "disk full", code: "disk_full", category: "disk_full", startAgain: true, messageKey: "queue.failure.disk_full"},
		{name: "permission", code: "permission_denied", category: "permission_denied", startAgain: true, messageKey: "queue.failure.permission_denied"},
		{name: "security", code: "security", category: "security_blocked", messageKey: "queue.failure.security_blocked"},
		{name: "exhausted", code: retryCodeFreshDownloadRequired, category: "retry_exhausted", messageKey: "queue.failure.retry_exhausted"},
		{name: "internal", code: "internal", category: "internal", retry: true, messageKey: "queue.failure.internal"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := &jobState{
				snap:        JobSnapshot{Status: StatusFailed, URL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ", ErrorReason: test.code, Bytes: 10},
				durable:     jobmodel.DurableJob{LastErrorCode: test.code, LastFailureCommittedBytes: 10},
				fromStateV2: true,
			}
			failure := queueFailureFor(state, state.snap)
			if failure.Category != test.category || failure.MessageKey != test.messageKey || failure.Retryable != test.retry || !failure.PartialOutput {
				t.Fatalf("failure = %#v", failure)
			}
			if failure.Heading == "" || failure.Message == "" || failure.RecommendedAction == "" {
				t.Fatalf("failure copy is incomplete: %#v", failure)
			}
			caps := queueCapabilitiesFor(state, state.snap)
			if caps.Retry != test.retry || caps.StartAgain != test.startAgain || caps.OpenSource != test.openSource || caps.CopyLink != test.openSource || !caps.Remove {
				t.Fatalf("capabilities = %#v", caps)
			}
		})
	}
}

func TestFailureReasonUsesWrappedFilesystemErrors(t *testing.T) {
	if got := errorReason(fmt.Errorf("write output: %w", syscall.ENOSPC)); got != "disk_full" {
		t.Fatalf("disk-full reason = %q", got)
	}
	if got := errorReason(fmt.Errorf("publish output: %w", os.ErrPermission)); got != "permission_denied" {
		t.Fatalf("permission reason = %q", got)
	}
	if got := humanError(fmt.Errorf("write /private/path: %w", syscall.ENOSPC)); got != "Not enough disk space" {
		t.Fatalf("disk-full copy = %q", got)
	}
}

func TestQueueStartAgainReleasesDurableRowBeforeReturningURL(t *testing.T) {
	store, _, _ := newV2TestStore(t, "disk-failed")
	store.state.Jobs[0].Lifecycle = jobmodel.LifecycleFailed
	store.state.Jobs[0].LastErrorCode = "disk_full"

	manager := New(nil, nil)
	defer manager.Close()
	if err := manager.SetStateStore(store); err != nil {
		t.Fatal(err)
	}
	if err := manager.RestoreStateV2(store.Snapshot()); err != nil {
		t.Fatal(err)
	}
	view := manager.QueueView()
	if len(view.Rows) != 1 || !view.Rows[0].Capabilities.StartAgain {
		t.Fatalf("queue view = %#v", view.Rows)
	}
	url, err := manager.QueueStartAgainURL("disk-failed", view.Rows[0].CommandToken)
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://www.youtube.com/watch?v=abc123" {
		t.Fatalf("source URL = %q", url)
	}
	if len(store.Snapshot().Jobs) != 0 || len(manager.QueueView().Rows) != 0 {
		t.Fatal("start again returned before the old row and reservation were removed")
	}
	if _, err := manager.QueueStartAgainURL("disk-failed", view.Rows[0].CommandToken); err == nil {
		t.Fatal("consumed start-again authority was replayed")
	}
}
