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
)

// TestManagementPostureActivatesOnReload proves #130's activation transition: from
// the vault-operable / ductile-closed posture, the admin token mints the api token
// over the socket; once the config references that token, a reload (modelled here
// as the buildRuntime swap the reload manager performs) brings up the public
// gateway plane through the normal #94 fail-closed seam — and the management socket
// is gone. No new bypass: activation IS the standard boot path succeeding because
// the secret now resolves.
func TestManagementPostureActivatesOnReload(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "plugins"), 0o755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}
	adminToken := genesisVaultForTest(t, tmp)

	sockDir, err := os.MkdirTemp("/tmp", "dtl")
	if err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	socket := filepath.Join(sockDir, "v.sock")
	publicAddr := freeLocalAddr(t)
	configPath := filepath.Join(tmp, "config.yaml")

	base := `
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
	// --- Boot the management posture (no api token configured yet) ---
	if err := os.WriteFile(configPath, []byte(base), 0o644); err != nil {
		t.Fatalf("write bootstrap config: %v", err)
	}
	cfg1, owner1, err := config.LoadWithVault(configPath)
	if err != nil {
		t.Fatalf("LoadWithVault (bootstrap): %v", err)
	}
	rt1, err := buildRuntime(cfg1, configPath, "test", nil, make(chan error, 4), runtimeBuildOptions{vaultOwner: owner1})
	if err != nil {
		t.Fatalf("buildRuntime (management): %v", err)
	}

	sockClient := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}
	waitManagementReady(t, sockClient)

	// --- Admin token mints the api token over the socket ---
	const apiTokenValue = "minted-api-token-value"
	raw, _ := json.Marshal(map[string]any{
		"name": "core-api-token", "value": apiTokenValue,
		"authorized_principals": []string{}, "pattern": "manual",
	})
	req, _ := http.NewRequest(http.MethodPost, "http://unix/vault/secret", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := sockClient.Do(req)
	if err != nil {
		t.Fatalf("mint api token: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mint api token: status %d, want 200", resp.StatusCode)
	}

	// --- Activate: config now references the minted token, then reload ---
	activated := base + `  auth:
    tokens:
      - secret_ref: core-api-token
        scopes: ["*"]
`
	if err := os.WriteFile(configPath, []byte(activated), 0o644); err != nil {
		t.Fatalf("write activated config: %v", err)
	}
	cfg2, owner2, err := config.LoadWithVault(configPath)
	if err != nil {
		t.Fatalf("LoadWithVault (activated) must resolve the minted token: %v", err)
	}

	rt1.Stop() // release the management socket (the reload manager stops the old runtime)

	rt2, err := buildRuntime(cfg2, configPath, "test", nil, make(chan error, 4), runtimeBuildOptions{vaultOwner: owner2})
	if err != nil {
		t.Fatalf("buildRuntime (gateway, post-activation) must succeed: %v", err)
	}
	defer rt2.Stop()

	// --- The public gateway plane now serves, authenticated by the api token ---
	gw := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(3 * time.Second)
	for {
		r, derr := gw.Get("http://" + publicAddr + "/healthz")
		if derr == nil {
			_ = r.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("gateway listener never came up at %s: %v", publicAddr, derr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	authed, _ := http.NewRequest(http.MethodGet, "http://"+publicAddr+"/topology", nil)
	authed.Header.Set("Authorization", "Bearer "+apiTokenValue)
	ra, err := gw.Do(authed)
	if err != nil {
		t.Fatalf("GET /topology with token: %v", err)
	}
	_ = ra.Body.Close()
	if ra.StatusCode != http.StatusOK {
		t.Fatalf("GET /topology with minted token: status %d, want 200", ra.StatusCode)
	}

	rn, err := gw.Get("http://" + publicAddr + "/topology")
	if err != nil {
		t.Fatalf("GET /topology without token: %v", err)
	}
	_ = rn.Body.Close()
	if rn.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /topology without token: status %d, want 401", rn.StatusCode)
	}

	// --- The management socket is gone (ductile-closed posture released) ---
	if c, derr := net.DialTimeout("unix", socket, 200*time.Millisecond); derr == nil {
		_ = c.Close()
		t.Fatal("management socket still serving after activation — it must be torn down")
	}
}
