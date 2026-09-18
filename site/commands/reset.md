---
title: avr reset
parent: Commands
---

# avr reset
{: .no_toc }

Destroys the environment for the current directory and creates it again
from a clean OS. Everything installed inside Linux is lost. Your project
files are shared, never copied, so they are not touched.

1. TOC
{:toc}

## Help

<!-- generated:help:begin — written by `make docs` from avr's help; edit cmd/root.go, not this section -->

This is exactly what `avr help reset` or `avr reset --help` prints.

{% raw %}
```text
Usage:
  avr [selector flags] reset [--yes]

Recreate the selected environment from a clean OS. Host project files are never changed; confirmation is required unless --yes is supplied.

Flags:
  --yes   skip the confirmation prompt

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```
{% endraw %}

<!-- generated:help:end -->

## Examples

```sh
avr reset                   # asks you to type the environment's name first
avr --distro debian reset   # this project's Debian environment
avr reset --yes             # no confirmation, for scripts
```

## What it does

1. Resolves the environment the way `avr` would. If it does not exist yet,
   avar says there is nothing to reset and exits 0: reset does not create an
   environment from nothing.
2. Prints what will be destroyed: the environment, whether it is shared or
   this project's own, and every project directory it shares.
3. Asks you to confirm, unless you passed `--yes`:

   ```text
   Type "Ubuntu 24.04 · arm64" and press enter to reset, or anything else to cancel:
   ```

   Only the environment's exact name confirms. Anything else, including
   Ctrl-D, prints `Reset cancelled. Nothing was changed.` and exits with
   status 0.
4. Destroys the environment and creates a fresh one, then prints
   `Reset complete.`

A shared environment serves every project, so resetting it resets it for all
of them. The summary lists the projects it shares before you confirm.

Packages approved in the project's `.avr.toml` are installed again the next
time you enter the environment with `avr`.

{: .note }
> The confirmation has to be typed at a terminal. Without one, as in a script
> or a pipeline, avar prints the summary, changes nothing, and exits with
> status 1, even if the right name arrives on standard input. From a script,
> pass `--yes`.

On Windows, a Linux-native workspace made by `--native-fs` lives inside the
environment and is destroyed with it. Run `avr sync --to-host` first if it
holds work you want.

## Exit status

| Status | When |
| --- | --- |
| `0` | The environment was reset, you cancelled, or there was no environment to reset |
| `1` | Destroying or re-creating the environment failed, or there was no terminal to confirm at and `--yes` was not given |
| `2` | An argument other than `--yes`, or an unsupported environment |

## Errors

```text
avr: `avr reset` does not understand "<arg>": it takes no arguments except --yes to skip confirmation
```

Selector flags go before `reset`. Exit status 2.

```text
avr: `avr reset` asks you to type a confirmation first, and there is no terminal to type it at. Nothing was changed. Run it from a terminal, or add --yes to go ahead without confirming
```

Standard input is not a terminal. Exit status 1, and nothing is changed.

## See also

- [avr destroy]({% link commands/destroy.md %}) to remove the environment without re-creating it
- [avr snapshot]({% link commands/snapshot.md %}) and [avr restore]({% link commands/restore.md %}) to go back to a saved state instead of a clean one
