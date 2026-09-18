---
title: Design decisions
nav_order: 6
---

# Design decisions
{: .no_toc }

avar makes a handful of choices on your behalf. Most of them trade a little
convenience for something harder to get back, such as your credentials
staying on your machine or a cloned repository not installing things. This
page explains each choice: what avar does, why, and what it costs you.

Everything follows from one rule: **you think in terms of the current
directory and the environment you pick.** Virtual machines, WSL
distributions, mounts, SSH and images are avar's problem, not yours.

1. TOC
{:toc}

## No daemon; a scheduled idle check instead

**The choice.** avar is only the `avr` command. Nothing of avar keeps running
between commands. To stop environments you have stopped using, avar registers
a scheduled job with your operating system: a launchd agent on macOS, a Task
Scheduler task on Windows. Every thirty minutes it runs `avr internal idle-check`,
which stops any environment that has had no live `avr` session for the idle
timeout (two hours by default). On Windows the task runs `avrw.exe`, the same
check built as a program with no console, so nothing appears on screen when it
runs; it ships beside `avr.exe`.

**Why.** A resident service is one more thing to install, keep running,
upgrade and trust with your machine. The operating system already has a
scheduler that survives restarts. Environments still hold memory while
running, so something has to stop the ones you forgot, and a short-lived
command run on a schedule does that without a service.

**The trade-off.**
- avar registers the job when it creates an environment, and tells you the
  first time, with the command that removes it. That is a background task
  you did not explicitly ask for. `idle_timeout = "0"` removes it too, and a
  job you delete stays deleted.
- Uninstalling avar cannot remove the job for you, because neither Homebrew
  nor winget runs avar when it uninstalls. `brew uninstall --zap` removes it
  on macOS; on Windows, delete the task first. The README's Uninstall section
  has the commands.
- Stopping happens on the next check after the timeout, not at it.
- An environment you leave idle stops, and your next `avr` pays a start of
  about ten to fifteen seconds.

Change or disable the timeout with [`idle_timeout`]({% link syntax/config-toml.md %}#idle_timeout).

## One shared environment by default; isolation is opt-in

**The choice.** Each distinct environment (a distribution, a release and an
architecture) gets one machine, and every project you use it in shares that
machine. `avr --isolate` or `avr isolate on` gives a project environments of
its own, and avar remembers that for the project.

**Why.** A shared environment is warm almost all the time. Once it is running
and a project is shared into it, attaching takes about 400 ms, and a new
project costs a one-time share rather than a new machine. Packages you
install persist and are available in every project, which is what you would
expect of your own Linux machine. A machine per project would make every new
project a download and a boot.

**The trade-off.**
- Projects in a shared environment are not isolated from each other. A
  package one project installs is there for all of them. Every project
  directory you have used in that environment is shared into the same
  machine, so code running for one project can read another's files.
- A shared environment holds at most sixteen project directories at once.
  Past that, avar unshares the least recently used one.
- `cpus` and `memory` in a project's `.avr.toml` cannot size a shared
  environment, because no one project owns it.

Isolate a project when any of that matters for it. An isolated environment
costs its own disk and memory.

## Linux sees only the project folders you use

**The choice.** avar shares a project directory into an environment the first
time you run `avr` in it, and shares nothing else: not your home directory,
not the rest of the disk. On Windows, avar also turns off two WSL defaults in
every distribution it creates:

- **Drive automount.** WSL normally mounts every Windows drive at `/mnt/c`,
  `/mnt/d` and so on. avar turns that off, checks after provisioning, and
  refuses an environment that still has a Windows drive mounted.
- **Windows `PATH` injection.** WSL normally appends your Windows `PATH` to
  Linux's, so `python` in Linux can quietly run `python.exe`. avar turns that
  off.

avar leaves WSL interop itself on: Linux can still start Windows programs.

**Why.** Your home directory holds SSH keys, cloud credentials and every other
project. Code you run in Linux (a dependency's install script, a test suite)
should reach the project it was run for, not all of that. Mounting whole
drives, or letting Windows' `PATH` leak in, would undo the rule by default.

**The trade-off.**
- A file outside the project, such as `~/.gitconfig`, a sibling repository or
  a shared data directory, is not visible in Linux unless it is inside a
  project you have used there.
- **This is not a sandbox.** It limits what Linux can read by default. On
  Windows, interop means Linux can start a Windows program, which runs as you
  with your usual access. The guest user also has passwordless `sudo`, so
  code in Linux that sets out to change WSL's integration settings can.
  Treat code you run in avar as you would code you run on your own machine.

## Nothing from your machine crosses unless you grant it

**The choice.** A guest session gets your `TERM`, `LANG` and `LC_*` variables
and nothing else from your environment. No credentials, no SSH agent. You
grant more explicitly: `--env`, `--env-file` or `--ssh-agent` for one
invocation, or `forward_env` for a standing grant.

**Why.** A developer's shell environment is full of secrets: cloud keys,
registry tokens, session cookies. avar builds the guest environment from an
allowlist rather than copying yours and removing known secrets, because a
blocklist is wrong the first time somebody invents a new variable name. An
allowlist can only be wrong about a variable you never asked avar to carry.

**The trade-off.** Tools that expect your credentials, such as `git push` over
SSH or a cloud CLI, do not have them in Linux until you pass them.
`--ssh-agent` has to be given on every invocation that needs it, and today it
works only on macOS. See the
[known gaps]({% link syntax/index.md %}#what-crosses-into-linux) in how grants
reach the guest.

See [what crosses into Linux]({% link syntax/index.md %}#what-crosses-into-linux)
for the exact order grants apply in.

## A cloned repository cannot install packages or forward variables without you

**The choice.** A project's `.avr.toml` can choose its distribution,
architecture and size without asking. Its `packages` and `forward_env` do
nothing until you approve each name, at a terminal, on your machine. The
approval is remembered per project, and for packages per environment.

**Why.** You run `git clone` on repositories you have not read. `packages`
runs a package manager as root inside a machine that may be shared with your
other projects, and `forward_env` sends values from your machine into Linux.
A file that arrives with `git pull` must not be able to do either of those on
its own. Distribution and architecture only choose among environments avar
itself offers, and nothing of yours crosses because of them.

**The trade-off.**
- The first `avr` in a project with such a file stops to ask.
- A script or CI job, with no terminal, never gets the pending packages or
  variables. avar says what is waiting and carries on without them.
- `avr init` writing the file is not an approval. You still approve at the
  next `avr`.

## .avr.toml is read only from the project directory

**The choice.** avar reads `.avr.toml` from the project directory and nowhere
else. It does not search parent directories.

**Why.** The project directory is what avar shares into Linux, so a
configuration file there describes the thing it configures. Searching upward
would read files avar has no reason to trust as belonging to this project:
one in your home directory, in a shared temporary directory, or in a monorepo
root you never ran `avr` in. It would also make `.avr.toml` a second way of
deciding where a project starts.

**The trade-off.** If the first `avr` in a repository ran in a subdirectory,
that subdirectory is the project, and a `.avr.toml` at the repository root is
not read from there. `avr init` shows the full path it will write, so you can
see which directory is the project.

## Configuration files are read strictly

**The choice.** avar reads both `.avr.toml` and `config.toml` strictly: an
unknown key, a value of the wrong type, or TOML it does not support stops the
command before any machine work, names the line and what to write, and
nothing in the file applies.

**Why.** A setting that silently does not apply is worse than a command that
stops, because you believe it is in force. A project file is shared by a team,
and a misspelt key quietly ignored would leave environments different between
machines. `config.toml` was once read leniently, on the reasoning that a typo in
your own settings should not cost you a shell. In practice that meant
`idle_timeout="0"` without spaces left environments stopping, and a misspelt
key did nothing at all, with no message to say so. It changed on 2026-09-17.

**The trade-off.** A file that uses a key from a newer avar fails on an older
one, and the error suggests upgrading. A broken `config.toml` stops most
commands until it is fixed, so `avr status`, `avr stop` and `avr destroy` still
run, and the background idle check stops nothing rather than guessing.

## Linux-native workspaces exist only on Windows

**The choice.** `avr --native-fs` and `avr sync` keep a second copy of a
project on Linux's own filesystem. They work on Windows. On macOS,
`--native-fs` is refused as unnecessary before any machine work.

**Why.** On Windows, the project stays on the Windows filesystem and Linux
reaches it through a translation layer. Anything that touches many files,
such as installing dependencies or building, pays for every crossing. A copy
on Linux's filesystem avoids that. On macOS, avar shares the project with a
native-architecture machine over VirtioFS at native speed, so a second copy
would bring the problems of keeping two copies in step without the speed
that justifies them.

**The trade-off.** On Windows, a native workspace means the project exists
twice, and copies diverge. `avr sync` shows every change before applying it,
compares contents rather than timestamps, and refuses to overwrite a file
both sides changed. Build output such as `node_modules` stays in Linux.
Removing the environment removes the Linux copy, so run `avr sync --to-host`
first if it holds work you want.

## Pinned, verified images

**The choice.** On macOS every distribution image is pinned to a dated URL
from its publisher and to the publisher's own digest, and Lima checks the
download against it. On Windows avar installs from WSL's own distribution
registry, then reads the new distribution's `/etc/os-release` and refuses the
environment if it is not the release avar promised.

**Why.** The image is everything underneath the code you run. A floating
"latest" link would mean avar boots whatever a mirror serves that day. On
Windows, Microsoft already runs a registry that fetches and verifies
distributions, and hosting a second copy of that supply chain would only add
a way to get checksums wrong.

**The trade-off.** Publishers eventually remove old dated images. When they
do, provisioning a new environment fails with the download error until avar
ships a newer pin. It never silently fetches something unverified instead.
WSL's registry names Debian by track rather than by release, so a Debian
environment can be refused on Windows once Debian's stable release moves on.

## Release cadence: Homebrew gets every release, winget gets minor releases

**The choice.** A release is cut automatically when a change that fixes or
adds something merges. Every release updates the GitHub releases page and the
Homebrew cask. Only minor and major releases (`x.y.0`) are submitted to
winget.

**Why.** Each winget submission is a pull request reviewed by people at
Microsoft. Submitting every patch release once opened five pull requests in
45 minutes for a package still in its first review.

**The trade-off.**
- `winget install olamide226.avar` can trail the newest patch release.
- A version reaches winget only after Microsoft's review merges its pull
  request, which usually takes a day or two.
- To get a patch release on Windows sooner, download the `windows_amd64` or
  `windows_arm64` archive from the
  [releases page](https://github.com/olamide226/avar/releases).
