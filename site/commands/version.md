---
title: avr version
parent: Commands
---

# avr version
{: .no_toc }

Prints the version of avar you are running.

1. TOC
{:toc}

## Help

<!-- generated:help:begin — written by `make docs` from avr's help; edit cmd/root.go, not this section -->

This is exactly what `avr help version` or `avr version --help` prints.

{% raw %}
```text
Usage:
  avr version

Print the avr version. Also available as `avr --version`.

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```
{% endraw %}

<!-- generated:help:end -->

## Examples

```sh
avr version
avr --version
avr -v
```

## What it does

Prints one line to standard output, such as `avr v0.11.3`, and exits 0. Like
help, it touches no state and needs no backend.

`--version` and `-v` count only in avar's flag position. After a command's
name they belong to that command: `avr -- node -v` asks Node.js for its
version.

When reporting a problem, include this line.

## Exit status

| Status | When |
| --- | --- |
| `0` | Always, once the command line has been read |
| `2` | The command line could not be read |
