package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/mattjoyce/ductile/internal/fsown"
)

// readabilityFixture builds a config dir resembling a real install: a config
// file, a plugin, a lock manifest, a vault blob, an age key and a state DB with
// its side files.
func readabilityFixture(t *testing.T) (string, *Config) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := "service:\n  unconfined: true\n  admission:\n    verify_integrity_on_boot: true\n" +
		"state:\n  path: \"" + filepath.Join(dir, "state", "d.db") + "\"\n" +
		"plugin_roots:\n  - \"" + filepath.Join(dir, "plugins") + "\"\n"
	write := func(path string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte("x\n"), mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dir, ".checksums"), 0o600)
	// A real identity, not a placeholder: config.Load parses the key, so a dummy
	// would fail the fixture for a reason unrelated to readability.
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "age.key"), []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dir, "state", "d.db"), 0o600)
	write(filepath.Join(dir, "state", "d.db-wal"), 0o600)
	write(filepath.Join(dir, "state", "d.db-shm"), 0o600)

	cfg, err := Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("load fixture config: %v", err)
	}
	return dir, cfg
}

func paths(arts []Artifact) []string {
	out := make([]string, 0, len(arts))
	for _, a := range arts {
		out = append(out, a.Path)
	}
	return out
}

// The inventory is the load-bearing part: a readability check over the wrong list
// reports clean and is believed. Each entry here is a file some defect in the
// #167 family actually landed on.
func TestServiceReadArtifacts_CoversEveryServiceReadFile(t *testing.T) {
	dir, cfg := readabilityFixture(t)
	got := paths(ServiceReadArtifacts(dir, cfg, fsown.CurrentAccount()))

	for _, want := range []string{
		dir,                                     // the config directory itself
		filepath.Join(dir, "config.yaml"),       // #169
		filepath.Join(dir, ".checksums"),        // #167
		filepath.Join(dir, "age.key"),           // #170
		filepath.Join(dir, "state", "d.db"),     // #171
		filepath.Join(dir, "state", "d.db-wal"), // #171, the side file
		filepath.Join(dir, "state", "d.db-shm"), // #171, the side file
	} {
		if !slices.Contains(got, want) {
			t.Fatalf("inventory is missing %s\ngot: %v", want, got)
		}
	}
}

// A privsep account's state_dir is read by THAT account, not the gateway. Asking
// the gateway's question about it gives a confidently wrong answer: the gateway
// usually can read it, and the account is the one that cannot.
func TestServiceReadArtifacts_AccountStateDirAsksAsThatAccount(t *testing.T) {
	dir, cfg := readabilityFixture(t)
	cfg.Accounts = map[string]AccountConf{
		"untrusted": {UID: 61000, GID: 61000, StateDir: filepath.Join(dir, "acct")},
	}

	var found *Artifact
	for _, a := range ServiceReadArtifacts(dir, cfg, fsown.CurrentAccount()) {
		if a.AccountName == "untrusted" {
			found = &a
			break
		}
	}
	if found == nil {
		t.Fatal("the account state_dir is not in the inventory")
	}
	if found.Account.UID != 61000 || found.Account.GID != 61000 {
		t.Fatalf("state_dir must be asked about as the account that owns it; got uid %d gid %d",
			found.Account.UID, found.Account.GID)
	}
}

// GatewayOnly is what keeps the boot path from aborting on a state_dir that
// ReconcileAccountFilesystem is about to fix later in the same boot.
func TestGatewayOnly_DropsPrivsepAccountArtifacts(t *testing.T) {
	dir, cfg := readabilityFixture(t)
	cfg.Accounts = map[string]AccountConf{
		"untrusted": {UID: 61000, GID: 61000, StateDir: filepath.Join(dir, "acct")},
	}
	all := ServiceReadArtifacts(dir, cfg, fsown.CurrentAccount())
	kept := GatewayOnly(all)

	if len(kept) >= len(all) {
		t.Fatalf("fixture is vacuous: nothing was dropped (%d in, %d out)", len(all), len(kept))
	}
	for _, a := range kept {
		if a.AccountName != "gateway" {
			t.Fatalf("GatewayOnly kept a %s artifact: %s", a.AccountName, a.Path)
		}
	}
}

// The whole point, at the package level: a present, well-formed artifact that
// cannot be opened is reported.
func TestCheckReadability_CatchesAnUnopenableArtifact(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses mode bits; this case needs an unprivileged euid")
	}
	dir, cfg := readabilityFixture(t)
	db := filepath.Join(dir, "state", "d.db")
	if err := os.Chmod(db, 0o000); err != nil {
		t.Fatal(err)
	}

	// Premise check: the artifact really is unopenable.
	if f, err := os.Open(db); err == nil {
		f.Close()
		t.Fatalf("fixture is vacuous: %s opened fine", db)
	}

	findings := CheckReadability(ServiceReadArtifacts(dir, cfg, fsown.CurrentAccount()))
	if len(findings) == 0 {
		t.Fatal("an unopenable state DB produced no finding")
	}
	var hit *ReadabilityFinding
	for i := range findings {
		if findings[i].Path == db {
			hit = &findings[i]
		}
	}
	if hit == nil {
		t.Fatalf("no finding names the state DB; got %+v", findings)
	}
	if hit.Role != "state database" {
		t.Fatalf("finding should name what the file IS, got role %q", hit.Role)
	}
	if !strings.Contains(hit.Detail, "mode 0000") {
		t.Fatalf("finding should carry the mode; got: %s", hit.Detail)
	}
}

// The no-regression guarantee. A healthy install must gain no findings, or the
// check is noise and gets ignored — which is worse than not having it.
func TestCheckReadability_HealthyInstallIsClean(t *testing.T) {
	dir, cfg := readabilityFixture(t)
	if findings := CheckReadability(ServiceReadArtifacts(dir, cfg, fsown.CurrentAccount())); len(findings) != 0 {
		t.Fatalf("a readable install must produce no findings; got %+v", findings)
	}
}

// Absence must not read as unreadability — an unlocked install has no .checksums
// and a fresh install has no WAL side files.
func TestCheckReadability_MissingArtifactsAreNotFindings(t *testing.T) {
	dir, cfg := readabilityFixture(t)
	for _, name := range []string{".checksums", "age.key"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(dir, "state", "d.db-wal")); err != nil {
		t.Fatal(err)
	}
	if findings := CheckReadability(ServiceReadArtifacts(dir, cfg, fsown.CurrentAccount())); len(findings) != 0 {
		t.Fatalf("missing files must not be readability findings; got %+v", findings)
	}
}

// With verify_integrity_on_boot off the daemon never opens .checksums, so an
// unreadable one is not a boot failure and an install in that state runs fine
// today. Including it would refuse a working deployment over a file it does not
// read — the #176 posture fixture asserts exactly this boot succeeds, and caught
// it when the first cut of this check did not.
func TestServiceReadArtifacts_ChecksumsFollowsTheIntegrityPolicy(t *testing.T) {
	dir, cfg := readabilityFixture(t)
	manifest := filepath.Join(dir, ".checksums")

	cfg.Service.Admission = &AdmissionConfig{VerifyIntegrityOnBoot: true}
	if !slices.Contains(paths(ServiceReadArtifacts(dir, cfg, fsown.CurrentAccount())), manifest) {
		t.Fatal("with verification on, .checksums must be in scope")
	}

	cfg.Service.Admission = &AdmissionConfig{VerifyIntegrityOnBoot: false}
	if slices.Contains(paths(ServiceReadArtifacts(dir, cfg, fsown.CurrentAccount())), manifest) {
		t.Fatal("with verification off, .checksums must NOT be in scope — the daemon never opens it")
	}
}
