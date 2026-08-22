package urlcheck

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	MinBatchLines      = 2
	MaxBatchLines      = 20
	MaxBatchInputBytes = 32 << 10
)

type BatchLineStatus string

const (
	BatchLineReady     BatchLineStatus = "ready"
	BatchLineDuplicate BatchLineStatus = "duplicate"
	BatchLineInvalid   BatchLineStatus = "invalid"
)

type BatchLine struct {
	LineNumber      int
	Input           string
	Status          BatchLineStatus
	MessageKey      string
	Message         string
	DuplicateOfLine int
	VideoID         string
	CanonicalURL    string
}

type BatchParseResult struct {
	Lines      []BatchLine
	Pasted     int
	Ready      int
	Duplicates int
	Invalid    int
}

// ParseBatch performs bounded syntactic validation and canonical, within-batch
// deduplication. It preserves physical source line numbers while ignoring
// blank lines. Media analysis happens later and may turn a ready line into an
// analysis failure without changing the identity of any sibling.
func ParseBatch(raw string) (BatchParseResult, error) {
	if len(raw) > MaxBatchInputBytes {
		return BatchParseResult{}, fmt.Errorf("batch input must be no larger than %d KiB", MaxBatchInputBytes>>10)
	}
	if !utf8.ValidString(raw) || strings.IndexByte(raw, 0) >= 0 {
		return BatchParseResult{}, errors.New("batch input contains invalid text")
	}

	physical := strings.Split(raw, "\n")
	result := BatchParseResult{Lines: make([]BatchLine, 0, len(physical))}
	firstLineByVideo := make(map[string]int)
	for index, value := range physical {
		trimmed := strings.TrimSpace(strings.TrimSuffix(value, "\r"))
		if trimmed == "" {
			continue
		}
		result.Pasted++
		if result.Pasted > MaxBatchLines {
			return BatchParseResult{}, fmt.Errorf("paste between %d and %d URLs", MinBatchLines, MaxBatchLines)
		}
		line := BatchLine{LineNumber: index + 1, Input: trimmed}
		validated, err := Validate(trimmed)
		if err != nil {
			line.Status = BatchLineInvalid
			line.MessageKey = "batch.invalid_url"
			line.Message = err.Error()
			result.Invalid++
			result.Lines = append(result.Lines, line)
			continue
		}
		if validated.Kind != KindSingleVideo || validated.VideoID == "" || validated.VideoURL == "" {
			line.Status = BatchLineInvalid
			line.MessageKey = "batch.individual_video_required"
			line.Message = "Use an individual public YouTube video or Short URL. Playlist links are not supported in a batch."
			result.Invalid++
			result.Lines = append(result.Lines, line)
			continue
		}
		if first, duplicate := firstLineByVideo[validated.VideoID]; duplicate {
			line.Status = BatchLineDuplicate
			line.MessageKey = "batch.duplicate"
			line.Message = fmt.Sprintf("Duplicate of line %d", first)
			line.DuplicateOfLine = first
			line.VideoID = validated.VideoID
			line.CanonicalURL = validated.VideoURL
			result.Duplicates++
			result.Lines = append(result.Lines, line)
			continue
		}
		firstLineByVideo[validated.VideoID] = line.LineNumber
		line.Status = BatchLineReady
		line.MessageKey = "batch.ready"
		line.Message = "Ready"
		line.VideoID = validated.VideoID
		line.CanonicalURL = validated.VideoURL
		result.Ready++
		result.Lines = append(result.Lines, line)
	}
	if result.Pasted < MinBatchLines {
		return BatchParseResult{}, fmt.Errorf("paste between %d and %d URLs", MinBatchLines, MaxBatchLines)
	}
	return result, nil
}
