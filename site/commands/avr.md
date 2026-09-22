---
title: avr
parent: Commands
nav_order: 1
---

# avr
{: .no_toc }

`avr` opens an interactive Linux shell in the current directory.
`avr <command>` runs one command there and exits with its status. The
environment is created on first use.

1. TOC
{:toc}

## Help

<!-- generated:help:begin — written by `make docs` from avr's help; edit cmd/root.go, not this section -->

This is exactly what `avr help` or `avr --help` prints.

{% raw %}
```text
avr opens the current directory in a complete Linux environment.

With no command it starts an interactive shell. With a command it runs
that command in Linux and exits with its status. The Linux environment
is created on first use; nothing needs configuring.

How avr reads a command line:
  avr [flags] [--] [COMMAND [ARGS...]]   no COMMAND opens a shell
  avr [flags] SUBCOMMAND [ARGS...]

Flags come first. The first token that is not one of avr's own flags
decides the rest: an avr subcommand if it names one, otherwise the
start of a command to run in Linux, whose own flags avr never reads.
`--` forces the command reading, so `avr -- status` runs the guest's
own `status` rather than avr's.

Management commands:
  status                         Show every Linux environment avar manages
  stop [--all]                   Stop this environment, or every avar environment
  snapshot [name]                List snapshots, or capture one named snapshot
  restore <name>                 Restore this environment from a snapshot
  reset [--yes]                  Recreate this environment from a clean OS
  isolate [on|off [--yes]]       Show or change this project's isolation default
  sync [--to-host|--to-guest]    Review or apply changes between a project's
                                 host copy and its Linux-native one
  destroy [--all|--orphaned] [--yes]
                                 Remove environments after confirmation
  code                           Open this project in VS Code in its Linux
                                 environment
  cursor                         Open this project in Cursor in its Linux
                                 environment
  zed                            Open this project in Zed in its Linux
                                 environment
  ports [--all]                  List the ports forwarded to this computer and
                                 the Linux process listening on each
  open <port>                    Open http://localhost:<port> in your browser
  init                           Propose a .avr.toml from this project's
                                 manifests, and write it if you confirm
  update                         Update avar to the latest release, or name the
                                 command that updates this installation
  help [command]                 Show general or command-specific help
  version                        Print the avr version

Run "avr help <command>" or "avr <command> --help" for command-specific help.

Usage:
  avr [flags] [--] [command [args...]]

Examples:
  avr                     Interactive Linux shell in the current directory
  avr npm test            Run one command in Linux
  avr npm test --watch    --watch goes to npm, not to avr
  avr --arch amd64 make   Run on x86_64 instead of the host architecture
  avr --distro fedora     Use Fedora instead of Ubuntu
  avr -- status           Run the guest's own `status`, not avr's

Flags:
      --arch string       guest CPU architecture: arm64 or amd64
      --distro string     guest distribution: ubuntu, debian or fedora, optionally :version
      --env strings       forward host env var to a guest session: NAME or NAME=value (repeatable)
      --env-file string   file of KEY=value lines to forward to a guest session (.env format)
  -h, --help              show how to use avr
      --isolate           use a machine dedicated to this project
      --native-fs         run in a copy of the project on the Linux filesystem, for speed
      --shared            use the machine shared by every project, just this once
      --ssh-agent         forward the host SSH agent socket for a guest session
  -v, --version           show the avr version
```
{% endraw %}

<!-- generated:help:end -->

## Examples

```sh
avr                                  # interactive shell in this project
avr npm test                         # one command; avr exits with its status
avr npm test --watch                 # --watch goes to npm, not to avr
avr --distro fedora                  # a shell in Fedora instead
avr --arch amd64 make                # build on x86_64
avr --isolate                        # this project's own environment, remembered
avr --env GITHUB_TOKEN gh pr list    # forward one host variable
avr --env-file .env.local npm start  # forward a file of KEY=value lines
avr -- status                        # a program called status in Linux, not avr status
```

## What it does

1. **Resolves the environment** from the selector flags, the project's
   `.avr.toml` and avar's defaults, and registers the current directory as a
   project if it is not inside one already. See
   [which environment a command acts on]({% link syntax/index.md %}#which-environment-a-command-acts-on).
2. **Reads `--env-file`**, if given, before any machine work.
3. **Checks the backend**: Lima on macOS, WSL 2 on Windows. If it is missing
   and avar is running in a terminal, avar offers to install it and waits for
   a yes.
4. **Asks about the project's `.avr.toml`**, if it lists packages or variables
   you have not approved. See [approval]({% link syntax/avr-toml.md %}#approval).
5. **Brings the environment up**: creates it the first time, starts it if it
   is stopped, and shares the project directory into it if it is not shared
   yet. Installs approved packages that are not installed yet.
6. **Attaches** your terminal to a shell, or runs the command, in the same
   directory inside Linux.

When the environment is already running and the project already shared, avar
adds about 400 ms and prints nothing of its own.

### Input and output

- A guest command's standard output and standard error are its own. Everything
  avar prints (progress, prompts, warnings, errors) goes to standard error, so
  `avr <command> | consumer` receives only the command's output.
- avar asks for a pseudo-terminal only when its standard input is a terminal.
  `avr <command> < /dev/null` from a script or a cron job gets none.
- Once the shell or command is attached, Ctrl-C and window resizes belong to
  it. avar keeps running to report the exit status.

### First run

The first run of a new environment downloads an image and provisions it.
avar says what it is creating before the slow part starts:

```text
Creating your Linux development environment · Ubuntu 24.04 · arm64 · <N> CPU · <N> GB RAM
```

On macOS, an environment for the architecture that is not the Mac's own adds
a warning that it runs under CPU emulation and is markedly slower. The first
visit to a project in an existing environment shares the project into it,
which on macOS means a restart of about ten seconds, and avar says so.

### Environment variables

The guest gets `TERM`, `LANG` and `LC_*` from your environment and nothing
else, unless you grant more with `--env`, `--env-file`, `forward_env` in
[config.toml]({% link syntax/config-toml.md %}), or approved `forward_env`
names in the project's `.avr.toml`.

`--env-file` lines are `KEY=value`. Blank lines and lines starting with `#`
are skipped, and whitespace around the name and the value is trimmed.
Everything else after the `=` is the value: quotes are kept, and a `#` later
in the line is part of the value rather than a comment. When a name appears
twice, the last line wins.

Grants reach an interactive shell and a one-shot command alike. In an
interactive shell they are set before your login profile runs, so a profile
that assigns the same name unconditionally — `export LANG=C` in `.profile`,
say — wins over the grant, exactly as it would over a variable you exported
before logging in.

{: .warning }
> **Known gaps, today.**
> - `--ssh-agent` lends the guest your SSH agent on macOS. On Windows avar
>   cannot forward an agent yet, so it refuses the flag with status 2 and
>   starts nothing, rather than opening a session without the agent.

### --native-fs

On Windows, `avr --native-fs` runs the session in the project's copy on
Linux's own filesystem. The first run copies the project in and says so.
Changes made on Windows since the last sync are carried in. If both copies
changed the same file, avar reports the conflict and still opens the session.
See [avr sync]({% link commands/sync.md %}).

On macOS the flag is refused before any machine work, because the project is
already shared at native speed.

## Exit status

| Status | When |
| --- | --- |
| The command's own | `avr <command>` finished; avar exits with the command's exit status. On macOS a command killed by a signal gives 128 plus the signal number |
| `0` | The interactive shell exited with 0 |
| `1` | avar could not prepare the environment: the backend is missing or too old, provisioning or starting failed, the `--env-file` could not be read, `.avr.toml` could not be read, or `--native-fs` was refused |
| `2` | The command line could not be read, or it names an environment avar does not support |
| `130` | You pressed Ctrl-C while avar was preparing the environment, or declined to restart an environment other sessions were using |

## Errors

### unknown flag

```text
avr: unknown flag "--arhc": run "avr --help" for avar's flags, or "avr -- --arhc ..." to pass it to a command in Linux
```

A word starting with `-` in avar's flag position that is not one of avar's
flags. Fix the spelling, or put `--` before it if it is meant for Linux.
Exit status 2.

### avar cannot run …

```text
avr: avar cannot run <distro> <version>: unsupported environment; supported <distro> versions are <versions> (omit the version to use <default>)
```

The environment is not in the [matrix]({% link environments.md %}). Exit
status 2. When `.avr.toml` chose the value, the message names the file.

### --env-file cannot be read

```text
avr: --env-file <path>: open env file: open <path>: no such file or directory
```

```text
avr: --env-file <path>: parse env file at line <N>: "<line>" is not a KEY=value line
```

avar reads the file before touching the environment. Exit status 1.

### sharing would restart the environment

```text
avr: sharing <project> with your Linux environment would restart it while <N> other sessions are attached to it; run in an interactive terminal, or close the other sessions, and try again
```

The project is not shared into this environment yet, and other `avr`
sessions are using it. From a terminal avar asks instead. Exit status 1, or
130 if you answer no at the prompt.

### --native-fs is unnecessary

```text
avr: this environment reaches your project directly rather than across a filesystem boundary, so it has no Linux-native workspace to keep; run avr without --native-fs
```

macOS only. Exit status 1.

For a missing Lima or WSL, see [Troubleshooting]({% link troubleshooting.md %}#setting-up).
