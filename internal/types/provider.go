package types

import (
	"fmt"
	"regexp"
	"strings"
)

// ProviderID names the backend that runs Linux for avar.
//
// It is not a user-visible choice. The host decides which backend avar uses —
// macOS is served by Lima, Windows by WSL 2 (design §1, REQ-18.1) — so the id
// is something avar records rather than something the user selects. It is part
// of a machine's identity all the same: a machine created by one backend means
// nothing to another, and a record that does not say which backend made it
// cannot be acted on safely by a host running the other one.
type ProviderID string

// ProviderLima is the macOS backend, and the only one that exists today. A
// second backend adds its own constant next to this one; nothing else in the
// shared vocabulary has to change (REQ-17.3, REQ-18.14).
const ProviderLima ProviderID = "lima"

// providerIDPattern constrains an id to a short, lower-case, separator-free
// token: it is written into avar's state files and read back by later versions,
// so it has to be stable and unambiguous.
var providerIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]{1,15}$`)

// ValidateProviderID rejects an id avar cannot record.
//
// The rule is a format, not a closed list of known backends, and that is
// deliberate. A closed list here would mean every backend — including the test
// double every integration test drives — had to be enumerated in the package
// that is supposed to hold vocabulary rather than implementations, and it would
// make a state file written by a newer avar unreadable by an older one merely
// because it names a backend the older one has not heard of. Provider ids come
// from constants in avar's own code and never from user input, so the risk a
// closed list would guard against is a typo in avar's source, which a format
// check catches just as well.
func ValidateProviderID(id ProviderID) error {
	if strings.TrimSpace(string(id)) == "" {
		return fmt.Errorf("no provider was named")
	}
	if !providerIDPattern.MatchString(string(id)) {
		return fmt.Errorf("%q is not a valid provider id: it must be a short lower-case name such as %q", id, ProviderLima)
	}
	return nil
}
