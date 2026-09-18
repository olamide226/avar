---
title: avr snapshot
parent: Commands
---

# avr snapshot
{: .no_toc }

Lists the snapshots held for the current environment, or captures a new one
with a name. A snapshot preserves the packages and files inside Linux so that
[avr restore]({% link commands/restore.md %}) can bring them back.

1. TOC
{:toc}

## Help

<!-- generated:help:begin — written by `make docs` from avr's help; edit cmd/root.go, not this section -->

This is exactly what `avr help snapshot` or `avr snapshot --help` prints.

{% raw %}
```text
Usage:
  avr [selector flags] snapshot [name]

List snapshots for the selected environment, or capture one with a name. Snapshots are available only in environments whose backend supports them.

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```
{% endraw %}

<!-- generated:help:end -->

## Examples

```sh
avr snapshot                          # list this environment's snapshots
avr snapshot before-upgrade           # capture one
avr --arch amd64 snapshot clean       # capture one of the amd64 environment
```

## What it does

`avr snapshot` acts on the environment the selector flags and `.avr.toml`
resolve to. The environment must already exist; this command never creates
or starts one.

**With no name**, it lists the snapshots, one per line with the time each was
captured, or says there are none yet.

**With a name**, it captures one and prints how to restore it:

```text
Captured snapshot "before-upgrade" of Ubuntu 24.04 · arm64.

Restore it with: avr restore before-upgrade
```

### Which environments can be snapshotted

- **macOS.** Lima can snapshot only an emulated machine, so snapshots work in
  an environment for the architecture that is not the Mac's own, such as
  `avr --arch amd64` on Apple Silicon. A running environment is stopped for
  the snapshot and started again afterwards. In a native environment the
  command refuses, both to list and to capture.
- **Windows.** Every environment can be snapshotted. A snapshot name must
  start with a letter or digit, contain only letters, digits, `.`, `_` and
  `-`, and be at most 64 characters.

A name already used for this environment is refused.

## Exit status

| Status | When |
| --- | --- |
| `0` | Listed or captured |
| `1` | The environment cannot be snapshotted, does not exist, the name is taken or invalid, or the backend failed |
| `2` | More than one argument, or an unsupported environment |

## Errors

### does not support snapshots

```text
avr: <environment> does not support snapshots: it runs on Apple's virtualization framework, which cannot take them. `avr reset` returns it to a clean state, and an emulated environment (`avr --arch amd64`) can be snapshotted
```

macOS, native architecture. Exit status 1. On Windows the same line carries
the reason that applies there instead: an environment registered as WSL 1, for
example, gets the `wsl --set-version` command that converts it.

### too many arguments

```text
avr: `avr snapshot` takes at most one argument (the snapshot name), but got <N>: "<args>"
```

Exit status 2.

## See also

- [avr restore]({% link commands/restore.md %})
- [avr reset]({% link commands/reset.md %}), which works in every environment
