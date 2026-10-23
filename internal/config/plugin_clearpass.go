package config

import (
	"errors"
	"fmt"
	"time"
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
type Clearpass struct {
	OpenedAt string `yaml:"opened_at"`
	Until    string `yaml:"until"`
	Note     string `yaml:"note,omitempty"`
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
	opened := now.UTC().Truncate(time.Second)
	return Clearpass{
		OpenedAt: opened.Format(time.RFC3339),
		Until:    opened.Add(d).Format(time.RFC3339),
		Note:     note,
	}, nil
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
