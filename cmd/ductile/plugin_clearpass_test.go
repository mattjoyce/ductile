package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/events"
	"github.com/mattjoyce/ductile/internal/state"
	"github.com/mattjoyce/ductile/internal/storage"
)

func captureRunPluginClearpass(t *testing.T, args []string) (int, string, string) {
	t.Helper()
	return captureOutputWithExitCode(t, func() int {
		return runPluginClearpass(args)
	})
}

// swapGmail changes the fixture plugin's entrypoint bytes after attestation.
func swapGmail(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "plugins", "gmail", "gmail"), []byte("#!/bin/sh\necho swapped\n"), 0o755); err != nil {
		t.Fatalf("swap: %v", err)
	}
}

// openClearpass records a sealed window for gmail directly, with a chosen
// clock, so tests can place "now" on either side of the deadline.
func openClearpass(t *testing.T, dir string, nonce fingerprintNonceSource, opened time.Time, d time.Duration) config.Clearpass {
	t.Helper()
	cp, err := config.NewClearpass(opened, d, "test")
	if err != nil {
		t.Fatal(err)
	}
	key, err := nonce.FingerprintNonce()
	if err != nil {
		t.Fatal(err)
	}
	cp, err = cp.Sealed(recordedGmail(t, dir), key)
	if err != nil {
		t.Fatal(err)
	}
	writeClearpass(t, dir, &cp)
	return cp
}

// writeClearpass stores cp for gmail as given, sealed or not.
func writeClearpass(t *testing.T, dir string, cp *config.Clearpass) {
	t.Helper()
	m, err := config.LoadChecksums(dir)
	if err != nil {
		t.Fatal(err)
	}
	set, found := config.SetPluginClearpass(m.PluginFingerprints, "gmail", cp)
	if !found {
		t.Fatal("fixture gmail must be attested first")
	}
	if err := config.WritePluginFingerprints(dir, set, false); err != nil {
		t.Fatal(err)
	}
}

func recordedGmail(t *testing.T, dir string) config.PluginFingerprint {
	t.Helper()
	m, err := config.LoadChecksums(dir)
	if err != nil {
		t.Fatal(err)
	}
	fp, ok := m.FindPluginFingerprint("gmail")
	if !ok {
		t.Fatal("gmail has no recorded fingerprint")
	}
	return fp
}

func openTestStore(t *testing.T) *state.Store {
	t.Helper()
	db, err := storage.OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "ductile.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return state.NewStore(db)
}

// --- verifier -----------------------------------------------------------------

func TestVerifierToleratesMismatchInsideOpenClearpassAndReportsIt(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	lockConfigAndPlugins(t, tmp, "gmail")
	before := recordedGmail(t, tmp)
	reg, v := buildRegistryAndVault(t, tmp)
	opened := time.Now()
	openClearpass(t, tmp, v, opened, time.Hour)
	swapGmail(t, tmp)

	var reports []clearpassSpawn
	pv := newPluginIdentityVerifier(reg, tmp, v, nil)
	pv.now = func() time.Time { return opened.Add(30 * time.Minute) }
	pv.onClearpass = func(s clearpassSpawn) { reports = append(reports, s) }

	if err := pv.VerifyIdentity("gmail"); err != nil {
		t.Fatalf("a swap inside an open window must pass: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("one tolerated spawn must be reported once, got %d", len(reports))
	}
	r := reports[0]
	if r.Plugin != "gmail" || r.Recorded.EntrypointHash != before.EntrypointHash || r.Live.EntrypointHash == before.EntrypointHash {
		t.Fatalf("report must carry both hashes: %+v", r)
	}
	if r.Window.Note != "test" {
		t.Fatalf("report must carry the window: %+v", r.Window)
	}
	// The attestation itself is untouched by a tolerated spawn: only close commits.
	if after := recordedGmail(t, tmp); after.EntrypointHash != before.EntrypointHash || after.Clearpass == nil {
		t.Fatalf("a spawn must not commit: %+v", after)
	}
}

func TestVerifierMatchInsideWindowDoesNotReport(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	lockConfigAndPlugins(t, tmp, "gmail")
	reg, v := buildRegistryAndVault(t, tmp)
	openClearpass(t, tmp, v, time.Now(), time.Hour)

	called := false
	pv := newPluginIdentityVerifier(reg, tmp, v, nil)
	pv.onClearpass = func(clearpassSpawn) { called = true }
	if err := pv.VerifyIdentity("gmail"); err != nil {
		t.Fatalf("unchanged bytes pass: %v", err)
	}
	if called {
		t.Fatal("a match is not a tolerated mismatch; nothing to report")
	}
}

func TestVerifierDeniesAfterWindowAndNamesIt(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	lockConfigAndPlugins(t, tmp, "gmail")
	reg, v := buildRegistryAndVault(t, tmp)
	opened := time.Now().Add(-3 * time.Hour)
	cp := openClearpass(t, tmp, v, opened, time.Hour)
	swapGmail(t, tmp)

	called := false
	pv := newPluginIdentityVerifier(reg, tmp, v, nil)
	pv.onClearpass = func(clearpassSpawn) { called = true }
	err := pv.VerifyIdentity("gmail")
	if err == nil {
		t.Fatal("a swap after the window must be denied")
	}
	if !strings.Contains(err.Error(), "entrypoint hash mismatch") || !strings.Contains(err.Error(), "clearpass expired at "+cp.Until) {
		t.Fatalf("denial must name the mismatch and the expired window: %v", err)
	}
	if called {
		t.Fatal("an expired window reports nothing")
	}
}

// --- closer -------------------------------------------------------------------

func TestCloserCommitsExpiredWindowAndReports(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	lockConfigAndPlugins(t, tmp, "gmail")
	before := recordedGmail(t, tmp)
	reg, v := buildRegistryAndVault(t, tmp)
	opened := time.Now().Add(-2 * time.Hour)
	openClearpass(t, tmp, v, opened, time.Hour)
	swapGmail(t, tmp)
	st := openTestStore(t)
	hub := events.NewHub(16)
	sub, cancel := hub.Subscribe()
	defer cancel()

	closer := newClearpassCloser(reg, tmp, v, nil, hub, st)
	// Two tolerated spawns happened during the window.
	pv := newPluginIdentityVerifier(reg, tmp, v, nil)
	pv.now = func() time.Time { return opened.Add(10 * time.Minute) }
	pv.onClearpass = closer.reportSpawn
	for i := 0; i < 2; i++ {
		if err := pv.VerifyIdentity("gmail"); err != nil {
			t.Fatalf("spawn %d inside window: %v", i, err)
		}
	}

	closer.sweep(context.Background(), true)

	after := recordedGmail(t, tmp)
	if after.Clearpass != nil {
		t.Fatalf("close must clear the window: %+v", after)
	}
	if after.EntrypointHash == before.EntrypointHash {
		t.Fatal("close must commit the live bytes")
	}
	if after.Enabled != before.Enabled {
		t.Fatal("close must carry the record's enabled flag")
	}
	// The committed bytes now verify strictly.
	if err := newPluginIdentityVerifier(reg, tmp, v, nil).VerifyIdentity("gmail"); err != nil {
		t.Fatalf("after close the strict rule holds and the new bytes match: %v", err)
	}

	rows, err := st.ListVaultAudit(context.Background(), "gmail", 10)
	if err != nil {
		t.Fatal(err)
	}
	var spawns, closes int
	var closeDetail string
	for _, r := range rows {
		switch r.Op {
		case auditOpClearpassSpawn:
			spawns++
		case auditOpClearpassClose:
			closes++
			closeDetail = r.Detail
		}
	}
	if spawns != 2 || closes != 1 {
		t.Fatalf("audit must hold 2 spawns and 1 close, got %d/%d: %+v", spawns, closes, rows)
	}
	if !strings.Contains(closeDetail, "observed in 2 spawns") || !strings.Contains(closeDetail, "committed entrypoint") {
		t.Fatalf("close detail must say what was accepted: %s", closeDetail)
	}

	var types []string
	for len(sub) > 0 {
		types = append(types, (<-sub).Type)
	}
	if strings.Join(types, ",") != strings.Join([]string{eventClearpassSpawn, eventClearpassSpawn, eventClearpassClosed}, ",") {
		t.Fatalf("hub events: %v", types)
	}
}

func TestCloserLeavesOpenWindowAloneAndCommitsUnchangedBytes(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	lockConfigAndPlugins(t, tmp, "gmail")
	before := recordedGmail(t, tmp)
	reg, v := buildRegistryAndVault(t, tmp)
	st := openTestStore(t)
	closer := newClearpassCloser(reg, tmp, v, nil, nil, st)

	// Open window: untouched.
	openClearpass(t, tmp, v, time.Now(), time.Hour)
	closer.sweep(context.Background(), true)
	if recordedGmail(t, tmp).Clearpass == nil {
		t.Fatal("an open window must not be closed early")
	}

	// Expired window with no change and no spawn: committed as unchanged.
	openClearpass(t, tmp, v, time.Now().Add(-2*time.Hour), time.Hour)
	closer.sweep(context.Background(), false)
	after := recordedGmail(t, tmp)
	if after.Clearpass != nil || after.EntrypointHash != before.EntrypointHash {
		t.Fatalf("unchanged bytes commit to the same hash and the window clears: %+v", after)
	}
	rows, _ := st.ListVaultAudit(context.Background(), "gmail", 5)
	if len(rows) != 1 || rows[0].Op != auditOpClearpassClose || !strings.Contains(rows[0].Detail, "unchanged") || !strings.Contains(rows[0].Detail, "never spawned") {
		t.Fatalf("close must record unchanged, never spawned: %+v", rows)
	}
}

// --- CLI ----------------------------------------------------------------------

func TestClearpassCLIOpenRefusals(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	if code, _, stderr := captureRunConfigHashUpdate(t, []string{"--config-dir", tmp}); code != 0 {
		t.Fatalf("config lock: %s", stderr)
	}

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no duration", []string{"--config-dir", tmp, "gmail"}, "--for <duration> is required"},
		{"over cap", []string{"--config-dir", tmp, "gmail", "--for", "25h"}, "at most 24h"},
		{"unattested", []string{"--config-dir", tmp, "gmail", "--for", "1h"}, "no recorded fingerprint"},
		{"unconfigured", []string{"--config-dir", tmp, "ghost", "--for", "1h"}, "not configured"},
		{"no name", []string{"--config-dir", tmp, "--for", "1h"}, ""},
	}
	for _, tc := range cases {
		code, stdout, stderr := captureRunPluginClearpass(t, tc.args)
		if code == 0 {
			t.Fatalf("%s: must fail; stdout=%s", tc.name, stdout)
		}
		if tc.want != "" && !strings.Contains(stderr, tc.want) {
			t.Fatalf("%s: stderr %q must mention %q", tc.name, stderr, tc.want)
		}
	}
}

func TestClearpassCLIOpenListCloseAndLockClears(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	lockConfigAndPlugins(t, tmp, "gmail")
	before := recordedGmail(t, tmp)

	code, stdout, stderr := captureRunPluginClearpass(t, []string{"--config-dir", tmp, "gmail", "--for", "2h", "--note", "card 208"})
	if code != 0 {
		t.Fatalf("open: %s", stderr)
	}
	if !strings.Contains(stdout, "Clearpass open for plugin \"gmail\" until") {
		t.Fatalf("open must say until when: %s", stdout)
	}
	rec := recordedGmail(t, tmp)
	if rec.Clearpass == nil || rec.Clearpass.Note != "card 208" || !rec.Clearpass.Open(time.Now()) {
		t.Fatalf("open must record the window: %+v", rec.Clearpass)
	}

	code, stdout, _ = captureRunPluginClearpass(t, []string{"--config-dir", tmp, "--list"})
	if code != 0 || !strings.Contains(stdout, "gmail") || !strings.Contains(stdout, "open") || !strings.Contains(stdout, "card 208") {
		t.Fatalf("list must show the open window: %s", stdout)
	}

	swapGmail(t, tmp)
	code, stdout, stderr = captureRunPluginClearpass(t, []string{"--config-dir", tmp, "gmail", "--close"})
	if code != 0 {
		t.Fatalf("close: %s", stderr)
	}
	if !strings.Contains(stdout, "Clearpass closed: plugin \"gmail\" committed entrypoint") {
		t.Fatalf("close must report the commit: %s", stdout)
	}
	after := recordedGmail(t, tmp)
	if after.Clearpass != nil || after.EntrypointHash == before.EntrypointHash {
		t.Fatalf("close must commit and clear: %+v", after)
	}
	if code, _, stderr := captureRunPluginClearpass(t, []string{"--config-dir", tmp, "gmail", "--close"}); code == 0 || !strings.Contains(stderr, "no clearpass to close") {
		t.Fatalf("a second close has nothing to do: code=%d stderr=%s", code, stderr)
	}

	code, stdout, _ = captureRunPluginClearpass(t, []string{"--config-dir", tmp, "--list"})
	if code != 0 || !strings.Contains(stdout, "No clearpass windows") {
		t.Fatalf("list after close: %s", stdout)
	}

	// plugin lock is the explicit commit and clears a window too.
	_, v := buildRegistryAndVault(t, tmp)
	openClearpass(t, tmp, v, time.Now(), time.Hour)
	code, stdout, stderr = captureRunPluginLock(t, []string{"--config-dir", tmp, "gmail"})
	if code != 0 {
		t.Fatalf("lock: %s", stderr)
	}
	if !strings.Contains(stdout, "Cleared the clearpass window") || recordedGmail(t, tmp).Clearpass != nil {
		t.Fatalf("lock must clear the window and say so: %s", stdout)
	}
}

// --- seal ---------------------------------------------------------------------

// A window written without the vault is not a window: the verifier denies and
// names the bad seal, and the closer will not commit under it even once it has
// expired. This is what keeps a .checksums write from becoming an attestation.
func TestUnsealedClearpassIsIgnoredByVerifierAndCloser(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	lockConfigAndPlugins(t, tmp, "gmail")
	before := recordedGmail(t, tmp)
	reg, v := buildRegistryAndVault(t, tmp)

	forged, _ := config.NewClearpass(time.Now().Add(-2*time.Hour), time.Hour, "forged")
	writeClearpass(t, tmp, &forged)
	swapGmail(t, tmp)

	pv := newPluginIdentityVerifier(reg, tmp, v, nil)
	pv.now = func() time.Time { return time.Now().Add(-90 * time.Minute) } // inside the forged window
	called := false
	pv.onClearpass = func(clearpassSpawn) { called = true }
	err := pv.VerifyIdentity("gmail")
	if err == nil || !strings.Contains(err.Error(), "seal does not verify") || called {
		t.Fatalf("an unsealed window must deny and say why (called=%v): %v", called, err)
	}

	st := openTestStore(t)
	closer := newClearpassCloser(reg, tmp, v, nil, nil, st)
	closer.sweep(context.Background(), true) // the forged window has expired by real time
	after := recordedGmail(t, tmp)
	if after.EntrypointHash != before.EntrypointHash || after.Clearpass == nil {
		t.Fatalf("the closer must not commit under an unsealed window: %+v", after)
	}
	if rows, _ := st.ListVaultAudit(context.Background(), "gmail", 5); len(rows) != 0 {
		t.Fatalf("nothing was closed, nothing is audited: %+v", rows)
	}

	// A sealed window whose fields were edited afterwards is just as dead.
	genuine := openClearpass(t, tmp, v, time.Now(), time.Hour)
	genuine.Until = time.Now().Add(20 * time.Hour).UTC().Format(time.RFC3339)
	writeClearpass(t, tmp, &genuine)
	pv.now = time.Now
	if err := pv.VerifyIdentity("gmail"); err == nil || !strings.Contains(err.Error(), "seal does not verify") {
		t.Fatalf("an edited window must deny: %v", err)
	}
}
