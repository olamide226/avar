---
title: Command reference
nav_order: 2
---

# Command reference
{: .no_toc }

Every section below is avar's own help text, exactly as `avr help` and
`avr help <command>` print it. The page is generated from the CLI, and a test
fails when the two disagree, so what you read here is what the installed
binary says. Run `avr help <command>` to read the same text in a terminal.

For how avar chooses which environment a command acts on, see
[Configuration]({% link configuration.md %}). For the distributions and
architectures the selector flags accept, see
[Environments]({% link environments.md %}).

1. TOC
{:toc}

<!-- generated:commands:begin — written by `make docs` from avr's help; edit cmd/root.go, not this section -->
{% raw %}

## `avr`

Printed by `avr help`, `avr --help`.

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

## `avr code`

Printed by `avr help code`.

```text
Usage:
  avr [selector flags] code

Open the current project in VS Code attached to its Linux environment: over Remote-SSH on macOS, through VS Code's WSL integration on Windows. Needs the `code` command on your PATH.

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```

## `avr cursor`

Printed by `avr help cursor`.

```text
Usage:
  avr [selector flags] cursor

Open the current project in Cursor attached to its Linux environment: over Remote-SSH on macOS, through Cursor's WSL integration on Windows. Needs the `cursor` command on your PATH.

`cursor` is an avar command, so it does not reach the guest: to run a program called `cursor` in Linux, use `avr -- cursor`.

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```

## `avr destroy`

Printed by `avr help destroy`.

```text
Usage:
  avr [selector flags] destroy [--all | --orphaned] [--yes]

Remove the selected environment, all avar environments, or isolated environments whose projects no longer exist. Host project files are never removed; confirmation is required unless --yes is supplied.

Flags:
  --all        remove every Linux environment avar manages
  --orphaned   remove isolated environments whose project directory is gone
  --yes        skip the confirmation prompt

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```

## `avr help`

Printed by `avr help help`.

```text
Usage:
  avr help [command]

Show avr's help, or help for one public management command.
```

## `avr init`

Printed by `avr help init`.

```text
Usage:
  avr [selector flags] init

Look at this project's manifests (package.json, pyproject.toml, go.mod, Cargo.toml, Dockerfile, docker-compose.yml, .tool-versions, mise.toml), show the stack they describe and the .avr.toml that would pin it, and write that file only if you confirm. Nothing is written without a terminal, and an existing .avr.toml is never replaced. Writing the file installs nothing: the next `avr` asks before installing its packages. --distro and --arch choose what the proposal is for.

`init` is an avar command, so it does not reach the guest: to run a program called `init` in Linux, use `avr -- init`.

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```

## `avr isolate`

Printed by `avr help isolate`.

```text
Usage:
  avr isolate [on | off [--yes]]

Show whether this project defaults to its own environment, or change that default. Turning isolation off offers to delete the isolated environment.

Flags:
  --yes   with `avr isolate off`, delete the isolated environment without asking

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```

## `avr open`

Printed by `avr help open`.

```text
Usage:
  avr [selector flags] open <port>

Open http://localhost:<port> in your default browser, if the selected environment forwards that port to this computer. If it does not — nothing is listening there, the environment is not running, or the port could not be forwarded — avar says so and opens nothing. The environment is never started to find out.

Note that `open` is an avar command, so it does not reach the guest: to run a command called `open` inside Linux, run `avr -- open`.

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```

## `avr ports`

Printed by `avr help ports`.

```text
Usage:
  avr [selector flags] ports [--all]

List the ports the selected environment forwards to this computer, with the address to open each at and the Linux process listening on it where avar can tell. Ports that are listening in Linux but cannot be reached from this computer are listed with the reason. The environment is never started to find out.

Flags:
  --all   list the ports of every running Linux environment avar manages

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```

## `avr reset`

Printed by `avr help reset`.

```text
Usage:
  avr [selector flags] reset [--yes]

Recreate the selected environment from a clean OS. Host project files are never changed; confirmation is required unless --yes is supplied.

Flags:
  --yes   skip the confirmation prompt

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```

## `avr restore`

Printed by `avr help restore`.

```text
Usage:
  avr [selector flags] restore <name>

Restore the selected environment from a named snapshot. Snapshots are available only in environments whose backend supports them.

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```

## `avr snapshot`

Printed by `avr help snapshot`.

```text
Usage:
  avr [selector flags] snapshot [name]

List snapshots for the selected environment, or capture one with a name. Snapshots are available only in environments whose backend supports them.

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```

## `avr status`

Printed by `avr help status`.

```text
Usage:
  avr status

Show every Linux environment avar manages, including state, resources, sessions, and forwarded-port diagnostics.

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```

## `avr stop`

Printed by `avr help stop`.

```text
Usage:
  avr [selector flags] stop [--all]

Stop the selected environment, or every Linux environment avar manages.

Flags:
  --all   stop every Linux environment avar manages

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```

## `avr sync`

Printed by `avr help sync`.

```text
Usage:
  avr [selector flags] sync [--to-host | --to-guest] [--yes]

Show what differs between this project's host copy and the Linux-native copy `avr --native-fs` keeps, and apply one side's changes to the other. With no direction it changes nothing. Files both copies changed are reported and never overwritten.

Note that `sync` is an avar command, so it does not reach the guest: if your project has a script called `sync`, or you want the guest's own sync(1), run `avr -- sync`.

Flags:
  --to-host    apply the Linux copy's changes to the host copy
  --to-guest   apply the host copy's changes to the Linux copy
  --yes        skip the confirmation prompt

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```

## `avr version`

Printed by `avr help version`.

```text
Usage:
  avr version

Print the avr version. Also available as `avr --version`.

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```

## `avr zed`

Printed by `avr help zed`.

```text
Usage:
  avr [selector flags] zed

Open the current project in Zed attached to its Linux environment: through Zed's SSH remote development on macOS, and its WSL support on Windows. Needs the `zed` command on your PATH.

`zed` is an avar command, so it does not reach the guest: to run a program called `zed` in Linux, use `avr -- zed`.

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```
{% endraw %}
<!-- generated:commands:end -->
