---
title: avr destroy
parent: Commands
---

# avr destroy
{: .no_toc }

Removes environments and everything inside them, after confirmation. Your
project files are shared, never copied, so they are not touched.

1. TOC
{:toc}

## Help

<!-- generated:help:begin — written by `make docs` from avr's help; edit cmd/root.go, not this section -->

This is exactly what `avr help destroy` or `avr destroy --help` prints.

{% raw %}
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
{% endraw %}

<!-- generated:help:end -->

## Examples

```sh
avr destroy                     # the environment this directory uses
avr --distro fedora destroy     # this project's Fedora environment
avr destroy --orphaned          # isolated environments whose project folder is gone
avr destroy --all               # every environment avar manages
avr destroy --all --yes         # the same, with no confirmation
```

## What it does

`avr destroy` chooses what to remove, shows it, asks, then removes it.

| Form | Removes | Confirm by typing |
| --- | --- | --- |
| `avr destroy` | The environment the selector flags and `.avr.toml` resolve to for this directory | The environment's name, such as `Ubuntu 24.04 · arm64` |
| `avr destroy --orphaned` | Isolated environments whose project directory no longer exists. Shared environments are never included | `yes` |
| `avr destroy --all` | Every environment avar manages, including the base images isolated environments are cloned from | `all` |

Before asking, avar lists each environment with its mode, every project
directory it shares, and how many `avr` sessions are attached to it right
now. `--yes` skips the question but not the list.

Anything other than the exact text cancels with `Nothing was destroyed.` and
exit status 0. So does standard input ending before a line is read. avar
reads the confirmation from standard input whether or not it is a terminal,
so from a script use `--yes`.

Environments are removed one at a time. If one fails, avar stops there and
reports it; the ones already removed stay removed. For each environment it
removes, avar also forgets its record and its SSH entry for the editor
commands.

On Windows, a Linux-native workspace made by `--native-fs` lives inside the
environment and is destroyed with it. Run `avr sync --to-host` first if it
holds work you want.

## Exit status

| Status | When |
| --- | --- |
| `0` | The environments were removed, you cancelled, or there was nothing to remove |
| `1` | Removing an environment failed |
| `2` | An unknown argument, `--all` with `--orphaned`, or an unsupported environment |

## Errors

```text
avr: `avr destroy` cannot take both --all and --orphaned: --all removes every environment, --orphaned removes only those whose project directory is gone
```

```text
avr: `avr destroy` does not understand "<arg>": it takes --all, --orphaned, and --yes to skip confirmation
```

Both exit with status 2 and remove nothing.

```text
There is no environment for this directory, so there is nothing to destroy.
```

Not an error: exit status 0. `avr status` lists the environments that exist.

## See also

- [avr reset]({% link commands/reset.md %}) to get a clean environment back in one step
- [avr isolate]({% link commands/isolate.md %}) `off`, which offers to delete a project's isolated environment
