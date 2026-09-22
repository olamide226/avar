---
title: Commands
nav_order: 2
has_children: true
has_toc: false
---

# Commands
{: .no_toc }

`avr` with no command opens a Linux shell in the current directory, and
`avr <command>` runs one command there. Everything else is a management
command that runs on your computer. Each page below gives the command's help
text exactly as `avr help` prints it, then examples, what it exits with, and
the errors you are likely to meet.

<!-- generated:command-index:begin — written by `make docs` from avr's help; edit cmd/root.go, not this section -->

| Command | Usage |
| --- | --- |
| [`avr`]({% link commands/avr.md %}) | <code>avr [flags] [--] [command [args...]]</code> |
| [`avr code`]({% link commands/code.md %}) | <code>avr [selector flags] code</code> |
| [`avr cursor`]({% link commands/cursor.md %}) | <code>avr [selector flags] cursor</code> |
| [`avr destroy`]({% link commands/destroy.md %}) | <code>avr [selector flags] destroy [--all &#124; --orphaned] [--yes]</code> |
| [`avr help`]({% link commands/help.md %}) | <code>avr help [command]</code> |
| [`avr init`]({% link commands/init.md %}) | <code>avr [selector flags] init</code> |
| [`avr isolate`]({% link commands/isolate.md %}) | <code>avr isolate [on &#124; off [--yes]]</code> |
| [`avr open`]({% link commands/open.md %}) | <code>avr [selector flags] open &lt;port&gt;</code> |
| [`avr ports`]({% link commands/ports.md %}) | <code>avr [selector flags] ports [--all]</code> |
| [`avr reset`]({% link commands/reset.md %}) | <code>avr [selector flags] reset [--yes]</code> |
| [`avr restore`]({% link commands/restore.md %}) | <code>avr [selector flags] restore &lt;name&gt;</code> |
| [`avr snapshot`]({% link commands/snapshot.md %}) | <code>avr [selector flags] snapshot [name]</code> |
| [`avr status`]({% link commands/status.md %}) | <code>avr status</code> |
| [`avr stop`]({% link commands/stop.md %}) | <code>avr [selector flags] stop [--all]</code> |
| [`avr sync`]({% link commands/sync.md %}) | <code>avr [selector flags] sync [--to-host &#124; --to-guest] [--yes]</code> |
| [`avr update`]({% link commands/update.md %}) | <code>avr update</code> |
| [`avr version`]({% link commands/version.md %}) | <code>avr version</code> |
| [`avr zed`]({% link commands/zed.md %}) | <code>avr [selector flags] zed</code> |

<!-- generated:command-index:end -->

## Before any command

Two rules apply everywhere. The [Command line]({% link syntax/command-line.md %})
page covers them in full.

- **avar's flags come before the command.** `avr --distro fedora stop` stops
  the Fedora environment. In `avr stop --distro fedora`, `--distro` is an
  argument to `stop`, which refuses it.
- **Selector flags choose "this environment".** `--distro`, `--arch`,
  `--isolate` and `--shared` pick which environment a command acts on. See
  [Syntax]({% link syntax/index.md %}#which-environment-a-command-acts-on)
  for how they combine with `.avr.toml`.

## Exit statuses

| Status | Meaning |
| --- | --- |
| `0` | Success, including "nothing to do" |
| The command's own | `avr <command>` exits with whatever the command in Linux exited with |
| `1` | avar tried and failed, and printed why |
| `2` | avar could not read the command line, or the environment it names is not one avar supports. Nothing was changed |
| `130` | Interrupted while preparing an environment, or you declined a prompt that would restart an environment or apply `avr sync` |

`--help` or `-h` anywhere after a command shows that command's help and runs
nothing, so `avr reset --help` never resets anything.
