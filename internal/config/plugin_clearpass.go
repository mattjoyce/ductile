package config

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/zeebo/blake3"
)

// Clearpass is an operator's standing pre-authorisation to accept changed bytes
// for one plugin until a deadline (card #144). It relaxes, for a bounded period,
// the compose-time attestation check that otherwise fails a spawn closed on a
// fingerprint mismatch. It never stands in for an attestation: a plugin with no
// recorded fingerprint cannot be clearpassed, only locked.
//
// The window lives in .checksums beside the fingerprint it relaxes, so `config
// lock` carries it forward verbatim and `plugin lock` (the explicit commit)
// replaces the whole record and thereby clears it. Timestamps are RFC3339 UTC.
//
// A window relaxes a keyed attestation, so it is keyed the same way: Seal is a
// keyed-BLAKE3 MAC under the vault nonce over the plugin name, the recorded
// hashes and the window's own fields. A record that fails Verify is not a
// window at all: the verifier denies and the closer refuses to commit. Opening
// one therefore needs the vault, exactly like `plugin lock`, and a writer who
// can reach .checksums but not the vault cannot open one.
type Clearpass struct {
	// ID is random per window and part of the seal, so two windows with the
	// same fields are distinct and the gateway's audit trail can tell a window
	// it saw opened from a record replayed from a backup.
	ID       string `yaml:"id"`
	OpenedAt string `yaml:"opened_at"`
	Until    string `yaml:"until"`
	Note     string `yaml:"note,omitempty"`
	Seal     string `yaml:"seal"`
}

// MaxClearpass bounds a window. A standing exemption is not a clearpass; an
// operator who needs longer re-opens one deliberately.
const MaxClearpass = 24 * time.Hour

// ErrClearpassDuration marks a requested window that is not positive or exceeds
// MaxClearpass.
var ErrClearpassDuration = errors.New("clearpass duration must be positive and at most 24h")

// NewClearpass opens a window of d from now. The deadline is rounded to the
// second so the record is stable across a YAML round trip.
func NewClearpass(now time.Time, d time.Duration, note string) (Clearpass, error) {
	if d <= 0 || d > MaxClearpass {
		return Clearpass{}, fmt.Errorf("%w: got %s", ErrClearpassDuration, d)
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Clearpass{}, fmt.Errorf("clearpass id: %w", err)
	}
	opened := now.UTC().Truncate(time.Second)
	return Clearpass{
		ID:       hex.EncodeToString(id[:]),
		OpenedAt: opened.Format(time.RFC3339),
		Until:    opened.Add(d).Format(time.RFC3339),
		Note:     note,
	}, nil
}

// Sealed returns a copy of c sealed for fp under the vault nonce. The seal
// binds the window to the attestation it relaxes, so a later `plugin lock`
// (which replaces the record) or any edit to the window invalidates it.
func (c Clearpass) Sealed(fp PluginFingerprint, nonce []byte) (Clearpass, error) {
	mac, err := clearpassMAC(c, fp, nonce)
	if err != nil {
		return Clearpass{}, err
	}
	c.Seal = mac
	return c, nil
}

// Verify reports whether c's seal is the MAC of its fields and fp's identity
// under nonce. It is nil-safe and fails closed on any error.
func (c *Clearpass) Verify(fp PluginFingerprint, nonce []byte) bool {
	if c == nil || c.Seal == "" {
		return false
	}
	want, err := clearpassMAC(*c, fp, nonce)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(c.Seal)) == 1
}

func clearpassMAC(c Clearpass, fp PluginFingerprint, nonce []byte) (string, error) {
	if len(nonce) != 32 {
		return "", fmt.Errorf("clearpass seal: nonce must be 32 bytes, got %d", len(nonce))
	}
	h, err := blake3.NewKeyed(nonce)
	if err != nil {
		return "", fmt.Errorf("clearpass seal: %w", err)
	}
	// NUL-delimited so field boundaries cannot be forged by concatenation.
	for _, field := range []string{"clearpass", fp.Name, fp.ManifestHash, fp.EntrypointHash, c.ID, c.OpenedAt, c.Until, c.Note} {
		_, _ = h.Write([]byte(field))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Deadline parses Until. A malformed deadline is an error, never an open window.
func (c *Clearpass) Deadline() (time.Time, error) {
	if c == nil {
		return time.Time{}, errors.New("no clearpass")
	}
	t, err := time.Parse(time.RFC3339, c.Until)
	if err != nil {
		return time.Time{}, fmt.Errorf("clearpass until %q: %w", c.Until, err)
	}
	return t, nil
}

// Opened parses OpenedAt.
func (c *Clearpass) Opened() (time.Time, error) {
	if c == nil {
		return time.Time{}, errors.New("no clearpass")
	}
	t, err := time.Parse(time.RFC3339, c.OpenedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("clearpass opened_at %q: %w", c.OpenedAt, err)
	}
	return t, nil
}

// Open reports whether the window admits a changed plugin at now. It is nil-safe
// and fails closed: a missing or malformed record is not open.
func (c *Clearpass) Open(now time.Time) bool {
	if c == nil {
		return false
	}
	until, err := c.Deadline()
	if err != nil {
		return false
	}
	return now.Before(until)
}

// Expired reports whether the window has a deadline and it has passed. A missing
// or malformed record is neither open nor expired.
func (c *Clearpass) Expired(now time.Time) bool {
	if c == nil {
		return false
	}
	until, err := c.Deadline()
	if err != nil {
		return false
	}
	return !now.Before(until)
}

// ProbeChecksumsWritable reports whether .checksums in dir can be replaced, by
// creating and removing a temporary file beside it the way the atomic writer
// does. A gateway whose config directory is mounted read-only cannot commit a
// clearpass window itself; the closer checks this before it consumes one.
func ProbeChecksumsWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".checksums-probe-*")
	if err != nil {
		return fmt.Errorf("config directory %s is not writable: %w", dir, err)
	}
	name := f.Name()
	_ = f.Close()
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("config directory %s: remove probe: %w", dir, err)
	}
	return nil
}

// SetPluginClearpass returns a copy of existing with the named plugin's window
// replaced by cp (nil clears it). The second result is false when no fingerprint
// is recorded for name: a clearpass relaxes an attestation and cannot be set
// without one. Every other entry is untouched; the result is sorted by Name.
func SetPluginClearpass(existing []PluginFingerprint, name string, cp *Clearpass) ([]PluginFingerprint, bool) {
	out := make([]PluginFingerprint, 0, len(existing))
	found := false
	for _, e := range existing {
		if e.Name == name {
			e.Clearpass = cp
			found = true
		}
		out = append(out, e)
	}
	sortPluginFingerprintsByName(out)
	return out, found
}
