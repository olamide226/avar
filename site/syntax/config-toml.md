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
| `idle_timeout` | string | A quoted duration with a unit, such as `"30m"`, `"2h"` or `"1h30m"`. `"0"` (or the number `0`, or a negative duration) turns idle stopping off | `"2h"` |

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

The file is read by the same strict reader as `.avr.toml`, and accepts the
same [subset of TOML]({% link syntax/avr-toml.md %}#what-the-reader-accepts).
Its keys are the two above and nothing else. A key avar does not know, a value
of the wrong kind, or TOML the reader does not support is an error, never a
setting quietly ignored:

```text
avr: /Users/you/.avr/config.toml line 2: unknown key "idle_timout": did you mean idle_timeout? config.toml understands idle_timeout, forward_env
     Nothing was started or changed. Fix the file and run the command again; until then idle auto-stop is paused, and `avr status`, `avr stop` and `avr destroy` still work
```

The message names the file, the line and the key, and says what to write
instead. Some examples of what it refuses:

| Line | Why, and what to write |
| --- | --- |
| `idle_timout = "0"` | A misspelt key: `idle_timeout = "0"` |
| `idle_timeout = 4h` | A duration needs quotes: `idle_timeout = "4h"` |
| `idle_timeout = 4` | A number has no unit: `idle_timeout = "4h"` or `"4m"` |
| `forward_env = "AWS_PROFILE"` | A single name is not a list: `forward_env = ["AWS_PROFILE"]` |
| `forward_env = ["AWS_PROFILE,GITHUB_TOKEN"]` | One quoted string per name: `forward_env = ["AWS_PROFILE", "GITHUB_TOKEN"]` |
| `distro = "fedora"` | Not a setting here yet: use a project's `.avr.toml` or `--distro` |
| `[defaults]` | Tables are not supported: every key is top-level |

While the file cannot be read:

- every command stops before it starts, creates or changes anything, and exits 1;
- `avr status`, `avr stop` and `avr destroy` print the same error and still
  run, so you can always see and release what is running;
- `avr help` and `avr version` work as usual;
- the background idle check stops nothing. It cannot tell what you meant, and
  guessing the two-hour default would stop environments you may have asked it
  to leave alone. It resumes once the file is fixed.

Earlier versions of avar read this file leniently and ignored what they could
not read. If a file that seemed to work is now refused, the line the message
names was either not being applied, or needs quotes or a unit.
