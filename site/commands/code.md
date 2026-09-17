---
title: avr code
parent: Commands
---

# avr code
{: .no_toc }

Opens the current project in VS Code, attached to its Linux environment, so
the editor's terminal, extensions and language servers run in Linux.
[avr cursor]({% link commands/cursor.md %}) and
[avr zed]({% link commands/zed.md %}) do the same for Cursor and Zed.

1. TOC
{:toc}

## Help

<!-- generated:help:begin — written by `make docs` from avr's help; edit cmd/root.go, not this section -->

This is exactly what `avr help code` or `avr code --help` prints.

{% raw %}
```text
Usage:
  avr [selector flags] code

Open the current project in VS Code attached to its Linux environment: over Remote-SSH on macOS, through VS Code's WSL integration on Windows. Needs the `code` command on your PATH.

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```
{% endraw %}

<!-- generated:help:end -->

## Examples

```sh
avr code                     # this project, in its usual environment
avr --distro fedora code     # this project, in Fedora
avr --isolate code           # this project's own environment
avr --native-fs code         # Windows: the Linux-native copy of the project
```

## What it does

1. Looks for the `code` command on your `PATH`, before anything slow, so a
   missing launcher is reported at once rather than after provisioning.
2. Resolves the environment from the selector flags and `.avr.toml`, asks
   about any unapproved packages or variables in `.avr.toml`, then creates or
   starts the environment and shares the project into it, exactly as `avr`
   does.
3. Opens VS Code on the current directory inside Linux, and prints
   `avr: opened <project> in VS Code on <environment>`.

How VS Code reaches the environment depends on the host:

| Host | Connection | VS Code needs |
| --- | --- | --- |
| macOS | Remote-SSH, to a host entry avar writes | The Remote - SSH extension |
| Windows | VS Code's WSL integration | The WSL extension |

avar does not check that the extension is installed. Without it, VS Code
opens but cannot connect.

### SSH configuration on macOS

avar writes the connection details for each environment to its own file,
`~/.avr/ssh/config`, and never edits entries of yours. For VS Code to find
them, your `~/.ssh/config` has to include that file. The first time it does
not, avar asks:

```text
avr: VS Code finds hosts through ~/.ssh/config, which does not yet include avar's.
     avar would add one line to the top of it and change nothing else:

       Include ~/.avr/ssh/config
```

Only `y` or `yes` adds the line, with a comment saying avar added it and how
to remove it. Declining is not a failure: VS Code still opens, and avar
prints the line to add yourself. Without a terminal avar does not ask, and
prints the line instead. When an environment is destroyed or reset, avar
removes its entry from its own file.

The paths above are the macOS defaults; avar shows the real paths it uses.

## Exit status

| Status | When |
| --- | --- |
| `0` | VS Code was launched, including when you declined the `Include` line |
| `1` | `code` is not on your `PATH`, the environment could not be prepared, `--native-fs` was refused, or the launcher failed |
| `2` | `avr code` was given an argument, or an unsupported environment |

## Errors

### code is not on your PATH

```text
avr: the `code` command was not found on your PATH (<cause>). Open VS Code, press Cmd+Shift+P and run "Shell Command: Install 'code' command in PATH"
```

The hint differs by host. On Windows, re-run the VS Code installer with its
"Add to PATH" option. No environment is started. Exit status 1.

### takes no arguments

```text
avr: `avr code` takes no arguments, but got "<args>"; it opens the current project in VS Code attached to the target Linux environment
```

`avr code` always opens the current directory. Selector flags go before
`code`. Exit status 2.

### the launcher failed

```text
avr: launch VS Code with `code <arguments>`: <exit status>
```

The launcher's own error output is shown above it. Exit status 1.
