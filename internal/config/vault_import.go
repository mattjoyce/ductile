package config

import (
	"fmt"
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
