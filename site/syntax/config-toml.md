---
title: config.toml
parent: Syntax
nav_order: 3
---

# config.toml
{: .no_toc }

`config.toml` holds avar's settings for you, on this machine, across every
project. It is optional, and avar never creates it.

1. TOC
{:toc}

## Location

The file lives in avar's state directory:

| Host | Path |
| --- | --- |
| macOS | `~/.avr/config.toml` |
| Windows | `%LocalAppData%\avar\config.toml` |

Setting the `AVR_HOME` environment variable moves the whole state directory,
this file included, to the directory it names.

## Example

```toml
# ~/.avr/config.toml
forward_env = ["AWS_PROFILE", "GITHUB_TOKEN"]
idle_timeout = "4h"
```

## Keys

| Key | Type | Allowed values | Default when absent |
| --- | --- | --- | --- |
| `forward_env` | list of strings | Environment variable names, on one line or across several | Nothing forwarded |
| `idle_timeout` | string | A duration such as `"30m"`, `"2h"` or `"1h30m"`. `"0"`, or any duration that is not positive, turns idle stopping off | `"2h"` |

### forward_env

A standing grant. Each named host variable is forwarded into every guest
session, in every project, when the host has it. Unlike `forward_env` in a
project's `.avr.toml`, it needs no approval: this file is yours.

`--env-file` and `--env` on the command line override it for the same name.
See [what crosses into Linux]({% link syntax/index.md %}#what-crosses-into-linux).

### idle_timeout

How long an environment with no live `avr` session waits before avar stops
it. Durations use Go's syntax: a number followed by `h`, `m` or `s`, and
combinations such as `1h30m`.

avar checks every ten minutes, so an environment stops some minutes after its
timeout rather than exactly at it. The check is registered with the host's
scheduler (a launchd agent on macOS, a Task Scheduler task on Windows) when
avar creates an environment, and avar says so the first time, with the
command that removes it. Setting `idle_timeout = "0"` leaves the check registered but
makes it stop nothing.

## How the file is read

<!-- pending: config.toml is moving to the strict reader .avr.toml uses; when that merges, replace this section: typos become errors instead of being ignored -->

Today this file is read leniently, on the reasoning that a typo in your own
settings should not cost you a shell. A value avar cannot read is ignored
rather than reported:

- An `idle_timeout` that is not a duration means the two-hour default.
- A `forward_env` that is not a list forwards nothing.
- A key avar does not know is ignored.

If a variable you granted does not arrive in Linux, or environments stop
sooner than you set, check this file first.
