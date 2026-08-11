package reservationfs

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/tejasa97/vidstow/internal/reservation"
)

const (
	// MaxRootPathBytes is the maximum UTF-8 byte length accepted for a root
	// path before platform-specific path encoding.
	MaxRootPathBytes = reservation.MaxCanonicalPathBytes
	// MaxProbeBasenameBytes is the maximum UTF-8 byte length accepted by Probe.
	MaxProbeBasenameBytes = reservation.MaxBasenameBytes
)

var (
	// ErrUnsupported means that the host or filesystem did not expose enough
	// authoritative information to preserve reservation semantics.
	ErrUnsupported = errors.New("reservationfs: unsupported platform or filesystem")
	// ErrUnsafe means that a root, identity, or inspection could not be
	// proven safe. Callers must not treat it as Available.
	ErrUnsafe = errors.New("reservationfs: unsafe filesystem state")
	// ErrClosed means the caller closed the root before using its probe.
	ErrClosed = errors.New("reservationfs: root is closed")
	// ErrRootChanged means the named root no longer denotes the handle's
	// original directory, or its case policy changed during inspection.
	ErrRootChanged = errors.New("reservationfs: output root changed")
	// ErrVolumeMismatch means a probe was asked to inspect a Volume other than
	// the one bound to its caller-owned Root.
	ErrVolumeMismatch = errors.New("reservationfs: probe volume does not match root")
	// ErrSymlinkRoot means the supplied root itself is a symlink or reparse
	// point. The adapter never resolves one into an apparently safe root.
	ErrSymlinkRoot = errors.New("reservationfs: root is a symlink or reparse point")
	// ErrInvalidRoot means the root path cannot be represented as a bounded,
	// absolute canonical path.
	ErrInvalidRoot = errors.New("reservationfs: invalid output root")
	// ErrInvalidProbe means the probe input is not a single bounded basename.
	ErrInvalidProbe = errors.New("reservationfs: invalid probe basename")
)

// ErrorKind identifies the conservative class of a platform adapter error.
type ErrorKind uint8

const (
	// ErrorKindUnsupported indicates missing platform authority or an
	// unsupported filesystem feature.
	ErrorKindUnsupported ErrorKind = iota + 1
	// ErrorKindUnsafe indicates a root replacement, link/reparse, or failed
	// security-relevant inspection.
	ErrorKindUnsafe
)

func (k ErrorKind) String() string {
	switch k {
	case ErrorKindUnsupported:
		return "unsupported"
	case ErrorKindUnsafe:
		return "unsafe"
	default:
		return "unknown"
	}
}

// Error is returned when a platform operation cannot prove the authority
// needed by a reservation probe. It is intentionally inspectable so callers
// can distinguish an unavailable platform from an unsafe root.
type Error struct {
	Kind ErrorKind
	Op   string
	Err  error
}

func (e *Error) Error() string {
	if e == nil {
		return "reservationfs: <nil>"
	}
	if e.Err == nil {
		return fmt.Sprintf("reservationfs: %s: %s", e.Kind, e.Op)
	}
	return fmt.Sprintf("reservationfs: %s: %s: %v", e.Kind, e.Op, e.Err)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *Error) Is(target error) bool {
	if e == nil {
		return target == nil
	}
	if target == ErrUnsupported && e.Kind == ErrorKindUnsupported {
		return true
	}
	if target == ErrUnsafe && e.Kind == ErrorKindUnsafe {
		return true
	}
	if other, ok := target.(*Error); ok {
		return e.Kind == other.Kind
	}
	return errors.Is(e.Err, target)
}

// IsUnsupported reports whether err is a typed unsupported-platform or
// unsupported-filesystem result.
func IsUnsupported(err error) bool { return errors.Is(err, ErrUnsupported) }

// IsUnsafe reports whether err is a typed fail-closed filesystem result.
func IsUnsafe(err error) bool { return errors.Is(err, ErrUnsafe) }

func unsupportedError(op string, cause error) error {
	return &Error{Kind: ErrorKindUnsupported, Op: op, Err: cause}
}

func unsafeError(op string, cause error) error {
	return &Error{Kind: ErrorKindUnsafe, Op: op, Err: cause}
}

// Facts are the complete adapter output for one caller-owned root. Probe is
// backed by the same Root handle and remains valid until Root.Close returns.
// The long field names are aliases for callers that prefer explicit wiring;
// Names, Volumes, and Probe are the compact forms used by reservation.Options.
type Facts struct {
	Volume reservation.Volume

	Names   reservation.NameComparison
	Volumes reservation.VolumeComparison
	Probe   reservation.AvailabilityProbe

	NameComparison    reservation.NameComparison
	VolumeComparison  reservation.VolumeComparison
	AvailabilityProbe reservation.AvailabilityProbe
	CaseSensitive     bool
}

// Policies returns the comparison pair derived from the opened root.
func (f Facts) Policies() reservation.Policies {
	names := f.Names
	if names == nil {
		names = f.NameComparison
	}
	volumes := f.Volumes
	if volumes == nil {
		volumes = f.VolumeComparison
	}
	return reservation.Policies{Names: names, Volumes: volumes}
}

type platformRoot interface {
	volume() reservation.Volume
	nameComparison() reservation.NameComparison
	volumeComparison() reservation.VolumeComparison
	caseSensitive() bool
	probe(context.Context, reservation.Volume, string) (reservation.Availability, error)
	close() error
}

// Root is a caller-owned, concurrency-safe handle to one output directory.
// It also implements reservation.AvailabilityProbe. A probe holds a read lock
// for its entire no-follow inspection; Close takes the write lock, so a handle
// can never be closed while an OS operation is using it.
type Root struct {
	mu      sync.RWMutex
	backend platformRoot
	closed  bool

	volume        reservation.Volume
	names         reservation.NameComparison
	volumes       reservation.VolumeComparison
	caseSensitive bool
}

// RootHandle is an explicit alias for callers that want to name ownership in
// their adapter layer.
type RootHandle = Root

// OpenRoot opens path without following a link/reparse root and derives all
// reservation facts from the resulting directory and its filesystem. The
// caller owns the returned handle and must call Close.
func OpenRoot(path string) (*Root, error) {
	backend, err := openPlatformRoot(path)
	if err != nil {
		return nil, err
	}
	root := &Root{
		backend:       backend,
		volume:        backend.volume(),
		names:         backend.nameComparison(),
		volumes:       backend.volumeComparison(),
		caseSensitive: backend.caseSensitive(),
	}
	return root, nil
}

// Open is an alias for OpenRoot.
func Open(path string) (*Root, error) { return OpenRoot(path) }

// New is an alias for OpenRoot.
func New(path string) (*Root, error) { return OpenRoot(path) }

// Facts snapshots the immutable adapter facts and returns a probe backed by
// this Root. The snapshot remains useful after Close for diagnostics, but its
// probe will return ErrClosed.
func (r *Root) Facts() Facts {
	if r == nil {
		return Facts{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return Facts{
		Volume:            r.volume,
		Names:             r.names,
		Volumes:           r.volumes,
		Probe:             r,
		NameComparison:    r.names,
		VolumeComparison:  r.volumes,
		AvailabilityProbe: r,
		CaseSensitive:     r.caseSensitive,
	}
}

// Adapter is an alias for Facts, useful when passing the result through an
// application adapter boundary.
func (r *Root) Adapter() Facts { return r.Facts() }

// Volume returns the root's stable reservation volume value.
func (r *Root) Volume() reservation.Volume {
	if r == nil {
		return reservation.Volume{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.volume
}

// NameComparison returns the root-derived filename equality policy.
func (r *Root) NameComparison() reservation.NameComparison {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.names
}

// VolumeComparison returns the root-derived output-root equality policy.
func (r *Root) VolumeComparison() reservation.VolumeComparison {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.volumes
}

// Policies returns the root-derived reservation policy pair.
func (r *Root) Policies() reservation.Policies {
	return reservation.Policies{Names: r.NameComparison(), Volumes: r.VolumeComparison()}
}

// CaseSensitive reports the case policy established from the actual opened
// directory/volume rather than from the host operating-system default.
func (r *Root) CaseSensitive() bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.caseSensitive
}

// Probe implements reservation.AvailabilityProbe. It only accepts the exact
// Volume returned by this Root and never follows a child link/reparse point.
func (r *Root) Probe(ctx context.Context, volume reservation.Volume, basename string) (reservation.Availability, error) {
	if r == nil {
		return reservation.Occupied, ErrClosed
	}
	if ctx == nil {
		return reservation.Occupied, unsafeError("probe context", errors.New("nil context"))
	}
	if err := ctx.Err(); err != nil {
		return reservation.Occupied, err
	}
	if err := validateProbeBasename(basename); err != nil {
		return reservation.Occupied, err
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return reservation.Occupied, ErrClosed
	}
	if volume != r.volume {
		return reservation.Occupied, unsafeError("probe volume", ErrVolumeMismatch)
	}
	return r.backend.probe(ctx, volume, basename)
}

// Close releases the caller-owned root handle. It is idempotent and waits for
// all in-flight probes before closing the native resource.
func (r *Root) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return r.backend.close()
}

func normalizeRootPath(path string) (string, error) {
	if path == "" || !utf8.ValidString(path) || strings.IndexByte(path, 0) >= 0 || len(path) > MaxRootPathBytes {
		return "", unsafeError("validate root path", ErrInvalidRoot)
	}
	for _, r := range path {
		if unicode.IsControl(r) {
			return "", unsafeError("validate root path", ErrInvalidRoot)
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", unsafeError("canonicalize root path", fmt.Errorf("%w: %v", ErrInvalidRoot, err))
	}
	abs = filepath.Clean(abs)
	if !filepath.IsAbs(abs) || len(abs) > MaxRootPathBytes {
		return "", unsafeError("canonicalize root path", ErrInvalidRoot)
	}
	return abs, nil
}

func validateProbeBasename(name string) error {
	if len(name) > MaxProbeBasenameBytes {
		return fmt.Errorf("%w: basename exceeds %d bytes", ErrInvalidProbe, MaxProbeBasenameBytes)
	}
	if err := reservation.ValidateBasename(name); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidProbe, err)
	}
	return nil
}

func validateAdapterVolume(volume reservation.Volume) error {
	if volume.CanonicalPath == "" || volume.Identity == "" || len(volume.CanonicalPath) > reservation.MaxCanonicalPathBytes || len(volume.Identity) > reservation.MaxVolumeIdentityBytes {
		return unsupportedError("validate adapter volume", errors.New("platform volume facts exceed reservation bounds"))
	}
	return nil
}
