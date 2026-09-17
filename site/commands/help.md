---
title: avr help
parent: Commands
---

# avr help
{: .no_toc }

Shows avar's help, or one command's. Help never runs the command it
describes.

1. TOC
{:toc}

## Help

<!-- generated:help:begin — written by `make docs` from avr's help; edit cmd/root.go, not this section -->

This is exactly what `avr help help` or `avr help --help` prints.

{% raw %}
```text
Usage:
  avr help [command]

Show avr's help, or help for one public management command.
```
{% endraw %}

<!-- generated:help:end -->

## Examples

```sh
avr help               # avar's help: the grammar, every command, every flag
avr help reset         # one command's help
avr reset --help       # the same; reset does not run
avr --help             # the same as avr help
avr -- help            # a program called help inside Linux
```

## What it does

`avr help`, `avr --help` and `avr -h` print avar's general help. It is the
text on the [avr]({% link commands/avr.md %}) page.

`avr help <command>` prints one command's usage, description and flags, the
text at the top of each command page. So does `--help` or `-h` anywhere
after a command's name: `avr destroy --all --help` shows help for
`avr destroy` and destroys nothing.

Help touches nothing: no state, no backend, no environment. It works before
Lima or WSL is installed.

## Exit status

| Status | When |
| --- | --- |
| `0` | Help was printed |
| `2` | The command name is not an avar command, or more than one name was given |

## Errors

```text
avr: unknown command "<name>"; run `avr --help` to see avar's commands
```

Exit status 2.

```text
avr: `avr help` takes at most one command name, got ["<name>" "<name>"]
```

Exit status 2.
