package diagnostics

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const MaxUploadResponseBytes = 64 << 10

var ErrUploadRetryable = errors.New("diagnostics: upload should be retried")

type EventsRequest struct {
	SchemaVersion int     `json:"schema_version"`
	Events        []Event `json:"events"`
}

type EventRejection struct {
	EventID string `json:"event_id"`
	Code    string `json:"code"`
}

type EventsResponse struct {
	AcceptedEventIDs []string         `json:"accepted_event_ids"`
	Rejected         []EventRejection `json:"rejected"`
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Uploader struct {
	outbox   *Outbox
	endpoint string
	client   HTTPDoer
}

func NewUploader(outbox *Outbox, endpoint string, client HTTPDoer) (*Uploader, error) {
	if outbox == nil || client == nil {
		return nil, errors.New("diagnostics: invalid uploader configuration")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("diagnostics: uploader endpoint must be a plain HTTPS URL")
	}
	return &Uploader{outbox: outbox, endpoint: parsed.String(), client: client}, nil
}

// UploadOnce performs at most one bounded request. It does not retry or log;
// callers control scheduling so transport failures can never recurse into
// diagnostic events or block application work.
func (u *Uploader) UploadOnce(ctx context.Context) (bool, error) {
	if u == nil {
		return false, errors.New("diagnostics: nil uploader")
	}
	events, err := u.outbox.Batch()
	if err != nil || len(events) == 0 {
		return false, err
	}
	payload, err := json.Marshal(EventsRequest{SchemaVersion: SchemaVersion, Events: events})
	if err != nil {
		return false, fmt.Errorf("diagnostics: encode events request: %w", err)
	}
	if len(events) > MaxUploadEvents || len(payload) > MaxUploadBytes {
		return false, errors.New("diagnostics: upload batch exceeded bound")
	}
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, u.endpoint, bytes.NewReader(payload))
	if err != nil {
		return false, fmt.Errorf("diagnostics: create upload request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	response, err := u.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("%w: transport", ErrUploadRetryable)
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, MaxUploadResponseBytes+1))
		return false, fmt.Errorf("%w: HTTP %d", ErrUploadRetryable, response.StatusCode)
	}
	batchIDs := make([]string, 0, len(events))
	batchSet := make(map[string]bool, len(events))
	for _, event := range events {
		batchIDs = append(batchIDs, event.EventID)
		batchSet[event.EventID] = true
	}
	if response.StatusCode >= 400 && response.StatusCode < 500 {
		// Except for 429, a client error permanently rejects the whole batch.
		if err := u.outbox.Remove(batchIDs); err != nil {
			return false, err
		}
		return true, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return false, fmt.Errorf("%w: HTTP %d", ErrUploadRetryable, response.StatusCode)
	}

	limited := io.LimitReader(response.Body, MaxUploadResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return false, fmt.Errorf("%w: read response", ErrUploadRetryable)
	}
	if len(data) > MaxUploadResponseBytes {
		return false, fmt.Errorf("%w: oversized response", ErrUploadRetryable)
	}
	var result EventsResponse
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil || ensureJSONEOF(decoder) != nil {
		return false, fmt.Errorf("%w: invalid response", ErrUploadRetryable)
	}
	remove := make([]string, 0, len(result.AcceptedEventIDs)+len(result.Rejected))
	seen := make(map[string]bool, cap(remove))
	for _, id := range result.AcceptedEventIDs {
		if !batchSet[id] || seen[id] {
			return false, fmt.Errorf("%w: invalid acknowledgement", ErrUploadRetryable)
		}
		seen[id] = true
		remove = append(remove, id)
	}
	for _, rejection := range result.Rejected {
		if !batchSet[rejection.EventID] || seen[rejection.EventID] || !permanentRejectionCode(rejection.Code) {
			return false, fmt.Errorf("%w: invalid rejection", ErrUploadRetryable)
		}
		seen[rejection.EventID] = true
		remove = append(remove, rejection.EventID)
	}
	if len(remove) == 0 {
		return false, fmt.Errorf("%w: empty acknowledgement", ErrUploadRetryable)
	}
	if err := u.outbox.Remove(remove); err != nil {
		return false, err
	}
	return true, nil
}

func permanentRejectionCode(code string) bool {
	switch code {
	case "invalid_event", "unsupported_schema", "invalid_resource", "expired_event":
		return true
	default:
		return false
	}
}

// Run uploads in the background until cancellation. wake may be notified when
// a new event arrives; periodic wakeups also recover pending work after a lost
// notification. Backoff is bounded and cancellation-aware.
func (u *Uploader) Run(ctx context.Context, wake <-chan struct{}) {
	backoff := time.Second
	const maxBackoff = 5 * time.Minute
	for {
		worked, err := u.UploadOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil && worked {
			backoff = time.Second
			continue
		}
		wait := 30 * time.Second
		if err != nil {
			wait = jitter(backoff)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		} else {
			backoff = time.Second
		}
		timer := time.NewTimer(wait)
		if err != nil {
			// New events do not bypass an active failure backoff.
			select {
			case <-ctx.Done():
				stopAndDrain(timer)
				return
			case <-timer.C:
			}
			continue
		}
		select {
		case <-ctx.Done():
			stopAndDrain(timer)
			return
		case <-wake:
			stopAndDrain(timer)
		case <-timer.C:
		}
	}
}

func jitter(base time.Duration) time.Duration {
	var sample [1]byte
	if _, err := rand.Read(sample[:]); err != nil {
		return base
	}
	// Uniformly vary retry delays from 75% through 125%.
	return base * time.Duration(75+int(sample[0])%51) / 100
}

func stopAndDrain(timer *time.Timer) {
	if timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}
