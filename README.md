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
between sessions, and any port you listen on reachable at `localhost` on the host.

## Install

**macOS.** Homebrew is the recommended route: the cask installs Lima as a
dependency and clears the quarantine attribute that would otherwise stop the
first run.

```bash
brew install --cask olamide226/tap/avar
```

This installs the latest stable release and Lima. Or download the archive for
your Mac from the [releases page](https://github.com/olamide226/avar/releases)
and put `avr` somewhere on your `PATH`.

**Windows.** Download the `windows_amd64` or `windows_arm64` archive from the
[releases page](https://github.com/olamide226/avar/releases), unzip it, and put
`avr.exe` somewhere on your `PATH`. avar checks for WSL 2 on first run and
offers to set it up. The binaries are unsigned, so SmartScreen may warn the
first time.

Homebrew installs both `avr` and its `avar` alias; they run the same command.
The shorter `avr` name remains canonical and is used throughout this guide.

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
avr npm run dev       # ports you listen on are reachable at localhost
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
| `avr destroy` | Remove the current environment and everything in it, after confirmation. Project files are never touched |
| `avr destroy --yes` | Remove the current environment without a confirmation prompt |
| `avr destroy --all` | Remove every environment avar manages |
| `avr destroy --orphaned` | Remove isolated environments whose project directory has been deleted |
| `avr snapshot` | List the snapshots held for the current environment |
| `avr snapshot <name>` | Capture a snapshot of the current environment |
| `avr restore <name>` | Restore the current environment to a snapshot |
| `avr isolate` | Report whether this project defaults to its own environment |
| `avr isolate on` | Give this project its own environment from now on |
| `avr isolate off` | Return this project to the shared environment and offer to delete its machine (`--yes` to delete unattended) |
| `avr code` | Open the current project in VS Code, running in the Linux environment over Remote-SSH |
| `avr version`, `avr help` | Also spelled `--version` and `--help` |

Use `avr help <command>` or `avr <command> --help` for the exact arguments and
flags supported by an individual management command. Help never starts, stops,
resets, snapshots, or destroys an environment.

Snapshots do not work in every environment — see [Limitations](#limitations).

## Choosing an environment

Environment-selection flags come before the guest command or management command
they select. They affect `avr`, one-shot guest commands, `stop`, `snapshot`,
`restore`, `reset`, `destroy` (without `--all` or `--orphaned`), and `code`.
`avr status`, `avr stop --all`, and the global `destroy` scopes operate across
environments instead. `avr isolate` changes the current project's remembered
default rather than selecting an environment.

| Flag | Meaning |
| --- | --- |
| `--arch arm64\|amd64` | Guest CPU architecture. The non-host one is emulated |
| `--distro ubuntu\|debian\|fedora[:version]` | Distribution, optionally pinned to a version. Defaults: Ubuntu 24.04, Debian 13, Fedora 43 |
| `--isolate` | Use an environment dedicated to this project |
| `--shared` | Use the environment shared by every project, just this once |
| `--env NAME` or `--env NAME=value` | Forward or set one variable in the guest. Repeatable |
| `--env-file PATH` | Forward a file of `KEY=value` lines |
| `--ssh-agent` | Lend the guest your SSH agent for this invocation only |

`--env`, `--env-file`, and `--ssh-agent` apply only to an interactive shell or
one-shot guest command. Management commands do not start a guest session, so
they do not forward environment values or an SSH agent.

Nothing crosses into the guest that you did not ask for: no host environment
beyond a small terminal allowlist, no home directory, no credentials, no agent.
`~/.avr/config.toml` accepts `forward_env = ["AWS_PROFILE", …]` for a standing
grant, and `idle_timeout` to change when idle environments stop themselves.

Each distinct environment gets its own machine, and projects share it unless you
ask otherwise. `avr` and `avr --distro fedora` in the same directory are two
environments over the same files.

## Requirements

One of:

- **macOS 13 or later**, Apple Silicon or Intel, plus [Lima](https://lima-vm.io).
  The Homebrew cask installs Lima as a dependency. If you installed `avr` some
  other way and Lima is missing, avar offers to install it with Homebrew on
  first run and waits for you to say yes.
- **Windows 11 22H2 or later**, x64 or Arm64, plus WSL 2. avar checks for it on
  first run and offers `wsl --install --no-distribution`, which installs the
  platform only and creates no Linux distribution of its own. Windows may ask
  you to approve the change and may need a restart; avar says so before acting.

Nothing else: `avr` is a single self-contained Go binary.

## How it works

avar is a thin, opinionated layer over [Lima](https://lima-vm.io) (Apache-2.0,
CNCF incubating), which supplies the virtual machines, VirtioFS file sharing, and
automatic port forwarding. avar's contribution is the mental model: it maps your
current directory and a chosen environment onto a machine, a mount, and a working
directory, so that you never have to name any of the three.

## Limitations

These are real and current, not caveats about a beta.

**Snapshots need an emulated environment, on macOS.** Lima's snapshot support is a QEMU
feature. avar runs host-native environments under Apple's Virtualization
framework (`vz`) deliberately, for VirtioFS speed and Rosetta, and `limactl
snapshot` answers `unimplemented` there. So on an Apple Silicon Mac the everyday
environment is exactly the one that cannot be snapshotted; `avr snapshot` says so
rather than appearing to work. An emulated environment (`avr --arch amd64`) can
be snapshotted, and `avr reset` works everywhere. On Windows this limitation
does not apply.

**Sixteen project directories per environment, on macOS.** macOS caps how many
directory-share devices one virtual machine may have. Measured against Lima
2.2.0: nineteen project mounts start, twenty do not, and the failure is a bare
"Internal Virtualization error" during boot with no way back. avar caps the set
at sixteen, leaving headroom for the Rosetta share. Past that the least recently
used project is unshared and you are told which; it stays a registered project
and comes back on the next visit, paying the same one-time restart a first visit
pays. The project you are entering is never the one dropped. WSL has no such
cap, because a project share there is a mount rather than a virtual device.

**Released binaries are unsigned.** On macOS the Homebrew cask strips the
quarantine attribute after install, so that route is unaffected; a tarball
downloaded directly from the releases page will be stopped by Gatekeeper until
you clear it yourself. On Windows, SmartScreen may warn the first time you run
`avr.exe`.

## Platform support

**macOS 13+**, Apple Silicon or Intel, backed by [Lima](https://lima-vm.io).
This is the platform avar has been used on.

**Windows 11 22H2+**, x64 or Arm64, backed by WSL 2. The command grammar is
identical — `avr`, `avr npm test`, `avr status`, `avr code` and the rest behave
the same way — because the backend sits behind the same provider boundary. Two
honest caveats:

- It has been built and unit-tested but **not yet exercised end to end against a
  real WSL installation.** No avar environment has been provisioned on Windows
  outside a test double. Treat it as ready to try, not as proven.
- Each environment runs on the host's own processor. WSL 2 has no CPU emulation,
  so `--arch` cannot ask for the architecture your machine is not, and avar says
  so before downloading anything rather than after.

Linux hosts, cloud and remote environments, and GUI applications are out of
scope.

### Roadmap

Nothing in this section exists. Each item is specified or sketched; none of it is
implemented, and there are no dates.

- A Linux-native workspace mode, so a project can live on the Linux filesystem
  and be synchronised rather than shared. On Windows this is what the
  cross-filesystem notice points at once it is built.
- `.avr.toml` and `avr init`, `avr ports` and `avr open`, more editors, and a
  second backend behind the provider interface.

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
