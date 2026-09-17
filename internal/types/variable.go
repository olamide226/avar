package types

import (
	"fmt"
	"regexp"
)

// variableName is a portable environment variable name.
var variableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// CheckVariableName refuses a string that is not a portable environment
// variable name. Both configuration files that name host variables to forward
// into a guest, a project's .avr.toml and the user's config.toml, hold their
// names to it, so a name one accepts the other does too.
func CheckVariableName(name string) error {
	if !variableName.MatchString(name) {
		return fmt.Errorf("%q is not a variable name: names are letters, digits and _, and do not start with a digit", name)
	}
	return nil
}
