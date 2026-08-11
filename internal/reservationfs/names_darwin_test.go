//go:build darwin

package reservationfs

import "testing"

func TestDarwinCaseSensitivePolicyNormalizesWithoutCaseFolding(t *testing.T) {
	policy := posixNameComparison(true)
	if !policy.Equal("café.mp4", "cafe\u0301.mp4") {
		t.Fatal("Darwin case-sensitive policy missed canonical normalization")
	}
	if policy.Equal("Case.mp4", "case.mp4") {
		t.Fatal("Darwin case-sensitive policy folded letter case")
	}
}
