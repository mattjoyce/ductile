package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mattjoyce/ductile/internal/fsown"
)

// WriteFileAtomic publishes data at path via temp → fsync → chown → rename, so
// the file at path is never observable half-written and never observable with the
// wrong owner (#167, #177).
//
// Both properties matter and they are easy to get separately. Four write→rename
// helpers existed independently in this tree before #167 and only one of them
// negotiated ownership; config.SetPath's persist did neither, writing straight
// over the file with os.WriteFile (#177). That was only accidentally safe:
// os.WriteFile truncates an EXISTING file in place, keeping the inode and
// therefore the owner. The safety evaporates the moment the target does not
// already exist — and the crash window was never covered at all. This is
// config.yaml; a truncated one is a gateway that cannot boot, with no obvious
// cause.
//
// The temp file is created in the target's own directory so the rename is a
// same-filesystem operation, which is what makes it atomic.
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file next to %s: %w", filepath.Base(path), err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("failed to write pending %s: %w", filepath.Base(path), err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("failed to set mode on pending %s: %w", filepath.Base(path), err)
	}
	// fsync before rename, not after. A rename is only atomic with respect to the
	// bytes that have actually reached the disk; without this a crash can publish
	// the new name over a file whose contents never landed.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("failed to fsync pending %s: %w", filepath.Base(path), err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("failed to close pending %s: %w", filepath.Base(path), err)
	}
	// Ownership before publication, so the artifact is never observable with the
	// wrong owner. Under `sudo ductile config set` on a privsep install the temp
	// file is root-owned; without this the renamed config is unreadable by the
	// service account and the next boot fails admission (#167).
	if err := fsown.ApplyToTemp(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("failed to publish %s: %w", filepath.Base(path), err)
	}
	return nil
}
