package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// TestAdmissionYAMLParsing guards the struct tags: an explicit admission block
// and the deprecated strict_mode alias must both parse and resolve correctly
// through real YAML, independent of any one policy.
func TestAdmissionYAMLParsing(t *testing.T) {
	var withBlock Config
	if err := yaml.Unmarshal([]byte(
		"service:\n  admission:\n    verify_integrity_on_boot: true\n    fail_on_drift: false\n    validate_config_on_boot: true\n    require_api_auth: false\n",
	), &withBlock); err != nil {
		t.Fatalf("parse admission block: %v", err)
	}
	got := withBlock.Service.AdmissionPolicy()
	if !got.VerifyIntegrityOnBoot || got.FailOnDrift || !got.ValidateConfigOnBoot || got.RequireAPIAuth {
		t.Fatalf("admission block resolved wrong: %+v", got)
	}

	var withAlias Config
	if err := yaml.Unmarshal([]byte("service:\n  strict_mode: true\n"), &withAlias); err != nil {
		t.Fatalf("parse strict_mode: %v", err)
	}
	if a := withAlias.Service.AdmissionPolicy(); !a.VerifyIntegrityOnBoot || !a.FailOnDrift || !a.ValidateConfigOnBoot || !a.RequireAPIAuth {
		t.Fatalf("strict_mode alias should enable all four, got %+v", a)
	}
}

// TestAdmissionPolicyExplicitBlockWins — when an admission block is present, its
// fields are used verbatim and the deprecated strict_mode alias is ignored.
func TestAdmissionPolicyExplicitBlockWins(t *testing.T) {
	svc := ServiceConfig{
		StrictMode: true, // must be ignored when admission is present
		Admission: &AdmissionConfig{
			VerifyIntegrityOnBoot: true,
			FailOnDrift:           false,
			ValidateConfigOnBoot:  true,
			RequireAPIAuth:        false,
		},
	}
	got := svc.AdmissionPolicy()
	want := AdmissionConfig{VerifyIntegrityOnBoot: true, ValidateConfigOnBoot: true}
	if got != want {
		t.Fatalf("AdmissionPolicy() = %+v, want %+v (explicit block must win over strict_mode)", got, want)
	}
}

// TestAdmissionPolicyStrictAliasEnablesAll — the deprecated strict_mode: true
// alias, with no admission block, turns on every policy (back-compat). The
// privsep side-door fail-closed (#111) is a strict gate, so it is included.
func TestAdmissionPolicyStrictAliasEnablesAll(t *testing.T) {
	svc := ServiceConfig{StrictMode: true}
	got := svc.AdmissionPolicy()
	want := AdmissionConfig{
		VerifyIntegrityOnBoot: true,
		FailOnDrift:           true,
		ValidateConfigOnBoot:  true,
		RequireAPIAuth:        true,
		FailOnSideDoor:        true,
	}
	if got != want {
		t.Fatalf("AdmissionPolicy() = %+v, want all-true %+v", got, want)
	}
}

// TestAdmissionPolicyDefaultIsPermissive — neither admission block nor
// strict_mode set: every policy is off, preserving today's zero-value default.
func TestAdmissionPolicyDefaultIsPermissive(t *testing.T) {
	got := ServiceConfig{}.AdmissionPolicy()
	if (got != AdmissionConfig{}) {
		t.Fatalf("AdmissionPolicy() = %+v, want all-false zero value", got)
	}
}

// TestStrictModeDeprecationWarning — the warning fires only when strict_mode is
// set, and distinguishes the ignored-because-superseded case.
func TestStrictModeDeprecationWarning(t *testing.T) {
	if w := (ServiceConfig{}).StrictModeDeprecationWarning(); w != "" {
		t.Errorf("no strict_mode set: want empty warning, got %q", w)
	}
	if w := (ServiceConfig{StrictMode: true}).StrictModeDeprecationWarning(); w == "" {
		t.Error("strict_mode set: want a deprecation warning, got empty")
	}
	w := ServiceConfig{StrictMode: true, Admission: &AdmissionConfig{}}.StrictModeDeprecationWarning()
	if w == "" {
		t.Error("strict_mode set alongside admission block: want an 'ignored' warning, got empty")
	}
}
