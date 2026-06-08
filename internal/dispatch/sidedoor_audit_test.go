package dispatch

import (
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/mattjoyce/ductile/internal/config"
)

// fakeLookup is the test double for osLookup — every probe is table data, and any
// probe can be made to error to exercise the inconclusive paths.
type fakeLookup struct {
	usernames  map[int]string
	groups     map[int][]string
	groupErr   map[int]error
	sudo       map[string]bool
	sudoErr    map[string]error
	wpath      map[int][]string
	wpathErr   map[int]error
	wsetuid    map[int][]string
	wsetuidErr map[int]error
}

func (f fakeLookup) UsernameForUID(uid int) (string, bool) {
	n, ok := f.usernames[uid]
	return n, ok
}
func (f fakeLookup) GroupNamesForUID(uid int) ([]string, error) {
	return f.groups[uid], f.groupErr[uid]
}
func (f fakeLookup) SudoNoPasswd(user string) (bool, error) {
	return f.sudo[user], f.sudoErr[user]
}
func (f fakeLookup) WritablePathDirs(uid int) ([]string, error)   { return f.wpath[uid], f.wpathErr[uid] }
func (f fakeLookup) WritableSetuidRoot(uid int) ([]string, error) { return f.wsetuid[uid], f.wsetuidErr[uid] }

func TestClassifySideDoor(t *testing.T) {
	clean := sideDoorFindings{}
	withDoor := sideDoorFindings{NoPasswdSudo: true}
	unsureOnly := sideDoorFindings{probeErrors: []string{"could not query sudo"}}

	cases := []struct {
		name       string
		findings   sideDoorFindings
		mode       AccountMode
		failClosed bool
		wantReport bool
		wantFail   bool
		wantLevel  slog.Level
	}{
		{"clean confined non-strict", clean, ModeConfined, false, false, false, 0},
		{"clean confined strict", clean, ModeConfined, true, false, false, 0},
		{"confined + door, non-strict -> warn", withDoor, ModeConfined, false, true, false, slog.LevelWarn},
		{"confined + door, strict -> fail closed", withDoor, ModeConfined, true, true, true, slog.LevelError},
		{"confined + INCONCLUSIVE, non-strict -> warn only", unsureOnly, ModeConfined, false, true, false, slog.LevelWarn},
		{"confined + INCONCLUSIVE, strict -> fail closed (unproven wall)", unsureOnly, ModeConfined, true, true, true, slog.LevelError},
		{"credentialed + door, strict -> warn, never fails", withDoor, ModeCredentialed, true, true, false, slog.LevelWarn},
		{"credentialed + INCONCLUSIVE, strict -> warn, never fails", unsureOnly, ModeCredentialed, true, true, false, slog.LevelWarn},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := classifySideDoor(tc.findings, tc.mode, tc.failClosed)
			if v.report != tc.wantReport {
				t.Fatalf("report = %v, want %v", v.report, tc.wantReport)
			}
			if v.failBoot != tc.wantFail {
				t.Fatalf("failBoot = %v, want %v", v.failBoot, tc.wantFail)
			}
			if tc.wantReport && v.level != tc.wantLevel {
				t.Fatalf("level = %v, want %v", v.level, tc.wantLevel)
			}
		})
	}
}

func TestGatherSideDoors(t *testing.T) {
	t.Run("detects all four doors", func(t *testing.T) {
		look := fakeLookup{
			usernames: map[int]string{1000: "matt"},
			groups:    map[int][]string{1000: {"matt", "docker", "sudo"}},
			sudo:      map[string]bool{"matt": true},
			wpath:     map[int][]string{1000: {"/usr/local/bin"}},
			wsetuid:   map[int][]string{1000: {"/usr/bin/passwd"}},
		}
		f := gatherSideDoors(1000, look)
		if !f.NoPasswdSudo {
			t.Error("expected nopasswd sudo")
		}
		if len(f.DangerGroups) != 1 || f.DangerGroups[0] != "docker" {
			t.Errorf("DangerGroups = %v, want [docker]", f.DangerGroups)
		}
		if len(f.WritablePath) != 1 || len(f.WritableSetuid) != 1 {
			t.Errorf("path/setuid findings = %v / %v", f.WritablePath, f.WritableSetuid)
		}
		if !f.any() || f.inconclusive() {
			t.Errorf("want any && !inconclusive; got any=%v inconclusive=%v", f.any(), f.inconclusive())
		}
	})

	t.Run("clean account: no findings, not inconclusive", func(t *testing.T) {
		look := fakeLookup{
			usernames: map[int]string{1001: "dplug"},
			groups:    map[int][]string{1001: {"dplug"}},
			sudo:      map[string]bool{"dplug": false},
		}
		f := gatherSideDoors(1001, look)
		if f.any() || f.inconclusive() {
			t.Errorf("clean account: any=%v inconclusive=%v, want both false", f.any(), f.inconclusive())
		}
	})

	t.Run("sudo undeterminable -> inconclusive, not silently safe", func(t *testing.T) {
		look := fakeLookup{
			usernames: map[int]string{1002: "x"},
			groups:    map[int][]string{1002: {"x"}},
			sudoErr:   map[string]error{"x": errors.New("cannot query sudoers")},
		}
		f := gatherSideDoors(1002, look)
		if f.NoPasswdSudo {
			t.Error("uncertain must not be a positive sudo finding")
		}
		if !f.inconclusive() {
			t.Error("expected inconclusive")
		}
	})

	t.Run("missing username -> inconclusive (sudo not checked)", func(t *testing.T) {
		look := fakeLookup{groups: map[int][]string{1003: {"g"}}}
		f := gatherSideDoors(1003, look)
		if f.NoPasswdSudo {
			t.Error("no username -> sudo not probed")
		}
		if !f.inconclusive() {
			t.Error("expected inconclusive when username unresolved")
		}
	})

	t.Run("probe errors (group/path/setuid) -> inconclusive, never fatal", func(t *testing.T) {
		look := fakeLookup{
			usernames:  map[int]string{1: "u"},
			groupErr:   map[int]error{1: errors.New("nss down")},
			sudo:       map[string]bool{"u": false},
			wpathErr:   map[int]error{1: errors.New("EACCES")},
			wsetuidErr: map[int]error{1: errors.New("EACCES")},
		}
		f := gatherSideDoors(1, look)
		if len(f.DangerGroups) != 0 {
			t.Error("no groups flagged when lookup failed")
		}
		if !f.inconclusive() {
			t.Error("expected inconclusive from swallowed probe errors")
		}
	})
}

func TestAuditAccountSideDoors(t *testing.T) {
	confinedAcct := config.AccountConf{UID: 1001, GID: 1001} // no Home -> confined
	credAcct := config.AccountConf{UID: 1000, GID: 1000, Home: "/home/matt"}

	t.Run("confined + side-door + strict -> boot refused, names account", func(t *testing.T) {
		cfg := &config.Config{Accounts: map[string]config.AccountConf{"untrusted": confinedAcct}}
		look := fakeLookup{
			usernames: map[int]string{1001: "dplug"},
			groups:    map[int][]string{1001: {"dplug", "docker"}},
			sudo:      map[string]bool{"dplug": false},
		}
		err := auditAccountSideDoors(cfg, true, discardLogger(), look)
		if err == nil || !strings.Contains(err.Error(), "untrusted") {
			t.Fatalf("expected fail naming the account, got %v", err)
		}
	})

	t.Run("confined + side-door + non-strict -> warn only, boots", func(t *testing.T) {
		cfg := &config.Config{Accounts: map[string]config.AccountConf{"untrusted": confinedAcct}}
		look := fakeLookup{
			usernames: map[int]string{1001: "dplug"},
			groups:    map[int][]string{1001: {"dplug", "docker"}},
			sudo:      map[string]bool{"dplug": false},
		}
		if err := auditAccountSideDoors(cfg, false, discardLogger(), look); err != nil {
			t.Fatalf("non-strict must not fail closed: %v", err)
		}
	})

	t.Run("confined + INCONCLUSIVE + strict -> fail closed (unverifiable wall)", func(t *testing.T) {
		cfg := &config.Config{Accounts: map[string]config.AccountConf{"untrusted": confinedAcct}}
		// every probe inconclusive: no username, group lookup down.
		look := fakeLookup{groupErr: map[int]error{1001: errors.New("nss down")}}
		err := auditAccountSideDoors(cfg, true, discardLogger(), look)
		if err == nil || !strings.Contains(err.Error(), "untrusted") {
			t.Fatalf("strict must refuse an unverifiable confined account, got %v", err)
		}
	})

	t.Run("confined + INCONCLUSIVE + non-strict -> warn only", func(t *testing.T) {
		cfg := &config.Config{Accounts: map[string]config.AccountConf{"untrusted": confinedAcct}}
		look := fakeLookup{groupErr: map[int]error{1001: errors.New("nss down")}}
		if err := auditAccountSideDoors(cfg, false, discardLogger(), look); err != nil {
			t.Fatalf("non-strict inconclusive must not fail: %v", err)
		}
	})

	t.Run("credentialed + side-door + strict -> never fails (acts as you)", func(t *testing.T) {
		cfg := &config.Config{Accounts: map[string]config.AccountConf{"trusted": credAcct}}
		look := fakeLookup{
			usernames: map[int]string{1000: "matt"},
			groups:    map[int][]string{1000: {"matt", "docker"}},
			sudo:      map[string]bool{"matt": true},
		}
		if err := auditAccountSideDoors(cfg, true, discardLogger(), look); err != nil {
			t.Fatalf("credentialed must never fail closed: %v", err)
		}
	})

	t.Run("clean accounts -> no error", func(t *testing.T) {
		cfg := &config.Config{Accounts: map[string]config.AccountConf{
			"default":   {UID: 1001, GID: 1001},
			"untrusted": {UID: 1002, GID: 1002},
		}}
		look := fakeLookup{
			usernames: map[int]string{1001: "a", 1002: "b"},
			groups:    map[int][]string{1001: {"a"}, 1002: {"b"}},
			sudo:      map[string]bool{"a": false, "b": false},
		}
		if err := auditAccountSideDoors(cfg, true, discardLogger(), look); err != nil {
			t.Fatalf("clean accounts must boot clean: %v", err)
		}
	})

	t.Run("mixed: confined door fails, credentialed door tolerated", func(t *testing.T) {
		cfg := &config.Config{Accounts: map[string]config.AccountConf{
			"untrusted": confinedAcct,
			"trusted":   credAcct,
		}}
		look := fakeLookup{
			usernames: map[int]string{1001: "dplug", 1000: "matt"},
			groups:    map[int][]string{1001: {"dplug", "lxd"}, 1000: {"matt", "docker"}},
			sudo:      map[string]bool{"dplug": false, "matt": false},
		}
		err := auditAccountSideDoors(cfg, true, discardLogger(), look)
		if err == nil || !strings.Contains(err.Error(), "untrusted") {
			t.Fatalf("expected fail naming the confined account, got %v", err)
		}
		if strings.Contains(err.Error(), "trusted,") || strings.Contains(err.Error(), ", trusted") {
			t.Errorf("credentialed account must not be in the fail list: %v", err)
		}
	})

	t.Run("misconfigured uid-0 account: warned + skipped, no crash", func(t *testing.T) {
		cfg := &config.Config{Accounts: map[string]config.AccountConf{"bad": {UID: 0, GID: 0}}}
		if err := auditAccountSideDoors(cfg, true, discardLogger(), fakeLookup{}); err != nil {
			t.Fatalf("uid-0 account must be skipped, not fail/crash: %v", err)
		}
	})

	t.Run("nil cfg and nil lookup are safe no-ops", func(t *testing.T) {
		if err := auditAccountSideDoors(nil, true, discardLogger(), fakeLookup{}); err != nil {
			t.Errorf("nil cfg: %v", err)
		}
		if err := auditAccountSideDoors(&config.Config{}, true, discardLogger(), nil); err != nil {
			t.Errorf("nil lookup: %v", err)
		}
	})
}

func TestSideDoorLogAttrs(t *testing.T) {
	f := sideDoorFindings{
		NoPasswdSudo: true,
		DangerGroups: []string{"docker"},
		probeErrors:  []string{"x"},
	}
	ra := ResolvedAccount{Name: "untrusted", UID: 1001, GID: 1001, Mode: ModeConfined}
	attrs := sideDoorLogAttrs("untrusted", ra, f)
	var haveNote, haveAccount, haveRisk, haveInconclusive bool
	for _, a := range attrs {
		switch a.Key {
		case "note":
			haveNote = strings.Contains(a.Value.String(), "best-effort")
		case "account":
			haveAccount = a.Value.String() == "untrusted"
		case "root_group_risk":
			haveRisk = strings.Contains(a.Value.String(), "docker")
		case "inconclusive":
			haveInconclusive = true
		}
	}
	if !haveNote || !haveAccount || !haveRisk || !haveInconclusive {
		t.Errorf("attrs missing: note=%v account=%v risk=%v inconclusive=%v",
			haveNote, haveAccount, haveRisk, haveInconclusive)
	}
}
