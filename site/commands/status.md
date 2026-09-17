---
title: avr status
parent: Commands
---

# avr status
{: .no_toc }

Shows every Linux environment avar manages: its state, its resources, the
projects it shares, the sessions attached to it and the ports it forwards.

1. TOC
{:toc}

## Help

<!-- generated:help:begin — written by `make docs` from avr's help; edit cmd/root.go, not this section -->

This is exactly what `avr help status` or `avr status --help` prints.

{% raw %}
```text
Usage:
  avr status

Show every Linux environment avar manages, including state, resources, sessions, and forwarded-port diagnostics.

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```
{% endraw %}

<!-- generated:help:end -->

## Examples

```sh
avr status
```

## What it does

`avr status` reads; it never creates, starts or stops anything. It always
covers every environment avar manages, so selector flags make no difference
to it.

With no environments yet it says so, and how to get one:

```text
avar is not managing any Linux environments yet.
```

Otherwise it prints one row per environment:

| Column | Shows |
| --- | --- |
| `ENVIRONMENT` | Distribution, release and architecture, such as `Ubuntu 24.04 · arm64` |
| `MODE` | `shared`, `this project` for an isolated environment, or `base image` for the pristine machine isolated environments are cloned from |
| `STATE` | What the backend reports, such as `running`, `stopped` or `broken` |
| `CPU`, `MEMORY` | The environment's allocation |
| `DISK` | Disk in use, or the most it can grow to when usage is unknown |
| `SESSIONS` | Live `avr` sessions attached to it |

Below the table, an environment that shares projects or forwards ports gets a
detail block listing each project directory, and, for a running environment,
each forwarded port. A port that is listening in Linux but unreachable from
your computer is listed with the reason. If avar cannot read an environment's
ports, that line says so and the rest of the output is unaffected.

## Exit status

| Status | When |
| --- | --- |
| `0` | The environments were listed, including when there are none |
| `1` | avar could not reach the backend (for example Lima is missing), or could not read its own records |
| `2` | `avr status` was given an argument |

## Errors

```text
avr: `avr status` takes no arguments, but got "<args>"; it shows every Linux environment avar manages
```

`avr status` takes no arguments. Exit status 2.

## See also

- [avr ports]({% link commands/ports.md %}) for one environment's ports in detail
- [avr stop]({% link commands/stop.md %})
