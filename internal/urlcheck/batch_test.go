package urlcheck

import (
	"strings"
	"testing"
)

func TestParseBatchPreservesOrderAndCanonicalDuplicates(t *testing.T) {
	raw := "\r\n https://youtu.be/abcdefghijk \r\nhttps://www.youtube.com/shorts/lmnopqrstuv\nhttps://www.youtube.com/watch?v=abcdefghijk&t=4\nnot-a-url\n"
	got, err := ParseBatch(raw)
	if err != nil {
		t.Fatalf("ParseBatch() error = %v", err)
	}
	if got.Pasted != 4 || got.Ready != 2 || got.Duplicates != 1 || got.Invalid != 1 {
		t.Fatalf("counts = %+v", got)
	}
	if len(got.Lines) != 4 || got.Lines[0].LineNumber != 2 || got.Lines[3].LineNumber != 5 {
		t.Fatalf("lines = %+v", got.Lines)
	}
	if got.Lines[2].Status != BatchLineDuplicate || got.Lines[2].DuplicateOfLine != 2 || got.Lines[2].Message != "Duplicate of line 2" {
		t.Fatalf("duplicate = %+v", got.Lines[2])
	}
}

func TestParseBatchRejectsPlaylistAndVideoPlaylistLinks(t *testing.T) {
	raw := strings.Join([]string{
		"https://www.youtube.com/playlist?list=PLabcdefghijk",
		"https://www.youtube.com/watch?v=abcdefghijk&list=PLabcdefghijk",
	}, "\n")
	got, err := ParseBatch(raw)
	if err != nil {
		t.Fatalf("ParseBatch() error = %v", err)
	}
	if got.Invalid != 2 || got.Ready != 0 {
		t.Fatalf("counts = %+v", got)
	}
	for _, line := range got.Lines {
		if line.MessageKey != "batch.individual_video_required" {
			t.Fatalf("line = %+v", line)
		}
	}
}

func TestParseBatchBoundsNonEmptyLinesAndRawBytes(t *testing.T) {
	if _, err := ParseBatch("\n https://youtu.be/abcdefghijk \n"); err == nil {
		t.Fatal("one non-empty line accepted")
	}
	lines := make([]string, MaxBatchLines)
	for index := range lines {
		lines[index] = "invalid-" + strings.Repeat("x", index+1)
	}
	if got, err := ParseBatch(strings.Join(lines, "\n")); err != nil || got.Pasted != MaxBatchLines {
		t.Fatalf("maximum lines: got %+v, err %v", got, err)
	}
	if _, err := ParseBatch(strings.Join(append(lines, "one-too-many"), "\n")); err == nil {
		t.Fatal("too many lines accepted")
	}
	if _, err := ParseBatch(strings.Repeat("x", MaxBatchInputBytes+1)); err == nil {
		t.Fatal("oversized input accepted")
	}
}
