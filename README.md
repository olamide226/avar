# avar

[![CI](https://github.com/olamide226/avar/actions/workflows/ci.yml/badge.svg)](https://github.com/olamide226/avar/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.23%2B-00ADD8.svg)](go.mod)

Run your current directory in Linux.

```bash
cd ~/code/my-project

avr                  # interactive Linux shell, same directory
avr npm test         # run one command in Linux
avr --arch amd64     # the same project on x86_64
avr --distro fedora  # the same project on Fedora
```

That is the whole mental model: **the current directory plus the operating
environment you pick.** No machine to name, no mounts to configure, no
`devcontainer.json`, no Docker flags, no SSH setup.

Inside Linux you get the same absolute path you were standing in, your files live
and writable in both directions, real passwordless `sudo`, packages that persist
between sessions, and any port you listen on reachable at `localhost` on macOS.

## Install

Homebrew is the recommended route: the cask installs Lima as a dependency and
clears the quarantine attribute that would otherwise stop the first run.

```bash
brew tap olamide226/tap
brew install --cask avar
```

Or download the archive for your Mac from the
[releases page](https://github.com/olamide226/avar/releases) and put `avr`
somewhere on your `PATH`.

> **No release is published yet.** Both routes above are how v0.1.0 will be
> installed, not something you can run today. Until the first release, build from
> source: `make build` puts `avr` in `./bin`.

## Sixty seconds to a Linux shell

```bash
cd ~/code/my-project
avr
```

That is the whole setup. The first invocation creates the environment; there is
nothing to configure before it and nothing to clean up after it.

What to expect:

- **The first run of a new environment** downloads an OS image and provisions a
  virtual machine. This is the slow one — minutes, mostly download — and it
  happens once per distribution and architecture, not once per project.
- **The first visit to a new project directory** shares that directory into the
  environment, which needs a one-time restart of about ten seconds. Returning to
  the project later costs nothing.
- **Starting a stopped environment** takes roughly ten to fifteen seconds
  (11.2 s and 12.7 s measured on an M-series Mac, Lima 2.2.0).
- **Every invocation after that** attaches to the running environment in about
  400 ms, against the ~500 ms budget avar holds itself to.

Then:

```bash
avr uname -a          # confirm you are in Linux
avr sudo apt install ripgrep   # packages persist between sessions
avr npm run dev       # ports you listen on are reachable at localhost on macOS
avr status            # what exists, and what it is costing you
avr stop              # give the memory back
```

Environments stop themselves after two hours with no live session, so forgetting
`avr stop` costs nothing.

## Commands

`avr` reads a command line as `avr [flags] [--] [COMMAND [ARGS...]]`. Flags come
first. The first token that is not one of avar's own flags decides the rest: an
avar subcommand if it names one, otherwise the start of a command to run in
Linux — whose own flags avar never reads.

| Command | What it does |
| --- | --- |
| `avr` | Interactive Linux shell in the current directory |
| `avr <command> [args...]` | Run one command in Linux and exit with its status |
| `avr -- <command>` | Force the guest reading, so `avr -- status` runs the guest's `status` rather than avar's |
| `avr status` | Every environment avar manages: state, resources, live sessions, forwarded ports |
| `avr stop` | Stop the environment for the current directory |
| `avr stop --all` | Stop every environment avar manages |
| `avr reset` | Return the current environment to a clean OS, after confirmation. Project files are never touched |
| `avr reset --yes` | The same, without the prompt |
| `avr snapshot` | List the snapshots held for the current environment |
| `avr snapshot <name>` | Capture a snapshot of the current environment |
| `avr restore <name>` | Restore the current environment to a snapshot |
| `avr isolate` | Report whether this project defaults to its own environment |
| `avr isolate on` | Give this project its own environment from now on |
| `avr isolate off` | Return this project to the shared environment and offer to delete its machine (`--yes` to delete unattended) |
| `avr code` | Open the current project in VS Code, running in the Linux environment over Remote-SSH |
| `avr version`, `avr help` | Also spelled `--version` and `--help` |

Snapshots do not work in every environment — see [Limitations](#limitations).

## Choosing an environment

These flags apply in every mode, including in front of a subcommand
(`avr --distro fedora status`).

| Flag | Meaning |
| --- | --- |
| `--arch arm64\|amd64` | Guest CPU architecture. The non-host one is emulated |
| `--distro ubuntu\|debian\|fedora[:version]` | Distribution, optionally pinned to a version. Defaults: Ubuntu 24.04, Debian 13, Fedora 43 |
| `--isolate` | Use an environment dedicated to this project |
| `--shared` | Use the environment shared by every project, just this once |
| `--env NAME` or `--env NAME=value` | Forward or set one variable in the guest. Repeatable |
| `--env-file PATH` | Forward a file of `KEY=value` lines |
| `--ssh-agent` | Lend the guest your SSH agent for this invocation only |

Nothing crosses into the guest that you did not ask for: no host environment
beyond a small terminal allowlist, no home directory, no credentials, no agent.
`~/.avr/config.toml` accepts `forward_env = ["AWS_PROFILE", …]` for a standing
grant, and `idle_timeout` to change when idle environments stop themselves.

Each distinct environment gets its own machine, and projects share it unless you
ask otherwise. `avr` and `avr --distro fedora` in the same directory are two
environments over the same files.

## Requirements

- macOS 13 or later, Apple Silicon or Intel.
- [Lima](https://lima-vm.io), which the Homebrew cask installs as a dependency.
  If you installed `avr` some other way and Lima is missing, avar offers to
  install it with Homebrew on first run and waits for you to say yes.

Nothing else: `avr` is a single self-contained Go binary.

## How it works

avar is a thin, opinionated layer over [Lima](https://lima-vm.io) (Apache-2.0,
CNCF incubating), which supplies the virtual machines, VirtioFS file sharing, and
automatic port forwarding. avar's contribution is the mental model: it maps your
current directory and a chosen environment onto a machine, a mount, and a working
directory, so that you never have to name any of the three.

## Limitations

These are real and current, not caveats about a beta.

**Snapshots need an emulated environment.** Lima's snapshot support is a QEMU
feature. avar runs host-native environments under Apple's Virtualization
framework (`vz`) deliberately, for VirtioFS speed and Rosetta, and `limactl
snapshot` answers `unimplemented` there. So on an Apple Silicon Mac the everyday
environment is exactly the one that cannot be snapshotted; `avr snapshot` says so
rather than appearing to work. An emulated environment (`avr --arch amd64`) can
be snapshotted, and `avr reset` works everywhere.

**Sixteen project directories per environment.** macOS caps how many
directory-share devices one virtual machine may have. Measured against Lima
2.2.0: nineteen project mounts start, twenty do not, and the failure is a bare
"Internal Virtualization error" during boot with no way back. avar caps the set
at sixteen, leaving headroom for the Rosetta share. Past that the least recently
used project is unshared and you are told which; it stays a registered project
and comes back on the next visit, paying the same one-time restart a first visit
pays. The project you are entering is never the one dropped.

**Released binaries are unsigned and unnotarised.** The Homebrew cask strips the
quarantine attribute after install, so that route is unaffected. A tarball
downloaded directly from the releases page will be stopped by Gatekeeper until
you clear it yourself.

## Platform support

**macOS is the supported platform.** macOS 13+, Apple Silicon or Intel. That is
the whole list. Linux hosts, cloud and remote environments, and GUI applications
are out of scope.

### Roadmap

Nothing in this section exists. Each item is specified or sketched; none of it is
implemented, and there are no dates.

- **Windows hosts via WSL 2** — Requirement 18, Phase 4 of the plan. **Work has
  not started:** there is no WSL provider in the codebase, and `avr` does not run
  on Windows at all. It is a design on paper behind avar's provider boundary,
  nothing more.
- **`avr destroy`** — removing an environment outright is specified
  (REQ-5.6–5.8) but not yet implemented. Today `avr reset` returns an
  environment to a clean OS, and `avr isolate off` can delete a project's own
  machine.
- Post-MVP work: a Linux-native workspace mode, `.avr.toml` and `avr init`,
  `avr ports` and `avr open`, more editors, and a second backend behind the
  provider interface.

## Development

```bash
make build   # compile ./bin/avr
make test    # unit and integration tests
make lint    # gofmt -s and go vet
make e2e     # real-Lima end-to-end tests (needs macOS and limactl)
```

avar is built spec-first. The requirements, design, and phased plan live in
[`.kiro/specs/avar-cli/`](.kiro/specs/avar-cli/) and are the source of truth;
[`docs/lessons.md`](docs/lessons.md) records the mistakes that changed how the
project is worked on. [`CONTRIBUTING.md`](CONTRIBUTING.md) explains how to
propose a change, and [`CLAUDE.md`](CLAUDE.md) is the working agreement every
change is held to.

## License

Apache-2.0. See [LICENSE](LICENSE).
