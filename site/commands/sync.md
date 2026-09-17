---
title: avr sync
parent: Commands
---

# avr sync
{: .no_toc }

Shows what differs between a project's copy on your computer and the
Linux-native copy `avr --native-fs` keeps, and applies one side's changes to
the other when you ask. Windows only.

1. TOC
{:toc}

## Help

<!-- generated:help:begin — written by `make docs` from avr's help; edit cmd/root.go, not this section -->

This is exactly what `avr help sync` or `avr sync --help` prints.

{% raw %}
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
{% endraw %}

<!-- generated:help:end -->

## Examples

```sh
avr sync                    # review; changes nothing
avr sync --to-host          # bring Linux-side changes back to Windows, after asking
avr sync --to-guest         # send Windows-side changes into the Linux copy
avr sync --to-host --yes    # apply without asking, once you have reviewed
avr -- sync                 # the guest's own sync(1), not this command
```

## What it does

`avr sync` acts on the environment the selector flags and `.avr.toml`
resolve to. It starts the environment if it is stopped, then compares the two
copies by content against what they last agreed on, so it can tell which side
changed each file.

**With no direction**, it prints both copies' locations and, for each
direction with changes, the files that would change there, then:

```text
Nothing has been changed. Run `avr sync --to-host` or `avr sync --to-guest` to apply one of these.
```

**With `--to-host` or `--to-guest`**, it lists the changes for that direction
and asks `Apply these changes to <destination>? (y/N)`. Only `y` or `yes`
applies them. `--yes` skips the question. Without a terminal and without
`--yes`, it lists the changes and stops.

**When both copies changed the same file**, it lists each conflict, applies
nothing in either direction, and exits 1, even when you only asked for a
review. avar does not pick a side: make the copies agree, or delete one side's
version, and run `avr sync` again.

Some things are never synchronized:

- build output directories such as `node_modules`, `target` and `__pycache__`,
  which stay in Linux;
- empty directories;
- symbolic links and other entries that are not regular files, which avar
  names on standard error rather than dropping quietly.

On macOS the project is shared at native speed, there is no Linux-native copy,
and `avr sync` is refused before starting anything.

See [--native-fs and avr sync]({% link platforms.md %}#native-fs-and-avr-sync)
for when to use a Linux-native copy at all.

## Exit status

| Status | When |
| --- | --- |
| `0` | Reviewed, applied, or already synchronized |
| `1` | Both copies changed a file, there is no Linux-native copy yet, changes need confirming and there is no terminal, the command was refused on macOS, or the backend failed |
| `2` | `--to-host` with `--to-guest`, an unknown argument, or an unsupported environment |
| `130` | You answered no at the prompt |

## Errors

### no Linux-native copy yet

```text
avr: <project> has no Linux-native copy yet, so there is nothing to synchronize; run `avr --native-fs` in this project to create one
```

Exit status 1.

### changes need confirming

```text
avr: applying this would change <destination>; run it in an interactive terminal, or pass --yes once you have reviewed the list above
```

Exit status 1.

### opposite directions

```text
avr: --to-guest and --to-host are opposite directions; run one, review the result, then run the other if you still want to
```

Exit status 2.

### on macOS

```text
avr: this environment reaches your project directly rather than across a filesystem boundary, so it has no Linux-native workspace to keep; run avr without --native-fs
```

Exit status 1.
