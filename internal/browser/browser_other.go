//go:build !darwin && !windows

package browser

import (
	"context"
	"errors"
)

// openURL has no mechanism on a host avar does not support (REQ-17.6). The
// package still compiles there so that avar's host-neutral code and tests do.
func openURL(context.Context, string) error {
	return errors.New("avar does not know how to open a browser on this host")
}
