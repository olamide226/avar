package lima

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// The canonical form of a set of shared project directories, and the host-side
// checks that decide whether sharing them can work at all.
//
// Both the configuration generated for a new machine and the mount list applied
// to an existing one go through here, so "the same set of directories" means the
// same thing in both places — which is what makes SetMounts' no-change case
// decidable, and with it the promise that returning to a known project is
// instant (REQ-17.1).

// normalizeMounts turns a caller's desired set into the canonical form avar
// compares and writes: absolute, cleaned, deduplicated and sorted.
//
// A relative path is refused rather than resolved against avar's own working
// directory, because "the directory to share" is the caller's decision and
// guessing at it could share the wrong tree.
func normalizeMounts(dirs []string) ([]string, error) {
	out := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		if dir == "" {
			return nil, fmt.Errorf("an empty path cannot be shared with a Linux environment")
		}
		if !filepath.IsAbs(dir) {
			return nil, fmt.Errorf("%q is not an absolute path: avar shares a project at the identical absolute path in the guest, so the path has to be absolute", dir)
		}
		out = append(out, filepath.Clean(dir))
	}
	return sortedUnique(out), nil
}

// checkHostDirs is the pre-flight that stops avar from configuring a share for
// something that is not a directory the user can reach.
//
// It runs before anything is provisioned or restarted, so that a mistyped or
// deleted project directory costs an immediate error rather than a ten-second
// machine restart that ends in one (REQ-6.5, design §6).
func checkHostDirs(dirs []string) error {
	for _, dir := range dirs {
		info, err := os.Stat(dir)
		if err != nil {
			return fmt.Errorf("checking the project directory %s before sharing it: %w", dir, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("checking the project directory %s before sharing it: it is not a directory", dir)
		}
	}
	return nil
}

// sortedUnique returns the input sorted with duplicates removed, and nil for an
// empty input so that comparisons do not have to distinguish nil from empty.
func sortedUnique(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// equalStrings reports whether two ordered sets are the same.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
