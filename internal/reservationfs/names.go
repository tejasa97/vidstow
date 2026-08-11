package reservationfs

import (
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

var fullCaseFolder = cases.Fold()

// ConservativeNormalizedNames compares reservation basenames using canonical
// NFD normalization without changing case. It is used for case-sensitive APFS,
// whose lookup remains normalization-insensitive. The policy follows the
// Unicode tables bundled with x/text; APFS may use a different Unicode version,
// so unknown version-specific equivalences remain a native-platform test risk.
type ConservativeNormalizedNames struct{}

func (ConservativeNormalizedNames) Equal(a, b string) bool {
	return conservativeNormalizationKey(a) == conservativeNormalizationKey(b)
}

func (ConservativeNormalizedNames) Key(name string) string {
	return conservativeNormalizationKey(name)
}

// ConservativeFoldedNames compares reservation basenames using canonical NFD
// normalization followed by full Unicode case folding. It deliberately
// permits false-positive collisions: supported case-insensitive filesystems
// do not all expose identical normalization, full-fold, or Unicode-version
// behavior, and two pending State reservations cannot be checked on disk yet.
//
// The policy follows the Unicode tables bundled with x/text. A native
// filesystem may use a different Unicode version or platform-specific table;
// unknown equivalences therefore remain a residual platform risk and must be
// covered by native release testing. Known normalization and full-fold
// equivalences are treated as occupied rather than risking publication-time
// aliasing.
type ConservativeFoldedNames struct{}

func (ConservativeFoldedNames) Equal(a, b string) bool {
	return conservativeFoldKey(a) == conservativeFoldKey(b)
}

func (ConservativeFoldedNames) Key(name string) string {
	return conservativeFoldKey(name)
}

func conservativeFoldKey(name string) string {
	decomposed := conservativeNormalizationKey(name)
	return norm.NFD.String(fullCaseFolder.String(decomposed))
}

func conservativeNormalizationKey(name string) string {
	return norm.NFD.String(name)
}
