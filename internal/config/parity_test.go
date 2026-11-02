package config

// Card 153 slice 3: loadAshlar is the loader the cutover (slice 4) would put in
// place of load(), built on yamlconfig with the decision note's policy and wired
// to no caller. It exists so TestAshlarParity can run both loaders over one
// corpus and name every place they disagree. It lives in a _test.go file on
// purpose: nothing outside the tests can reach it.
//
// What it models, by decision (card 153, "Compatibility decision note"):
//   - D: the filesystem is rooted at "/" and paths are root-relative, so
//     absolute and parent-relative includes keep working.
//   - E: every fragment's environment_vars.include is parsed with
//     yamlconfig.ParseDotEnv and exported before interpolation, first
//     definition winning across files and the process environment winning over
//     every file. This is the card's two-pass sketch; the note says it is not
//     proof of parity, and the harness is what shows where it is not.
//   - I: /plugins/* entries replace whole; the listed lists append; every other
//     mapping merges and every present scalar replaces.
//   - J/L: the scope verifier hashes the on-disk bytes it is handed (before age
//     decryption), the same bytes .checksums records.
//   - K: multi-file mode is "more than one resolved file".
//
// What it emulates because Ashlar v0.4.0 lacks it (decision C, an Ashlar
// prerequisite): unknown keys are collected and pruned so the known keys still
// decode. v0.4.0's DecodeStrict stops at the first unknown key; every key
// collected here is reported by the harness as an "unknown-keys" divergence.
//
// What it does not model: ${VAR} in include paths (F) and an enumeration policy
// for directory includes (H). Ashlar v0.4.0 has neither, so the harness shows
// those divergences instead of hiding them.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mattjoyce/ashlar"
	"github.com/mattjoyce/ashlar/yamlconfig"
	"github.com/mattjoyce/ductile/internal/secrets"
	"github.com/mattjoyce/ductile/internal/vault"
	"go.yaml.in/yaml/v3"
)

// parityAppendPaths are the lists decision I concatenates across files, in the
// order the files are read.
var parityAppendPaths = []ashlar.Pointer{
	"/routes",
	"/instances",
	"/plugin_roots",
	"/api/auth/tokens",
	"/api/allowed_origins",
	"/remote_ingress/peers",
	"/webhooks/endpoints",
}

// parityPolicy is decision I as an Ashlar composition policy.
func parityPolicy() yamlconfig.CompositionPolicy {
	p := yamlconfig.DefaultCompositionPolicy()
	p.PatternRules = []yamlconfig.PatternMergeRule{{Pattern: "/plugins/*", Rule: yamlconfig.MergeReplace}}
	for _, ptr := range parityAppendPaths {
		p.Rules[ptr] = yamlconfig.MergeAppendSlice
	}
	return p
}

// ashlarLoad is loadAshlar's result: the config, the files Ashlar read (absolute,
// in traversal order), and the unknown keys the decision-C emulation pruned.
type ashlarLoad struct {
	cfg     *Config
	files   []string
	unknown []string
}

// loadAshlar mirrors load(configPath, verifyScopes, validateConfig,
// applyPluginDefaults) with the read, include, merge and interpolation steps
// done by Ashlar. The steps after the decode are load()'s own functions, called
// in load()'s order, so a divergence can only come from the Ashlar half. When a
// step after the decode fails, the partial result still carries the unknown
// keys the decode collected, so the harness reports them either way.
func loadAshlar(configPath string, verifyScopes, validateConfig, applyPluginDefaults bool) (*ashlarLoad, error) {
	absPath, err := resolveConfigEntry(configPath)
	if err != nil {
		return nil, err
	}
	configDir := filepath.Dir(absPath)
	kr, err := resolveKeyring(configDir, nil)
	if err != nil {
		return nil, err
	}
	if kr, err = keyringFromRootAgeKeyFile(kr, absPath, configDir); err != nil {
		return nil, err
	}

	ctx := context.Background()
	include := yamlconfig.IncludeOptions{Transformer: ageTransformer(kr)}
	if verifyScopes {
		include.Verifier = scopeFileVerifier
	}
	fragments, err := yamlconfig.LoadIncludeGraph(ctx, os.DirFS("/"), strings.TrimPrefix(absPath, "/"), include)
	if err != nil {
		return nil, err
	}
	if err := exportFragmentDotEnv(fragments, kr); err != nil {
		return nil, err
	}
	resolved, err := yamlconfig.ResolveFragments(ctx, fragments, yamlconfig.ResolveOptions{
		Policy: parityPolicy(),
		Env:    yamlconfig.EnvBinding{InterpolateScalar: true},
	})
	if err != nil {
		return nil, err
	}
	cfg, unknown, err := decodeCollectingUnknown(withoutDedicatedDomains(resolved.Root()))
	if err != nil {
		return &ashlarLoad{unknown: unknown}, err
	}

	out := &ashlarLoad{cfg: cfg, unknown: unknown}
	cfg.SourceFiles = make(map[string]*yaml.Node, len(resolved.Files()))
	for _, rel := range resolved.Files() {
		abs := "/" + rel
		out.files = append(out.files, abs)
		cfg.SourceFiles[abs] = nil
	}
	if err := finishLoad(cfg, configDir, kr, len(out.files) > 1, validateConfig, applyPluginDefaults); err != nil {
		return &ashlarLoad{unknown: unknown}, err
	}
	return out, nil
}

// resolveConfigEntry is load()'s path handling: absolute, and a directory means
// its config.yaml.
func resolveConfigEntry(configPath string) (string, error) {
	absPath, err := filepath.Abs(configPath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve config path %q: %w", configPath, err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("config file not found: %s", absPath)
	}
	if info.IsDir() {
		absPath = filepath.Join(absPath, "config.yaml")
		if _, err := os.Stat(absPath); err != nil {
			return "", fmt.Errorf("directory provided but config.yaml not found: %s", absPath)
		}
	}
	return absPath, nil
}

// keyringFromRootAgeKeyFile is load()'s second keyring pass: when no key was
// found from the environment or the default locations, the root file's own
// secrets.age_key_file names one for the encrypted includes. It reads the root
// once, before the include graph, because Ashlar's transformer cannot see a
// parsed fragment.
func keyringFromRootAgeKeyFile(kr *secrets.Keyring, absPath, configDir string) (*secrets.Keyring, error) {
	if !kr.Empty() {
		return kr, nil
	}
	raw, err := readConfigBytes(kr, absPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}
	root, err := yamlconfig.Read(absPath, raw)
	if err != nil {
		return nil, err
	}
	keyFile, ok := root.At("/secrets/age_key_file").StringVal()
	if !ok || keyFile == "" {
		return kr, nil
	}
	if keyFile, err = expandTilde(keyFile); err != nil {
		return nil, fmt.Errorf("secrets.age_key_file: %w", err)
	}
	return resolveKeyring(configDir, &Config{Secrets: SecretsConfig{AgeKeyFile: keyFile}})
}

// ageTransformer decrypts an age-encrypted fragment with the keyring, as
// readConfigBytes does for load().
func ageTransformer(kr *secrets.Keyring) yamlconfig.ByteTransformer {
	return func(_ context.Context, _ string, raw []byte) ([]byte, error) {
		if !secrets.IsEncrypted(raw) {
			return raw, nil
		}
		return kr.Decrypt(raw)
	}
}

// scopeFileVerifier is verifyScopeFilesRecursively as an Ashlar hook: a file
// named webhooks.yaml must match its directory's .checksums entry. Ashlar hands
// it the on-disk bytes, before decryption, which is what .checksums hashes (L).
func scopeFileVerifier(_ context.Context, rel string, raw []byte) error {
	abs := "/" + rel
	if filepath.Base(abs) != "webhooks.yaml" {
		return nil
	}
	dir := filepath.Dir(abs)
	manifest, err := LoadChecksums(dir)
	if err != nil {
		return fmt.Errorf("checksum verification failed in %s: %w", dir, err)
	}
	want, ok := manifest.Hashes[abs]
	if !ok {
		return fmt.Errorf("scope file %s has no hash in checksums at %s", filepath.Base(abs), dir)
	}
	sum := sha256.Sum256(raw)
	if got := "sha256:" + hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("hash mismatch for %s: expected %s, got %s", filepath.Base(abs), want, got)
	}
	return nil
}

// exportFragmentDotEnv parses every fragment's environment_vars.include, in
// fragment order, and exports each variable the process does not already have.
// The first file to define a name wins; a name defined twice in one file keeps
// ParseDotEnv's choice (the last), which is a known difference from load().
func exportFragmentDotEnv(fragments []yamlconfig.Fragment, kr *secrets.Keyring) error {
	for _, f := range fragments {
		includes := f.Root.At("/environment_vars/include")
		if includes.Kind() != ashlar.KindArray {
			continue
		}
		baseDir := "/" + path.Dir(f.Path)
		for i, el := range includes.Elements() {
			raw, ok := el.StringVal()
			if !ok {
				return fmt.Errorf("environment_vars.include[%d] in /%s: not a string", i, f.Path)
			}
			envPath, err := expandTilde(raw)
			if err != nil {
				return fmt.Errorf("environment_vars.include[%d]: %w", i, err)
			}
			if !filepath.IsAbs(envPath) {
				envPath = filepath.Join(baseDir, envPath)
			}
			if err := exportDotEnvFile(envPath, kr); err != nil {
				return fmt.Errorf("environment_vars.include[%d]: %w", i, err)
			}
		}
	}
	return nil
}

// exportDotEnvFile reads (and decrypts) one .env file and exports each variable
// not already set, in sorted order so the result does not depend on map order.
func exportDotEnvFile(envPath string, kr *secrets.Keyring) error {
	data, err := readConfigBytes(kr, envPath)
	if err != nil {
		return fmt.Errorf("failed to read env file %s: %w", envPath, err)
	}
	vars, err := yamlconfig.ParseDotEnv(envPath, data)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(vars))
	for name := range vars {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, set := os.LookupEnv(name); set {
			continue
		}
		if err := os.Setenv(name, vars[name]); err != nil {
			return fmt.Errorf("failed to set env %s from %s: %w", name, envPath, err)
		}
	}
	return nil
}

// finishLoad runs load()'s steps after the decode, in load()'s order, with
// multi-file mode decided from the resolved files (K). Keep it in step with
// load(): the harness is only as good as this mirror.
func finishLoad(cfg *Config, configDir string, kr *secrets.Keyring, multiFile, validateConfig, applyPluginDefaults bool) error {
	if err := expandTildePaths(cfg); err != nil {
		return err
	}
	applyConfigDefaults(cfg)
	resolveStatePath(cfg, configDir)

	owner, warnings, err := projectVaultSecrets(cfg, configDir, kr)
	if err != nil {
		return err
	}
	logSecretProjectionWarnings(warnings)

	if validateConfig {
		if err := validateLoaded(cfg, configDir, kr, owner, multiFile); err != nil {
			return err
		}
	}
	if applyPluginDefaults {
		for name, pluginConf := range cfg.Plugins {
			cfg.Plugins[name] = mergePluginDefaults(pluginConf, cfg.Service.MaxWorkers)
		}
	}
	return nil
}

// validateLoaded is load()'s validation block.
func validateLoaded(cfg *Config, configDir string, kr *secrets.Keyring, owner *vault.Vault, multiFile bool) error {
	if multiFile {
		validator := &ConfigValidator{
			config:     cfg,
			tokens:     cfg.ResolvedSecrets,
			vaultBlind: vaultBlind(configDir, cfg, kr),
			bootstrap:  DecideBootPosture(cfg, owner != nil) == PostureManagementOnly,
		}
		if err := validator.ValidateCrossReferences(); err != nil {
			return fmt.Errorf("configuration validation failed: %w", err)
		}
	}
	if err := validate(cfg, owner != nil); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	return nil
}
