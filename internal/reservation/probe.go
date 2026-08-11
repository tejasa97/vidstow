package reservation

import (
	"context"
)

// Availability is the conservative result of a no-follow target inspection.
type Availability uint8

const (
	Available Availability = iota
	Occupied
)

// AvailabilityProbe checks whether a proposed destination is available. Each
// call must first prove that volume.CanonicalPath is still the directory named
// by volume.Identity and then traverse/open without following directory links
// or Windows reparse points. It must fail closed for every existing child,
// root replacement, unsupported platform condition, or inspection error.
//
// The engine/store platform owner supplies this seam. This pure package has no
// default probe because Lstat of only filepath.Join(root, basename) would
// follow a replaced root and is not a safe no-follow operation.
type AvailabilityProbe interface {
	Probe(context.Context, Volume, string) (Availability, error)
}
