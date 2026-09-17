---
title: .avr.toml
parent: Syntax
nav_order: 2
---

# .avr.toml
{: .no_toc }

A project can commit a `.avr.toml` so that everyone who runs `avr` in it gets
the same environment. The file is optional. A project without one behaves
exactly as if the feature did not exist.

1. TOC
{:toc}

## Example

<!-- example:valid — parsed by avar's own reader in a test; it must be accepted -->
```toml
# .avr.toml
distro = "fedora"                 # ubuntu, debian or fedora, optionally "fedora:43"
arch = "amd64"                    # arm64 or amd64
cpus = 4                          # the project's own environment only, at creation
memory = "8GiB"                   # likewise
packages = ["ripgrep", "jq"]      # Fedora's package names, because distro is fedora
forward_env = ["GITHUB_TOKEN"]    # host variables to forward, once you approve
```

Every key is optional, and an empty file is valid. You do not have to write
the file by hand: [`avr init`]({% link commands/init.md %}) proposes one from
the project's manifests and writes it only if you confirm.

## Keys

<!-- keys:begin — every key internal/projconfig accepts must appear in this table, in this order; a test checks it -->

| Key | Type | Allowed values | Default when absent | Needs approval |
| --- | --- | --- | --- | --- |
| `distro` | string | A distribution in the [environment matrix]({% link environments.md %}), optionally with a release after a colon: `"ubuntu"`, `"debian:13"` | The flag if given, otherwise avar's default distribution | No |
| `arch` | string | `"arm64"` or `"amd64"` | The flag if given, otherwise the host's architecture | No |
| `cpus` | integer | A whole number, at least `1` | The backend's default size | No |
| `memory` | string | A whole number followed by `GiB` or `MiB`: `"8GiB"`, `"512MiB"` | The backend's default size | No |
| `packages` | list of strings | Package names in the named distribution's own repositories. Requires `distro` | No packages | Yes |
| `forward_env` | list of strings | Environment variable names | Nothing forwarded | Yes |

<!-- keys:end -->

<!-- pending: rejection of oversized cpus and memory values is in review; document the upper limits here once it merges -->

### distro and arch

These mean exactly what `--distro` and `--arch` mean, and they apply without
asking. A flag on the command line still wins. Every command that picks "this
environment" uses them, so `avr stop` stops the environment `avr` started.

A value outside the environment matrix stops the command with exit status 2,
as the flag would, and the message names the file as the source.

### cpus and memory

These size the project's **own** environment (`avr --isolate`), and only when
that environment is created. They never resize or restart an environment.

When a size cannot apply, avar says so once, with the next step:

| Situation | What avar tells you |
| --- | --- |
| The project uses the shared environment | Run `avr --isolate` to give it its own |
| The isolated environment already exists at another size | Run `avr reset` to recreate it at the declared size |
| The host cannot size environments one at a time | It cannot apply on this host. On Windows every WSL distribution shares one allocation |

`memory` accepts only the binary units `GiB` and `MiB`. `"8GB"` is refused
because it could mean two different sizes.

### packages

Package names belong to a distribution: `golang-go` is Debian's name and
`golang` is Fedora's. So `packages` requires `distro`, and when a different
distribution resolves, for example `avr --distro debian` in a project whose
file says `distro = "fedora"`, avar installs none of them and says so.

A name must start with a letter or digit and contain only letters, digits and
`+ . _ -`. That rules out anything a package manager could read as an option,
a file path, a URL, a version pin or a pattern. A name may appear only once.

Approved packages are installed once per environment, as root, with:

| Distribution | Commands |
| --- | --- |
| Ubuntu, Debian | `apt-get update`, then `apt-get install -y <names>` |
| Fedora | `dnf install -y <names>` |

The package manager's output goes to stderr, so `avr <command> | consumer`
still receives only the command's output. avar records what it installed, so
a warm `avr` does not ask Linux again. After `avr reset` or `avr destroy` the
record goes with the environment, and approved packages are installed again
the next time you enter it. A package you remove by hand inside Linux is not
reinstalled.

A failed install is reported with the package manager's exit status, the
session still starts, and the next invocation tries again.

### forward_env

Each name must be a valid variable name: letters, digits and `_`, not
starting with a digit. A name may appear only once. Once approved, the host's
value of each name is forwarded into every guest session in the project when
the host has one. See [what crosses into Linux]({% link syntax/index.md %}#what-crosses-into-linux)
for how it combines with `--env` and `config.toml`.

## Approval

A file in a repository you cloned is not you. `distro`, `arch`, `cpus` and
`memory` choose among environments avar itself offers, so they apply without
asking. `packages` runs a package manager as root, and `forward_env` sends
values from your machine into Linux, so those two do nothing until you
approve them.

- **When avar asks.** `avr`, `avr <command>`, `avr code`, `avr cursor` and
  `avr zed` ask after resolving the environment and before any machine work,
  and only when the file lists a name you have not approved.
- **What it shows.** Each pending package with the environment it would be
  installed into, saying when that environment is shared by every project,
  and each pending variable, saying its host value will be forwarded into
  every session in the project.
- **What counts as yes.** Only an explicit yes. Anything else declines.
- **What is remembered.** Names, not the file. Approvals are stored on your
  machine, per project. Packages are approved per environment, so approving
  `jq` for the project's own environment does not approve it for the shared
  one. Adding a name to the file asks about that name alone. Removing one
  stops applying it. Editing a comment asks nothing.
- **Declining.** Nothing is recorded. avar continues with what you approved
  before and asks again next time.
- **Without a terminal.** Nothing is asked and nothing pending applies. avar
  prints one line naming what is waiting and carries on without it.
- **`avr init` is not approval.** Writing the file and approving an install
  are separate questions. The next `avr` still asks.

## Where avar reads it

Only `<project>/.avr.toml`, where the project is the directory you first ran
`avr` in, or that directory's project when you are in a subdirectory of it.
avar never searches parent directories.

If the first `avr` in a repository ran in a subdirectory, that subdirectory is
the project, and a `.avr.toml` at the repository root is not read from there.
`avr init` shows the full path it will write before it asks.

## What the reader accepts

The file is a strict subset of TOML, chosen so that every file avar accepts
means the same thing to any TOML parser:

- `key = value` lines, one key per line, with bare keys only
- full-line and trailing `#` comments
- strings: basic strings (`"..."`) without escape sequences, and literal
  strings (`'...'`)
- whole numbers, with no sign, underscores or leading zeros
- lists of strings, on one line or across several, with comments and a
  trailing comma allowed

Everything else TOML allows is refused: tables, dotted or quoted keys, inline
tables, floats, booleans, dates, multi-line strings and escape sequences. So
is a key avar does not know, a value of the wrong type, a key set twice, a
file that is not UTF-8, and a file larger than 64 KiB.

A refusal stops the command before any machine work. The message names the
file, the line and the problem. avar never applies part of a file. An unknown
key can mean the file was written for a newer avar.

<!-- example:invalid — parsed by avar's own reader in a test; it must be refused -->
```toml
[environment]
distro = "fedora"
```

```text
avr: <project>/.avr.toml line 1: tables are not supported: every setting in .avr.toml is a top-level key (distro, arch, cpus, memory, packages, forward_env)
```

<!-- example:invalid — parsed by avar's own reader in a test; it must be refused -->
```toml
packages = ["ripgrep"]
```

```text
avr: <project>/.avr.toml line 1: packages needs distro: package names belong to one distribution, so name it, for example distro = "ubuntu"
```
