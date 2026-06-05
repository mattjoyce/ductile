package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mattjoyce/ductile/internal/secrets"
	"gopkg.in/yaml.v3"
)

// ReadRawTokens reads a tokens.yaml table (decrypting if age-encrypted) WITHOUT
// ${ENV} interpolation — PlanTokenImport needs the raw entries to tell an
// env-pointer from a literal value. Decryption is delegated to readConfigBytes
// so the read/decrypt rules have a single home.
func ReadRawTokens(path string, kr *secrets.Keyring) ([]TokenEntry, error) {
	data, err := readConfigBytes(kr, path)
	if err != nil {
		return nil, err
	}
	var tf TokensFileConfig
	if err := yaml.Unmarshal(data, &tf); err != nil {
		return nil, fmt.Errorf("parse %q: %w", path, err)
	}
	return tf.Tokens, nil
}

// ImportedSecret is a tokens.yaml entry whose value can move into the vault.
type ImportedSecret struct {
	Name  string
	Value string
}

// FlaggedSecret is a tokens.yaml entry that cannot be imported automatically and
// needs operator attention (an env-pointer with no resolution, or an empty value).
type FlaggedSecret struct {
	Name   string
	Reason string
}

// ImportPlan is the classification of a tokens.yaml table for vault migration:
// what moves in as-is, and what an operator must handle.
type ImportPlan struct {
	Imported []ImportedSecret
	Flagged  []FlaggedSecret
}

// PlanTokenImport classifies legacy tokens.yaml entries for migration into the
// vault (ADR §6). A literal value imports as-is. An ${ENV} pointer is *flagged*
// by default — the value lives in the host environment, not in ductile, so
// importing it silently would defeat the point — unless resolveEnv is set, in
// which case a fully-resolvable pointer imports its resolved value and an
// unresolvable one is flagged. Pure: env access is injected via lookupEnv.
func PlanTokenImport(entries []TokenEntry, resolveEnv bool, lookupEnv func(string) (string, bool)) ImportPlan {
	var plan ImportPlan
	for _, e := range entries {
		switch {
		case strings.TrimSpace(e.Key) == "":
			plan.Flagged = append(plan.Flagged, FlaggedSecret{e.Name, "empty value"})

		case !envVarPattern.MatchString(e.Key):
			plan.Imported = append(plan.Imported, ImportedSecret{e.Name, e.Key}) // literal

		case !resolveEnv:
			plan.Flagged = append(plan.Flagged, FlaggedSecret{e.Name,
				fmt.Sprintf("env-pointer %q; re-provision into the vault or re-run with --resolve-env", e.Key)})

		default:
			if resolved, ok := resolveEnvValue(e.Key, lookupEnv); ok {
				plan.Imported = append(plan.Imported, ImportedSecret{e.Name, resolved})
			} else {
				plan.Flagged = append(plan.Flagged, FlaggedSecret{e.Name,
					fmt.Sprintf("env-pointer %q could not be resolved; variable not set", e.Key)})
			}
		}
	}
	return plan
}

// ParityStatus classifies one tokens.yaml entry against the live vault during a
// read-only verify (epic #48 slice 1). The migration invariant is "if it is a
// secret, it is in the vault, with the same value" — verify proves it per entry
// before any destructive cutover.
type ParityStatus string

const (
	// ParityMatch: the vault holds an active secret of this name whose value
	// equals the resolved tokens.yaml value. Green.
	ParityMatch ParityStatus = "match"
	// ParityVaultOnly: tokens.yaml cannot supply a value (an ${ENV} pointer or an
	// empty entry), but the vault holds an active value that supersedes it. Green —
	// the vault is authoritative, which is the migration's whole point.
	ParityVaultOnly ParityStatus = "vault-only"
	// ParityMissing: tokens.yaml resolves to a concrete value but the vault has no
	// active secret of that name. Not green — import is needed before cutover.
	ParityMissing ParityStatus = "missing"
	// ParityDrift: both sides hold a value and they differ. Almost always the vault
	// was rolled since import. Not green — and never auto-clobbered; an operator
	// reconciles. This is the idempotency guard: verify refuses to call a
	// since-rolled secret "out of parity to be overwritten".
	ParityDrift ParityStatus = "drift"
	// ParityUnresolved: an ${ENV} pointer (or empty entry) with no vault value and
	// no env resolution. Not green — forces an explicit per-entry decision instead
	// of silently freezing a host-local value.
	ParityUnresolved ParityStatus = "unresolved"
)

// ParityEntry is one tokens.yaml entry's verdict against the vault.
type ParityEntry struct {
	Name   string
	Status ParityStatus
	Detail string
}

// ParityReport is the per-entry classification of a tokens.yaml table against
// the live vault. Green reports that every entry is satisfied by the vault.
type ParityReport struct {
	Entries []ParityEntry
}

// Green reports whether every entry is satisfied by the vault (match or
// superseded). Any missing/drift/unresolved entry makes the whole report
// non-green — the caller should exit non-zero and refuse to cut over.
func (r ParityReport) Green() bool {
	for _, e := range r.Entries {
		if e.Status != ParityMatch && e.Status != ParityVaultOnly {
			return false
		}
	}
	return true
}

// VaultLookup returns the active value of a secret by name and whether an active
// secret of that name exists in the vault. A revoked or absent secret reports
// (\"\", false) — a revoked secret must not count as parity.
type VaultLookup func(name string) (value string, active bool)

// VerifyTokenParity classifies every tokens.yaml entry against the live vault
// WITHOUT mutating anything (epic #48 slice 1). It reuses PlanTokenImport's
// literal-vs-${ENV} classification, then compares each resolvable value to what
// the vault actually yields. Pure: env and vault access are injected. Result is
// deterministically ordered by name so the report is stable across runs.
func VerifyTokenParity(entries []TokenEntry, resolveEnv bool, lookupEnv func(string) (string, bool), vault VaultLookup) ParityReport {
	plan := PlanTokenImport(entries, resolveEnv, lookupEnv)
	var rep ParityReport

	for _, s := range plan.Imported {
		value, active := vault(s.Name)
		switch {
		case !active:
			rep.Entries = append(rep.Entries, ParityEntry{s.Name, ParityMissing,
				"resolvable in tokens.yaml but absent/inactive in the vault — import before cutover"})
		case value == s.Value:
			rep.Entries = append(rep.Entries, ParityEntry{s.Name, ParityMatch,
				"vault value matches the resolved tokens.yaml value"})
		default:
			rep.Entries = append(rep.Entries, ParityEntry{s.Name, ParityDrift,
				"vault value differs from tokens.yaml (rolled since import?) — not clobbered; reconcile manually"})
		}
	}

	for _, f := range plan.Flagged {
		if _, active := vault(f.Name); active {
			rep.Entries = append(rep.Entries, ParityEntry{f.Name, ParityVaultOnly,
				"no usable tokens.yaml value, but an active vault value supersedes it"})
			continue
		}
		rep.Entries = append(rep.Entries, ParityEntry{f.Name, ParityUnresolved, f.Reason})
	}

	sort.Slice(rep.Entries, func(i, j int) bool { return rep.Entries[i].Name < rep.Entries[j].Name })
	return rep
}

// resolveEnvValue interpolates every ${VAR} in s via lookupEnv. ok is false if
// any referenced variable is unset (the value cannot be fully resolved).
func resolveEnvValue(s string, lookupEnv func(string) (string, bool)) (string, bool) {
	ok := true
	out := envVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		name := envVarPattern.FindStringSubmatch(match)[1]
		if v, found := lookupEnv(name); found {
			return v
		}
		ok = false
		return match
	})
	return out, ok
}
