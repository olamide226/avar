---
title: Syntax
nav_order: 3
has_children: true
has_toc: false
---

# Syntax
{: .no_toc }

avar needs no configuration. A project with no `.avr.toml`, on a machine with
no `config.toml`, gets the default distribution on the host's architecture, in
an environment shared with every other project. Everything in this section is
optional.

| Page | What it covers |
| --- | --- |
| [Command line]({% link syntax/command-line.md %}) | How `avr` reads a command line: flags, subcommands, guest commands, `--`, and the reserved names |
| [.avr.toml]({% link syntax/avr-toml.md %}) | The project file: every key, the reader's rules, and what needs your approval |
| [config.toml]({% link syntax/config-toml.md %}) | avar's own settings on your machine |

The rest of this page is how those three combine.

1. TOC
{:toc}

## Which environment a command acts on

Every command that acts on "this environment" (`avr`, a one-shot command,
`stop`, `reset`, `destroy`, `snapshot`, `restore`, `code`, `cursor`, `zed`,
`ports`, `open` and `sync`) resolves it the same way. `avr init` resolves the
same distribution and architecture to decide what to propose for.

**Distribution and architecture.** Each is decided on its own, and the first
of these that sets it wins:

1. `--distro` and `--arch` on the command line.
2. `distro` and `arch` in the project's [.avr.toml]({% link syntax/avr-toml.md %}).
3. avar's defaults: the default distribution in the
   [environment matrix]({% link environments.md %}), and the host's own
   architecture.

So `avr --arch amd64` in a project whose file says `distro = "fedora"` runs
Fedora on amd64. A source that names a distribution also fixes its release:
`--distro debian` never inherits a release the file wrote for another
distribution, and a bare name gets that distribution's default release.

`config.toml` has no distribution or architecture setting.

**Isolation.** The first of these that applies wins:

1. `--isolate` or `--shared` on the command line. `--isolate` is also
   remembered for the project. `--shared` applies to that one invocation.
2. What avar remembers for the project: set by `--isolate` or
   `avr isolate on`, and cleared by `avr isolate off`.
3. The environment shared by every project.

`.avr.toml` has no isolation setting.

## What crosses into Linux

A guest session gets the host's `TERM`, `LANG` and `LC_*` variables and
nothing else unless you grant more. When `TERM` is unset or `dumb`, the guest
gets `xterm-256color`. The grants are applied in this order, so a later one
overrides an earlier one for the same name:

1. `forward_env` in [config.toml]({% link syntax/config-toml.md %}), and the
   `forward_env` names you approved in this project's `.avr.toml`. Each
   forwards the host's value when the host has one.
2. `--env-file PATH`: every `KEY=value` line in the file.
3. `--env NAME` (the host's value, when it has one) or `--env NAME=value`.

`--ssh-agent` lends the guest your SSH agent for that one invocation.

Grants on the command line apply to an interactive shell or a one-shot
command. Management commands start no guest session, so they forward nothing.
An interactive shell receives them before your login profile runs, so a
profile that assigns the same name unconditionally wins over the grant.

{: .warning }
> **Known gaps, today.**
> - On Windows, `--ssh-agent` is accepted and has no effect.
