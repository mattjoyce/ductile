//go:build darwin || linux || freebsd || openbsd || netbsd

package storage

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// #171: the DB and both WAL/SHM sidecars must not be world- or group-readable.
// Before this they were created 0644 and the 0700 parent directory was the only
// thing keeping job payloads and baggage private — one chmod on the state dir
// away from exposure.
func TestOpenSQLite_ConstrainsDBAndSidecarModes(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ductile.db")
	db, err := OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec("INSERT INTO storage_sequences(name, value) VALUES('t', 1)"); err != nil {
		t.Fatalf("write to materialise the WAL: %v", err)
	}

	// All three files must exist by now, and skipping an absent one would be the
	// bug hiding from its own test: the sidecars are what carry job payloads and
	// baggage between checkpoints, so "0600 on the ones that happened to exist" is
	// not the property #171 needs. `PRAGMA journal_mode = WAL` materialises both
	// sidecars inside OpenSQLite, before secureSQLiteFiles runs — if that ordering
	// ever changes, this fails loudly instead of quietly checking one file.
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("%s missing after open+write: %v — secureSQLiteFiles cannot have constrained it", filepath.Base(p), err)
		}
		if perm := fi.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("%s mode = %04o, want no group/other access", filepath.Base(p), perm)
		}
	}
}

// #171: opening the DB must not fail when ownership cannot be corrected. This is
// the no-regression guarantee — OpenSQLite is on every CLI path as well as the
// daemon's, and NFS, userns and Docker bind mounts all refuse chown legitimately.
func TestOpenSQLite_SucceedsWhenOwnershipCannotBeCorrected(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ductile.db")
	db, err := OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite must not fail on an un-correctable owner: %v", err)
	}
	_ = db.Close()
}

// #171, the real defect: a privileged CLI opening the DB must leave it and its
// sidecars owned by the service account. `sudo ductile job list` — a read —
// previously left root-owned WAL/SHM files in the daemon's state dir, and the
// daemon then failed at query time rather than at admission, so nothing caught it.
func TestOpenSQLite_InheritsStateDirOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root: reproduces a privileged CLI writing into a service-owned state dir")
	}
	dir := t.TempDir()
	if err := os.Chown(dir, 12345, 12345); err != nil {
		t.Skipf("host refuses chown: %v", err)
	}
	dbPath := filepath.Join(dir, "ductile.db")

	db, err := OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec("INSERT INTO storage_sequences(name, value) VALUES('t', 1)"); err != nil {
		t.Fatalf("write to materialise the WAL: %v", err)
	}

	checked := 0
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		fi, statErr := os.Stat(p)
		if statErr != nil {
			continue
		}
		checked++
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			t.Skip("no Unix stat on this platform")
		}
		if int(st.Uid) != 12345 {
			t.Errorf("%s owned by uid %d, want the state dir's 12345 — "+
				"this is #171: the daemon cannot write its own sidecars", filepath.Base(p), st.Uid)
		}
	}
	if checked == 0 {
		t.Fatal("no database files found to check — test is vacuous")
	}
}
