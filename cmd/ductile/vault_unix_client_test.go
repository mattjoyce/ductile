package main

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// TestVaultAPIPostOverUnixSocket proves the CLI can reach the management posture's
// /vault/* surface over a unix:// api-url — the operator path for minting the api
// token before the gateway is up (#129).
func TestVaultAPIPostOverUnixSocket(t *testing.T) {
	sockDir, err := os.MkdirTemp("/tmp", "dtl")
	if err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	socket := filepath.Join(sockDir, "v.sock")

	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer func() { _ = ln.Close() }()

	var gotAuth, gotPath string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	_, err = vaultAPIPost("unix://"+socket, "admin-secret", "/vault/secret", map[string]any{"name": "core-api-token"})
	if err != nil {
		t.Fatalf("vaultAPIPost over unix socket: %v", err)
	}
	if gotAuth != "Bearer admin-secret" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer admin-secret")
	}
	if gotPath != "/vault/secret" {
		t.Errorf("request path = %q, want /vault/secret", gotPath)
	}
}

// TestVaultHTTPClientRejectsEmptyUnixPath: a unix:// with no path is a clear error,
// not a confusing dial failure.
func TestVaultHTTPClientRejectsEmptyUnixPath(t *testing.T) {
	if _, _, err := vaultHTTPClient("unix://"); err == nil {
		t.Fatal("expected an error for unix:// with no socket path")
	}
}

// TestVaultHTTPClientTCPUnchanged: a plain http base URL still routes through the
// default client with the trailing slash trimmed.
func TestVaultHTTPClientTCPUnchanged(t *testing.T) {
	client, base, err := vaultHTTPClient("http://127.0.0.1:8080/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client != http.DefaultClient {
		t.Error("TCP api-url should use http.DefaultClient")
	}
	if base != "http://127.0.0.1:8080" {
		t.Errorf("base = %q, want http://127.0.0.1:8080", base)
	}
}
