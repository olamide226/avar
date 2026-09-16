package browser

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// openPath is macOS's own open(1), named absolutely so that nothing earlier on
// PATH can stand in for it.
const openPath = "/usr/bin/open"

// openURL hands address to LaunchServices, which opens it in the default
// browser. open(1) returns as soon as the request is delivered.
func openURL(ctx context.Context, address string) error {
	out, err := exec.CommandContext(ctx, openPath, "-u", address).CombinedOutput()
	if err != nil {
		if detail := strings.TrimSpace(string(out)); detail != "" {
			return fmt.Errorf("%w: %s", err, detail)
		}
		return err
	}
	return nil
}
