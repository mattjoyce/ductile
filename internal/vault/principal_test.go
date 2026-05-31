package vault

import (
	"errors"
	"reflect"
	"testing"
)

func TestRegisterPrincipal(t *testing.T) {
	s := NewStore()
	if err := s.RegisterPrincipal("withings", KindPlugin); err != nil {
		t.Fatalf("register: %v", err)
	}
	p, ok := s.Principal("withings")
	if !ok || p.Kind != KindPlugin || p.Status != StatusActive {
		t.Fatalf("got %+v, ok=%v; want active plugin", p, ok)
	}
}

func TestRegisterPrincipalDuplicateFails(t *testing.T) {
	s := NewStore()
	_ = s.RegisterPrincipal("core", KindGateway)
	err := s.RegisterPrincipal("core", KindGateway)
	if !errors.Is(err, ErrDuplicatePrincipal) {
		t.Fatalf("duplicate register err = %v, want ErrDuplicatePrincipal", err)
	}
}

func TestRegisterPrincipalInvalidName(t *testing.T) {
	s := NewStore()
	for _, bad := range []string{"With_Ings", "UPPER", "trailing-", "-leading", "has space", ""} {
		if err := s.RegisterPrincipal(bad, KindPlugin); !errors.Is(err, ErrInvalidName) {
			t.Errorf("name %q: err = %v, want ErrInvalidName", bad, err)
		}
	}
}

func TestRegisterPrincipalInvalidKind(t *testing.T) {
	s := NewStore()
	if err := s.RegisterPrincipal("x", "robot"); !errors.Is(err, ErrInvalidKind) {
		t.Fatalf("kind err = %v, want ErrInvalidKind", err)
	}
}

func TestPrincipalNamesSorted(t *testing.T) {
	s := NewStore()
	_ = s.RegisterPrincipal("zeta", KindPlugin)
	_ = s.RegisterPrincipal("alpha", KindConsumer)
	_ = s.RegisterPrincipal("core", KindGateway)
	if got := s.PrincipalNames(); !reflect.DeepEqual(got, []string{"alpha", "core", "zeta"}) {
		t.Fatalf("PrincipalNames = %v, want sorted", got)
	}
}
