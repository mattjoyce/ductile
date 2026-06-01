package vault

import (
	"fmt"
	"os"
	"time"

	"github.com/mattjoyce/ductile/internal/secrets"
)

const (
	// CorePrincipal is the reserved gateway principal seeded at genesis. It holds
	// the fingerprint nonce.
	CorePrincipal = "core"
	// AdminTokenSecret is the vault entry holding the initial management-API admin
	// token. It has no authorized_principals: it is verified by the API (#8), not
	// delivered to any principal via Compose.
	AdminTokenSecret = "core-admin-token"

	nonceBytes      = 32
	adminTokenBytes = 32
)

// Init creates a brand-new vault at path: it seeds the reserved `core` principal
// with a freshly generated fingerprint nonce, mints an initial admin token
// (stored as an undelivered secret), and persists the first encrypted blob. It
// returns the live Vault and the plaintext admin token, which the caller must
// surface once — it is not recoverable in plaintext afterward without the key.
//
// Genesis is composition of the existing primitives (RegisterPrincipal +
// SetSecret + Save), not new mutation logic.
//
// Fail-closed (Armstrong): Init refuses to run if path already exists, so a
// re-init cannot silently clobber a live vault and wipe its secrets. `now` is
// injected to keep timestamps deterministic in tests.
func Init(path string, kr *secrets.Keyring, now time.Time) (*Vault, string, error) {
	switch _, err := os.Stat(path); {
	case err == nil:
		return nil, "", fmt.Errorf("vault: refusing to init — %q already exists", path)
	case !os.IsNotExist(err):
		return nil, "", fmt.Errorf("vault: stat %q: %w", path, err)
	}

	nonce, err := secrets.GenerateToken(nonceBytes)
	if err != nil {
		return nil, "", fmt.Errorf("vault: genesis nonce: %w", err)
	}
	adminToken, err := secrets.GenerateToken(adminTokenBytes)
	if err != nil {
		return nil, "", fmt.Errorf("vault: genesis admin token: %w", err)
	}

	store := NewStore()
	if err := store.RegisterPrincipal(CorePrincipal, KindGateway); err != nil {
		return nil, "", fmt.Errorf("vault: genesis core principal: %w", err)
	}
	store.Principals[CorePrincipal].Nonce = nonce
	// No authorized_principals: the admin token is API-internal, never composed
	// to a principal.
	if err := store.SetSecret(AdminTokenSecret, adminToken, nil, PatternAuto, now); err != nil {
		return nil, "", fmt.Errorf("vault: genesis admin token entry: %w", err)
	}

	v := New(path, kr, store)
	if err := v.Save(); err != nil {
		return nil, "", err
	}
	return v, adminToken, nil
}
