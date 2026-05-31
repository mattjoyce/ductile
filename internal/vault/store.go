// Package vault is ductile's owned secret store: a single whole-store age blob
// at rest, held as an in-memory model at runtime. See ADR "Ductile — Vault".
//
// This file is the *data* half (Hickey: data vs. process). Store/Secret/
// Principal are plain values — YAML-serialisable, no I/O, no knowledge that they
// are encrypted or on disk. The *process* half (load, decrypt, persist, atomic
// write) lives in vault.go and is the only thing that touches the keyring or the
// filesystem. Operations on the model (register/set/compose) arrive in later
// rungs and act on these values.
package vault

// Status / pattern / kind enumerations. Kept as strings for transparent YAML;
// the constants give later code a single source for the legal values.
const (
	StatusActive  = "active"
	StatusRevoked = "revoked"

	PatternAuto   = "auto"   // value minted by the vault (CSPRNG)
	PatternManual = "manual" // value supplied by the operator

	KindPlugin   = "plugin"
	KindConsumer = "consumer"
	KindGateway  = "gateway"
)

// Secret is one stored secret. Value is plaintext only in memory; it is never
// written un-encrypted (the whole Store is age-encrypted as one blob).
type Secret struct {
	Value                string   `yaml:"value"`
	AuthorizedPrincipals []string `yaml:"authorized_principals"`
	Status               string   `yaml:"status"`               // active | revoked
	Pattern              string   `yaml:"pattern"`              // auto | manual
	RollCount            int      `yaml:"roll_count"`           // supersession counter (audit only)
	CreatedAt            string   `yaml:"created_at,omitempty"` // RFC3339
	UpdatedAt            string   `yaml:"updated_at,omitempty"` // RFC3339
	RevokedAt            string   `yaml:"revoked_at,omitempty"` // RFC3339
	Description          string   `yaml:"description,omitempty"`
}

// Principal is a registered deliver-to identity (a plugin, a consumer, or the
// gateway). Fingerprint binding is added with the registry rung.
type Principal struct {
	Kind   string `yaml:"kind"`   // plugin | consumer | gateway
	Status string `yaml:"status"` // active | revoked
}

// Store is the whole vault document: the unit that is serialised to YAML and
// age-encrypted as one blob. It is a value — copy it, compare it, marshal it.
type Store struct {
	Secrets    map[string]*Secret    `yaml:"secrets"`
	Principals map[string]*Principal `yaml:"principals"`
}

// NewStore returns an empty, initialised store (maps non-nil so callers and YAML
// round-trips never hit a nil map).
func NewStore() *Store {
	return &Store{
		Secrets:    make(map[string]*Secret),
		Principals: make(map[string]*Principal),
	}
}
