package jobs

import (
	"context"
	"strings"
	"testing"

	"github.com/tejasa97/vidstow/internal/outputplan"
	"github.com/tejasa97/ytdlp-go/engine"
	"github.com/tejasa97/ytdlp-go/engine/value"
)

func TestSubmitAdmittedPreservesIDAndUsesExistingManager(t *testing.T) {
	manager := New(nil, nil)
	started := make(chan struct{})
	var gotRequest engine.Request
	manager.runDownload = func(ctx context.Context, request engine.Request, _ engine.EventHandler) (engine.Result, error) {
		gotRequest = request
		close(started)
		<-ctx.Done()
		return engine.Result{}, ctx.Err()
	}

	plan := outputplan.Plan{ID: "video-1080-mp4", Kind: outputplan.KindVideo, Label: "1080p", Container: "MP4", Selector: "bv1+ba1"}
	request := Request{
		URL:       "https://www.youtube.com/watch?v=abc123",
		VideoID:   "abc123",
		Title:     "Title",
		PlanID:    plan.ID,
		Quality:   Quality1080p,
		OutputDir: t.TempDir(),
	}
	reservedBasename := "Title [abc123] [1080p] (2).mp4"
	id, err := manager.SubmitAdmitted("admitted-job", request, &plan, AdmittedOutput{Basename: reservedBasename})
	if err != nil {
		t.Fatalf("SubmitAdmitted: %v", err)
	}
	if id != "admitted-job" {
		t.Fatalf("id = %q, want admitted-job", id)
	}
	<-started
	snapshot, ok := manager.Find(id)
	if !ok || snapshot.ID != id {
		t.Fatalf("Find(%q) = %#v, %v", id, snapshot, ok)
	}
	if snapshot.Status != StatusActive {
		t.Fatalf("status = %q, want active", snapshot.Status)
	}
	if got, want := gotRequest.OutputTemplate, strings.ReplaceAll(reservedBasename, "%", "%%"); got != want {
		t.Fatalf("engine output template = %q, want escaped reserved target %q", got, want)
	}
	if _, err := manager.SubmitAdmitted("missing-plan", request, nil, AdmittedOutput{Basename: reservedBasename}); err == nil {
		t.Fatal("SubmitAdmitted() error = nil for nil selected plan")
	}
	missingPlanID := request
	missingPlanID.PlanID = ""
	if _, err := manager.SubmitAdmitted("missing-plan-id", missingPlanID, &plan, AdmittedOutput{Basename: reservedBasename}); err == nil {
		t.Fatal("SubmitAdmitted() error = nil for empty plan ID")
	}
	if _, err := manager.SubmitAdmitted(id, request, &plan, AdmittedOutput{Basename: reservedBasename}); err == nil {
		t.Fatal("SubmitAdmitted() error = nil for duplicate job id")
	}
	if _, err := manager.SubmitAdmitted("other-job", request, &outputplan.Plan{ID: "different", Kind: plan.Kind}, AdmittedOutput{Basename: reservedBasename}); err == nil {
		t.Fatal("SubmitAdmitted() error = nil for mismatched plan")
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestLiteralOutputTemplateRendersPercentBasenameExactly(t *testing.T) {
	reservedBasename := "Profit 100% [abc123] [MP3] (2).mp3"
	encoded, err := literalOutputTemplate(reservedBasename)
	if err != nil {
		t.Fatalf("literalOutputTemplate: %v", err)
	}
	if encoded == reservedBasename || !strings.Contains(encoded, "%%") {
		t.Fatalf("encoded template = %q, want escaped percent", encoded)
	}
	metadata := value.NewInfo(value.NewObject())
	rendered, err := engine.RenderOutputArtifacts(engine.OutputPreviewRequest{
		Template:  encoded,
		Metadata:  metadata,
		Extension: "mp3",
	})
	if err != nil {
		t.Fatalf("RenderOutputArtifacts: %v", err)
	}
	if len(rendered) != 1 || rendered[0].ProposedBasename != reservedBasename {
		t.Fatalf("rendered artifacts = %#v, want exact basename %q", rendered, reservedBasename)
	}
}
