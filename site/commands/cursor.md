---
title: avr cursor
parent: Commands
---

# avr cursor
{: .no_toc }

Opens the current project in Cursor, attached to its Linux environment. It
works exactly like [avr code]({% link commands/code.md %}), with Cursor's
launcher in place of VS Code's.

1. TOC
{:toc}

## Help

<!-- generated:help:begin — written by `make docs` from avr's help; edit cmd/root.go, not this section -->

This is exactly what `avr help cursor` or `avr cursor --help` prints.

{% raw %}
```text
Usage:
  avr [selector flags] cursor

Open the current project in Cursor attached to its Linux environment: over Remote-SSH on macOS, through Cursor's WSL integration on Windows. Needs the `cursor` command on your PATH.

`cursor` is an avar command, so it does not reach the guest: to run a program called `cursor` in Linux, use `avr -- cursor`.

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```
{% endraw %}

<!-- generated:help:end -->

## Examples

```sh
avr cursor                    # this project, in its usual environment
avr --distro debian cursor    # this project, in Debian
avr -- cursor                 # a program called cursor inside Linux, not this command
```

## What it does

The same steps as [avr code]({% link commands/code.md %}#what-it-does):
find the `cursor` launcher first, prepare the environment as `avr` does, then
open Cursor on the current directory inside Linux.

| Host | Connection |
| --- | --- |
| macOS | Remote-SSH, through avar's SSH configuration and the one-time `Include` line described on the [avr code]({% link commands/code.md %}#ssh-configuration-on-macos) page |
| Windows | Cursor's WSL integration |

Cursor provides its own remote extensions. avar does not check for them.

## Exit status

The same as [avr code]({% link commands/code.md %}#exit-status).

## Errors

### cursor is not on your PATH

```text
avr: the `cursor` command was not found on your PATH (<cause>). Open Cursor, press Cmd+Shift+P and run "Shell Command: Install 'cursor' command in PATH", then open a new terminal.
```

That is the macOS hint. On Windows, re-run Cursor's installer with its "Add to
PATH" option. No environment is started. Exit status 1.

### takes no arguments

```text
avr: `avr cursor` takes no arguments, but got "<args>"; it opens the current project in Cursor attached to the target Linux environment
```

Exit status 2.
