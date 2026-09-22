package update

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a release version: exactly MAJOR.MINOR.PATCH, optionally written
// with a leading "v".
//
// Nothing looser is accepted, and that is the point. The version avar was
// built with is the only evidence it has that this binary came from a release
// somebody published: `dev` is a plain `go build`, and `v0.12.12-3-gabc` is
// `git describe` over commits that are not in any release. Replacing either
// with a release archive would swap a build somebody is working on for a
// different program (REQ-19.9).
type Version struct {
	Major, Minor, Patch int
}

// String renders the version without a leading "v", which is how the release
// archives spell it.
func (v Version) String() string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

// Before reports whether v is older than other.
func (v Version) Before(other Version) bool {
	switch {
	case v.Major != other.Major:
		return v.Major < other.Major
	case v.Minor != other.Minor:
		return v.Minor < other.Minor
	default:
		return v.Patch < other.Patch
	}
}

// ParseVersion reads a release version, and refuses anything that is not
// exactly one.
func ParseVersion(s string) (Version, error) {
	notAVersion := fmt.Errorf("%q is not a release version: a release version is MAJOR.MINOR.PATCH, such as 1.4.0", s)

	trimmed := strings.TrimPrefix(strings.TrimSpace(s), "v")
	parts := strings.Split(trimmed, ".")
	if len(parts) != 3 {
		return Version{}, notAVersion
	}
	var v Version
	for i, dest := range []*int{&v.Major, &v.Minor, &v.Patch} {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 {
			return Version{}, notAVersion
		}
		*dest = n
	}
	return v, nil
}
