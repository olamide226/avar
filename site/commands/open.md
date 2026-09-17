---
title: avr open
parent: Commands
---

# avr open
{: .no_toc }

Opens `http://localhost:<port>` in your default browser, if the current
environment forwards that port to your computer. If it does not, avar says
why and opens nothing.

1. TOC
{:toc}

## Help

<!-- generated:help:begin — written by `make docs` from avr's help; edit cmd/root.go, not this section -->

This is exactly what `avr help open` or `avr open --help` prints.

{% raw %}
```text
Usage:
  avr [selector flags] open <port>

Open http://localhost:<port> in your default browser, if the selected environment forwards that port to this computer. If it does not — nothing is listening there, the environment is not running, or the port could not be forwarded — avar says so and opens nothing. The environment is never started to find out.

Note that `open` is an avar command, so it does not reach the guest: to run a command called `open` inside Linux, run `avr -- open`.

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```
{% endraw %}

<!-- generated:help:end -->

## Examples

```sh
avr npm run dev               # start a server in Linux
avr open 3000                 # then, from another terminal, open it
avr --distro fedora open 8080 # a port forwarded from the Fedora environment
avr -- open file.txt          # a program called open inside Linux, not this command
```

## What it does

`avr open` checks the port against the same information
[avr ports]({% link commands/ports.md %}) shows, and never starts or creates
an environment to find out. The port is the one on your computer, as `avr
ports` lists it.

When the port is forwarded, avar opens it and prints, for example,
`Opened http://localhost:3000 in your browser, served by node server.js (pid 4121).`

When it is not, the message names the reason:

| Reason | What to do |
| --- | --- |
| There is no environment yet | Start your server with `avr <command>`, then run `avr open` again |
| The environment is not running | The same |
| Nothing inside it is listening on that port | Check `avr ports`, or `avr ports --all` when other environments are running |
| A process is listening but the port cannot be reached from your computer | The message gives the backend's reason |

On macOS the browser is opened with `open`. On Windows it is opened through
the shell's default handler for web addresses.

## Exit status

| Status | When |
| --- | --- |
| `0` | The address was opened |
| `1` | The port is not forwarded, or the browser could not be opened |
| `2` | No port, more than one argument, a port outside 1 to 65535, or an unsupported environment |

## Errors

### not forwarded

```text
avr: port 3000 is not forwarded from <environment>: nothing inside it is listening on that port. Run `avr ports` to see the ports that are
```

Exit status 1.

### not a port number

```text
avr: `avr open` needs a port number between 1 and 65535, but got "abc"
```

Exit status 2.

### the browser could not be opened

```text
avr: opening http://localhost:3000 in your browser: <cause>; open that address in a browser yourself
```

Exit status 1.
