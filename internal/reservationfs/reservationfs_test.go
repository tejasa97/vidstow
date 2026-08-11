package reservationfs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tejasa97/vidstow/internal/reservation"
)

var _ reservation.AvailabilityProbe = (*Root)(nil)

func TestOpenRootProvidesBoundFactsAndProbe(t *testing.T) {
	root := openTestRoot(t)
	defer root.Close()

	facts := root.Facts()
	if facts.Volume.CanonicalPath == "" || !filepath.IsAbs(facts.Volume.CanonicalPath) {
		t.Fatalf("volume path = %#v, want absolute path", facts.Volume.CanonicalPath)
	}
	if filepath.Clean(facts.Volume.CanonicalPath) != facts.Volume.CanonicalPath {
		t.Fatalf("volume path = %#v, want clean path", facts.Volume.CanonicalPath)
	}
	if facts.Volume.Identity == "" || len(facts.Volume.Identity) > reservation.MaxVolumeIdentityBytes {
		t.Fatalf("volume identity = %q, want bounded non-empty identity", facts.Volume.Identity)
	}
	if facts.Names == nil || facts.Volumes == nil || facts.Probe == nil {
		t.Fatalf("incomplete facts: %#v", facts)
	}
	if facts.NameComparison == nil || facts.VolumeComparison == nil || facts.AvailabilityProbe == nil {
		t.Fatalf("incomplete explicit facts aliases: %#v", facts)
	}
	if got := facts.Policies(); got.Names == nil || got.Volumes == nil {
		t.Fatalf("facts policies = %#v", got)
	}
	if !facts.Volumes.Equal(facts.Volume, facts.Volume) {
		t.Fatal("volume policy did not equal the root to itself")
	}

	availability, err := facts.Probe.Probe(context.Background(), facts.Volume, "not-present.bin")
	if err != nil {
		t.Fatalf("probe missing child: %v", err)
	}
	if availability != reservation.Available {
		t.Fatalf("missing child availability = %v, want Available", availability)
	}
	if got := facts.Names.Equal("CaseProbe", "caseprobe"); got == facts.CaseSensitive {
		t.Fatalf("case policy mismatch: CaseSensitive=%v, equality=%v", facts.CaseSensitive, got)
	}
}

func TestProbeTreatsEveryExistingChildAsOccupied(t *testing.T) {
	root := openTestRoot(t)
	defer root.Close()
	facts := root.Facts()

	if err := os.WriteFile(filepath.Join(facts.Volume.CanonicalPath, "regular.bin"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(facts.Volume.CanonicalPath, "directory.bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("regular.bin", filepath.Join(facts.Volume.CanonicalPath, "symlink.bin")); err != nil {
		if isLinkCreationPermission(err) {
			t.Skipf("native symlink creation is unavailable: %v", err)
		}
		t.Fatal(err)
	}

	for _, basename := range []string{"regular.bin", "directory.bin", "symlink.bin"} {
		availability, err := facts.Probe.Probe(context.Background(), facts.Volume, basename)
		if err != nil {
			t.Fatalf("probe %q: %v", basename, err)
		}
		if availability != reservation.Occupied {
			t.Fatalf("probe %q = %v, want Occupied", basename, availability)
		}
	}
}

func TestOpenRootRejectsSymlinkRoot(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(target, link); err != nil {
		if isLinkCreationPermission(err) {
			t.Skipf("native symlink creation is unavailable: %v", err)
		}
		t.Fatal(err)
	}
	_, err := OpenRoot(link)
	if !errors.Is(err, ErrUnsafe) || !errors.Is(err, ErrSymlinkRoot) {
		t.Fatalf("OpenRoot(symlink) error = %v, want typed unsafe symlink error", err)
	}
}

func TestProbeRejectsDifferentVolumeFacts(t *testing.T) {
	root := openTestRoot(t)
	defer root.Close()
	facts := root.Facts()

	_, err := facts.Probe.Probe(context.Background(), reservation.Volume{
		CanonicalPath: facts.Volume.CanonicalPath,
		Identity:      facts.Volume.Identity + ".different",
	}, "target.bin")
	if !errors.Is(err, ErrUnsafe) || !errors.Is(err, ErrVolumeMismatch) {
		t.Fatalf("mismatched volume error = %v, want typed unsafe mismatch", err)
	}
}

func TestDirectoryIdentityIsStableAcrossHandles(t *testing.T) {
	directory := t.TempDir()
	first, err := OpenRoot(directory)
	if err != nil {
		if IsUnsupported(err) {
			t.Skipf("host filesystem does not expose required authority: %v", err)
		}
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	firstFacts, secondFacts := first.Facts(), second.Facts()
	if firstFacts.Volume.Identity != secondFacts.Volume.Identity {
		t.Fatalf("same directory identities differ: %q != %q", firstFacts.Volume.Identity, secondFacts.Volume.Identity)
	}
	if !firstFacts.Volumes.Equal(firstFacts.Volume, secondFacts.Volume) {
		t.Fatal("volume comparison rejected two handles for the same directory")
	}
}

func TestRootReplacementFailsClosed(t *testing.T) {
	parent := t.TempDir()
	rootPath := filepath.Join(parent, "output")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := OpenRoot(rootPath)
	if err != nil {
		if IsUnsupported(err) {
			t.Skipf("host filesystem does not expose required authority: %v", err)
		}
		t.Fatal(err)
	}
	facts := root.Facts()
	defer root.Close()

	replaced := rootPath + ".old"
	if err := os.Rename(rootPath, replaced); err != nil {
		if isDirectoryRenamePermission(err) {
			t.Skipf("native directory replacement is unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}

	_, err = facts.Probe.Probe(context.Background(), facts.Volume, "target.bin")
	if !errors.Is(err, ErrUnsafe) || !errors.Is(err, ErrRootChanged) {
		t.Fatalf("probe after root replacement = %v, want typed root-change error", err)
	}
}

func TestProbeRejectsUnboundedOrPathBasename(t *testing.T) {
	root := openTestRoot(t)
	defer root.Close()
	facts := root.Facts()
	for _, basename := range []string{"", "nested/file.bin", strings.Repeat("x", MaxProbeBasenameBytes+1)} {
		_, err := facts.Probe.Probe(context.Background(), facts.Volume, basename)
		if !errors.Is(err, ErrInvalidProbe) {
			t.Errorf("probe(%q) error = %v, want ErrInvalidProbe", basename, err)
		}
	}
}

func TestCloseIsConcurrencySafeWithProbe(t *testing.T) {
	root := openTestRoot(t)
	facts := root.Facts()
	const workers = 16
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 100; j++ {
				_, _ = facts.Probe.Probe(context.Background(), facts.Volume, "concurrent.bin")
			}
		}()
	}
	close(start)
	if err := root.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	wg.Wait()
	if err := root.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}
	if _, err := facts.Probe.Probe(context.Background(), facts.Volume, "after-close.bin"); !errors.Is(err, ErrClosed) {
		t.Fatalf("probe after Close() error = %v, want ErrClosed", err)
	}
}

func TestRootFactsRemainBoundedAfterClose(t *testing.T) {
	root := openTestRoot(t)
	facts := root.Facts()
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if facts.Volume.Identity == "" || facts.Names == nil || facts.Volumes == nil {
		t.Fatalf("facts lost immutable data after Close: %#v", facts)
	}
}

func openTestRoot(t *testing.T) *Root {
	t.Helper()
	root, err := OpenRoot(t.TempDir())
	if err != nil {
		if IsUnsupported(err) {
			t.Skipf("host filesystem does not expose required authority: %v", err)
		}
		t.Fatalf("OpenRoot: %v", err)
	}
	return root
}

func isLinkCreationPermission(err error) bool {
	return errors.Is(err, os.ErrPermission) || strings.Contains(strings.ToLower(err.Error()), "privilege") || strings.Contains(strings.ToLower(err.Error()), "operation not permitted")
}

func isDirectoryRenamePermission(err error) bool {
	return errors.Is(err, os.ErrPermission) || strings.Contains(strings.ToLower(err.Error()), "sharing violation")
}

func TestErrorKindsAreInspectable(t *testing.T) {
	unsupported := unsupportedError("test", fmt.Errorf("%w", ErrUnsupported))
	unsafe := unsafeError("test", fmt.Errorf("%w", ErrRootChanged))
	if !IsUnsupported(unsupported) || errors.Is(unsupported, ErrUnsafe) {
		t.Fatalf("unsupported classification = %v", unsupported)
	}
	if !IsUnsafe(unsafe) || !errors.Is(unsafe, ErrRootChanged) {
		t.Fatalf("unsafe classification = %v", unsafe)
	}
	var typed *Error
	if !errors.As(unsafe, &typed) || typed.Kind != ErrorKindUnsafe {
		t.Fatalf("typed unsafe error = %#v", typed)
	}
}
