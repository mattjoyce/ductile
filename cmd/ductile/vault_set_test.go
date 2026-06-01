package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// vaultSetServer stands up a fake daemon that accepts /vault/secret with a
// fixed admin token, recording the decoded body.
func vaultSetServer(t *testing.T, wantToken string) (*httptest.Server, *map[string]any) {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/vault/secret" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid vault admin token"}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"name":"api_key","status":"set"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestDoVaultSet_Success(t *testing.T) {
	srv, got := vaultSetServer(t, "admin-tok")

	err := doVaultSet(srv.URL, "admin-tok", "api_key", "shh", []string{"mailer"}, "manual")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if (*got)["name"] != "api_key" || (*got)["value"] != "shh" || (*got)["pattern"] != "manual" {
		t.Fatalf("server received unexpected body: %+v", *got)
	}
}

func TestDoVaultSet_Unauthorized(t *testing.T) {
	srv, _ := vaultSetServer(t, "admin-tok")

	err := doVaultSet(srv.URL, "wrong-token", "api_key", "shh", nil, "manual")
	if err == nil {
		t.Fatalf("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 in error, got %v", err)
	}
}

func TestDoVaultSet_RequiresURLAndToken(t *testing.T) {
	if err := doVaultSet("", "tok", "n", "v", nil, "manual"); err == nil {
		t.Error("expected error when api-url is missing")
	}
	if err := doVaultSet("http://x", "", "n", "v", nil, "manual"); err == nil {
		t.Error("expected error when token is missing")
	}
}
