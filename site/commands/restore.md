---
title: avr restore
parent: Commands
---

# avr restore
{: .no_toc }

Returns the current environment to a snapshot captured with
[avr snapshot]({% link commands/snapshot.md %}). Changes made inside Linux
since that snapshot are lost. Your project files are shared, not part of the
snapshot, and not touched.

1. TOC
{:toc}

## Help

<!-- generated:help:begin — written by `make docs` from avr's help; edit cmd/root.go, not this section -->

This is exactly what `avr help restore` or `avr restore --help` prints.

{% raw %}
```text
Usage:
  avr [selector flags] restore <name>

Restore the selected environment from a named snapshot. Snapshots are available only in environments whose backend supports them.

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```
{% endraw %}

<!-- generated:help:end -->

## Examples

```sh
avr snapshot                       # see which snapshots exist
avr restore before-upgrade         # go back to one
avr --arch amd64 restore clean     # restore the amd64 environment
```

## What it does

`avr restore` acts on the environment the selector flags and `.avr.toml`
resolve to, and restores it to the named snapshot. **It does not ask for
confirmation.**

It prints `Restoring <environment> to snapshot "<name>"…` on standard error
while it works, and `Restored <environment> to snapshot "<name>".` when done.

- **macOS.** Only an emulated environment has snapshots (see
  [avr snapshot]({% link commands/snapshot.md %}#which-environments-can-be-snapshotted)).
  A running environment is stopped, restored, and started again.
- **Windows.** The distribution is replaced by the snapshot's copy. If a
  restore is interrupted, running the same command again finishes it.

## Exit status

| Status | When |
| --- | --- |
| `0` | Restored |
| `1` | No snapshot has that name, the environment cannot be snapshotted, or the backend failed |
| `2` | No name, more than one argument, or an unsupported environment |

## Errors

### snapshot does not exist

```text
avr: snapshot does not exist: <details>
Available snapshots for <environment>: <names>
```

When there are none at all, the message says so instead of listing them.
Exit status 1.

### missing name

```text
avr: `avr restore` needs the name of a snapshot to restore; run `avr snapshot` to see the ones available for this environment
```

Exit status 2.

### does not support snapshots

On macOS, a native-architecture environment refuses with the same message as
[avr snapshot]({% link commands/snapshot.md %}#does-not-support-snapshots).
Exit status 1.

## See also

- [avr snapshot]({% link commands/snapshot.md %})
- [avr reset]({% link commands/reset.md %})
