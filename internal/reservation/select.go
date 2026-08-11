package reservation

import (
	"context"
	"errors"
	"fmt"
	"path"
)

const (
	// MaxAllowedSuffix bounds work spent looking for a collision-free name.
	// Callers should use a smaller volume-specific operational limit when one
	// is available.
	MaxAllowedSuffix uint64 = 1_000_000
	defaultMaxSuffix        = MaxAllowedSuffix
)

// Options configures a Selector. Policies should be selected from the output
// root's platform/volume facts rather than guessed from the running OS.
type Options struct {
	Policies  Policies
	Probe     AvailabilityProbe
	MaxSuffix uint64
}

// Selector is a pure selector configuration. It never acquires a State lock or
// commits a reservation; those actions belong to the store transaction.
type Selector struct {
	policies  Policies
	probe     AvailabilityProbe
	maxSuffix uint64
}

// NewSelector requires caller-owned platform policies and an identity-validating
// availability probe. There is deliberately no generic filesystem default:
// Lstat on a child alone can follow a root that was replaced by a link.
func NewSelector(options Options) (*Selector, error) {
	if !options.Policies.valid() {
		return nil, errors.New("reservation: names and volumes policies must both be provided")
	}
	if options.Probe == nil {
		return nil, errors.New("reservation: identity-validating availability probe is required")
	}
	if options.MaxSuffix == 0 {
		options.MaxSuffix = defaultMaxSuffix
	}
	if options.MaxSuffix > MaxAllowedSuffix {
		return nil, fmt.Errorf("reservation: maximum suffix exceeds %d", MaxAllowedSuffix)
	}
	return &Selector{policies: options.Policies, probe: options.Probe, maxSuffix: options.MaxSuffix}, nil
}

// Select chooses the first suffix whose full artifact set is free on disk and
// in active. Suffix one keeps the proposed basenames; later suffixes
// are formatted as "Title (2).ext". It does not create a durable claim.
func (s *Selector) Select(ctx context.Context, request SelectionRequest, active []ReservationSet) (ReservationSet, error) {
	callback, err := s.Callback(request)
	if err != nil {
		return ReservationSet{}, err
	}
	return callback(ctx, active)
}

// SelectionCallback is invoked exactly once by the State store while it holds
// its process and stable cross-process State lock and works on its latest
// cloned durable image. On success, the store applies the returned reservation
// and its associated job mutation to that same clone, then atomically commits
// the one image. On error, including a stale/late conflict, it discards the
// clone and releases both locks. The selector neither commits nor retries.
type SelectionCallback func(context.Context, []ReservationSet) (ReservationSet, error)

// Callback validates immutable admission input before a transaction begins and
// returns the callback that the store invokes with its current durable claims.
// The supplied active input must include live jobs and reservation-retaining
// tombstones. The store must reread it under the cross-process State lock,
// retain that lock through callback return and the single clone commit, and
// never invoke the callback again to silently accept a different suffix.
func (s *Selector) Callback(request SelectionRequest) (SelectionCallback, error) {
	if s == nil {
		return nil, errors.New("reservation: nil selector")
	}
	if err := validateRequest(request, s.policies); err != nil {
		return nil, err
	}
	prepared := SelectionRequest{
		GroupID:   request.GroupID,
		Directory: request.Directory,
		Artifacts: append([]ArtifactDeclaration(nil), request.Artifacts...),
	}
	return func(ctx context.Context, active []ReservationSet) (ReservationSet, error) {
		return s.choose(ctx, prepared, active)
	}, nil
}

func (s *Selector) choose(ctx context.Context, request SelectionRequest, active []ReservationSet) (ReservationSet, error) {
	if err := ctx.Err(); err != nil {
		return ReservationSet{}, err
	}
	if len(active) > MaxActiveReservationSets {
		return ReservationSet{}, fmt.Errorf("%w: too many active reservations", ErrInvalidReservation)
	}
	for _, set := range active {
		if err := validateReservation(set, s.policies); err != nil {
			return ReservationSet{}, err
		}
	}
	for suffix := uint64(1); suffix <= s.maxSuffix; suffix++ {
		set, err := buildSet(request, suffix)
		if err != nil {
			return ReservationSet{}, err
		}
		occupied, err := s.occupied(ctx, set, active)
		if err != nil {
			return ReservationSet{}, err
		}
		if !occupied {
			return set, nil
		}
	}
	return ReservationSet{}, ErrNoAvailableName
}

func buildSet(request SelectionRequest, suffix uint64) (ReservationSet, error) {
	set := ReservationSet{GroupID: request.GroupID, Directory: request.Directory, Artifacts: make([]ReservedArtifact, len(request.Artifacts))}
	for i, artifact := range request.Artifacts {
		basename := artifact.ProposedBasename
		if suffix > 1 {
			basename = suffixedBasename(basename, suffix)
			if err := ValidateBasename(basename); err != nil {
				return ReservationSet{}, ErrNoAvailableName
			}
		}
		set.Artifacts[i] = ReservedArtifact{Kind: artifact.Kind, Identity: artifact.Identity, Basename: basename}
	}
	return set, nil
}

func suffixedBasename(basename string, suffix uint64) string {
	ext := path.Ext(basename)
	stem := basename[:len(basename)-len(ext)]
	return fmt.Sprintf("%s (%d)%s", stem, suffix, ext)
}

func (s *Selector) occupied(ctx context.Context, set ReservationSet, active []ReservationSet) (bool, error) {
	for _, artifact := range set.Artifacts {
		for _, existing := range active {
			if !s.policies.Volumes.Equal(set.Directory, existing.Directory) {
				continue
			}
			for _, reserved := range existing.Artifacts {
				if s.policies.Names.Equal(artifact.Basename, reserved.Basename) {
					return true, nil
				}
			}
		}
		availability, err := s.probe.Probe(ctx, set.Directory, artifact.Basename)
		if err != nil {
			return false, err
		}
		if availability != Available {
			return true, nil
		}
	}
	return false, nil
}
