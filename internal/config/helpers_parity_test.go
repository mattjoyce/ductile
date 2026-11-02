package config

// Card 153 slice 2 parity proof. Each table runs the same inputs through the
// frozen pre-swap helper bodies (helpers_legacy_test.go) and the live,
// Ashlar-backed helpers and requires the same answer, except where a delta is
// a recorded decision or an unrecorded side effect pinned by name in the
// *Deltas and *SkipsMissing tests.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/mattjoyce/ashlar"
)

// hashParityContents are the byte strings hashed across the existing hash
// tests plus the shapes most likely to expose a read difference: empty,
// binary, CRLF, a BOM, and a buffer larger than io.ReadAll's first several
// growth steps (Ashlar reads with io.ReadAll; the old body used os.ReadFile).
// It is kept small because the race detector slows hashing by an order of
// magnitude and the suite's timing-sensitive dispatch tests share the CPU.
func hashParityContents() map[string][]byte {
	all := make([]byte, 256)
	for i := range all {
		all[i] = byte(i)
	}
	large := make([]byte, 256<<10+17)
	for i := range large {
		large[i] = byte((i * 31) ^ (i >> 7))
	}
	return map[string][]byte{
		"empty":                {},
		"keyed_hash_test":      []byte("config file bytes"),
		"entrypoint":           []byte("plugin entrypoint bytes"),
		"same bytes":           []byte("same bytes"),
		"single byte":          []byte("x"),
		"hash_test tokens":     []byte("tokens:\n  - name: test\n    key: test\n    scopes_file: scopes/test.json\n    scopes_hash: blake3:deadbeef\n"),
		"hash_test webhooks":   []byte("listen: :8080\n"),
		"every byte value":     all,
		"crlf":                 []byte("service:\r\n  name: crlf\r\n"),
		"utf8 bom":             []byte("\xef\xbb\xbfservice:\n  name: bom\n"),
		"no trailing newline":  []byte("service:\n  name: x"),
		"larger than one read": large,
	}
}

func TestComputeFileHashParityWithLegacy(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	keys := map[string][]byte{"0x01": key32(0x01), "0x02": key32(0x02), "0xff": key32(0xff)}
	for name, content := range hashParityContents() {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "_"))
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		link := path + ".link"
		if err := os.Symlink(path, link); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{path, link} {
			want, err := legacyComputeFileHash(p)
			if err != nil {
				t.Fatalf("%s: legacy hash: %v", name, err)
			}
			got, err := ComputeFileHash(p)
			if err != nil {
				t.Fatalf("%s: hash: %v", name, err)
			}
			if got != want {
				t.Errorf("%s (%s): ComputeFileHash = %s, legacy = %s", name, p, got, want)
			}
			for keyName, key := range keys {
				wantKeyed, err := legacyComputeKeyedFileHash(p, key)
				if err != nil {
					t.Fatalf("%s: legacy keyed hash: %v", name, err)
				}
				gotKeyed, err := ComputeKeyedFileHash(p, key)
				if err != nil {
					t.Fatalf("%s: keyed hash: %v", name, err)
				}
				if gotKeyed != wantKeyed {
					t.Errorf("%s key %s: ComputeKeyedFileHash = %s, legacy = %s", name, keyName, gotKeyed, wantKeyed)
				}
			}
		}
	}
}

// TestComputeFileHashKnownAnswers pins the digests against values computed
// outside Go (shasum -a 256; openssl dgst -sha256 -mac HMAC), so neither the
// live helper nor the frozen oracle can drift together unnoticed.
func TestComputeFileHashKnownAnswers(t *testing.T) {
	t.Parallel()
	empty := writeTempFile(t, "")
	if got, err := ComputeFileHash(empty); err != nil || got != "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("sha256(empty) = %q, %v", got, err)
	}
	cfg := writeTempFile(t, "config file bytes")
	if got, err := ComputeFileHash(cfg); err != nil || got != "sha256:a1502e132d1d0d122b3621a6522fdf234536e921a8c13d6b625b9e20c21403c2" {
		t.Errorf("sha256(config file bytes) = %q, %v", got, err)
	}
	x := writeTempFile(t, "x")
	if got, err := ComputeKeyedFileHash(x, key32(0x01)); err != nil || got != "sha256:9a6525695460ebcfaee4b950ffa6c3c733d87c4af567a4bdfd2ae664147b7fc0" {
		t.Errorf("hmac-sha256(key 0x01*32, x) = %q, %v", got, err)
	}
}

func TestComputeKeyedFileHashKeyLengthParity(t *testing.T) {
	t.Parallel()
	f := writeTempFile(t, "x")
	for n := 0; n <= 64; n++ {
		key := make([]byte, n)
		_, legacyErr := legacyComputeKeyedFileHash(f, key)
		_, err := ComputeKeyedFileHash(f, key)
		if (legacyErr == nil) != (err == nil) {
			t.Errorf("%d-byte key: legacy err=%v, new err=%v", n, legacyErr, err)
		}
	}
}

func TestComputeFileHashMissingFileStillMatchesNotExist(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "absent.yaml")
	_, err := ComputeFileHash(missing)
	if err == nil {
		t.Fatal("hash of a missing file succeeded")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("errors.Is(err, os.ErrNotExist) = false for %v", err)
	}
}

// TestChecksumsManifestParityWithLegacy hashes a real config tree the way
// `config lock` does (GenerateChecksumsFromDiscovery) and requires every stored
// entry to equal the frozen oracle's digest of the same file: a manifest written
// before the swap verifies after it, and the reverse.
func TestChecksumsManifestParityWithLegacy(t *testing.T) {
	tmpDir := t.TempDir()
	setupIntegrityDir(t, tmpDir)
	files, err := DiscoverConfigFiles(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := GenerateChecksumsFromDiscovery(files, false); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadChecksums(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Hashes) == 0 {
		t.Fatal("manifest has no hashes; the parity check would be vacuous")
	}
	for path, stored := range manifest.Hashes {
		want, err := legacyComputeFileHash(path)
		if err != nil {
			t.Fatalf("legacy hash %s: %v", path, err)
		}
		if stored != want {
			t.Errorf("%s: manifest stores %s, legacy digest %s", path, stored, want)
		}
	}
}

func TestParseByteSizeParityWithLegacy(t *testing.T) {
	t.Parallel()
	inputs := []string{
		// bytesize_test.go
		"1024", "2KB", "1MB", "1GB", " 3 MB ", "", "0", "abc", "12abKB",
		// case, spacing, sign
		"1kb", "1Kb", "1kB", "1 kb", "\t5MB\n", "+5", "+5KB", "-1", "-5MB", " ", "KB", "MB ",
		// forms the parser does not accept
		"1.5MB", "1e3", "0x10", "1TB", "1B", "1 K B", "1KBKB", "１MB", "1_000",
		// boundaries
		"1", "9223372036854775807", "9223372036854775808",
		"9007199254740991KB", "9007199254740992KB",
		"8796093022207MB", "8796093022208MB",
		"8589934591GB", "8589934592GB",
		"00012MB", "-0",
	}
	for _, in := range inputs {
		want, legacyErr := legacyParseByteSize(in)
		got, err := ParseByteSize(in)
		if (legacyErr == nil) != (err == nil) {
			t.Errorf("ParseByteSize(%q): legacy err=%v, new err=%v", in, legacyErr, err)
			continue
		}
		if got != want {
			t.Errorf("ParseByteSize(%q) = %d, legacy = %d", in, got, want)
		}
		if err == nil {
			continue
		}
		if !errors.Is(err, ashlar.ErrValidation) {
			t.Errorf("ParseByteSize(%q): error %v does not match ashlar.ErrValidation", in, err)
		}
		// C-FRO-12: the error quotes what the operator wrote, trimmed.
		if trimmed := strings.TrimSpace(in); trimmed != "" && !strings.Contains(err.Error(), strconv.Quote(trimmed)) {
			t.Errorf("ParseByteSize(%q): error %q does not quote the operator input", in, err.Error())
		}
	}
}

func TestExpandTildeParityWithLegacy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	inputs := []string{
		"~", "~/", "~/x", "~/a/b/../c", "~//x", "~/./x",
		"/abs/~/x", "relative/~", "", "./~", "x~", "/~", " ~/x",
	}
	for _, in := range inputs {
		want := legacyExpandTilde(in)
		got, err := expandTilde(in)
		if err != nil {
			t.Errorf("expandTilde(%q): unexpected error %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("expandTilde(%q) = %q, legacy = %q", in, got, want)
		}
	}
}

// TestExpandTildeDeltas pins every input on which the Ashlar-backed helper and
// the frozen oracle disagree. "~user" is card 153's recorded decision; the
// other two are unrecorded side effects of yamlconfig.ExpandPath, pinned here
// so a reviewer sees them and any further drift fails this test.
func TestExpandTildeDeltas(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Decision (card 153 slice 2): "~user" is refused instead of passed through.
	if legacy := legacyExpandTilde("~user/x"); legacy != "~user/x" {
		t.Fatalf("oracle changed: legacyExpandTilde(~user/x) = %q", legacy)
	}
	if _, err := expandTilde("~user/x"); err == nil {
		t.Error(`expandTilde("~user/x") succeeded; the card decided to refuse it`)
	}

	// Unrecorded: ExpandPath accepts the Windows separator form "~\" on every
	// OS and expands it; the old helper left it untouched.
	if legacy := legacyExpandTilde(`~\x`); legacy != `~\x` {
		t.Fatalf(`oracle changed: legacyExpandTilde(~\x) = %q`, legacy)
	}
	if got, err := expandTilde(`~\x`); err != nil || got != filepath.Join(home, "x") {
		t.Errorf(`expandTilde("~\x") = %q, %v; want %q`, got, err, filepath.Join(home, "x"))
	}

	// Unrecorded: ExpandPath trims surrounding whitespace from a "~" path.
	if legacy := legacyExpandTilde("~/x "); legacy != filepath.Join(home, "x ") {
		t.Fatalf("oracle changed: legacyExpandTilde(~/x ) = %q", legacy)
	}
	if got, err := expandTilde("~/x "); err != nil || got != filepath.Join(home, "x") {
		t.Errorf(`expandTilde("~/x ") = %q, %v; want %q`, got, err, filepath.Join(home, "x"))
	}
}

func TestDetectSymlinksParityWithLegacy(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(realDir, "config.yaml")
	if err := os.WriteFile(file, []byte("service: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileLink := filepath.Join(dir, "config-link.yaml")
	if err := os.Symlink(file, fileLink); err != nil {
		t.Fatal(err)
	}
	dirLink := filepath.Join(dir, "linked")
	if err := os.Symlink(realDir, dirLink); err != nil {
		t.Fatal(err)
	}
	// Resolve the temp root itself so the plain-path cases carry no symlink
	// (on macOS t.TempDir sits under /var -> /private/var).
	resolvedRoot, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	plainFile := filepath.Join(resolvedRoot, "real", "config.yaml")

	cases := map[string][]string{
		"no symlinks":       {plainFile, filepath.Dir(plainFile)},
		"symlinked file":    {fileLink},
		"through dir link":  {filepath.Join(dirLink, "config.yaml")},
		"mixed":             {plainFile, fileLink, dirLink},
		"empty input":       {},
		"unresolved tmpdir": {file},
	}
	for name, paths := range cases {
		want, err := legacyDetectSymlinks(paths)
		if err != nil {
			t.Fatalf("%s: legacy: %v", name, err)
		}
		got, err := DetectSymlinks(paths)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got == nil {
			t.Errorf("%s: DetectSymlinks returned nil; want a non-nil slice", name)
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: DetectSymlinks = %v, legacy = %v", name, got, want)
		}
	}
}

// TestDetectSymlinksSkipsMissingPath pins card 153's decision: a missing path
// is skipped (the old helper failed the whole check), while the remaining
// paths are still checked.
func TestDetectSymlinksSkipsMissingPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent.yaml")
	if _, err := legacyDetectSymlinks([]string{missing}); err == nil {
		t.Fatal("oracle changed: legacy DetectSymlinks accepted a missing path")
	}

	realFile := filepath.Join(dir, "real.yaml")
	if err := os.WriteFile(realFile, []byte("x: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(realFile, link); err != nil {
		t.Fatal(err)
	}
	got, err := DetectSymlinks([]string{missing, link})
	if err != nil {
		t.Fatalf("DetectSymlinks with a missing path: %v", err)
	}
	if len(got) != 1 || got[0].Path != link {
		t.Fatalf("DetectSymlinks = %v; want exactly one warning for %s", got, link)
	}
}

// TestLoadEnvFileFirstDefinitionWinsWithinFile pins why loadEnvFile keeps its
// own parse (decision E): a key defined twice in one file keeps its first
// value. yamlconfig.ParseDotEnv returns a map, where the last value wins.
func TestLoadEnvFileFirstDefinitionWinsWithinFile(t *testing.T) {
	const key = "DUCTILE_TEST_DOTENV_FIRST_WINS"
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv(key) })
	path := filepath.Join(t.TempDir(), "dup.env")
	if err := os.WriteFile(path, []byte(key+"=first\n"+key+"=second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadEnvFile(path, nil); err != nil {
		t.Fatalf("loadEnvFile: %v", err)
	}
	if got := os.Getenv(key); got != "first" {
		t.Fatalf("%s = %q, want first (first definition wins)", key, got)
	}
}

// TestLoadEnvFileAcceptsNamesAshlarRefuses pins the other half of keeping the
// parse: a name outside [A-Za-z_][A-Za-z0-9_]* loads today.
func TestLoadEnvFileAcceptsNamesAshlarRefuses(t *testing.T) {
	const key = "DUCTILE-TEST-DASHED"
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv(key) })
	path := filepath.Join(t.TempDir(), "dashed.env")
	if err := os.WriteFile(path, []byte(key+"=v\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadEnvFile(path, nil); err != nil {
		t.Fatalf("loadEnvFile: %v", err)
	}
	if got := os.Getenv(key); got != "v" {
		t.Fatalf("%s = %q, want v", key, got)
	}
}
