---
title: avr init
parent: Commands
---

# avr init
{: .no_toc }

Looks at the project's manifests, proposes a
[.avr.toml]({% link syntax/avr-toml.md %}) that pins the environment they
describe, and writes it only if you confirm. It installs nothing and starts
nothing.

1. TOC
{:toc}

## Help

<!-- generated:help:begin — written by `make docs` from avr's help; edit cmd/root.go, not this section -->

This is exactly what `avr help init` or `avr init --help` prints.

{% raw %}
```text
Usage:
  avr [selector flags] init

Look at this project's manifests (package.json, pyproject.toml, go.mod, Cargo.toml, Dockerfile, docker-compose.yml, .tool-versions, mise.toml), show the stack they describe and the .avr.toml that would pin it, and write that file only if you confirm. Nothing is written without a terminal, and an existing .avr.toml is never replaced. Writing the file installs nothing: the next `avr` asks before installing its packages. --distro and --arch choose what the proposal is for.

`init` is an avar command, so it does not reach the guest: to run a program called `init` in Linux, use `avr -- init`.

Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.
```
{% endraw %}

<!-- generated:help:end -->

## Examples

```sh
avr init                       # propose a .avr.toml for this project
avr --distro fedora init       # propose one for Fedora
avr --arch amd64 init          # propose one pinned to amd64
```

## What it does

1. Finds the project for the current directory. If it already has a
   `.avr.toml`, avar stops: `avr init` never replaces a file.
2. Reads these manifests from the project directory itself, not its
   subdirectories:

   <!-- generated:init-manifests:begin — written by `make docs` from internal/projconfig/detect.go; edit that file, not this section -->

   `package.json`, `pyproject.toml`, `go.mod`, `Cargo.toml`, `.tool-versions`, `mise.toml`, `Dockerfile`, `docker-compose.yml`, `docker-compose.yaml`

   <!-- generated:init-manifests:end -->

   A manifest larger than 1 MiB is noted but not read.
3. Prints what it found, any notes, and the exact file it would write, with
   the full path.
4. Asks `Write <path>? (y/N)`. Only `y` or `yes` writes the file.

When no manifest is found, it says avar needs no configuration and exits 0.

### What it proposes

- **`distro`**, always: the `--distro` you gave, otherwise the distribution of
  the Dockerfile's final `FROM` image when avar can match it (Ubuntu, Debian,
  Fedora, and official language images based on Debian), otherwise avar's
  default.
- **`arch`**, only from `--arch`, or from `--platform=linux/<arch>` on the
  Dockerfile's final `FROM`.
- **`packages`**: the distribution's own packages for each runtime it
  detects.

<!-- generated:init-packages:begin — written by `make docs` from internal/projconfig/detect.go; edit that file, not this section -->

| Runtime | Ubuntu, Debian | Fedora |
| --- | --- | --- |
| Node.js | `nodejs`, `npm` | `nodejs`, `nodejs-npm` |
| Python | `python3`, `python3-pip`, `python3-venv` | `python3`, `python3-pip` |
| Go | `golang-go` | `golang` |
| Rust | `cargo`, `rustc` | `cargo`, `rust` |
| Ruby | `ruby-full` | `ruby` |

<!-- generated:init-packages:end -->

It never proposes `cpus`, `memory` or `forward_env`: no manifest states a
size, and a proposal to forward credentials is exactly what a detected file
must not make.

Its notes say what the proposal does not do. When a manifest pins a runtime
version, the note says the distribution's package version is what will be
installed, not the pinned one. Docker Compose is reported and proposes
nothing, because installing a container engine is not a package line. A tool
listed in `.tool-versions` or `mise.toml` that maps to none of the runtimes
above is named as having no proposal.

### After writing

Writing the file is not approval. The next `avr` in the project lists the
packages and asks before installing them.

## Exit status

| Status | When |
| --- | --- |
| `0` | The file was written, you declined, or there was nothing to propose |
| `1` | `.avr.toml` already exists, there is no terminal to confirm at, or the file could not be written |
| `2` | `avr init` was given an argument, or `--distro` or `--arch` names an unsupported environment |

## Errors

### already exists

```text
avr: <project>/.avr.toml already exists, and avr init only writes a new one; nothing was changed. Edit it, or remove it and run avr init again
```

Exit status 1.

### no terminal

```text
avr: nothing was written: avr init writes .avr.toml only after you confirm, so run it from a terminal
```

The proposal is still printed first. There is no `--yes`. Exit status 1.

### takes no arguments

```text
avr: `avr init` takes no arguments, but got "<args>"; selector flags such as --distro go before it, as in `avr --distro fedora init`
```

Exit status 2.
