package state

import (
	"fmt"
	"strings"
	"time"

	"github.com/olamide226/avar/internal/tomlsubset"
	"github.com/olamide226/avar/internal/types"
)

// Config is what the user's global config.toml asks for. The zero Config is a
// State_Dir with no config.toml, and means avar's defaults.
//
//	idle_timeout = "2h"                # how long an environment may sit unused before it is stopped
//	forward_env  = ["AWS_PROFILE"]     # host variables forwarded into every session
type Config struct {
	// Path is the file the configuration was read from. Empty when there is
	// no file.
	Path string

	// IdleTimeout is how long an environment may go without a session before
	// avar stops it, and IdleTimeoutSet reports whether the file sets it at
	// all. A set IdleTimeout of zero means never stop anything automatically.
	IdleTimeout    time.Duration
	IdleTimeoutSet bool

	// ForwardEnv names host variables the user grants to every guest session,
	// in the order the file lists them (REQ-12.4).
	ForwardEnv []string
}

// configSchema is the whole of what config.toml may contain.
//
// It is closed for the reason .avr.toml's is: a misspelt key quietly ignored
// leaves the user believing a setting applied that never did. Keys that belong
// in a project's .avr.toml are refused with a message saying so, rather than as
// unknown, because writing one here is a reasonable guess that avar does not
// support yet, not a typo.
var configSchema = tomlsubset.Schema{
	File: configFile,
	Keys: []tomlsubset.Key{
		{Name: "idle_timeout", Kind: tomlsubset.String},
		{Name: "forward_env", Kind: tomlsubset.StringList},
	},
	Unsupported: map[string]string{
		"distro":   `distro is not supported in config.toml yet: avar has no global default distribution. Remove this line, and choose one for a project in its .avr.toml, as in distro = "fedora", or for one command with avr --distro fedora`,
		"arch":     `arch is not supported in config.toml yet: avar has no global default architecture. Remove this line, and choose one for a project in its .avr.toml, as in arch = "amd64", or for one command with avr --arch amd64`,
		"cpus":     `cpus is not supported in config.toml yet: avar has no global default size. Remove this line; a project can size its own isolated environment in its .avr.toml, as in cpus = 4`,
		"memory":   `memory is not supported in config.toml yet: avar has no global default size. Remove this line; a project can size its own isolated environment in its .avr.toml, as in memory = "8GiB"`,
		"packages": `packages is not supported in config.toml: package names belong to one distribution, so they are listed per project. Remove this line, and list them in a project's .avr.toml next to its distro, as in packages = ["jq"]`,
	},
}

// Config reads config.toml from the State_Dir. A State_Dir without one yields
// the zero Config and no error. Anything the strict reader cannot read exactly
// is an error naming the file, the line, the key and what to write instead:
// config.toml is only ever edited by hand, and the setting it fails to apply is
// one the user believes is in force.
func (s *Store) Config() (Config, error) {
	path := s.ConfigPath()
	body, found, err := tomlsubset.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read avar's configuration %s: %w", path, err)
	}
	if !found {
		return Config{}, nil
	}
	return ParseConfig(path, body)
}

// ParseConfig reads the contents of a config.toml. path is used only in
// messages and recorded as Config.Path.
func ParseConfig(path string, body []byte) (Config, error) {
	cfg := Config{Path: path}
	err := tomlsubset.Parse(path, body, configSchema, func(s tomlsubset.Setting) error {
		switch s.Key {
		case "idle_timeout":
			return applyIdleTimeout(&cfg, s.Value)
		case "forward_env":
			return applyForwardEnv(&cfg, s.Value)
		}
		return fmt.Errorf("internal error: config.toml key %q has no meaning", s.Key)
	})
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// idleTimeoutFix is what to write, for every idle_timeout avar cannot read.
const idleTimeoutFix = `write a quoted duration with a unit, such as idle_timeout = "30m" or "2h", or idle_timeout = "0" to never stop environments automatically`

// applyIdleTimeout reads idle_timeout = "2h", a Go duration.
//
// Two forms are accepted beyond a positive duration because files written for
// earlier versions of avar used them and meant exactly this: the bare integer
// 0, and a negative duration, both of which turn auto-stop off.
func applyIdleTimeout(c *Config, v tomlsubset.Value) error {
	switch {
	case v.Kind == tomlsubset.Integer && v.Int == 0:
		c.IdleTimeout, c.IdleTimeoutSet = 0, true
		return nil
	case v.Kind == tomlsubset.Integer:
		return fmt.Errorf("%d has no unit, so avar cannot tell minutes from hours: write it quoted with one, as in idle_timeout = \"%dh\" or \"%dm\"", v.Int, v.Int, v.Int)
	}
	if err := v.Expect(tomlsubset.String, "a quoted duration, such as idle_timeout = \"2h\""); err != nil {
		return err
	}

	d, err := time.ParseDuration(strings.TrimSpace(v.Str))
	if err != nil {
		return fmt.Errorf("%q is not a duration: %s", v.Str, idleTimeoutFix)
	}
	c.IdleTimeout, c.IdleTimeoutSet = max(d, 0), true
	return nil
}

// applyForwardEnv reads forward_env = ["NAME", ...].
//
// A name listed twice is kept once rather than refused, as .avr.toml refuses
// it: this list has only ever been read as a set, so a repeat changes nothing
// the user meant.
func applyForwardEnv(c *Config, v tomlsubset.Value) error {
	if err := v.Expect(tomlsubset.StringList, `a list of quoted variable names, such as forward_env = ["AWS_PROFILE", "GITHUB_TOKEN"]`); err != nil {
		return err
	}
	seen := make(map[string]bool, len(v.Strs))
	var names []string
	for _, name := range v.Strs {
		if err := types.CheckVariableName(name); err != nil {
			return fmt.Errorf("%w; list each variable as its own quoted name, as in forward_env = [\"AWS_PROFILE\", \"GITHUB_TOKEN\"]", err)
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	c.ForwardEnv = names
	return nil
}
