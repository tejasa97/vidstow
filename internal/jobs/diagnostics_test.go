package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tejasa97/youtube_dlp/engine"
)

func TestTerminalDownloadDiagnosticUsesTypedFacts(t *testing.T) {
	tests := []struct {
		name                        string
		err                         error
		sawDownload, sawPostprocess bool
		stage, category             string
	}{
		{"media 403", &engine.DownloadHTTPStatusError{Code: 403}, true, false, "media_transfer", "http_403"},
		{"media 429", &engine.DownloadHTTPStatusError{Code: 429}, true, false, "media_transfer", "http_429"},
		{"typed authentication before transfer", &engine.Error{Category: engine.ErrorAuthentication, Err: errors.New("private value")}, false, false, "extraction", "authentication_required"},
		{"postprocess failure", errors.New("/private/path/token"), true, true, "postprocessing", "ffmpeg_failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := terminalDownloadDiagnostic(test.err, test.sawDownload, test.sawPostprocess, time.Second)
			if got == nil || got.Stage != test.stage || got.Category != test.category {
				t.Fatalf("terminalDownloadDiagnostic() = %#v, want %s/%s", got, test.stage, test.category)
			}
		})
	}
}

func TestTerminalDownloadDiagnosticSkipsCancellation(t *testing.T) {
	if got := terminalDownloadDiagnostic(contextCanceledError{}, true, false, time.Second); got != nil {
		t.Fatalf("cancellation diagnostic = %#v", got)
	}
}

type contextCanceledError struct{}

func (contextCanceledError) Error() string        { return "context canceled" }
func (contextCanceledError) Is(target error) bool { return target == context.Canceled }
