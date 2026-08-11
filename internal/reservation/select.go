package reservation

import (
	"context"
	"errors"
	"fmt"
	"path"
	"unicode/utf8"
)

const (
	// DefaultMaxSuffix bounds normal collision search to a predictable amount
	// of filesystem probing. It can be lowered per admission request.
	DefaultMaxSuffix uint64 = 1_000
	// MaxAllowedSuffix is the hard operational ceiling for collision search.
	MaxAllowedSuffix uint64 = 10_000
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
	if isTypedNil(options.Probe) {
		return nil, errors.New("reservation: identity-validating availability probe is required")
	}
	if options.MaxSuffix == 0 {
		options.MaxSuffix = DefaultMaxSuffix
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
	index, err := s.indexActive(ctx, active)
	if err != nil {
		return ReservationSet{}, err
	}
	if _, exists := index.groups[request.GroupID]; exists {
		return ReservationSet{}, fmt.Errorf("%w: selection group ID already has an active reservation", ErrInvalidReservation)
	}
	for suffix := uint64(1); suffix <= s.maxSuffix; suffix++ {
		if err := ctx.Err(); err != nil {
			return ReservationSet{}, err
		}
		set, err := buildSet(request, suffix, s.policies.Names)
		if err != nil {
			return ReservationSet{}, err
		}
		occupied, err := s.occupied(ctx, set, index)
		if err != nil {
			return ReservationSet{}, err
		}
		if !occupied {
			return set, nil
		}
	}
	return ReservationSet{}, ErrNoAvailableName
}

func buildSet(request SelectionRequest, suffix uint64, names NameComparison) (ReservationSet, error) {
	set := ReservationSet{GroupID: request.GroupID, Directory: request.Directory, Artifacts: make([]ReservedArtifact, len(request.Artifacts))}
	seenNames := make([]string, 0, len(request.Artifacts))
	for i, artifact := range request.Artifacts {
		basename := artifact.ProposedBasename
		if suffix > 1 {
			basename = suffixedBasename(basename, suffix)
			if err := ValidateBasename(basename); err != nil {
				return ReservationSet{}, ErrNoAvailableName
			}
		}
		for _, existing := range seenNames {
			if names.Equal(existing, basename) {
				return ReservationSet{}, ErrNoAvailableName
			}
		}
		seenNames = append(seenNames, basename)
		set.Artifacts[i] = ReservedArtifact{Kind: artifact.Kind, Identity: artifact.Identity, Basename: basename}
	}
	return set, nil
}

func suffixedBasename(basename string, suffix uint64) string {
	marker := fmt.Sprintf(" (%d)", suffix)
	ext := path.Ext(basename)
	// A leading dotfile has no extension for reservation suffix purposes.
	if ext == basename {
		ext = ""
	}
	stem := basename[:len(basename)-len(ext)]
	room := MaxBasenameBytes - len(marker) - len(ext)
	if room >= 0 {
		return truncateUTF8(stem, room) + marker + ext
	}
	// A pathological extension can consume the whole portable component. Keep
	// the deterministic marker and the largest valid UTF-8 extension prefix.
	return marker + truncateUTF8(ext, MaxBasenameBytes-len(marker))
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	for maxBytes > 0 && !utf8.ValidString(value[:maxBytes]) {
		maxBytes--
	}
	return value[:maxBytes]
}

type destinationKey struct {
	volume string
	name   string
}

type activeIndex struct {
	claims map[destinationKey]struct{}
	groups map[string]struct{}
}

func (s *Selector) indexActive(ctx context.Context, active []ReservationSet) (activeIndex, error) {
	if len(active) > MaxActiveReservationSets {
		return activeIndex{}, fmt.Errorf("%w: too many active reservations", ErrInvalidReservation)
	}
	index := activeIndex{claims: make(map[destinationKey]struct{}), groups: make(map[string]struct{}, len(active))}
	groups := make(map[string]struct{}, len(active))
	for _, set := range active {
		if err := ctx.Err(); err != nil {
			return activeIndex{}, err
		}
		if err := validateReservation(ctx, set, s.policies); err != nil {
			return activeIndex{}, err
		}
		if _, exists := groups[set.GroupID]; exists {
			return activeIndex{}, fmt.Errorf("%w: duplicate active reservation group ID", ErrInvalidReservation)
		}
		groups[set.GroupID] = struct{}{}
		index.groups[set.GroupID] = struct{}{}
		volumeKey := s.policies.Volumes.Key(set.Directory)
		for _, artifact := range set.Artifacts {
			if err := ctx.Err(); err != nil {
				return activeIndex{}, err
			}
			claim := destinationKey{volume: volumeKey, name: s.policies.Names.Key(artifact.Basename)}
			if _, exists := index.claims[claim]; exists {
				return activeIndex{}, fmt.Errorf("%w: duplicate active destination claim", ErrInvalidReservation)
			}
			index.claims[claim] = struct{}{}
		}
	}
	return index, nil
}

func (s *Selector) occupied(ctx context.Context, set ReservationSet, active activeIndex) (bool, error) {
	volumeKey := s.policies.Volumes.Key(set.Directory)
	for _, artifact := range set.Artifacts {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		claim := destinationKey{volume: volumeKey, name: s.policies.Names.Key(artifact.Basename)}
		if _, exists := active.claims[claim]; exists {
			return true, nil
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		availability, err := s.probe.Probe(ctx, set.Directory, artifact.Basename)
		if err != nil {
			return false, err
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if availability != Available {
			return true, nil
		}
	}
	return false, nil
}
