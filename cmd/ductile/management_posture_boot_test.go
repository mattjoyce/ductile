package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/secrets"
	"github.com/mattjoyce/ductile/internal/vault"
)

// genesisVaultForTest writes age.key + vault.age into configDir via the sanctioned
// genesis path and returns the minted admin token plaintext (the only time it is
// recoverable, exactly like `vault init`). Default resolution finds both files, so
// no config secrets block is needed.
func genesisVaultForTest(t *testing.T, configDir string) string {
	t.Helper()
	id, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatalf("generate age identity: %v", err)
	}
	keyPath := filepath.Join(configDir, "age.key")
	if err := os.WriteFile(keyPath, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatalf("write age key: %v", err)
	}
	kr, err := secrets.LoadKeyringFromFile(keyPath)
	if err != nil {
		t.Fatalf("load keyring: %v", err)
	}
	_, adminToken, err := vault.Init(filepath.Join(configDir, "vault.age"), kr, time.Now())
	if err != nil {
		t.Fatalf("vault init: %v", err)
	}
	return adminToken
}

func freeLocalAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate local addr: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close temp listener: %v", err)
	}
	return addr
}

// TestBuildRuntimeManagementPostureBootstrap is the end-to-end #129 proof: a
// from-scratch vault-native gateway (genesis vault with an admin token but NO api
// token, admission.require_api_auth ON) boots — it does NOT hit the old
// no-api-tokens dead-end — into the vault-operable / ductile-closed posture. The
// admin token mints the api token over the local management socket, and the public
// gateway listener is never opened.
func TestBuildRuntimeManagementPostureBootstrap(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "plugins"), 0o755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}
	adminToken := genesisVaultForTest(t, tmp)

	// Short socket path — sun_path is capped near 104 bytes and t.TempDir is long.
	sockDir, err := os.MkdirTemp("/tmp", "dtl")
	if err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	socket := filepath.Join(sockDir, "v.sock")

	// The public listener address that must STAY CLOSED in management posture.
	publicAddr := freeLocalAddr(t)

	cfgYAML := `
plugin_roots:
  - plugins
service:
  allow_symlinks: true
  unconfined: true
  admission:
    require_api_auth: true
state:
  path: ` + filepath.Join(tmp, "state.db") + `
api:
  enabled: true
  listen: ` + publicAddr + `
  management_socket: ` + socket + `
`
	configPath := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(configPath, []byte(cfgYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	loaded, owner, err := config.LoadWithVault(configPath)
	if err != nil {
		t.Fatalf("LoadWithVault: %v", err)
	}
	if owner == nil {
		t.Fatal("expected a non-nil vault owner from genesis")
	}

	rt, err := buildRuntime(loaded, configPath, "test", nil, make(chan error, 4), runtimeBuildOptions{vaultOwner: owner})
	if err != nil {
		t.Fatalf("buildRuntime must NOT abort in management posture (require_api_auth, zero api tokens, vault present): %v", err)
	}
	defer rt.Stop()

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		},
	}
	waitManagementReady(t, client)

	// The admin token operates /vault/* over the local socket — minting the api
	// token that the gateway posture will later require.
	raw, _ := json.Marshal(map[string]any{
		"name":                  "core-api-token",
		"value":                 "minted-api-token",
		"authorized_principals": []string{},
		"pattern":               "manual",
	})
	req, err := http.NewRequest(http.MethodPost, "http://unix/vault/secret", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("mint api token over socket: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin token mint via /vault/secret: status = %d, want 200", resp.StatusCode)
	}

	// The public gateway listener must NOT be open in management posture.
	if conn, derr := net.DialTimeout("tcp", publicAddr, 300*time.Millisecond); derr == nil {
		_ = conn.Close()
		t.Fatalf("public gateway listener is OPEN at %s in management posture — it must stay closed", publicAddr)
	}
}

func waitManagementReady(t *testing.T, client *http.Client) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := client.Get("http://unix/healthz")
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("management socket never became ready: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
