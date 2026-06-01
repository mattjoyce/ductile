package vault

import (
	"errors"
	"reflect"
	"testing"
)

// composeFixture: principals withings (active) + idle (revoked); secrets a (for
// withings), b (for withings, revoked), c (for someone else), d (active, for
// withings).
func composeFixture(t *testing.T) *Store {
	t.Helper()
	s := NewStore()
	for _, n := range []string{"withings", "other"} {
		if err := s.RegisterPrincipal(n, KindPlugin); err != nil {
			t.Fatalf("register %s: %v", n, err)
		}
	}
	if err := s.RegisterPrincipal("idle", KindPlugin); err != nil {
		t.Fatalf("register idle: %v", err)
	}
	s.Principals["idle"].Status = StatusRevoked

	if err := s.SetSecret("a-secret", "AV", []string{"withings"}, PatternManual, testTime); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecret("b-secret", "BV", []string{"withings"}, PatternManual, testTime); err != nil {
		t.Fatal(err)
	}
	s.Secrets["b-secret"].Status = StatusRevoked // authorized-but-revoked -> denial
	if err := s.SetSecret("c-secret", "CV", []string{"other"}, PatternManual, testTime); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecret("d-secret", "DV", []string{"withings"}, PatternManual, testTime); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestComposeReturnsAuthorizedActiveSecrets(t *testing.T) {
	s := composeFixture(t)
	comp, err := s.Compose("withings")
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	want := map[string]string{"a-secret": "AV", "d-secret": "DV"}
	if !reflect.DeepEqual(comp.Secrets, want) {
		t.Fatalf("secrets = %v, want %v (only authorized + active; c excluded, b revoked)", comp.Secrets, want)
	}
}

func TestComposeRevokedAuthorizedSecretBecomesDenial(t *testing.T) {
	s := composeFixture(t)
	comp, _ := s.Compose("withings")
	want := []Denial{{Secret: "b-secret", Reason: DenialSecretRevoked}}
	if !reflect.DeepEqual(comp.Denials, want) {
		t.Fatalf("denials = %v, want %v (revoked-but-granted is a named signal)", comp.Denials, want)
	}
	if _, leaked := comp.Secrets["b-secret"]; leaked {
		t.Fatal("revoked secret leaked into delivered set")
	}
}

func TestComposeUnknownPrincipalFailsClosed(t *testing.T) {
	s := composeFixture(t)
	comp, err := s.Compose("ghost")
	if !errors.Is(err, ErrUnknownPrincipal) {
		t.Fatalf("err = %v, want ErrUnknownPrincipal", err)
	}
	if comp.Secrets != nil || comp.Denials != nil {
		t.Fatal("expected zero Composition on error (fail-closed, not empty-but-valid)")
	}
}

func TestComposeInactivePrincipalFailsClosed(t *testing.T) {
	s := composeFixture(t)
	_, err := s.Compose("idle")
	if !errors.Is(err, ErrPrincipalInactive) {
		t.Fatalf("err = %v, want ErrPrincipalInactive (a revoked principal must not run)", err)
	}
}

func TestComposeNoGrantsIsEmptyNotError(t *testing.T) {
	s := composeFixture(t)
	// "other" is authorized only for c-secret (active) -> one secret, no denials.
	comp, err := s.Compose("other")
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if !reflect.DeepEqual(comp.Secrets, map[string]string{"c-secret": "CV"}) {
		t.Fatalf("secrets = %v", comp.Secrets)
	}
	if len(comp.Denials) != 0 {
		t.Fatalf("denials = %v, want none", comp.Denials)
	}
}

func TestComposeDenialsDeterministic(t *testing.T) {
	s := NewStore()
	_ = s.RegisterPrincipal("p", KindPlugin)
	for _, n := range []string{"z-sec", "a-sec", "m-sec"} {
		_ = s.SetSecret(n, "v", []string{"p"}, PatternManual, testTime)
		s.Secrets[n].Status = StatusRevoked
	}
	comp, _ := s.Compose("p")
	got := []string{}
	for _, d := range comp.Denials {
		got = append(got, d.Secret)
	}
	if !reflect.DeepEqual(got, []string{"a-sec", "m-sec", "z-sec"}) {
		t.Fatalf("denials not sorted deterministically: %v", got)
	}
}
