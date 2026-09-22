---
title: Troubleshooting
nav_order: 7
---

# Troubleshooting
{: .no_toc }

avar's errors are written to say what it was doing, why that failed, and what
to do next, so the message itself is usually the fastest answer. This page
collects the ones people meet most, with the reasoning behind each. Quoted
messages are avar's own wording, with the parts that vary shown as
`<placeholders>`.

1. TOC
{:toc}

## Exit statuses

| Status | Meaning |
| --- | --- |
| The guest command's own | `avr <command>` exits with whatever the command in Linux exited with. A failing test run is not an avar error |
| `2` | avar could not read the command line: an unknown flag, a missing value, or a `--distro` or `--arch` it does not support |
| `130` | You interrupted avar while it was preparing an environment, or answered no when asked to restart an environment other sessions were using, or to apply an `avr sync` |
| `1` | Anything else avar reports as a failure |

## Setting up

### Lima is not installed, or is too old (macOS)

```text
avr: Lima is not installed, and avar is not running on a terminal, so it cannot ask for permission to install it.
```

```text
avr: Lima <version> at <path> is too old: avar needs Lima 2.0.0 or newer.
```

avar needs Lima 2.0.0 or newer. With Lima missing, `avr` run from a terminal
offers `brew install lima` and waits for a yes. From a script, or without
Homebrew, it stops and prints the command to run instead. It never upgrades
an old Lima for you: run `brew upgrade lima`, then `avr` again.

### WSL 2 is not usable (Windows)

```text
avr: WSL 2 is not usable, and avar is not running on a terminal, so it cannot ask for permission to set it up.
```

```text
avr: WSL was set up, but Windows needs to restart before avar can use it.
```

avar needs WSL 2.0.0 or newer. From a terminal it offers
`wsl --install --no-distribution` or `wsl --update`, and acts only on a yes.
When the install needs a restart, restart Windows and run the same `avr`
command again. Nothing was created, so there is nothing to clean up first.

### An environment is registered as WSL 1 (Windows)

```text
avr: the environment <name> is registered as WSL 1, and avar needs WSL 2.
```

Run the `wsl --set-version <name> 2` command the message names, then `avr`
again. avar does not convert it for you, because the conversion rewrites the
whole filesystem and takes several minutes.

## Choosing an environment

### "avar cannot run …"

```text
avr: avar cannot run <distro> <version>: unsupported environment; supported <distro> versions are <versions> (omit the version to use <default>)
```

The distribution, release or architecture is not in the
[environment matrix]({% link environments.md %}). avar exits with status 2
before doing any work. When the value came from the project's `.avr.toml`
rather than a flag, the message ends by naming the file, for example
`(distro is set in /Users/you/code/app/.avr.toml)`.

On Windows, `--arch` for an architecture that is not the machine's own is
also refused before anything is downloaded, because WSL 2 cannot emulate
another processor. The message names the one architecture that host
supports.

### "The term 'avar' is not recognized" (Windows)

```text
avar : The term 'avar' is not recognized as the name of a cmdlet, function, script file, or operable program.
```

avar answers to `avr` and to `avar`, but Windows packages before 0.12.13
shipped only `avr.exe`. Upgrade with `winget upgrade olamide226.avar`, or
download the newer archive and keep `avar.exe` beside `avr.exe` in the folder
you put on your `PATH`; open a new terminal afterwards. Until then, use `avr`,
which is the canonical name everywhere in avar's own output.

### A command in my project has the same name as an avar command

`avr sync`, `avr open`, `avr init` and the other
[reserved names](https://github.com/olamide226/avar/blob/main/README.md#reserved-command-names)
run avar's command, not yours. Put `--` first to force the Linux reading:

```sh
avr -- sync
avr -- open file.txt
```

## Project configuration

### An error naming .avr.toml and a line

```text
avr: /Users/you/code/app/.avr.toml line 3: unknown key "packges": did you mean packages? .avr.toml understands <keys>
```

The reader is strict, and a file it cannot read completely stops the command
before any machine work. avar never applies part of a file. Fix the line
the message names; the [reader's rules]({% link syntax/avr-toml.md %}#what-the-reader-accepts)
list what it accepts. A key that is not a near miss of a known one says instead
that a key from a newer avar needs a newer `avr`.

### An error naming config.toml and a line

```text
avr: /Users/you/.avr/config.toml line 2: unknown key "idle_timout": did you mean idle_timeout? config.toml understands idle_timeout, forward_env
     Nothing was started or changed. Fix the file and run the command again; until then idle auto-stop is paused, and `avr status`, `avr stop` and `avr destroy` still work
```

Your own settings file is read as strictly as `.avr.toml`. Fix the line the
message names; [config.toml]({% link syntax/config-toml.md %}#how-the-file-is-read)
lists what each key accepts. Until you do, most commands stop before doing
anything, `avr status`, `avr stop` and `avr destroy` still work, and
environments are not stopped for being idle.

### "… which needs your approval first"

```text
avr: <path>/.avr.toml asks to <install packages or forward variables>, which needs your approval first.
     Run avr in <project> from a terminal to review it. Continuing without them.
```

The project's file lists `packages` or `forward_env` you have not approved,
and avar was run without a terminal, so it could not ask. It carried on
without them. Run `avr` in that directory from a terminal to review and
approve them. See [the approval model]({% link syntax/avr-toml.md %}#approval).

### My cpus or memory setting did nothing

avar says why once, when it first sees the setting. `cpus` and `memory` apply
only to the project's own environment, and only when that environment is
created: run `avr --isolate` to give the project its own, or `avr reset` to
recreate an existing one at the declared size. On Windows the setting cannot
apply, because every WSL distribution shares one allocation.

### avr init will not write a file

```text
avr: <path>/.avr.toml already exists, and avr init only writes a new one; nothing was changed. Edit it, or remove it and run avr init again
```

```text
avr: nothing was written: avr init writes .avr.toml only after you confirm, so run it from a terminal
```

`avr init` never replaces a file, and it writes only after an explicit yes
at a terminal.

## Files and projects

### "… would restart it while N other sessions are attached to it"

```text
avr: sharing <project> with your Linux environment would restart it while <N> other sessions are attached to it; run in an interactive terminal, or close the other sessions, and try again
```

A project avar has not shared with this environment yet was entered while
other `avr` sessions were using the environment. From a terminal, avar asks
first. Without one it stops. Close the other sessions, or run the command from
a terminal and accept the prompt.

### A project directory was unshared

```text
avr: this environment shares as many project directories as it can hold, so the least recently used one made room:
```

On macOS, an environment shares at most sixteen project directories. You do
not see this on Windows, which has no such limit. Nothing was deleted. Run `avr` in the named directory again to share it back. To stop
projects competing for one environment, give a busy project its own with
`avr isolate on`.

### --native-fs is refused on macOS

```text
avr: this environment reaches your project directly rather than across a filesystem boundary, so it has no Linux-native workspace to keep; run avr without --native-fs
```

On macOS the project is already shared at native speed, so there is nothing
for `--native-fs` to fix. Run `avr` without it. See
[Platform notes]({% link platforms.md %}#native-fs-and-avr-sync).

### avr sync reports files changed in both copies

```text
avr: <N> files have changed in both copies of <project> since they were last synchronized:
       <file>: <change> on the host, <change> in Linux
     avar will not overwrite either copy, so nothing has been changed.
```

avar does not pick a side. Make the two copies agree, or delete one side's
version of each listed file, then run `avr sync` again.

### avr sync says there is no Linux-native copy

```text
avr: <project> has no Linux-native copy yet, so there is nothing to synchronize; run `avr --native-fs` in this project to create one
```

`avr sync` works on the copy `avr --native-fs` creates. Run that first.

### "Access is denied" reading avar's own state (Windows)

```text
avr: read avar's project records to resolve C:\Users\you: read avar state file
C:\Users\you\AppData\Local\avar\projects.json: open ...: Access is denied.
```

One `avr` run from an elevated PowerShell creates avar's files owned by
`BUILTIN\Administrators`, and an ordinary shell afterwards cannot read them.
Current versions of avar name your account in the rules they set and repair the
directory themselves, but a file already owned by somebody else needs an
administrator once:

```powershell
icacls "$env:LOCALAPPDATA\avar" /grant "$($env:USERDOMAIN)\$($env:USERNAME):(OI)(CI)F" /T /C
```

Run avar as yourself, not elevated. It never needs administrator rights, and
`wsl --install` is the one step that does, which avar asks about before acting.

## Environments

### Snapshots are not supported

```text
avr: <environment> does not support snapshots: it runs on Apple's virtualization framework, which cannot take them. `avr reset` returns it to a clean state, and an emulated environment (`avr --arch amd64`) can be snapshotted
```

On macOS, Lima can snapshot only an emulated environment, and avar runs the
Mac's own architecture natively. Use `avr reset` for a clean state, or work in
an emulated environment if you need snapshots.

On Windows, the text after the colon is the reason that applies there. An
environment registered as WSL 1 gets the command that converts it to WSL 2.

### A console window flashes every few minutes (Windows)

avar 0.12.3 and earlier registered its idle check to run `avr.exe`, a console
program, so Windows opened a window each time the check ran. Upgrade, then run
any `avr` command that opens an environment: avar replaces the task with one
that runs `avrw.exe`, which opens no window. It ships beside `avr.exe` in the
Windows download and the winget package.

```text
avr: idle auto-stop is off: avrw.exe is not in the same folder as avr.exe.
```

avar found no `avrw.exe` beside `avr.exe`, so it registered nothing rather than
a task that opens a window, and removed one an earlier version registered. Put
`avrw.exe` from the same download next to `avr.exe`, and the next `avr` turns
idle auto-stop on. Until then, stop environments you are not using with
`avr stop`.

```text
avr: idle auto-stop is not set up, because avr is running from a temporary folder (…).
```

A scheduled check would outlive a binary in the temporary folder, so avar does
not register one. This happens when `avr.exe` is run straight out of a zip
archive, or built with `go run`. Install it somewhere permanent.

### My environment stopped by itself

Environments with no live session stop after two hours, so an environment you
forgot costs nothing. The next `avr` starts it again. Change the timeout, or
turn idle stopping off with `"0"`, through `idle_timeout` in
[config.toml]({% link syntax/config-toml.md %}#idle_timeout).

An editor window opened with `avr code`, `avr cursor` or `avr zed` counts as a
session while it is connected. The editor's remote server running on its own,
after its windows have closed, does not
([details]({% link commands/code.md %}#idle-auto-stop-while-the-editor-is-open)).

### My environment will not stop by itself

A connected editor window keeps it running, including a VS Code window whose
connection dropped without closing: VS Code keeps that window's session for
up to three hours so it can reconnect. Close the window, or run `avr stop`.

## Ports and editors

### "port N is not forwarded"

```text
avr: port 3000 is not forwarded from <environment>: nothing inside it is listening on that port. Run `avr ports` to see the ports that are
```

`avr open` opens a port only if the environment forwards it, and it never
starts an environment to find out. The message says which case applies: no
environment yet, the environment is not running, nothing is listening on the
port, or a process is listening but the port cannot be reached from the host,
with the reason. Start your server with `avr <command>`, then check
`avr ports`.

### An editor command is not found

```text
avr: the `cursor` command was not found on your PATH (<cause>). <how to install it>
```

`avr code`, `avr cursor` and `avr zed` launch the editor's own command-line
launcher, and check for it before starting any environment. The message says
how to put it on your `PATH` for that editor and host. On macOS, for example,
VS Code and Cursor install theirs from the command palette with
"Shell Command: Install 'code' command in PATH" (or `'cursor'`), and Zed with
"cli: install cli binary". Open a new terminal afterwards.

## Still stuck

`avr status` shows every environment avar manages, its state, its sessions and
its forwarded ports, which is usually enough to see what avar sees. If it is
not, [open an issue](https://github.com/olamide226/avar/issues) with the
command you ran, avar's full output, and `avr version`.
