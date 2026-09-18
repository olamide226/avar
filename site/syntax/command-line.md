---
title: Command line
parent: Syntax
nav_order: 1
---

# Command line
{: .no_toc }

1. TOC
{:toc}

## The grammar

```text
avr [flags] [--] [COMMAND [ARGS...]]
avr [flags] SUBCOMMAND [ARGS...]
```

avar reads a command line from the left, in three steps:

1. **Flags come first.** Leading tokens that are avar's own flags are avar's.
2. **`--` ends avar's flags** and makes everything after it a command to run
   in Linux, even a word that names an avar subcommand. `avr --` on its own
   opens a shell.
3. **The first other token decides the rest.** If it names an avar
   subcommand, avar runs that subcommand with the remaining tokens as its
   arguments. Otherwise it is the start of a command to run in Linux.

Nothing at or after that deciding token is read as an avar flag. The command
in Linux keeps its own flags:

```sh
avr npm test --watch     # --watch goes to npm
avr --arch amd64 make    # --arch is avar's: it comes first
avr make --arch amd64    # --arch goes to make
```

With no deciding token at all, `avr` opens an interactive shell.

## Flags

These are the only flags avar reads, and only in the flag position.

<!-- flags:begin — every flag internal/cli parses must appear in this table; a test checks it -->

| Flag | Value | What it does |
| --- | --- | --- |
| `--distro` | `ubuntu`, `debian` or `fedora`, optionally `name:version` | Chooses the distribution. See the [environment matrix]({% link environments.md %}) |
| `--arch` | `arm64` or `amd64` | Chooses the guest CPU architecture |
| `--isolate` | none | Uses an environment dedicated to this project, and remembers that choice for the project |
| `--shared` | none | Uses the environment shared by every project, for this invocation only |
| `--env` | `NAME` or `NAME=value` | Forwards the host's `NAME`, or sets `NAME` to `value`, in the guest session. Repeatable |
| `--env-file` | a path | Forwards every `KEY=value` line of the file into the guest session |
| `--ssh-agent` | none | Lends the guest your SSH agent for this invocation |
| `--native-fs` | none | Runs the session in a copy of the project on the Linux filesystem. Windows only; see [Platform notes]({% link platforms.md %}#native-fs-and-avr-sync) |
| `--help`, `-h` | none | Shows avar's help |
| `--version`, `-v` | none | Prints avar's version |

<!-- flags:end -->

### Spelling a flag

- A flag that takes a value accepts it as the next token or after `=`:
  `--arch amd64` and `--arch=amd64` are the same.
- A flag that takes no value also accepts an explicit boolean after `=`, such
  as `--isolate=false`. Anything that is not a boolean is refused.
- `--distro` names are case-insensitive. A colon with nothing after it, as in
  `--distro fedora:`, is refused.
- `--isolate` and `--shared` together are refused.

### Where flags apply

- `--distro`, `--arch`, `--isolate` and `--shared` select the environment for
  the shell, a one-shot command, and the management commands that act on
  "this environment": `stop`, `reset`, `destroy`, `snapshot`, `restore`,
  `sync`, `code`, `cursor`, `zed`, `ports` and `open`. They also choose what
  `avr init` proposes for. `avr status`, `avr stop --all`, `avr ports --all`
  and `avr destroy --all` or `--orphaned` act across environments instead.
- `--env`, `--env-file` and `--ssh-agent` apply only to a shell or a one-shot
  command.
- `--native-fs` applies to a shell, a one-shot command and the editor
  commands.

### Mistakes avar refuses

Each of these exits with status 2 and changes nothing:

| Command line | Why |
| --- | --- |
| `avr --arhc amd64` | A token starting with `-` in the flag position that is not an avar flag. The message suggests `avr --help`, or `avr -- --arhc ...` to pass it to Linux |
| `avr --distro arch` | A distribution avar does not know |
| `avr --distro fedora:` | A colon with no version |
| `avr --arch` | A flag that needs a value, at the end of the line |
| `avr --isolate=maybe` | Not a boolean |
| `avr --isolate --shared` | The two contradict each other |
| `avr --ssh-agent` on Windows | avar cannot forward your SSH agent there yet. It says so rather than starting a session without the agent |

A release that is not in the environment matrix, such as `--distro fedora:41`,
passes the grammar and is refused a moment later when avar resolves the
environment, also with status 2.

## Reserved names

The first word after avar's flags decides everything. These names are avar's
subcommands, so they never reach Linux as the start of a command:

<!-- generated:reserved-names:begin — written by `make docs` from cli.Subcommands(); edit internal/cli/grammar.go, not this section -->

`code` `cursor` `destroy` `help` `init` `internal` `isolate` `open` `ports` `reset` `restore` `snapshot` `status` `stop` `sync` `version` `zed`

<!-- generated:reserved-names:end -->

`internal` runs avar's own scheduled idle check and is not a command you run.

To run a program in Linux whose name is on that list, put `--` first:

```sh
avr -- sync          # the guest's sync(1), or your project's ./sync
avr -- open file.txt # a program called open inside Linux
avr -- init          # a program called init inside Linux
```

The ones most likely to catch you are `sync`, a standard Unix command, and
`open`, a common name for a project script. Without `--`, avar runs its own
command instead of yours.
