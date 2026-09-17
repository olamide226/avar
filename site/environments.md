---
title: Environments
nav_order: 4
---

# Environments
{: .no_toc }

An environment is a distribution, a release of it, and a CPU architecture.
`--distro` and `--arch` choose one for a single invocation, and `distro` and
`arch` in a project's `.avr.toml` choose one for a project
([Configuration]({% link configuration.md %})).

1. TOC
{:toc}

## Supported environments

The table below is generated from avar's environment matrix, the one list the
resolver checks every `--distro`, `--arch` and `.avr.toml` against. A test
fails when this page and that list disagree.

<!-- generated:matrix:begin — written by `make docs` from internal/resolve/matrix.go; edit that file, not this section -->

| Distribution | Release | Selected with | Architectures |
| --- | --- | --- | --- |
| Ubuntu | 24.04 (default) | `--distro ubuntu` or `--distro ubuntu:24.04` | `arm64`, `amd64` |
| Debian | 13 (default) | `--distro debian` or `--distro debian:13` | `arm64`, `amd64` |
| Fedora | 43 (default) | `--distro fedora` or `--distro fedora:43` | `arm64`, `amd64` |

With no distribution chosen anywhere, avar uses `ubuntu:24.04`.

<!-- generated:matrix:end -->

With no architecture chosen anywhere, avar uses the host's own.

A distribution named without a version gets its default release, so the two
spellings in each row name the same environment. Anything outside the table
is refused before any work starts, with exit status 2 and the supported
values listed.

## On macOS

Every environment in the table is available, on both architectures. The
backend is [Lima](https://lima-vm.io), and each environment is a virtual
machine booted from a cloud image pinned to a dated publisher URL and checked
against the publisher's digest.

- **The Mac's own architecture** runs on Apple's Virtualization framework, and
  the project is shared over VirtioFS. On Apple Silicon, Rosetta is enabled in
  arm64 environments.
- **The other architecture** runs fully emulated under QEMU, with the project
  shared over reverse SSHFS. It works, and it is markedly slower. avar says so
  when it creates one.

Snapshots are the one feature that differs between the two: Lima can snapshot
only an emulated machine. See [Troubleshooting]({% link troubleshooting.md %}#snapshots-are-not-supported).

## On Windows

Every distribution and release in the table is available, on **the Windows
machine's own architecture only**. The backend is WSL 2, which runs Linux on
the host's processor and has no CPU emulation, so `--arch` for the other
architecture is refused before anything is downloaded.

Each environment is a WSL distribution that avar installs from WSL's own
distribution registry, under a name and in a directory avar owns. WSL's
registry names Debian by track rather than by release, so after installing,
avar reads the new distribution's `/etc/os-release` and refuses the
environment if it is not the release the table promises.

## One machine per environment

Each distinct environment gets its own machine (a virtual machine on macOS, a
distribution on Windows), and every project shares it unless you ask for
isolation. `avr` and `avr --distro fedora` in the same directory are two
environments over the same project files.

`avr --isolate`, or `avr isolate on`, gives a project environments of its own.
avar remembers that choice for the project, and `avr isolate off` returns it
to the shared ones.
