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

	err := doVaultSet(srv.URL, "admin-tok", "api_key", "shh", &[]string{"mailer"}, "manual")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if (*got)["name"] != "api_key" || (*got)["value"] != "shh" || (*got)["pattern"] != "manual" {
		t.Fatalf("server received unexpected body: %+v", *got)
	}
	if _, ok := (*got)["authorized_principals"]; !ok {
		t.Fatalf("authorized_principals should be sent when --principal given: %+v", *got)
	}
}

// TestDoVaultSet_OmitsGrantsAndPatternWhenAbsent proves the CLI sends neither
// authorized_principals (nil → leave grants) nor pattern (\"\" → leave) when not
// supplied — the partial-update wire contract (#23).
func TestDoVaultSet_OmitsGrantsAndPatternWhenAbsent(t *testing.T) {
	srv, got := vaultSetServer(t, "admin-tok")
	if err := doVaultSet(srv.URL, "admin-tok", "api_key", "shh", nil, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := (*got)["authorized_principals"]; ok {
		t.Fatalf("authorized_principals must be omitted when nil (leave grants): %+v", *got)
	}
	if _, ok := (*got)["pattern"]; ok {
		t.Fatalf("pattern must be omitted when empty (leave/default): %+v", *got)
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

// TestVaultAPIPost_ForwardsPathAndBody pins that the shared poster hits the
// exact endpoint path and forwards the JSON body, returning the response.
func TestVaultAPIPost_ForwardsPathAndBody(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"name":"p","rolled":["a"],"skipped":["m"]}`))
	}))
	t.Cleanup(srv.Close)

	resp, err := vaultAPIPost(srv.URL, "tok", "/vault/principal/roll", map[string]any{"name": "p"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/vault/principal/roll" {
		t.Fatalf("expected path /vault/principal/roll, got %q", gotPath)
	}
	if gotBody["name"] != "p" {
		t.Fatalf("expected forwarded body name=p, got %+v", gotBody)
	}
	if !strings.Contains(string(resp), `"rolled"`) {
		t.Fatalf("expected response body returned to caller, got %s", resp)
	}
}

func TestVaultAPIPost_NonOKIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"unknown secret"}`))
	}))
	t.Cleanup(srv.Close)

	if _, err := vaultAPIPost(srv.URL, "tok", "/vault/secret/revoke", map[string]any{"name": "ghost"}); err == nil {
		t.Fatal("expected error on 400 response")
	}
}
