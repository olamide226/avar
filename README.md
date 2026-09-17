# avar

[![CI](https://github.com/olamide226/avar/actions/workflows/ci.yml/badge.svg)](https://github.com/olamide226/avar/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.23%2B-00ADD8.svg)](go.mod)

Run your current directory in Linux, from macOS or Windows.

```bash
cd ~/code/my-project

avr                  # interactive Linux shell, same directory
avr npm test         # run one command in Linux
avr --arch amd64     # the same project on x86_64 (macOS)
avr --distro fedora  # the same project on Fedora
```

That is the whole mental model: **the current directory plus the operating
environment you pick.** No machine to name, no mounts to configure, no
`devcontainer.json`, no Docker flags, no SSH setup.

Inside Linux you get the same absolute path you were standing in, your files live
and writable in both directions, real passwordless `sudo`, packages that persist
between sessions, and any port you listen on reachable at `localhost` on the host.

The same commands work on both hosts. On macOS the environments are
[Lima](https://lima-vm.io) virtual machines; on Windows they are WSL 2
distributions. Which one you are on is avar's problem, not yours.

The [documentation site](https://olamide226.github.io/avar/) has the full
command reference, configuration, troubleshooting, and environment matrix.

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

Homebrew installs both `avr` and its `avar` alias; they run the same command.
The shorter `avr` name remains canonical and is used throughout this guide.

**Windows.** winget is the recommended route:

```powershell
winget install olamide226.avar
```

winget picks the build for your processor and puts `avr` on your `PATH`; open a
new terminal afterwards so it is found. winget receives each minor release
(x.y.0) once Microsoft has reviewed and merged it, usually within a day or two
of it appearing on the [releases page](https://github.com/olamide226/avar/releases),
so it can trail the newest patch release. To skip
the wait, download the `windows_amd64` or `windows_arm64` archive from there,
unzip it, and put `avr.exe` somewhere on your `PATH`.

Either way, avar checks for WSL 2 on first run and offers to set it up. The
binaries are unsigned, so SmartScreen may warn the first time.

## Sixty seconds to a Linux shell

```bash
cd ~/code/my-project
avr
```

That is the whole setup. The first invocation creates the environment; there is
nothing to configure before it and nothing to clean up after it.

What to expect:

- **The first run of a new environment** downloads an OS image and provisions it:
  a virtual machine on macOS, a WSL distribution on Windows. This is the slow
  one — minutes, mostly download — and it happens once per distribution and
  architecture, not once per project.
- **The first visit to a new project directory** shares that directory into the
  environment. On macOS that needs a one-time restart of about ten seconds; on
  Windows it is a mount and costs milliseconds. Returning to the project later
  costs nothing.
- **Starting a stopped environment** takes roughly ten to fifteen seconds
  (11.2 s and 12.7 s measured on an M-series Mac, Lima 2.2.0).
- **Every invocation after that** attaches to the running environment in about
  400 ms, against the ~500 ms budget avar holds itself to.

Then:

```bash
avr uname -a          # confirm you are in Linux
avr sudo apt install ripgrep   # packages persist between sessions
avr npm run dev       # ports you listen on are reachable at localhost
avr ports             # which ones, and what is serving each
avr open 3000         # open one in your browser
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
| `avr --native-fs` | Run in a copy of the project on the Linux filesystem, for dependency-heavy work. Windows only, where the project is otherwise reached across a filesystem boundary |
| `avr sync` | Show what differs between the project's host copy and its Linux-native copy, and change nothing |
| `avr sync --to-host` | Apply the Linux copy's changes to the host copy, after showing them (`--to-guest` goes the other way, `--yes` skips the prompt) |
| `avr code` | Open the current project in VS Code, running in the Linux environment (Remote-SSH on macOS, VS Code's WSL integration on Windows) |
| `avr cursor` | The same in Cursor. Needs the `cursor` command on your PATH |
| `avr zed` | The same in Zed, through its SSH remote development on macOS and its WSL support on Windows. Needs the `zed` command on your PATH |
| `avr ports` | The ports the current environment forwards to this computer, and the Linux process listening on each |
| `avr ports --all` | The same, for every running environment |
| `avr open <port>` | Open `http://localhost:<port>` in your browser, or say why that port is not forwarded |
| `avr init` | Propose a `.avr.toml` from the project's manifests, and write it only if you confirm. Installs nothing |
| `avr version`, `avr help` | Also spelled `--version` and `--help` |

Use `avr help <command>` or `avr <command> --help` for the exact arguments and
flags supported by an individual management command. Help never starts, stops,
resets, snapshots, or destroys an environment.

### Reserved command names

The first word after avar's own flags decides everything that follows it. If it
names one of avar's commands, avar handles it; otherwise it is the start of a
command to run in Linux. So these names belong to avar and will not reach the
guest:

<!-- reserved-names:begin — checked against cli.Subcommands() by a test; keep both markers -->
`code` `cursor` `destroy` `help` `init` `internal` `isolate` `open` `ports` `reset` `restore` `snapshot` `status` `stop` `sync` `version` `zed`
<!-- reserved-names:end -->

(`internal` carries avar's own scheduled idle check and is not a command you run.)

**`--` forces the guest reading**, which is what to use if your project has a
script or binary by one of those names:

```sh
avr -- sync          # runs the guest's sync(1), or your project's ./sync
avr -- open file.txt # runs the guest's own open, not avar's
avr -- status        # runs the guest's own status, not avar's
avr -- zed           # runs a zed installed inside Linux, not avar's editor command
avr -- init          # runs a program called init inside Linux, not avr init
```

`sync` and `open` are the ones worth knowing about. `sync` is a standard Unix
command, `open` is a plausible name for a project script or a helper you
installed, and both are words a macOS hand types without thinking — and the
failure mode is quiet: without `--`, avar answers instead of your command,
rather than running it. `cursor` and `zed` are the editors' own command names,
so a copy of either installed inside the Linux environment is reached with
`avr -- cursor` or `avr -- zed`. The other reserved names are unlikely to
collide with anything you would run.

Snapshots do not work in every environment — see [Limitations](#limitations).

## Choosing an environment

Environment-selection flags come before the guest command or management command
they select. They affect `avr`, one-shot guest commands, `stop`, `snapshot`,
`restore`, `reset`, `destroy` (without `--all` or `--orphaned`), `code`, `cursor`,
`zed`, `ports` (without `--all`), and `open`, and they choose what `avr init`
proposes for. `avr status`, `avr stop --all`, `avr ports --all`, and the global
`destroy` scopes operate across environments instead. `avr isolate` changes the
current project's remembered
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
A standing grant goes in your own settings file, described
[below](#your-own-settings-configtoml).

Each distinct environment gets its own machine, and projects share it unless you
ask otherwise. `avr` and `avr --distro fedora` in the same directory are two
environments over the same files.

### Pinning a project's environment

avar needs no configuration, and a project without any behaves exactly as
described above. A team that wants everyone on the same environment can commit
a `.avr.toml` to the project directory:

```toml
distro = "fedora"                 # ubuntu, debian or fedora, optionally "fedora:43"
arch = "amd64"                    # arm64 or amd64
cpus = 4                          # for the project's own environment (avr --isolate)
memory = "8GiB"                   # likewise; GiB or MiB
packages = ["ripgrep", "jq"]      # Fedora's own package names, since distro says fedora
forward_env = ["GITHUB_TOKEN"]    # host variables to pass into this project's sessions
```

Every key is optional. `distro` and `arch` mean exactly what `--distro` and
`--arch` mean, and every command that picks "this environment" (`avr`, `stop`,
`reset`, `code`, and the rest) uses them. A flag you type still wins, and so
does anything avar already remembers for the project on your machine.

The rest are held to the same rule as everything else that crosses into Linux:
**a file in a repository you cloned is not you.**

- `packages` and `forward_env` do nothing until you approve them. The first
  `avr` in the project lists each package, says which environment it would be
  installed into (including when that environment is shared by every project),
  lists each variable whose value would be forwarded, and asks. Only a yes
  approves; the answer is remembered on your machine, per project, and avar
  asks again only if the file asks for something new. Without a terminal
  nothing is approved: avar says what is waiting and carries on without it.
  Packages are installed once per environment with `apt-get` or `dnf`, and are
  installed again after `avr reset` the next time you enter the environment.
- `cpus` and `memory` apply only to the project's own environment, and only when
  it is created. The shared environment serves every project, so no one
  project's file resizes it, and avar never resizes or restarts an environment
  because a file changed. When a size cannot apply, avar says so once. On
  Windows every WSL distribution shares one allocation, so the size cannot be
  set per environment at all.
- A size larger than your computer has (more `cpus` than it has logical CPUs,
  or more `memory` than it has RAM) is refused before avar creates the
  project's own environment, naming the file, the line, and what your computer
  has. Lower the value, or remove it to let avar choose the size. A size equal
  to your computer's is allowed. Where the size would not apply anyway, it does
  not stop you, and the notice above also says it is too large.

You do not have to write the file by hand. `avr init` reads the project's
`package.json`, `pyproject.toml`, `go.mod`, `Cargo.toml`, `Dockerfile`,
`docker-compose.yml`, `.tool-versions` and `mise.toml`, shows you the stack they
describe and the exact file it would write, and writes it only when you say yes.
It proposes a distribution and its packages, never a size or a variable to
forward, and it never replaces an existing `.avr.toml`. Writing the file is not
an approval: the next `avr` still asks before installing anything.

avar reads the file only from the project directory itself, the directory you
first ran `avr` in (or that directory's project, if you are in a subdirectory),
and never from a parent. The reader is strict: a key it does not know, or TOML it
does not support, stops the command with the line and the reason, rather than
applying half the file.

### Your own settings: `config.toml`

Settings that are yours rather than a project's live in `config.toml` in avar's
state directory: `~/.avr/config.toml` on macOS, `%LocalAppData%\avar\config.toml`
on Windows. The file is optional, and avar never writes it.

```toml
idle_timeout = "2h"                          # stop an environment unused this long; "0" never stops one
forward_env = ["AWS_PROFILE", "GITHUB_TOKEN"] # host variables passed into every session
```

- `idle_timeout` is a quoted duration with a unit: `"30m"`, `"2h"`, `"90m"`.
  The default is two hours, and `"0"` turns automatic stopping off.
- `forward_env` lists host variable names, each one its own quoted string.
  Unlike a project's `forward_env`, this is your own grant, so nothing asks.

The file is read as strictly as `.avr.toml`. A misspelt key, a value of the
wrong kind, or TOML avar does not support stops the command before anything is
started, with the file, the line, and what to write instead:

```text
avr: /Users/you/.avr/config.toml line 2: unknown key "idle_timout": did you mean idle_timeout? config.toml understands idle_timeout, forward_env
     Nothing was started or changed. Fix the file and run the command again; until then idle auto-stop is paused, and `avr status`, `avr stop` and `avr destroy` still work
```

While the file is broken, `avr status`, `avr stop`, `avr destroy`, `avr help`
and `avr version` still work, and the background idle check stops nothing.
`distro`, `arch`, `cpus`, `memory` and `packages` are not settings here yet; set
them per project in `.avr.toml`, or per command with a flag. Earlier versions
read this file leniently and ignored some mistakes in it, so a file that seemed
to work may now be refused: the
[config.toml page](https://olamide226.github.io/avar/syntax/config-toml.html#how-the-file-is-read) lists
what changed and what to write.

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

avar is a thin, opinionated layer over the Linux environment each host already
knows how to run:

- **macOS:** [Lima](https://lima-vm.io) (Apache-2.0, CNCF incubating), which
  supplies the virtual machines, VirtioFS file sharing, and automatic port
  forwarding.
- **Windows:** WSL 2, where each environment is a distribution avar registers
  and owns, projects are DrvFS mounts, and ports reach the host through WSL's
  localhost forwarding.

Both sit behind one provider interface, so the command layer never knows which
it is talking to. avar's contribution is the mental model: it maps your current
directory and a chosen environment onto a machine, a mount, and a working
directory, so that you never have to name any of the three.

### What Linux can see

Only the project folders you have run `avr` in. avar shares each one into the
environment as you use it, and shares nothing else by default.

- **On Windows**, WSL normally mounts every drive inside Linux, at `/mnt/c`,
  `/mnt/d` and so on, so any Linux program could read your whole disk. avar
  turns that off in every environment it creates, and checks afterwards,
  refusing an environment that still has a Windows drive mounted. It also
  stops your Windows `PATH` being added to Linux's, so `python` in Linux never
  quietly runs `python.exe` from Windows.
- **On macOS**, the virtual machine gets your project folders and nothing
  more: not your home directory, and not the rest of the disk.
- **On both**, Linux gets none of your environment variables beyond a few that
  terminals need, no credentials, and no SSH agent, unless you pass them with
  `--env`, `--env-file` or `--ssh-agent`.

This limits what Linux can read. It is not a sandbox. On Windows, WSL still
lets Linux start Windows programs, and a Windows program runs as you, with
your usual access to your files. Treat code you run in avar as you would code
you run on your own machine.

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

**A Linux-native workspace is a second copy, and copies diverge.** `avr --native-fs`
exists because WSL reaches a Windows directory through a translation layer, and a
`npm install` that stats a hundred thousand files pays for every crossing. The
cost is that the project then exists twice. avar compares the two by content
against what they last agreed on, so it can tell which side changed; it carries a
one-sided change without asking, shows you every change before applying one you
asked for, and when both sides changed the same file it says so and touches
neither. Build output (`node_modules`, `target`, `__pycache__` and the like) is
never copied back — keeping it in Linux is the point. Empty directories and
symbolic links are not synchronized; the latter are reported rather than dropped
quietly. Deleting the environment deletes the Linux copy with it, so run
`avr sync` before `avr reset` or `avr destroy`.

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
the same way — because the backend sits behind the same provider boundary.

It is exercised end to end against a real WSL installation: `make e2e` on Windows
provisions a distribution, runs commands in it, and checks the things that are
meant to be true of it — a non-root account, the project visible from both sides,
no Windows drive mounted, no host environment variable crossing, and the guest's
own exit status coming back out. It has had far less mileage than the macOS path,
which has been in daily use; treat it as working rather than as worn in.

Two things to know:

- Each environment runs on the host's own processor. WSL 2 has no CPU emulation,
  so `--arch` cannot ask for the architecture your machine is not, and avar says
  so before downloading anything rather than after.
- Your project stays on the Windows filesystem, which Linux reaches through a
  translation layer. For editing that is invisible; for a large dependency tree
  it is not, and avar says so once per project.

Linux hosts, cloud and remote environments, and GUI applications are out of
scope.

## Development

```bash
make build   # compile ./bin/avr
make test    # unit and integration tests
make lint    # gofmt -s and go vet
make e2e     # end-to-end tests against the host's real backend
```

`make e2e` runs against Lima on macOS (needs `limactl` and virtualization) and
against WSL 2 on Windows. CI runs lint, build, and tests on both hosts, and the
WSL end-to-end suite nightly.

avar is built spec-first. The requirements, design, and phased plan live in
[`.kiro/specs/avar-cli/`](.kiro/specs/avar-cli/) and are the source of truth;
[`docs/lessons.md`](docs/lessons.md) records the mistakes that changed how the
project is worked on. [`CONTRIBUTING.md`](CONTRIBUTING.md) explains how to
propose a change, and [`CLAUDE.md`](CLAUDE.md) is the working agreement every
change is held to.

## License

Apache-2.0. See [LICENSE](LICENSE).
