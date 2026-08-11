package reservation

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxGroupIDBytes, MaxKindBytes, and MaxIdentityBytes bound persisted
	// application/engine identifiers before they are used as map keys.
	MaxGroupIDBytes  = 128
	MaxKindBytes     = 64
	MaxIdentityBytes = 256
	// MaxCanonicalPathBytes and MaxVolumeIdentityBytes bound the safe output
	// root facts supplied by the platform owner.
	MaxCanonicalPathBytes  = 4096
	MaxVolumeIdentityBytes = 256
	// MaxArtifactsPerSet, MaxActiveReservationSets, and
	// MaxArtifactsPerActiveSet bound durable State input before iteration or
	// allocation.
	MaxArtifactsPerSet       = 32
	MaxActiveReservationSets = 4096
	MaxArtifactsPerActiveSet = 32
	// MaxBasenameBytes is the portable filename-component ceiling.
	MaxBasenameBytes = 255
)

var (
	// ErrInvalidDeclaration means an engine-rendered artifact declaration is not
	// safe to use as a single destination basename.
	ErrInvalidDeclaration = errors.New("reservation: invalid artifact declaration")
	// ErrInvalidReservation means durable conflict input cannot be safely
	// compared. Callers must reconcile it instead of ignoring it.
	ErrInvalidReservation = errors.New("reservation: invalid active reservation")
	// ErrNoAvailableName means no candidate in the configured suffix range was
	// available. No existing destination has been changed.
	ErrNoAvailableName = errors.New("reservation: no available destination name")
)

// Volume is an output root already canonicalized by the owner of platform
// path rules. Identity is a required stable directory identity used by the
// caller-provided availability probe to reject replaced roots.
type Volume struct {
	CanonicalPath string
	Identity      string
}

// ArtifactDeclaration is the exact engine-rendered output shape before a
// reservation suffix is chosen. Kind and Identity identify the artifact in the
// engine manifest; ProposedBasename is one basename, never a path.
type ArtifactDeclaration struct {
	Kind             string
	Identity         string
	ProposedBasename string
}

// ReservedArtifact is an ArtifactDeclaration with its selected basename.
type ReservedArtifact struct {
	Kind     string
	Identity string
	Basename string
}

// ReservationSet reserves every artifact required for one publication. All
// artifacts in a set use the same suffix number, so a sidecar collision cannot
// produce a mixed set of names.
type ReservationSet struct {
	GroupID   string
	Directory Volume
	Artifacts []ReservedArtifact
}

// SelectionRequest contains immutable admission input. The store supplies its
// active durable claims only when it invokes the SelectionCallback.
type SelectionRequest struct {
	GroupID   string
	Directory Volume
	Artifacts []ArtifactDeclaration
}

// NameComparison defines the filename equality semantics for the selected
// output volume. Implementations may use the filesystem's native rules.
type NameComparison interface {
	Equal(a, b string) bool
}

// VolumeComparison defines when two canonical output roots refer to the same
// comparison domain. It is separate from filename comparison because volume
// identities and path casing are platform-specific.
type VolumeComparison interface {
	Equal(a, b Volume) bool
}

// Policies contains the caller-selected platform policy pair.
type Policies struct {
	Names   NameComparison
	Volumes VolumeComparison
}

// ExactNames compares basenames byte-for-byte.
type ExactNames struct{}

func (ExactNames) Equal(a, b string) bool { return a == b }

// FoldedNames compares basenames with Unicode simple case folding. A caller
// that knows a volume uses different rules can provide its own policy.
type FoldedNames struct{}

func (FoldedNames) Equal(a, b string) bool { return strings.EqualFold(a, b) }

// CanonicalVolumes compares stable directory identities. Output-root
// canonicalization and identity acquisition are intentionally owned outside
// this package.
type CanonicalVolumes struct{}

func (CanonicalVolumes) Equal(a, b Volume) bool {
	return a.Identity == b.Identity
}

func (p Policies) valid() bool { return p.Names != nil && p.Volumes != nil }

func validateVolume(volume Volume) error {
	if err := validateBoundedText("output root", volume.CanonicalPath, MaxCanonicalPathBytes); err != nil {
		return err
	}
	if err := validateBoundedText("output root identity", volume.Identity, MaxVolumeIdentityBytes); err != nil {
		return err
	}
	if !filepath.IsAbs(volume.CanonicalPath) {
		return fmt.Errorf("%w: output root must be an absolute canonical path", ErrInvalidDeclaration)
	}
	if filepath.Clean(volume.CanonicalPath) != volume.CanonicalPath {
		return fmt.Errorf("%w: output root is not clean", ErrInvalidDeclaration)
	}
	return nil
}

func validateDeclarations(artifacts []ArtifactDeclaration, names NameComparison) error {
	if len(artifacts) == 0 || len(artifacts) > MaxArtifactsPerSet {
		return fmt.Errorf("%w: no artifacts", ErrInvalidDeclaration)
	}
	seenIDs := make(map[string]struct{}, len(artifacts))
	seenNames := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		if err := validateBoundedText("artifact kind", artifact.Kind, MaxKindBytes); err != nil {
			return err
		}
		if err := validateBoundedText("artifact identity", artifact.Identity, MaxIdentityBytes); err != nil {
			return err
		}
		id := artifact.Kind + "\x00" + artifact.Identity
		if _, exists := seenIDs[id]; exists {
			return fmt.Errorf("%w: duplicate artifact kind and identity", ErrInvalidDeclaration)
		}
		seenIDs[id] = struct{}{}
		if err := ValidateBasename(artifact.ProposedBasename); err != nil {
			return err
		}
		for _, name := range seenNames {
			if names.Equal(name, artifact.ProposedBasename) {
				return fmt.Errorf("%w: artifacts share a destination basename", ErrInvalidDeclaration)
			}
		}
		seenNames = append(seenNames, artifact.ProposedBasename)
	}
	return nil
}

// ValidateBasename accepts one portable output filename component. It rejects
// path syntax on every supported platform and Windows-reserved names even when
// the selecting host is not Windows, so a durable reservation remains portable.
func ValidateBasename(name string) error {
	if !utf8.ValidString(name) {
		return fmt.Errorf("%w: basename is not valid UTF-8", ErrInvalidDeclaration)
	}
	if name == "" || name == "." || name == ".." || len(name) > MaxBasenameBytes {
		return fmt.Errorf("%w: invalid basename length or dot component", ErrInvalidDeclaration)
	}
	if strings.ContainsAny(name, "/\\\x00") || filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
		return fmt.Errorf("%w: basename contains path syntax", ErrInvalidDeclaration)
	}
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return fmt.Errorf("%w: Windows-trimmed basename", ErrInvalidDeclaration)
	}
	for _, r := range name {
		if unicode.IsControl(r) || strings.ContainsRune(`<>:"|?*`, r) {
			return fmt.Errorf("%w: invalid filename character", ErrInvalidDeclaration)
		}
	}
	base := strings.TrimRight(name, " .")
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	base = strings.TrimRight(base, " .")
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return fmt.Errorf("%w: Windows reserved basename", ErrInvalidDeclaration)
	}
	return nil
}

func validateReservation(set ReservationSet, policies Policies) error {
	if err := validateBoundedText("reservation group ID", set.GroupID, MaxGroupIDBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidReservation, err)
	}
	if err := validateVolume(set.Directory); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidReservation, err)
	}
	if len(set.Artifacts) == 0 || len(set.Artifacts) > MaxArtifactsPerActiveSet {
		return fmt.Errorf("%w: invalid active artifact count", ErrInvalidReservation)
	}
	seenIDs := make(map[string]struct{}, len(set.Artifacts))
	seenNames := make([]string, 0, len(set.Artifacts))
	for _, artifact := range set.Artifacts {
		if err := validateBoundedText("reserved artifact kind", artifact.Kind, MaxKindBytes); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidReservation, err)
		}
		if err := validateBoundedText("reserved artifact identity", artifact.Identity, MaxIdentityBytes); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidReservation, err)
		}
		id := artifact.Kind + "\x00" + artifact.Identity
		if _, exists := seenIDs[id]; exists {
			return fmt.Errorf("%w: duplicate artifact kind and identity", ErrInvalidReservation)
		}
		seenIDs[id] = struct{}{}
		if err := ValidateBasename(artifact.Basename); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidReservation, err)
		}
		for _, name := range seenNames {
			if policies.Names.Equal(name, artifact.Basename) {
				return fmt.Errorf("%w: artifacts share a destination basename", ErrInvalidReservation)
			}
		}
		seenNames = append(seenNames, artifact.Basename)
	}
	return nil
}

func validateRequest(request SelectionRequest, policies Policies) error {
	if err := validateBoundedText("reservation group ID", request.GroupID, MaxGroupIDBytes); err != nil {
		return err
	}
	if err := validateVolume(request.Directory); err != nil {
		return err
	}
	return validateDeclarations(request.Artifacts, policies.Names)
}

func validateBoundedText(field, value string, maxBytes int) error {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) {
		return fmt.Errorf("%w: invalid %s", ErrInvalidDeclaration, field)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: invalid %s", ErrInvalidDeclaration, field)
		}
	}
	return nil
}
