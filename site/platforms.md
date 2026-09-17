---
title: Platform notes
nav_order: 5
---

# Platform notes
{: .no_toc }

The commands behave the same on both hosts. What differs is how Linux is run
underneath, and a few things follow from that. This page covers those
differences. The README's
[Requirements](https://github.com/olamide226/avar/blob/main/README.md#requirements)
and [Limitations](https://github.com/olamide226/avar/blob/main/README.md#limitations)
sections are the short version.

1. TOC
{:toc}

## What Linux can see

On both hosts, an environment sees only the project directories you have run
`avr` in. avar shares each one as you use it, and nothing else: not your home
directory, not the rest of the disk. Linux gets no host environment variables
beyond `TERM`, `LANG` and `LC_*`, no credentials, and no SSH agent, unless you
grant them ([Configuration]({% link configuration.md %}#environment-variables-in-the-guest)).

This limits what Linux can read. It is not a sandbox. Treat code you run in
avar as you would code you run on your own machine.

## Sharing a new project

The first time you run `avr` in a project directory, avar adds it to the
environment's shares. If other `avr` sessions are attached to that
environment at the time, avar asks before changing its shares, and without a
terminal it stops and says why rather than risk disconnecting them.

An environment shares at most sixteen project directories. Sharing another
unshares the least recently used one and names it. Nothing is deleted: that
project stays registered, and running `avr` in it shares it again. The
project you are entering is never the one unshared. The limit exists because
macOS caps how many directory shares one virtual machine can have (nineteen,
measured against Lima 2.2.0), and avar applies the same cap on both hosts.

## macOS

Each environment is a [Lima](https://lima-vm.io) virtual machine. avar needs
Lima 2.0.0 or newer. If Lima is missing and avar is running in a terminal, it
offers to install it with Homebrew and waits for a yes. An older Lima is
refused with the upgrade command, and avar never upgrades it for you.

### Files

A project is shared into the machine at **the same absolute path** it has on
your Mac, so paths in error messages, build output and editor links mean the
same thing on both sides. Files are live and writable in both directions.

- In an environment on the Mac's own architecture the share is VirtioFS, at
  native speed. There is no filesystem boundary to work around, so
  `--native-fs` is refused as unnecessary, before any machine work.
- In an emulated environment (`--arch` for the other architecture) the share
  is reverse SSHFS, and the whole machine is markedly slower.

Adding a project to a machine's shares restarts the machine, which takes
about ten seconds. That happens on a project's first visit, and again if it
was unshared to make room.

### Snapshots

Lima can snapshot only an emulated machine. On Apple Silicon that means
`avr snapshot` works in `avr --arch amd64` environments and not in everyday
arm64 ones, where it says so rather than appearing to work. `avr reset` works
in every environment.

### Ports and editors

A port a Linux process listens on is forwarded to `localhost` on your Mac by
Lima. `avr ports` lists them and `avr open <port>` opens one.

`avr code` and `avr cursor` connect over Remote-SSH, and `avr zed` over Zed's
SSH remote development.

## Windows

Each environment is a WSL 2 distribution that avar registers and owns. avar
needs WSL 2.0.0 or newer.

- If WSL is missing, or too old, avar offers `wsl --install --no-distribution`
  or `wsl --update` and acts only on a yes. The install creates no Linux
  distribution of its own. Windows may ask you to approve it.
- If Windows must restart before WSL can be used, avar says so and stops.
  Nothing has been created, so running the same command after the restart is
  all that is left to do.
- An avar distribution registered as WSL 1 is refused, with the
  `wsl --set-version <name> 2` command that converts it. avar never converts
  one for you, because conversion rewrites the whole filesystem.

### What Linux can see on Windows

WSL normally mounts every Windows drive inside Linux, at `/mnt/c`, `/mnt/d`
and so on. avar turns that off in every environment it creates, then checks,
and refuses an environment that still has a Windows drive mounted. It also
stops your Windows `PATH` from being added to Linux's, so `python` in Linux
never quietly runs `python.exe` from Windows.

WSL still lets Linux start Windows programs, and a Windows program runs as
you, with your usual access to your files. That is the main reason this is
not a sandbox.

### Files and the filesystem boundary

A project stays on the Windows filesystem and is mounted into the
distribution under `/mnt/avr/projects/`, in a directory named for the project,
such as `/mnt/avr/projects/app-3fa9c2b1d0`. A Windows path has no Linux
equivalent, so unlike on macOS the path inside Linux differs from the one on
the host. You start in the directory under it that matches where you ran
`avr`.

Linux reaches that mount through a translation layer. Editing across it is
unnoticeable. Anything that touches many files, such as installing
dependencies, building or running a test suite, is markedly slower than on a
Linux filesystem. When avar sees a large dependency tree in a project on the
Windows filesystem, it says so, once per project.

### --native-fs and avr sync

`avr --native-fs` runs the session in a copy of the project on the
distribution's own filesystem, at `~/workspaces/<project>`, where those
operations run at native speed. The first run copies the project in.

The cost is that the project now exists twice, and copies diverge. `avr sync`
manages that:

- `avr sync` shows what differs between the two copies and changes nothing.
- `avr sync --to-host` applies the Linux copy's changes to the Windows copy,
  and `avr sync --to-guest` the other way. Each shows the changes and asks
  first; `--yes` skips the question once you have reviewed them.
- avar compares file contents against what the two copies last agreed on, so
  it can tell which side changed a file. When both sides changed the same
  file, it lists the conflict and applies nothing in either direction.
- Build output directories, such as `node_modules`, `target` and
  `__pycache__`, are never copied back: keeping them in Linux is the point.
  Empty directories and symbolic links are not synchronized, and symbolic
  links are reported rather than dropped quietly.

Removing the environment removes the Linux copy with it. Run `avr sync
--to-host` before `avr reset` or `avr destroy` if it holds work you want.

### Sizes, architectures, ports and editors

- Every WSL distribution runs in one shared virtual machine, sized for all of
  them together, so `cpus` and `memory` in `.avr.toml` cannot apply per
  environment on Windows.
- WSL has no CPU emulation. `--arch` for an architecture that is not the
  Windows machine's own is refused before anything is downloaded.
- Ports reach Windows through WSL's localhost forwarding. `avr ports` lists a
  port that is listening in Linux but unreachable from Windows, with the
  reason.
- `avr code`, `avr cursor` and `avr zed` use each editor's WSL integration.
