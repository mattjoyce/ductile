//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd

package dispatch

import "errors"

// errProbeUnsupported is returned by the non-POSIX OSLookup so that the audit treats
// such a platform as INCONCLUSIVE (loudly), never as a silent clean pass.
var errProbeUnsupported = errors.New("side-door probes unsupported on this platform")

// newOSLookup returns a probe that reports INCONCLUSIVE on platforms without the
// POSIX user/sudo model. It deliberately does NOT report "clean": claiming
// containment was verified on a host the audit cannot inspect is a silent fail-open.
// Every probe returns errProbeUnsupported, so the account is surfaced as inconclusive
// (and, for a confined account under strict mode, fails the boot closed).
func newOSLookup() osLookup { return noopOSLookup{} }

type noopOSLookup struct{}

func (noopOSLookup) UsernameForUID(int) (string, bool)        { return "", false }
func (noopOSLookup) GroupNamesForUID(int) ([]string, error)   { return nil, errProbeUnsupported }
func (noopOSLookup) SudoNoPasswd(string) (bool, error)        { return false, errProbeUnsupported }
func (noopOSLookup) WritablePathDirs(int) ([]string, error)   { return nil, errProbeUnsupported }
func (noopOSLookup) WritableSetuidRoot(int) ([]string, error) { return nil, errProbeUnsupported }
