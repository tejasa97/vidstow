package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const recoveryMarkerVersion = 1

type recoveryMarker struct {
	Version       int       `json:"version"`
	TargetPath    string    `json:"targetPath"`
	TempPath      string    `json:"tempPath"`
	StoreRevision uint64    `json:"storeRevision"`
	CreatedAt     time.Time `json:"createdAt"`
}

type markerWriteResult struct {
	err     error
	present bool
}

const maxRecoveryMarkerBytes = 64 << 10

func writeRecoveryMarker(path string, marker recoveryMarker) markerWriteResult {
	data, err := json.Marshal(marker)
	if err != nil {
		return markerWriteResult{err: err}
	}
	f, err := createPrivateExclusive(path)
	if err != nil {
		// A failed exclusive create does not prove that the marker path is
		// absent: it may already exist behind an ACL or have changed during
		// the probe. Keep the candidate and quarantine unless the caller
		// explicitly supplies a no-evidence fault result in a test seam.
		return markerWriteResult{err: err, present: true}
	}
	present := true
	var written int
	if written, err = f.Write(data); err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return markerWriteResult{err: fmt.Errorf("store: write recovery marker: %w", err), present: present}
	}
	if err := syncPrivateParent(filepath.Dir(path)); err != nil {
		return markerWriteResult{err: fmt.Errorf("store: sync recovery marker: %w", err), present: present}
	}
	return markerWriteResult{present: true}
}

func readRecoveryMarker(path string) (recoveryMarker, bool, error) {
	f, err := openPrivateRead(path)
	if errors.Is(err, os.ErrNotExist) {
		return recoveryMarker{}, false, nil
	}
	if err != nil {
		return recoveryMarker{}, false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxRecoveryMarkerBytes+1))
	if err != nil {
		return recoveryMarker{}, true, err
	}
	if len(data) > maxRecoveryMarkerBytes {
		return recoveryMarker{}, true, errors.New("store: recovery marker exceeds size limit")
	}
	var marker recoveryMarker
	if err := decodeStrict(data, &marker); err != nil {
		return recoveryMarker{}, true, err
	}
	if marker.Version != recoveryMarkerVersion || !validPath(marker.TargetPath) || !validPath(marker.TempPath) || marker.StoreRevision > maxDurableCounter || !validTime(marker.CreatedAt) {
		return recoveryMarker{}, true, errors.New("store: invalid recovery marker")
	}
	return marker, true, nil
}

func removeRecoveryMarker(path string) error {
	marker, exists, err := readRecoveryMarker(path)
	if err != nil {
		return err
	}
	if !exists {
		return os.ErrNotExist
	}
	if err := removePrivate(path); err != nil {
		// If unlink or its parent sync was uncertain, restore the exact marker
		// image so a restart remains fail-closed.
		remark := writeRecoveryMarker(path, marker)
		if remark.err != nil {
			return fmt.Errorf("store: remove recovery marker: %w; re-mark: %v", err, remark.err)
		}
		return err
	}
	return nil
}
