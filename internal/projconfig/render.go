package projconfig

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// header opens every file avr init writes, so that a teammate who finds the
// file knows what it is and that it asks for nothing on its own.
const header = `# .avr.toml: the Linux environment this project wants, for avr.
# Every key is optional. avr asks before installing packages or forwarding
# variables, and applies cpus and memory only to the project's own environment.
`

// Render writes c as a .avr.toml. Keys appear in the schema's order and only
// when set.
//
// The output is read back before it is returned, and a Config the reader
// would not return unchanged is refused. That is what guarantees avr init
// never writes a file that fails, or means something else, the next time avr
// runs: a value holding a quote or a backslash cannot be written as a string
// this reader accepts, and is caught here rather than in the user's project.
func Render(c Config) ([]byte, error) {
	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n")

	if c.Distro != "" {
		distro := string(c.Distro)
		if c.Version != "" {
			distro += ":" + c.Version
		}
		fmt.Fprintf(&b, "distro = %s\n", quote(distro))
	}
	if c.Arch != "" {
		fmt.Fprintf(&b, "arch = %s\n", quote(string(c.Arch)))
	}
	if c.CPUs > 0 {
		fmt.Fprintf(&b, "cpus = %s\n", strconv.Itoa(c.CPUs))
	}
	if c.MemoryMiB > 0 {
		fmt.Fprintf(&b, "memory = %s\n", quote(formatMemory(c.MemoryMiB)))
	}
	if len(c.Packages) > 0 {
		fmt.Fprintf(&b, "packages = %s\n", list(c.Packages))
	}
	if len(c.ForwardEnv) > 0 {
		fmt.Fprintf(&b, "forward_env = %s\n", list(c.ForwardEnv))
	}

	body := []byte(b.String())
	back, err := Parse(c.Path, body)
	if err != nil {
		return nil, fmt.Errorf("write %s: the file would not read back: %w", FileName, err)
	}
	want := c
	if len(want.Packages) == 0 {
		want.Packages = nil
	}
	if len(want.ForwardEnv) == 0 {
		want.ForwardEnv = nil
	}
	if !reflect.DeepEqual(back, want) {
		return nil, fmt.Errorf("write %s: the file would read back as %+v rather than %+v", FileName, back, c)
	}
	return body, nil
}

func quote(s string) string { return `"` + s + `"` }

func list(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = quote(name)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
