---
title: avr ports
parent: Commands
---

# avr ports
{: .no_toc }

Lists the ports the current environment forwards to your computer, the
address to open each at, and the Linux process listening on it. With `--all`,
it does the same for every running environment.

1. TOC
{:toc}

## Help

<!-- generated:help:begin — written by `make docs` from avr's help; edit cmd/root.go, not this section -->

This is exactly what `avr help ports` or `avr ports --help` prints.

{% raw %}
```text
Usage:
  avr [selector flags] ports [--all]

List the ports the selected environment forwards to this computer, with the address to open each at and the Linux process listening on it where avar can tell. Ports that are listening in Linux but cannot be reached from this computer are listed with the reason. The environment is never started to find out.

Flags:
  --all   list the ports of every running Linux environment avar manages

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```
{% endraw %}

<!-- generated:help:end -->

## Examples

```sh
avr ports                     # the environment this directory uses
avr --distro fedora ports     # this project's Fedora environment
avr ports --all               # every running environment
```

## What it does

`avr ports` reads; it never creates or starts an environment. When the
environment does not exist or is not running, it says so, and how to start
it, and exits 0.

For a running environment it prints a table:

```text
Ubuntu 24.04 · arm64 forwards 1 port to this computer:

  PORT  ADDRESS                PROCESS
  3000  http://localhost:3000  node server.js (pid 4121)
```

`PORT` and `ADDRESS` are where to reach the port on your computer. `PROCESS`
is the command line and process ID listening in Linux, where avar can tell,
and `—` where it cannot.

A port that is listening in Linux but cannot be reached from your computer is
listed below the table under
`Listening in Linux but not reachable from this computer:`, with the reason.
On macOS the reason is Lima's, for example that the port on your Mac is
already in use by another program. On Windows it usually means localhost
forwarding is turned off in `.wslconfig`, or another program holds the port.

When nothing in the environment is listening, `avr ports` says so. A server
you start with `avr <command>` appears once it is listening.

**With `--all`**, selector flags make no difference: every running
environment avar manages is listed, one after another. If avar cannot read
one of them, it says so in that environment's place, lists the rest, and
exits 1 at the end.

## Exit status

| Status | When |
| --- | --- |
| `0` | Listed, including when the environment is not running or nothing is listening |
| `1` | avar could not read an environment's ports, or reach the backend |
| `2` | An argument other than `--all`, or an unsupported environment |

## Errors

```text
avr: `avr ports` does not understand "<arg>": it takes no arguments, or --all to list the ports of every running Linux environment
```

Exit status 2.

```text
avr: listing the ports forwarded from <environment>: <cause>; `avr status` shows whether the environment is healthy
```

Exit status 1.

## See also

- [avr open]({% link commands/open.md %}) to open one of the listed ports in your browser
- [avr status]({% link commands/status.md %}), which shows every environment's ports alongside its state
