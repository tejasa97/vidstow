// Package diagnostics records bounded, allowlisted operational facts for local
// troubleshooting. It deliberately has no network transport and accepts no
// arbitrary log or error strings.
package diagnostics

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const SchemaVersion = 1

const (
	TypeProblemObserved = "problem_observed"
	TypeHealthSummary   = "health_summary"
)

var (
	stages = allowed(
		"startup", "persistence", "extraction", "ejs_preprocess", "ejs_solve",
		"helper", "media_transfer", "postprocessing", "filesystem", "frontend", "internal",
	)
	categories = allowed(
		"http_403", "http_429", "network_timeout", "network_offline", "dns_failure", "tls_failure",
		"resource_unavailable", "resource_restricted", "authentication_required", "unsupported_resource", "extractor_failed",
		"helper_start_failed", "helper_timeout", "helper_crashed", "helper_security_limit", "preprocess_failed", "solve_failed", "invalid_solver_result",
		"range_rejected", "resume_invalid", "remote_content_changed", "incomplete_transfer", "transfer_failed",
		"ffmpeg_missing", "ffmpeg_start_failed", "ffmpeg_failed",
		"permission_denied", "disk_full", "path_unavailable", "destination_conflict", "unsafe_path",
		"state_unavailable", "state_corrupt", "state_unsupported", "state_contended", "state_indeterminate",
		"frontend_unhandled", "backend_panic", "unexpected_internal",
	)
	outcomes         = allowed("recovered", "terminal", "degraded")
	retryBuckets     = allowed("none", "one", "two", "three_plus")
	durationBucket   = allowed("lt_100ms", "100_499ms", "500_1999ms", "2_9s", "10_29s", "30_59s", "gte_60s")
	resourceTypes    = allowed("video", "playlist", "channel")
	operatingSystems = allowed("macos", "windows", "linux")
	architectures    = allowed("arm64", "amd64")
)

type Event struct {
	SchemaVersion int       `json:"schema_version"`
	EventID       string    `json:"event_id"`
	SessionID     string    `json:"session_id"`
	OperationID   string    `json:"operation_id,omitempty"`
	OccurredAt    time.Time `json:"occurred_at"`
	AppVersion    string    `json:"app_version"`
	EngineVersion string    `json:"engine_version"`
	Platform      Platform  `json:"platform"`
	Type          string    `json:"type"`
	Problem       *Problem  `json:"problem,omitempty"`
	Health        *Health   `json:"health,omitempty"`
	Resource      *Resource `json:"resource,omitempty"`
}

type Platform struct {
	OS           string `json:"os"`
	OSMajor      string `json:"os_major"`
	Architecture string `json:"architecture"`
}

type Problem struct {
	Stage            string `json:"stage"`
	Category         string `json:"category"`
	Outcome          string `json:"outcome"`
	RetryBucket      string `json:"retry_bucket"`
	DurationBucket   string `json:"duration_bucket,omitempty"`
	PanicFingerprint string `json:"panic_fingerprint,omitempty"`
}

type Resource struct {
	Provider     string `json:"provider"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
}

type Health struct {
	PeriodStartedAt                time.Time         `json:"period_started_at"`
	PeriodEndedAt                  time.Time         `json:"period_ended_at"`
	OperationsStarted              uint16            `json:"operations_started"`
	OperationsCompleted            uint16            `json:"operations_completed"`
	OperationsTerminallyFailed     uint16            `json:"operations_terminally_failed"`
	Retries                        uint16            `json:"retries"`
	SuccessfulRecoveries           uint16            `json:"successful_recoveries"`
	EJSMemoryCacheHits             uint16            `json:"ejs_memory_cache_hits"`
	EJSMemoryCacheMisses           uint16            `json:"ejs_memory_cache_misses"`
	EJSPersistentCacheHits         uint16            `json:"ejs_persistent_cache_hits"`
	EJSPersistentCacheMisses       uint16            `json:"ejs_persistent_cache_misses"`
	EJSPersistentCacheInvalid      uint16            `json:"ejs_persistent_cache_invalid"`
	EJSPersistentCacheExpired      uint16            `json:"ejs_persistent_cache_expired"`
	EJSPersistentCacheReadFailures uint16            `json:"ejs_persistent_cache_read_failures"`
	EJSPersistentCacheWriteFailure uint16            `json:"ejs_persistent_cache_write_failures"`
	EJSPreprocessDurations         DurationHistogram `json:"ejs_preprocess_durations"`
	EJSSolveDurations              DurationHistogram `json:"ejs_solve_durations"`
}

type DurationHistogram struct {
	LT100MS     uint16 `json:"lt_100ms"`
	MS100To499  uint16 `json:"100_499ms"`
	MS500To1999 uint16 `json:"500_1999ms"`
	S2To9       uint16 `json:"2_9s"`
	S10To29     uint16 `json:"10_29s"`
	S30To59     uint16 `json:"30_59s"`
	GTE60S      uint16 `json:"gte_60s"`
}

func NewUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("diagnostics: random identifier: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func (e Event) Validate() error {
	if e.SchemaVersion != SchemaVersion {
		return errors.New("diagnostics: unsupported schema version")
	}
	if !validUUID4(e.EventID) || !validUUID4(e.SessionID) || (e.OperationID != "" && !validUUID4(e.OperationID)) {
		return errors.New("diagnostics: invalid identifier")
	}
	if e.OccurredAt.IsZero() || e.OccurredAt.Location() != time.UTC {
		return errors.New("diagnostics: occurrence time must be UTC")
	}
	if !safeVersion(e.AppVersion) || !safeVersion(e.EngineVersion) {
		return errors.New("diagnostics: invalid build version")
	}
	if err := e.Platform.validate(); err != nil {
		return err
	}
	switch e.Type {
	case TypeProblemObserved:
		if e.Problem == nil || e.Health != nil {
			return errors.New("diagnostics: problem event payload mismatch")
		}
		if err := e.Problem.validate(); err != nil {
			return err
		}
		if e.Resource != nil {
			if err := e.Resource.validate(); err != nil {
				return err
			}
		}
	case TypeHealthSummary:
		if e.Health == nil || e.Problem != nil || e.Resource != nil || e.OperationID != "" {
			return errors.New("diagnostics: health event payload mismatch")
		}
		if err := e.Health.validate(); err != nil {
			return err
		}
	default:
		return errors.New("diagnostics: invalid event type")
	}
	return nil
}

func (p Platform) validate() error {
	if !operatingSystems[p.OS] || !architectures[p.Architecture] || len(p.OSMajor) == 0 || len(p.OSMajor) > 16 {
		return errors.New("diagnostics: invalid platform")
	}
	if _, err := strconv.ParseUint(p.OSMajor, 10, 32); err != nil {
		return errors.New("diagnostics: invalid OS major version")
	}
	return nil
}

func (p Problem) validate() error {
	if !stages[p.Stage] || !categories[p.Category] || !outcomes[p.Outcome] || !retryBuckets[p.RetryBucket] || !validStageCategory(p.Stage, p.Category) {
		return errors.New("diagnostics: invalid problem classification")
	}
	if p.DurationBucket != "" && !durationBucket[p.DurationBucket] {
		return errors.New("diagnostics: invalid duration bucket")
	}
	if p.PanicFingerprint != "" {
		if p.Stage != "internal" || p.Category != "backend_panic" || len(p.PanicFingerprint) != 64 {
			return errors.New("diagnostics: invalid panic fingerprint")
		}
		if _, err := hex.DecodeString(p.PanicFingerprint); err != nil || strings.ToLower(p.PanicFingerprint) != p.PanicFingerprint {
			return errors.New("diagnostics: invalid panic fingerprint")
		}
	}
	return nil
}

func (r Resource) validate() error {
	if r.Provider != "youtube" || !resourceTypes[r.ResourceType] {
		return errors.New("diagnostics: invalid resource")
	}
	switch r.ResourceType {
	case "video":
		if len(r.ResourceID) != 11 {
			return errors.New("diagnostics: invalid resource")
		}
	case "playlist":
		if len(r.ResourceID) < 2 || len(r.ResourceID) > 128 {
			return errors.New("diagnostics: invalid resource")
		}
	case "channel":
		if len(r.ResourceID) != 24 || !strings.HasPrefix(r.ResourceID, "UC") {
			return errors.New("diagnostics: invalid resource")
		}
	}
	for _, char := range r.ResourceID {
		if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') && char != '_' && char != '-' {
			return errors.New("diagnostics: invalid resource")
		}
	}
	return nil
}

func (h Health) validate() error {
	if h.PeriodStartedAt.IsZero() || h.PeriodEndedAt.IsZero() || h.PeriodStartedAt.Location() != time.UTC || h.PeriodEndedAt.Location() != time.UTC || h.PeriodEndedAt.Before(h.PeriodStartedAt) {
		return errors.New("diagnostics: invalid health interval")
	}
	for _, value := range h.values() {
		if value > 10000 {
			return errors.New("diagnostics: health counter exceeds limit")
		}
	}
	return nil
}

func (h Health) values() []uint16 {
	values := []uint16{
		h.OperationsStarted, h.OperationsCompleted, h.OperationsTerminallyFailed, h.Retries, h.SuccessfulRecoveries,
		h.EJSMemoryCacheHits, h.EJSMemoryCacheMisses, h.EJSPersistentCacheHits, h.EJSPersistentCacheMisses,
		h.EJSPersistentCacheInvalid, h.EJSPersistentCacheExpired, h.EJSPersistentCacheReadFailures, h.EJSPersistentCacheWriteFailure,
	}
	values = append(values, h.EJSPreprocessDurations.values()...)
	return append(values, h.EJSSolveDurations.values()...)
}

func (h DurationHistogram) values() []uint16 {
	return []uint16{h.LT100MS, h.MS100To499, h.MS500To1999, h.S2To9, h.S10To29, h.S30To59, h.GTE60S}
}

func validUUID4(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' || value[14] != '4' {
		return false
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return err == nil && len(decoded) == 16 && decoded[8]&0xc0 == 0x80
}

var categoryStages = map[string]map[string]bool{
	"http_403": {"extraction": true, "media_transfer": true}, "http_429": {"extraction": true, "media_transfer": true},
	"network_timeout": {"extraction": true, "media_transfer": true}, "network_offline": {"extraction": true, "media_transfer": true},
	"dns_failure": {"extraction": true, "media_transfer": true}, "tls_failure": {"extraction": true, "media_transfer": true},
	"resource_unavailable": {"extraction": true}, "resource_restricted": {"extraction": true},
	"authentication_required": {"extraction": true}, "unsupported_resource": {"extraction": true}, "extractor_failed": {"extraction": true},
	"helper_start_failed": {"helper": true}, "helper_timeout": {"helper": true}, "helper_crashed": {"helper": true}, "helper_security_limit": {"helper": true},
	"preprocess_failed": {"ejs_preprocess": true}, "solve_failed": {"ejs_solve": true}, "invalid_solver_result": {"ejs_solve": true},
	"range_rejected": {"media_transfer": true}, "resume_invalid": {"media_transfer": true}, "remote_content_changed": {"media_transfer": true},
	"incomplete_transfer": {"media_transfer": true}, "transfer_failed": {"media_transfer": true},
	"ffmpeg_missing": {"postprocessing": true}, "ffmpeg_start_failed": {"postprocessing": true}, "ffmpeg_failed": {"postprocessing": true},
	"permission_denied": {"filesystem": true, "persistence": true}, "disk_full": {"filesystem": true, "persistence": true},
	"path_unavailable": {"filesystem": true}, "destination_conflict": {"filesystem": true}, "unsafe_path": {"filesystem": true},
	"state_unavailable": {"startup": true, "persistence": true}, "state_corrupt": {"startup": true, "persistence": true},
	"state_unsupported": {"startup": true, "persistence": true}, "state_contended": {"startup": true, "persistence": true},
	"state_indeterminate": {"startup": true, "persistence": true},
	"frontend_unhandled":  {"frontend": true}, "backend_panic": {"internal": true}, "unexpected_internal": {"internal": true},
}

func validStageCategory(stage, category string) bool { return categoryStages[category][stage] }

func safeVersion(value string) bool {
	if value == "" || len(value) > 64 || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') && !strings.ContainsRune("._+-", char) {
			return false
		}
	}
	return true
}

func allowed(values ...string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}
