package config

import (
	"path/filepath"

	"github.com/mattjoyce/ductile/internal/fsown"
)

// Artifact is one file or directory some account has to be able to open, paired
// with the account that has to open it (#179).
type Artifact struct {
	Path string
	// Role is the operator-facing name for what this file is, so a finding reads
	// "state database" rather than only a path.
	Role string
	// Account is who must be able to read it. Almost everything is read by the
	// gateway; a privsep account's state_dir is read by that account instead.
	Account fsown.Account
	// AccountName names the account in the finding — "gateway", or the account
	// key from the config's accounts table.
	AccountName string
}

// ReadabilityFinding is one artifact the named account cannot open.
type ReadabilityFinding struct {
	Path    string
	Role    string
	Account string
	// Detail is the operator sentence: which path blocks, who owns it, what mode
	// it has, and who was asking.
	Detail string
	// Indeterminate marks a finding the mode bits alone cannot decide, because a
	// POSIX ACL on the path may grant access they deny. Callers report these as
	// warnings rather than errors: a hard failure here would break a working
	// ACL-based install, and `config check` is a deploy gate.
	Indeterminate bool
}

// ServiceReadArtifacts enumerates every file the gateway (or a privsep account)
// must be able to open, so that readability can be checked over one list instead
// of being inferred one writer at a time (#179).
//
// The list is deliberately assembled from the same resolvers the daemon itself
// uses — DiscoverConfigFiles, ResolveVaultPath, ResolveAgeKeyPath, cfg.State.Path
// — rather than being restated here. A hand-maintained copy would drift from what
// the gateway actually opens, and a readability check that checks the wrong list
// is worse than none: it reports clean and is believed.
//
// Paths that do not exist stay in the list; the check skips them, because absence
// is a different failure with a different owner (integrity, or first-run
// bootstrap). Reporting a never-created .checksums as "unreadable" would repeat
// the ENOENT/EACCES confusion of #167 in the opposite direction.
func ServiceReadArtifacts(configDir string, cfg *Config, gateway fsown.Account) []Artifact {
	var arts []Artifact
	add := func(path, role string) {
		if path == "" {
			return
		}
		arts = append(arts, Artifact{Path: path, Role: role, Account: gateway, AccountName: "gateway"})
	}

	add(configDir, "config directory")
	// The config SUBDIRECTORIES, not only the files discovered inside them. An
	// unsearchable scopes/ makes DiscoverConfigFiles return fewer files — or fail —
	// and a check built only from what discovery returned would then find nothing
	// wrong with a directory it could not read. Silent under-enumeration is the same
	// shape of bug as the one being fixed.
	add(filepath.Join(configDir, "scopes"), "scopes directory")
	if cfg != nil {
		for _, root := range cfg.EffectivePluginRoots() {
			add(root, "plugin root")
		}
	}
	if files, err := DiscoverConfigFiles(configDir); err == nil {
		for _, f := range files.AllFiles() {
			add(f, "config file")
		}
	}
	// .checksums is in scope only when the daemon will actually open it.
	//
	// With verify_integrity_on_boot off, an unreadable manifest is not a boot
	// failure — the daemon never reads it — and an install in that state runs
	// perfectly well today. Refusing it would break a working deployment to warn
	// about a problem it does not have, which is the exact trade this whole check
	// is supposed to be on the right side of. The posture fixture asserts the
	// verify=false boot succeeds, and caught this when it did not.
	//
	// loadPluginFingerprintRecords also opens the manifest when plugins are
	// configured, but tolerates failure by design (#173), so it does not make the
	// file boot-critical.
	if cfg != nil && cfg.Service.AdmissionPolicy().VerifyIntegrityOnBoot {
		add(filepath.Join(configDir, ".checksums"), "integrity manifest")
	}
	add(ResolveVaultPath(configDir, cfg), "vault blob")
	add(ResolveAgeKeyPath(configDir, cfg), "age key")

	if cfg != nil {
		if cfg.State.Path != "" {
			add(cfg.State.Path, "state database")
			// The WAL and shared-memory side files are created lazily by the
			// driver and are exactly what #171 was about: the database opens, and
			// the failure surfaces later at whichever query first needs the side
			// file. Checking them here is what turns that into a boot-time answer.
			add(cfg.State.Path+"-wal", "state database WAL")
			add(cfg.State.Path+"-shm", "state database shared memory")
		}
		// A privsep account's state_dir is read by THAT account, not the gateway.
		// Asking the gateway's question about it would give a confidently wrong
		// answer — the gateway usually can read it and the account is the one that
		// cannot.
		for name, acc := range cfg.Accounts {
			if acc.StateDir == "" {
				continue
			}
			arts = append(arts, Artifact{
				Path:        acc.StateDir,
				Role:        "account state dir",
				Account:     fsown.Account{UID: acc.UID, GID: acc.GID},
				AccountName: name,
			})
		}
	}
	return arts
}

// GatewayOnly drops the artifacts belonging to privsep accounts, keeping those
// the gateway itself opens.
//
// The boot path needs this. A privsep account's state_dir is created and corrected
// by ReconcileAccountFilesystem, which runs LATER in the boot sequence — so a
// state_dir that is briefly wrong at this point is about to be fixed, and aborting
// on it would refuse a boot that would otherwise have succeeded. `config check`
// keeps them, because there the reconcile has not happened and will not happen
// until the next restart, which is exactly what the operator wants told in advance.
func GatewayOnly(arts []Artifact) []Artifact {
	kept := make([]Artifact, 0, len(arts))
	for _, a := range arts {
		if a.AccountName == "gateway" {
			kept = append(kept, a)
		}
	}
	return kept
}

// CheckReadability returns one finding per artifact the responsible account
// cannot open, by MODELLING the Unix permission bits against that account.
//
// Use this only when asking on someone else's behalf — which is `config check`
// under sudo, where the process is root and root opens anything. The model is
// necessarily an approximation: POSIX ACLs, CAP_DAC_READ_SEARCH and
// exec-time supplementary groups all grant access the mode bits deny, so a
// modelled failure can be a false alarm. That is acceptable for an advisory
// operator command and NOT acceptable for a fail-closed boot gate, which is why
// the boot path uses CheckOpenable instead.
func CheckReadability(arts []Artifact) []ReadabilityFinding {
	findings := check(arts, func(a Artifact) (bool, string) {
		return fsown.Diagnose(a.Path, a.Account)
	})
	for i := range findings {
		findings[i].Indeterminate = fsown.HasACL(findings[i].Path)
	}
	return findings
}

// CheckOpenable returns one finding per artifact this process cannot open, by
// actually opening it.
//
// This is the boot-time check, and the reason it is a different function is that
// at boot the process IS the gateway — so an open is ground truth rather than a
// model. It cannot produce a false refusal: if the open fails, the gateway
// genuinely cannot read the file and was going to fail on it later anyway, with a
// worse message. It also catches what no permission model sees — ACLs granting
// access, capabilities, SELinux and AppArmor denials.
func CheckOpenable(arts []Artifact) []ReadabilityFinding {
	return check(arts, func(a Artifact) (bool, string) {
		return fsown.Openable(a.Path)
	})
}

func check(arts []Artifact, probe func(Artifact) (bool, string)) []ReadabilityFinding {
	var findings []ReadabilityFinding
	for _, a := range arts {
		ok, detail := probe(a)
		if ok {
			continue
		}
		findings = append(findings, ReadabilityFinding{
			Path: a.Path, Role: a.Role, Account: a.AccountName, Detail: detail,
		})
	}
	return findings
}

// GatewayAccount resolves the account the gateway runs as, for a check that may
// be running as somebody else.
//
// The config directory's owner is the answer, matching fsown.Desired on the write
// side. Falling back to the current process is right for the ordinary
// single-account development install, where the directory owner and the caller
// are the same person anyway.
func GatewayAccount(configDir string) fsown.Account {
	if acct, ok := fsown.AccountOwning(configDir); ok {
		return acct
	}
	return fsown.CurrentAccount()
}
