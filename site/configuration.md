---
title: Configuration
nav_order: 3
---

# Configuration
{: .no_toc }

avar needs no configuration. A project with no `.avr.toml`, on a machine with
no `config.toml`, gets Ubuntu on the host's architecture in an environment
shared with every other project. Everything on this page is optional.

1. TOC
{:toc}

## How avar picks the environment

Every command that acts on "this environment" (`avr`, a one-shot command,
`stop`, `reset`, `destroy`, `snapshot`, `restore`, `code`, `cursor`, `zed`,
`ports` and `open`) resolves it the same way.

**Distribution and architecture**, first match wins:

1. `--distro` and `--arch` on the command line.
2. `distro` and `arch` in the project's `.avr.toml`.
3. avar's defaults: the default distribution in the
   [environment matrix]({% link environments.md %}), on the host's own
   architecture.

Each setting is decided on its own, so `avr --arch amd64` in a project whose
file says `distro = "fedora"` runs Fedora on amd64. A layer that names a
distribution also fixes its release: `--distro debian` never inherits a
version the file wrote for Ubuntu.

**Isolation**, first match wins:

1. `--isolate` or `--shared` on the command line. `--isolate` is also
   remembered for the project; `--shared` applies to that one invocation.
2. What avar remembers for the project, set by `--isolate` or
   `avr isolate on` and cleared by `avr isolate off`.
3. The shared environment.

`.avr.toml` has no isolation setting.

## .avr.toml

A team that wants everyone on the same environment commits a `.avr.toml` to
the project directory. `avr init` proposes one from the project's manifests
and writes it only if you confirm.

```toml
distro = "fedora"                 # ubuntu, debian or fedora, optionally "fedora:43"
arch = "amd64"                    # arm64 or amd64
cpus = 4                          # the project's own environment only, at creation
memory = "8GiB"                   # likewise; a whole number of GiB or MiB
packages = ["ripgrep", "jq"]      # the named distribution's own package names
forward_env = ["GITHUB_TOKEN"]    # host variables to forward, once you approve
```

Every key is optional, and an empty file is valid.

### Keys

| Key | Value | What it does |
| --- | --- | --- |
| `distro` | A quoted distribution, optionally `"name:version"` | Chooses the distribution, exactly as `--distro` does |
| `arch` | `"arm64"` or `"amd64"` | Chooses the architecture, exactly as `--arch` does |
| `cpus` | A whole number, at least 1 | Processors for the project's isolated environment, applied when it is created |
| `memory` | A quoted whole number of `GiB` or `MiB`, such as `"8GiB"` | Memory for the project's isolated environment, applied when it is created |
| `packages` | A list of quoted package names | Installed into the environment after you approve them. Requires `distro` |
| `forward_env` | A list of quoted variable names | Host variables forwarded into the project's sessions after you approve them |

`packages` needs `distro` because package names belong to a distribution:
`golang-go` is Debian's name and `golang` is Fedora's. When a different
distribution resolves, for example `avr --distro debian` in a project whose
file says `distro = "fedora"`, avar installs none of the packages and says so.

### Where avar reads it

Only from the project's own directory: the directory you first ran `avr` in,
or that directory's project when you are in one of its subdirectories. avar
never searches parent directories. If the first `avr` in a repository ran in a
subdirectory, that subdirectory is the project, and a `.avr.toml` at the
repository root is not read from there. `avr init` shows the full path it will
write before it asks.

### What the reader accepts

The file is a strict subset of TOML: flat `key = value` lines, `#` comments,
strings (basic strings without escape sequences, or literal strings), whole
numbers without signs, underscores or leading zeros, and lists of strings on
one line or several. Tables, dotted or quoted keys, floats, booleans, dates
and multi-line strings are refused.

A key avar does not know, a value of the wrong kind, a duplicate key, or a
file larger than 64 KiB stops the command before any machine work, naming the
file, the line and the problem. avar never applies part of a file. An unknown
key can mean the file was written for a newer avar.

### The approval model

A file in a repository you cloned is not you, so the settings split in two.

**Applied without asking:** `distro` and `arch`. They choose among the
environments avar itself supports, and nothing of your machine crosses into
Linux because of them.

**Applied only after you approve them:** `packages` and `forward_env`.

- The first `avr` in the project, or `avr <command>`, or an editor command,
  lists each package with the environment it would be installed into (saying
  when that environment is shared by every project), and each variable whose
  host value would be forwarded. Then it asks. Only an explicit yes approves.
- Approval is of names, not of the file. It is remembered on your machine, per
  project, and avar asks again only about a name it has not seen approved.
  Packages are approved per environment, so approving `jq` for a project's
  own environment does not approve it for the shared one.
- Declining records nothing. avar continues with what you approved before and
  asks again next time.
- Without a terminal nothing is approved. avar prints one line saying what is
  waiting and carries on without it.
- Writing the file with `avr init` is not an approval. The next `avr` still
  asks.

Approved packages are installed once per environment, with `apt-get` on
Ubuntu and Debian and `dnf` on Fedora, and avar records what it installed.
After `avr reset` or `avr destroy` they are installed again the next time you
enter the environment. A failed install is reported with the package
manager's exit status, the session still starts, and the next invocation
tries again.

### cpus and memory

These apply only to the project's own environment (`avr --isolate`), and only
when that environment is created. The shared environment serves every
project, so no one project's file sizes it, and avar never resizes or restarts
an environment because a file changed.

When a size cannot apply, avar says so once, with the next step:

- the project uses the shared environment: run `avr --isolate`;
- the isolated environment already exists at another size: run `avr reset`
  to recreate it at the declared size;
- the host cannot size environments one at a time: on Windows every WSL
  distribution shares one allocation, so the setting cannot apply there.

## config.toml

avar's own configuration file lives in its state directory:

| Host | Path |
| --- | --- |
| macOS | `~/.avr/config.toml` |
| Windows | `%LocalAppData%\avar\config.toml` |

Setting `AVR_HOME` moves the whole state directory, this file included.

It has two keys:

| Key | Value | What it does |
| --- | --- | --- |
| `forward_env` | A list of variable names, such as `["AWS_PROFILE"]` | A standing grant: each named host variable is forwarded into every guest session, in every project, when the host has it |
| `idle_timeout` | A quoted duration, such as `"2h"` or `"30m"` | How long an environment with no live session waits before it is stopped. `"0"` turns idle stopping off. The default is two hours |

```toml
forward_env = ["AWS_PROFILE", "GITHUB_TOKEN"]
idle_timeout = "4h"
```

Unlike `.avr.toml`, this file is read leniently, because it is your own and a
typo in it should not cost you a shell. A value avar cannot read is ignored:
an `idle_timeout` that is not a duration means the two-hour default, and a
`forward_env` that is not a list forwards nothing. If a variable you granted
does not arrive in Linux, check this file first.

The idle check runs every ten minutes. avar registers it with the host's
scheduler (launchd on macOS, Task Scheduler on Windows) when it creates an
environment, and says so once, with the command that removes it.

## Environment variables in the guest

Nothing crosses into Linux that you did not ask for. A guest session gets the
host's `TERM`, `LANG` and `LC_*` variables and nothing else, unless you grant
more. The grants, applied in this order so that a later one overrides an
earlier one:

1. `forward_env` from `config.toml`, and the `forward_env` names you approved
   for this project.
2. `--env-file PATH`, a file of `KEY=value` lines.
3. `--env NAME` (the host's value, if it has one) or `--env NAME=value`.

`--ssh-agent` lends the guest your SSH agent for that one invocation. Grants
on the command line apply to an interactive shell or a one-shot command only;
management commands start no guest session.
