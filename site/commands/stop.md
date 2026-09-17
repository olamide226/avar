---
title: avr stop
parent: Commands
---

# avr stop
{: .no_toc }

Stops the environment for the current directory, or with `--all`, every
environment avar manages, to give their memory back. Nothing inside them is
lost, and the next `avr` starts them again.

1. TOC
{:toc}

## Help

<!-- generated:help:begin — written by `make docs` from avr's help; edit cmd/root.go, not this section -->

This is exactly what `avr help stop` or `avr stop --help` prints.

{% raw %}
```text
Usage:
  avr [selector flags] stop [--all]

Stop the selected environment, or every Linux environment avar manages.

Flags:
  --all   stop every Linux environment avar manages

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```
{% endraw %}

<!-- generated:help:end -->

## Examples

```sh
avr stop                   # the environment this directory uses
avr --distro fedora stop   # this project's Fedora environment
avr stop --all             # every environment avar manages
```

## What it does

**Without `--all`**, avar resolves the environment the way `avr` would, from
the selector flags and the project's `.avr.toml`, then:

| The environment is | avar prints | Exit |
| --- | --- | --- |
| not created yet | `There is no <environment> environment yet, so there is nothing to stop.` | `0` |
| already stopped | `<environment> is already stopped.` | `0` |
| running | `Stopping <environment>…` then `Stopped <environment>. Run `avr` to start it again.` | `0` |
| in another state, such as broken | `<environment> is <state>, so avar left it alone. Run `avr status` to see what its backend says about it.` | `0` |

Stopping an already stopped environment still clears up anything its backend
left running for it.

**With `--all`**, avar goes through every environment it manages, including
the base images isolated environments are cloned from. It stops the running
ones, skips the stopped ones, leaves any in another state alone and names
them on standard error, and ends with a summary such as
`Stopped 2 Linux environments.` It tries every environment even when one
fails.

`avr stop` never starts or creates an environment.

On macOS, an environment that does not shut down cleanly is ended, with a
warning that unsaved work inside it may be lost. Your project files on the
Mac are not affected.

Environments also stop on their own after the
[idle timeout]({% link syntax/config-toml.md %}#idle_timeout), so forgetting
`avr stop` costs nothing.

## Exit status

| Status | When |
| --- | --- |
| `0` | Stopped, or nothing needed stopping |
| `1` | The backend failed to stop an environment (with `--all`, at least one), or the environment is not one avar has a record of creating |
| `2` | An argument other than `--all`, or an unsupported environment |

## Errors

### does not understand

```text
avr: `avr stop` does not understand "--distro": it takes no arguments, or --all to stop every Linux environment avar manages
```

Selector flags go before `stop`: `avr --distro fedora stop`. Exit status 2.

### no record of creating

```text
avr: stopping <machine>: <cause>. avar will not act on a machine it has no record of creating; run `avr` to let avar adopt or clean it up, or stop it with your virtualization tool directly
```

avar acts only on machines it created and recorded. Exit status 1.

## See also

- [avr status]({% link commands/status.md %})
- [avr destroy]({% link commands/destroy.md %}) to remove an environment instead
