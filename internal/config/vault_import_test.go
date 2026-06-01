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
