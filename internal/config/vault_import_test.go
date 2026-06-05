package config

import "testing"

func TestPlanTokenImportClassifies(t *testing.T) {
	env := map[string]string{"WITHINGS": "resolved-secret"}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }

	entries := []TokenEntry{
		{Name: "literal_key", Key: "abc123"},          // imports as-is
		{Name: "env_set", Key: "${WITHINGS}"},         // resolves with --resolve-env
		{Name: "env_unset", Key: "${MISSING}"},        // cannot resolve -> flagged
		{Name: "empty", Key: ""},                      // flagged
	}

	t.Run("without resolveEnv, pointers are flagged", func(t *testing.T) {
		plan := PlanTokenImport(entries, false, lookup)
		imported := importedMap(plan)
		if imported["literal_key"] != "abc123" {
			t.Errorf("literal should import as-is, got %q", imported["literal_key"])
		}
		if _, ok := imported["env_set"]; ok {
			t.Error("env-pointer should be flagged when resolveEnv is off, not imported")
		}
		if !flaggedHas(plan, "env_set") || !flaggedHas(plan, "env_unset") || !flaggedHas(plan, "empty") {
			t.Errorf("expected env_set, env_unset, empty flagged; got %+v", plan.Flagged)
		}
	})

	t.Run("with resolveEnv, set pointers import and unset stay flagged", func(t *testing.T) {
		plan := PlanTokenImport(entries, true, lookup)
		imported := importedMap(plan)
		if imported["env_set"] != "resolved-secret" {
			t.Errorf("set env-pointer should import resolved value, got %q", imported["env_set"])
		}
		if _, ok := imported["env_unset"]; ok {
			t.Error("unset env-pointer must not import")
		}
		if !flaggedHas(plan, "env_unset") {
			t.Error("unset env-pointer should remain flagged")
		}
		if !flaggedHas(plan, "empty") {
			t.Error("empty value should remain flagged")
		}
	})
}

func TestVerifyTokenParityClassifies(t *testing.T) {
	env := map[string]string{"SET": "env-val"}
	lookupEnv := func(k string) (string, bool) { v, ok := env[k]; return v, ok }

	// The vault: a name->value table of ACTIVE secrets, plus one revoked entry to
	// prove a revoked secret never counts as parity.
	vaultActive := map[string]string{
		"match_lit":   "same",      // equals tokens.yaml literal
		"drift_lit":   "rolled",    // differs from tokens.yaml literal (rolled since import)
		"vault_super": "from-vault", // supersedes an unresolvable env-pointer
	}
	vault := func(name string) (string, bool) {
		if name == "revoked_name" {
			return "", false // revoked -> not active
		}
		v, ok := vaultActive[name]
		return v, ok
	}

	entries := []TokenEntry{
		{Name: "match_lit", Key: "same"},          // -> match
		{Name: "drift_lit", Key: "different"},     // -> drift (vault has "rolled")
		{Name: "missing_lit", Key: "needs-import"}, // -> missing (not in vault)
		{Name: "vault_super", Key: "${UNSET}"},    // env-pointer, vault supersedes -> vault-only
		{Name: "unresolved", Key: "${UNSET}"},     // env-pointer, no vault value -> unresolved
		{Name: "revoked_name", Key: "x"},          // literal but vault entry revoked -> missing
	}

	rep := VerifyTokenParity(entries, false, lookupEnv, vault)

	got := make(map[string]ParityStatus, len(rep.Entries))
	for _, e := range rep.Entries {
		got[e.Name] = e.Status
	}
	want := map[string]ParityStatus{
		"match_lit":    ParityMatch,
		"drift_lit":    ParityDrift,
		"missing_lit":  ParityMissing,
		"vault_super":  ParityVaultOnly,
		"unresolved":   ParityUnresolved,
		"revoked_name": ParityMissing,
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s: want %q, got %q", name, w, got[name])
		}
	}

	if rep.Green() {
		t.Error("report with drift/missing/unresolved must not be green")
	}

	// Deterministic ordering: entries sorted by name.
	for i := 1; i < len(rep.Entries); i++ {
		if rep.Entries[i-1].Name > rep.Entries[i].Name {
			t.Errorf("entries not sorted by name: %q before %q", rep.Entries[i-1].Name, rep.Entries[i].Name)
		}
	}

	t.Run("all-satisfied report is green", func(t *testing.T) {
		green := VerifyTokenParity(
			[]TokenEntry{{Name: "match_lit", Key: "same"}, {Name: "vault_super", Key: "${UNSET}"}},
			false, lookupEnv, vault)
		if !green.Green() {
			t.Errorf("match + vault-only should be green, got %+v", green.Entries)
		}
	})

	t.Run("resolveEnv turns a resolvable pointer into a value comparison", func(t *testing.T) {
		// SET resolves to "env-val"; vault must match it to be green.
		vaultWithSet := func(name string) (string, bool) {
			if name == "ptr" {
				return "env-val", true
			}
			return vault(name)
		}
		rep := VerifyTokenParity([]TokenEntry{{Name: "ptr", Key: "${SET}"}}, true, lookupEnv, vaultWithSet)
		if len(rep.Entries) != 1 || rep.Entries[0].Status != ParityMatch {
			t.Errorf("resolved pointer matching vault should be match, got %+v", rep.Entries)
		}
	})
}

func importedMap(p ImportPlan) map[string]string {
	m := make(map[string]string, len(p.Imported))
	for _, s := range p.Imported {
		m[s.Name] = s.Value
	}
	return m
}

func flaggedHas(p ImportPlan, name string) bool {
	for _, f := range p.Flagged {
		if f.Name == name {
			return true
		}
	}
	return false
}
