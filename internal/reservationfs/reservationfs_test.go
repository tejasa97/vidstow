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
	parent := realTempDir(t)
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

func TestEnsureRootRejectsSymlinkedParentBeforeCreatingChild(t *testing.T) {
	// A symlinked parent must be rejected before any component below it is
	// created, so an untrusted root is never touched. The child directory is
	// only present if a parent link was followed, which this test forbids.
	base := realTempDir(t)
	realDir := filepath.Join(base, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "marker.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(realDir, link); err != nil {
		if isLinkCreationPermission(err) {
			t.Skipf("native symlink creation is unavailable: %v", err)
		}
		t.Fatal(err)
	}

	untrusted := filepath.Join(link, "child", "output")
	if _, err := EnsureRoot(untrusted); !errors.Is(err, ErrUnsafe) || !errors.Is(err, ErrSymlinkRoot) {
		t.Fatalf("EnsureRoot(symlinked-parent) error = %v, want typed unsafe symlink error", err)
	}
	if _, err := os.Lstat(filepath.Join(link, "child")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("EnsureRoot created a child through a symlinked parent: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(realDir, "child")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("EnsureRoot created a child inside the linked target: %v", err)
	}
}

func TestOpenRootRejectsSymlinkedParent(t *testing.T) {
	base := realTempDir(t)
	realDir := filepath.Join(base, "real")
	output := filepath.Join(realDir, "child", "output")
	if err := os.MkdirAll(output, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(realDir, link); err != nil {
		if isLinkCreationPermission(err) {
			t.Skipf("native symlink creation is unavailable: %v", err)
		}
		t.Fatal(err)
	}

	if _, err := OpenRoot(filepath.Join(link, "child", "output")); !errors.Is(err, ErrUnsafe) || !errors.Is(err, ErrSymlinkRoot) {
		t.Fatalf("OpenRoot(symlinked-parent) error = %v, want typed unsafe symlink error", err)
	}
}

func TestEnsureOpenRootCreatesRequestedAbsolutePathWithoutCWDShadow(t *testing.T) {
	requested := filepath.Join(realTempDir(t), "missing", "output")
	root, err := EnsureOpenRoot(requested)
	if err != nil {
		t.Fatalf("EnsureOpenRoot(%q): %v", requested, err)
	}
	defer root.Close()

	facts := root.Facts()
	if facts.Volume.CanonicalPath != requested {
		t.Fatalf("root path = %q, want %q", facts.Volume.CanonicalPath, requested)
	}
	if info, err := os.Stat(requested); err != nil || !info.IsDir() {
		t.Fatalf("requested output path was not created: info=%v err=%v", info, err)
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	shadow := filepath.Join(workingDirectory, strings.TrimPrefix(requested, string(filepath.Separator)))
	if _, err := os.Lstat(shadow); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("EnsureOpenRoot created a CWD-relative shadow path %q: %v", shadow, err)
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
	directory := realTempDir(t)
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
	parent := realTempDir(t)
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
	root, err := OpenRoot(realTempDir(t))
	if err != nil {
		if IsUnsupported(err) {
			t.Skipf("host filesystem does not expose required authority: %v", err)
		}
		t.Fatalf("OpenRoot: %v", err)
	}
	return root
}

func realTempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temporary directory: %v", err)
	}
	return path
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
