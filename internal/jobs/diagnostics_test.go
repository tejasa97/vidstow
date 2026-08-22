package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tejasa97/ytdlp-go/engine"
)

func TestTerminalDownloadDiagnosticMatrix(t *testing.T) {
	private := errors.New("cookie=secret /private/path/token")
	tests := []struct {
		name                        string
		err                         error
		sawDownload, sawPostprocess bool
		wantNil                     bool
		stage, category             string
	}{
		{name: "success", wantNil: true},
		{name: "context canceled during transfer", err: context.Canceled, sawDownload: true, wantNil: true},
		{name: "typed cancelled before transfer", err: &engine.Error{Category: engine.ErrorCancelled, Err: private}, wantNil: true},
		{name: "wrapped canceled", err: contextCanceledError{}, sawDownload: true, wantNil: true},
		{name: "deadline before transfer", err: context.DeadlineExceeded, stage: "extraction", category: "network_timeout"},
		{name: "deadline during transfer", err: context.DeadlineExceeded, sawDownload: true, stage: "media_transfer", category: "network_timeout"},
		{name: "deadline during postprocess", err: context.DeadlineExceeded, sawDownload: true, sawPostprocess: true, stage: "media_transfer", category: "network_timeout"},
		{name: "typed authentication before transfer", err: &engine.Error{Category: engine.ErrorAuthentication, Err: private}, stage: "extraction", category: "authentication_required"},
		{name: "typed unsupported before transfer", err: &engine.Error{Category: engine.ErrorUnsupported, Err: private}, stage: "extraction", category: "unsupported_resource"},
		{name: "typed network before transfer", err: &engine.Error{Category: engine.ErrorNetwork, Err: private}, stage: "extraction", category: "extractor_failed"},
		{name: "typed network during transfer", err: &engine.Error{Category: engine.ErrorNetwork, Err: private}, sawDownload: true, stage: "media_transfer", category: "transfer_failed"},
		{name: "untyped before transfer", err: private, stage: "extraction", category: "extractor_failed"},
		{name: "untyped during transfer", err: private, sawDownload: true, stage: "media_transfer", category: "transfer_failed"},
		{name: "postprocess failure", err: private, sawDownload: true, sawPostprocess: true, stage: "postprocessing", category: "ffmpeg_failed"},
		{name: "media 403", err: &engine.DownloadHTTPStatusError{Code: http.StatusForbidden}, sawDownload: true, stage: "media_transfer", category: "http_403"},
		{name: "wrapped media 403", err: fmt.Errorf("multi-track transfer: %w", &engine.DownloadHTTPStatusError{Code: http.StatusForbidden}), sawDownload: true, stage: "media_transfer", category: "http_403"},
		{name: "media 429", err: &engine.DownloadHTTPStatusError{Code: http.StatusTooManyRequests}, sawDownload: true, stage: "media_transfer", category: "http_429"},
		{name: "media 401 stays coarse", err: &engine.DownloadHTTPStatusError{Code: http.StatusUnauthorized}, sawDownload: true, stage: "media_transfer", category: "transfer_failed"},
		{name: "media 500 stays coarse", err: &engine.DownloadHTTPStatusError{Code: http.StatusInternalServerError}, sawDownload: true, stage: "media_transfer", category: "transfer_failed"},
		{name: "download HTTP status before transfer is not a transfer fact", err: &engine.DownloadHTTPStatusError{Code: http.StatusForbidden}, stage: "extraction", category: "extractor_failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := terminalDownloadDiagnostic(test.err, test.sawDownload, test.sawPostprocess, time.Second)
			if test.wantNil {
				if got != nil {
					t.Fatalf("terminalDownloadDiagnostic() = %#v, want nil", got)
				}
				return
			}
			if got == nil || got.Stage != test.stage || got.Category != test.category {
				t.Fatalf("terminalDownloadDiagnostic() = %#v, want %s/%s", got, test.stage, test.category)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "cookie") || strings.Contains(string(encoded), "/private/") {
				t.Fatalf("diagnostic leaked private error text: %s", encoded)
			}
		})
	}
}

type contextCanceledError struct{}

func (contextCanceledError) Error() string        { return "context canceled" }
func (contextCanceledError) Is(target error) bool { return target == context.Canceled }
