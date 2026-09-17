---
title: avr zed
parent: Commands
---

# avr zed
{: .no_toc }

Opens the current project in Zed, attached to its Linux environment. It works
like [avr code]({% link commands/code.md %}), with Zed's launcher and Zed's
own ways of reaching Linux.

1. TOC
{:toc}

## Help

<!-- generated:help:begin — written by `make docs` from avr's help; edit cmd/root.go, not this section -->

This is exactly what `avr help zed` or `avr zed --help` prints.

{% raw %}
```text
Usage:
  avr [selector flags] zed

Open the current project in Zed attached to its Linux environment: through Zed's SSH remote development on macOS, and its WSL support on Windows. Needs the `zed` command on your PATH.

`zed` is an avar command, so it does not reach the guest: to run a program called `zed` in Linux, use `avr -- zed`.

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```
{% endraw %}

<!-- generated:help:end -->

## Examples

```sh
avr zed                       # this project, in its usual environment
avr --arch amd64 zed          # this project, in the amd64 environment
avr -- zed                    # a zed installed inside Linux, not this command
```

## What it does

The same steps as [avr code]({% link commands/code.md %}#what-it-does):
find the `zed` launcher first, prepare the environment as `avr` does, then
open Zed on the current directory inside Linux.

| Host | Connection |
| --- | --- |
| macOS | Zed's SSH remote development, opening an `ssh://` address for the environment. Zed uses the `ssh` on your `PATH` and your `~/.ssh/config`, so it needs the one-time `Include` line described on the [avr code]({% link commands/code.md %}#ssh-configuration-on-macos) page |
| Windows | Zed's WSL support, through its launcher's `--wsl` option. An older Zed without that option fails, and avar shows Zed's own error |

## Exit status

The same as [avr code]({% link commands/code.md %}#exit-status).

## Errors

### zed is not on your PATH

```text
avr: the `zed` command was not found on your PATH (<cause>). Open Zed, press Cmd+Shift+P and run "cli: install cli binary" to put `zed` on your PATH, then open a new terminal.
```

That is the macOS hint. On Windows, re-run Zed's installer with its "Add to
PATH" option, or add the `bin` folder of Zed's install directory to your
`PATH`. No environment is started. Exit status 1.

### takes no arguments

```text
avr: `avr zed` takes no arguments, but got "<args>"; it opens the current project in Zed attached to the target Linux environment
```

Exit status 2.

### the launcher failed

```text
avr: launch Zed with `zed <arguments>`: <exit status>
```

Zed's own error output is shown above it. Exit status 1.
