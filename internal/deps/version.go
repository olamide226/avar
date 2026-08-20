package deps

import (
	"cmp"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// MinLimaVersion is the oldest Lima release avar supports. It is the single
// point of change for the version gate: nothing else in avar hard-codes a Lima
// version.
//
// The floor is 2.0.0 because 2.x is the only surface avar has evidence for.
// avar's generated configurations are checked with `limactl validate` inside
// the test suite, and that evidence comes from Lima 2.x; the guest account
// behaviour REQ-1.4 depends on is asserted the same way. Lima 1.x appears to
// carry every field and flag avar uses, but "appears to, from reading its
// source" is not the same standard, and a floor that admits a version nothing
// is ever tested against is not a gate at all — it only defers the failure to a
// user's machine.
//
// Templates emit this value as `minimumLimaVersion`, so raising the constant
// also stops an older Lima from accepting a configuration written for a newer
// one. Lowering it again is fine once 1.x is genuinely exercised.
const MinLimaVersion = "2.0.0"

// minLima is MinLimaVersion parsed once. A malformed constant is a programming
// error caught by TestMinLimaVersion_Parses before it can ship.
var minLima = mustParseVersion(MinLimaVersion)

// MinLima returns the minimum supported Lima version.
func MinLima() Version { return minLima }

// Version is a semantic version as reported by a tool's version output.
// Build metadata is retained so avar can echo back exactly what it found, but
// it takes no part in precedence.
type Version struct {
	Major int
	Minor int
	Patch int
	// Pre is the pre-release identifier set without its leading '-',
	// e.g. "beta.1" from "1.1.0-beta.1".
	Pre string
	// Build is the build metadata without its leading '+', e.g. "dirty".
	Build string
}

// versionToken matches a single semantic-version-shaped token, tolerating a
// leading "v", a missing patch component, a fourth numeric component, a
// pre-release suffix, and build metadata.
//
// The fourth component is matched and discarded rather than rejected, because
// Windows numbers its components with four: `wsl --version` reports "WSL
// version: 2.7.12.0". Refusing that token would make the parser skip past the
// version it was looking for and find the Windows build number further down the
// same output, which is a worse answer than ignoring a trailing zero. Nothing
// avar gates on has ever distinguished two releases by their fourth component.
var versionToken = regexp.MustCompile(`^[vV]?([0-9]+)\.([0-9]+)(?:\.([0-9]+))?(?:\.[0-9]+)?(?:-([0-9A-Za-z.-]+))?(?:\+([0-9A-Za-z.-]+))?$`)

// ParseVersion extracts a version from a tool's version output.
//
// It is deliberately tolerant: `limactl --version` prints "limactl version
// 1.0.4", but the wording, a leading "v", pre-release and build suffixes, and
// trailing whitespace or punctuation are all things a future release could
// change without avar caring. The first version-shaped token wins.
func ParseVersion(output string) (Version, error) {
	for _, field := range strings.Fields(output) {
		field = strings.Trim(field, `,;:()[]"'`)
		m := versionToken.FindStringSubmatch(field)
		if m == nil {
			continue
		}
		// The numeric groups are \d+ matches, so they only fail to convert
		// when they overflow an int — treat that as "not a version".
		major, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		minor, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		patch := 0
		if m[3] != "" {
			if patch, err = strconv.Atoi(m[3]); err != nil {
				continue
			}
		}
		return Version{Major: major, Minor: minor, Patch: patch, Pre: m[4], Build: m[5]}, nil
	}
	return Version{}, fmt.Errorf("no version number found in %q", truncate(output, 120))
}

func mustParseVersion(s string) Version {
	v, err := ParseVersion(s)
	if err != nil {
		panic("deps: unparseable version constant " + s + ": " + err.Error())
	}
	return v
}

// String renders the version as the tool would have printed it.
func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	if v.Build != "" {
		s += "+" + v.Build
	}
	return s
}

// Compare orders two versions by semantic-version precedence, returning -1, 0
// or 1. Components are compared numerically, so 1.10.0 is newer than 1.9.0.
// Build metadata is ignored; a pre-release precedes its own release.
func (v Version) Compare(o Version) int {
	if c := cmp.Compare(v.Major, o.Major); c != 0 {
		return c
	}
	if c := cmp.Compare(v.Minor, o.Minor); c != 0 {
		return c
	}
	if c := cmp.Compare(v.Patch, o.Patch); c != 0 {
		return c
	}
	return comparePre(v.Pre, o.Pre)
}

// AtLeast reports whether v is o or newer.
func (v Version) AtLeast(o Version) bool { return v.Compare(o) >= 0 }

// comparePre orders two pre-release identifier sets. An absent set outranks any
// present one, because 1.0.0 is newer than 1.0.0-beta.1.
func comparePre(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}

	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if c := comparePreIdent(as[i], bs[i]); c != 0 {
			return c
		}
	}
	// All shared identifiers are equal: the shorter set has lower precedence.
	return cmp.Compare(len(as), len(bs))
}

// comparePreIdent orders one pair of pre-release identifiers: numeric ones
// compare numerically and rank below alphanumeric ones.
func comparePreIdent(a, b string) int {
	an, aErr := strconv.Atoi(a)
	bn, bErr := strconv.Atoi(b)
	switch {
	case aErr == nil && bErr == nil:
		return cmp.Compare(an, bn)
	case aErr == nil:
		return -1
	case bErr == nil:
		return 1
	default:
		return cmp.Compare(a, b)
	}
}

// truncate shortens s for inclusion in an error message, so unexpected tool
// output cannot flood the terminal.
func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
