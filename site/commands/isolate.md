---
title: avr isolate
parent: Commands
---

# avr isolate
{: .no_toc }

Shows or changes whether the current project defaults to environments of its
own instead of the ones shared by every project.

1. TOC
{:toc}

## Help

<!-- generated:help:begin — written by `make docs` from avr's help; edit cmd/root.go, not this section -->

This is exactly what `avr help isolate` or `avr isolate --help` prints.

{% raw %}
```text
Usage:
  avr isolate [on | off [--yes]]

Show whether this project defaults to its own environment, or change that default. Turning isolation off offers to delete the isolated environment.

Flags:
  --yes   with `avr isolate off`, delete the isolated environment without asking

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```
{% endraw %}

<!-- generated:help:end -->

## Examples

```sh
avr isolate              # does this project default to its own environment?
avr isolate on           # from now on, yes
avr isolate off          # back to shared; offers to delete the isolated machine
avr isolate off --yes    # the same, deleting it without asking
```

## What it does

`avr isolate` works on the project that contains the current directory. The
project must already exist, which it does once you have run `avr` there.
Selector flags make no difference to it.

| Form | What happens |
| --- | --- |
| `avr isolate` | Prints whether the project defaults to its own environment, and the command that changes it |
| `avr isolate on` | Records that the project defaults to its own environment. Nothing is created until the next `avr` |
| `avr isolate off` | Records that the project uses the shared environments again, then deals with the isolated machine (below) |

`avr --isolate` on any command records the same default as `avr isolate on`.
`avr --shared` uses the shared environment for one invocation without
changing it.

### avr isolate off and the isolated machine

Turning isolation off does not delete anything by itself. avar first records
the change, then says the isolated machine still exists, and:

- with `--yes`, deletes it;
- in a terminal, asks `Delete <machine>? (y/N)` and deletes it only on `y` or
  `yes`;
- with no terminal and no `--yes`, leaves it and prints the command that
  deletes it.

If deleting fails, the project stays un-isolated, and running
`avr isolate off` again retries the deletion.

The machine in question is the isolated one for the project's default
environment: the distribution and architecture its `.avr.toml` or avar's
defaults choose. If you also used the project with `avr --isolate --distro
<other>`, that environment is not deleted here. `avr status` lists it.

## Exit status

| Status | When |
| --- | --- |
| `0` | Shown, changed, already in the requested state, or you declined the deletion |
| `1` | No project is recorded for this directory, the change could not be saved, or deleting the machine failed |
| `2` | An argument other than `on` or `off`, anything after `on`, or anything other than `--yes` after `off` |

## Errors

```text
avr: no avar project is recorded for <directory> yet — run `avr` first to create one
```

`on` and `off` need a project. Plain `avr isolate` prints a friendlier
version of the same message and exits 0. Exit status 1.

```text
avr: `avr isolate` does not understand "<arg>": it takes `on`, `off`, or no arguments to show the current isolation status
```

Exit status 2.

## See also

- [One shared environment by default]({% link design.md %}#one-shared-environment-by-default-isolation-is-opt-in), for why
- [avr destroy]({% link commands/destroy.md %}) `--orphaned`, for isolated environments whose project is gone
