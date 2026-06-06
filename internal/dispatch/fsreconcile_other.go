//go:build !(darwin || linux || freebsd || openbsd || netbsd)

package dispatch

import (
	"fmt"

	"github.com/mattjoyce/ductile/internal/config"
)

// reconcileAccountFilesystem fails closed on a platform with no Unix ownership
// model. It is only ever called when the boot gate decided to enforce, which the
// gate refuses on non-Unix hosts — so reaching here is already a contradiction.
func reconcileAccountFilesystem(cfg *config.Config, secretPaths []string, euid int) error {
	if len(cfg.Accounts) > 0 {
		return fmt.Errorf("privsep: filesystem reconciliation unsupported on this platform")
	}
	return nil
}
