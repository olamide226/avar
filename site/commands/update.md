---
title: avr update
parent: Commands
---

# avr update
{: .no_toc }

Brings avar up to date: by replacing the binary itself, or by naming the one
command that updates the installation you have.

1. TOC
{:toc}

## Help

<!-- generated:help:begin — written by `make docs` from avr's help; edit cmd/root.go, not this section -->

This is exactly what `avr help update` or `avr update --help` prints.

{% raw %}
```text
Usage:
  avr update

Bring avar up to date.

If this avar was installed with Homebrew or with winget, that package manager owns the file and keeps its own record of the version it installed, so avar prints the one command that updates it — `brew upgrade --cask avar` or `winget upgrade olamide226.avar` — and changes nothing itself.

Otherwise avar updates itself: it asks github.com/olamide226/avar for the latest release, downloads the archive for this computer, checks it against the checksums that release published, and only then replaces this binary. On Windows every program the archive ships — `avr.exe`, `avar.exe` and the windowless `avrw.exe` that runs the background idle check — is replaced from that one archive or none of them is, and the files replaced are kept beside them until a later run removes them.

Nothing is downloaded or replaced unless you run this command: no other avar command asks whether a newer release exists. If anything fails, the avar you have now is still installed.

`update` is an avar command, so it does not reach the guest: to run a program called `update` in Linux, use `avr -- update`.

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```
{% endraw %}

<!-- generated:help:end -->

## Examples

```sh
avr update       # update avar, or print the command that does
avr -- update    # run a program called update inside Linux
```

## What it does

First it works out how this copy of avar was installed, from the path of the
running binary with its links resolved. What happens next depends on the
answer.

| How avar was installed | What `avr update` does |
| --- | --- |
| Homebrew cask (`brew install --cask olamide226/tap/avar`) | Prints `brew upgrade --cask avar`. Downloads nothing, changes nothing |
| winget (`winget install olamide226.avar`) | Prints `winget upgrade olamide226.avar`. Downloads nothing, changes nothing |
| A release archive you unpacked yourself | Updates avar itself, as below |

**avar does not run a package manager for you.** Homebrew and winget each keep
a record of the version they installed, and overwriting a file they own leaves
that record describing something that is not there. On Windows it could not
work anyway: `winget upgrade` replaces `avr.exe`, and Windows cannot replace
the file of a running program — winget started by `avr update` would be asked
to overwrite its own parent. Either command may also ask for administrator
approval, which is yours to give rather than avar's to arrange.

### Updating an archive install

1. avar asks `github.com/olamide226/avar` for its latest release. If it is not
   newer than the version you are running, avar says so and stops — nothing is
   downloaded.
2. It downloads the archive for this computer, and the `checksums.txt` that
   release published.
3. It compares the SHA-256 of what arrived with the checksum recorded for that
   exact file. **Nothing is unpacked before that comparison passes.** A file
   that was altered, one that arrived incomplete, and one the checksums do not
   mention are all refused the same way.
4. Only then does it unpack the new binary beside the installed one and rename
   it into place.

On Windows a running `avr.exe` cannot be overwritten, so each installed file is
renamed to `<name>.avr-old` first and the new one is renamed into the freed
name. Every program the archive ships moves together and from the same archive:
`avr.exe`, `avar.exe` (the same command under the product's name), and
`avrw.exe`, which runs the background idle check without opening a window — a
new `avr.exe` beside an older `avrw.exe` breaks idle auto-stop, and beside an
older `avar.exe` it leaves half your commands on the previous version. If any
step fails, everything is put back, and the avar you had is still installed.
The files left aside are deleted by a later run of avar, because the run that
made them may still have the old program open.

The `avr` you are running is the old one until it exits; the next `avr` you
type is the new version.

### Nothing happens unless you ask

No other avar command contacts the network, and none of them tells you a newer
release exists. That is deliberate: a check on the warm path would spend an
HTTPS round trip on every `avr` in a command avar promises to keep under half a
second.

### Unsigned binaries

Released binaries are unsigned. Because avar downloads the archive itself
rather than a browser, the file it installs carries no quarantine attribute on
macOS and no mark of the web on Windows, so neither Gatekeeper nor SmartScreen
stops it the way they may stop an archive you downloaded and unpacked by hand.

## Exit status

| Status | When |
| --- | --- |
| `0` | avar was updated, is already the latest release, or the command that updates it was printed |
| `1` | The release could not be read, the download did not match its checksum, or the binary could not be replaced |
| `2` | `avr update` was given an argument |

## Errors

```text
avr: download avar_<version>_darwin_all.tar.gz: it hashes to <a> and the release published <b>: the download does not match the checksum the release published for it. The download was discarded and avar was not changed
```

The archive that arrived is not the one the release published — it was altered
in transit, or it arrived incomplete. Nothing was unpacked. Exit status 1.

```text
avr: update this avr: "dev" is not a release version: a release version is MAJOR.MINOR.PATCH, such as 1.4.0. This is a build from source rather than a release, and replacing it with a release archive would swap it for a different program
```

A binary built from source is not one avar replaces. Exit status 1.

```text
avr: update the avr in /tmp/avar: it is a temporary folder, so whatever avar installed there would be deleted with it
```

Exit status 1.

## See also

- [Install]({% link index.md %}), for how each route installs avar in the first place
- [avr version]({% link commands/version.md %}), for the version you are running
