package reservationfs

import "testing"

func TestConservativeFoldedNamesKeyMatchesKnownNativeEquivalences(t *testing.T) {
	policy := ConservativeFoldedNames{}
	tests := []struct {
		name string
		a    string
		b    string
	}{
		{name: "canonical normalization", a: "café.mp4", b: "cafe\u0301.mp4"},
		{name: "simple fold cycle", a: "Σ.mp4", b: "ς.mp4"},
		{name: "full case fold", a: "straße.mp4", b: "STRASSE.mp4"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !policy.Equal(test.a, test.b) {
				t.Fatalf("Equal(%q, %q) = false", test.a, test.b)
			}
			if a, b := policy.Key(test.a), policy.Key(test.b); a != b {
				t.Fatalf("Key(%q) = %q, Key(%q) = %q", test.a, a, test.b, b)
			}
		})
	}
}

func TestConservativeFoldedNamesKeepsDistinctNamesDistinct(t *testing.T) {
	policy := ConservativeFoldedNames{}
	if policy.Equal("video-a.mp4", "video-b.mp4") {
		t.Fatal("distinct ASCII basenames unexpectedly compare equal")
	}
}

func TestConservativeNormalizedNamesPreservesCase(t *testing.T) {
	policy := ConservativeNormalizedNames{}
	composed, decomposed := "café.mp4", "cafe\u0301.mp4"
	if !policy.Equal(composed, decomposed) {
		t.Fatalf("Equal(%q, %q) = false", composed, decomposed)
	}
	if a, b := policy.Key(composed), policy.Key(decomposed); a != b {
		t.Fatalf("normalization keys differ: %q != %q", a, b)
	}
	if policy.Equal("Case.mp4", "case.mp4") {
		t.Fatal("case-sensitive normalization policy folded letter case")
	}
}
