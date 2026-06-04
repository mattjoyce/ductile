package protocol

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// TestSecretsLogValueRedacts proves that logging a Secrets value via slog emits
// the names + a count but never the values (#27 redaction-by-construction).
func TestSecretsLogValueRedacts(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	s := Secrets{"smtp_pw": "hunter2", "api_key": "sk-leak-me"}

	logger.Info("delivering", slog.Any("secrets", s))
	out := buf.String()

	if strings.Contains(out, "hunter2") || strings.Contains(out, "sk-leak-me") {
		t.Fatalf("LogValue leaked a secret value: %s", out)
	}
	if !strings.Contains(out, "smtp_pw") || !strings.Contains(out, "api_key") {
		t.Errorf("LogValue should keep the names (debuggability): %s", out)
	}
	if !strings.Contains(out, "REDACTED") {
		t.Errorf("expected a REDACTED marker: %s", out)
	}
}

// TestSecretsLogValueEmpty: an empty/nil Secrets renders cleanly.
func TestSecretsLogValueEmpty(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	logger.Info("none", slog.Any("secrets", Secrets(nil)))
	if !strings.Contains(buf.String(), "(none)") {
		t.Errorf("empty Secrets should render (none): %s", buf.String())
	}
}

// TestSecretsJSONStillRealValues proves the redaction does NOT touch JSON
// marshaling — the stdin delivery must still carry real values.
func TestSecretsJSONStillRealValues(t *testing.T) {
	s := Secrets{"k": "real-value"}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), "real-value") {
		t.Fatalf("JSON delivery must keep real values, got: %s", b)
	}
}
