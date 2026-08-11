//go:build windows

package reservationfs

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsTraversalAttributesRespectParentCasePolicy(t *testing.T) {
	if got := windowsTraversalAttributes(false); got&windows.OBJ_CASE_INSENSITIVE == 0 || got&windows.OBJ_DONT_REPARSE == 0 {
		t.Fatalf("case-insensitive parent attributes = %#x, want no-reparse and case-insensitive", got)
	}
	if got := windowsTraversalAttributes(true); got&windows.OBJ_CASE_INSENSITIVE != 0 || got&windows.OBJ_DONT_REPARSE == 0 {
		t.Fatalf("case-sensitive parent attributes = %#x, want no-reparse without case-insensitive", got)
	}
}
