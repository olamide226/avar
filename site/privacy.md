---
title: Privacy
nav_order: 8
---

# Privacy policy
{: .no_toc }

Last updated: 23 November 2026

- TOC
{:toc}

---

avar is an open-source command-line tool that runs your current project
directory inside a Linux environment on your own computer, using
[Lima](https://lima-vm.io) on macOS and WSL 2 on Windows.

**avar has no servers.** There is no avar account, no avar service, and
nowhere for avar to send your data. Everything below happens on the computer
you installed it on.

## In short

- **No telemetry, analytics, crash reporting or usage measurement of any kind.**
- Nothing is transmitted anywhere except when you run `avr update`, which asks
  GitHub whether a newer release exists.
- Your project files stay on your computer. avar makes them available to a
  Linux environment that also runs on your computer.
- **No environment variable from your shell reaches Linux unless you
  explicitly grant it by name.**
- Everything avar remembers lives in one directory that you can delete.

## What avar accesses

### Your project directory

avar's purpose is to run commands against the directory you are in, so that
directory's contents are made available to the Linux environment. On macOS the
project directory is mounted into the virtual machine; on Windows it is reached
through WSL 2. Either way the files stay where they are, on your disk.

avar makes available only directories you have used it in. It does not mount
your home directory, and it does not read files elsewhere on your computer.

### Configuration you write

avar reads two configuration files, both yours:

- `config.toml` in avar's state directory, for your own settings.
- `.avr.toml` in a project, for that project's settings.

A project's `.avr.toml` travels with the repository, so it is treated as a
proposal rather than an instruction. In particular, a name it lists under
`forward_env` does nothing until you approve it, and your approval is recorded
on your machine rather than in the project. A repository you clone cannot
decide what leaves your computer.

### Environment variables, including credentials

This is the part worth being exact about.

By default **only four kinds of variable cross into Linux**: `TERM`, `LANG`,
and any `LC_*` locale variables. These describe the terminal you are sitting
at, not your machine or your accounts.

avar builds that set from an allowlist rather than by removing known-secret
names from your shell's environment. The difference matters: a blocklist is
wrong the moment someone invents a new variable name, whereas an allowlist can
only be wrong about variables avar was never asked to carry. **Your
`AWS_SECRET_ACCESS_KEY`, `GITHUB_TOKEN`, `NPM_TOKEN` and `SSH_AUTH_SOCK` are
absent from the Linux environment — not overwritten, absent.** This is enforced
by an automated test that generates arbitrary host environments and fails if
any variable outside the allowlist appears.

You can grant more, and nothing crosses without such a grant:

| How you grant it | Lasts for |
| --- | --- |
| `avr --env NAME` or `--env NAME=value` | one command |
| `avr --env-file PATH` | one command |
| `forward_env` in your own `config.toml` | until you change it |
| a name in a project's `.avr.toml` that you approved | until you withdraw it |

If you choose to forward a credential this way, avar passes that value to the
Linux environment so your command can use it. It is not written to avar's
state, not logged, and not sent anywhere. Granting is the whole of the
mechanism: avar has no way to discover a secret you did not name.

### Your SSH agent

`avr --ssh-agent` makes your host SSH agent reachable from a Linux session, so
that a command can authenticate as you — to push to a repository, for example.
It is off unless you pass that flag, and it lasts only for that command. Your
private keys stay on the host: what is shared is the agent socket, so the guest
can ask the agent to sign something but cannot read the keys themselves.

## What is transmitted, and when

**Only `avr update`, and only when you run it.** No other avar command checks
for a newer release, and nothing is downloaded or replaced unless you ask.

When you run it, avar makes ordinary HTTPS requests to:

- `https://api.github.com/repos/olamide226/avar/releases/latest`, to learn the
  latest version; and
- the release's archive and checksum files on `github.com`, if you are updating.

Those requests send a `User-Agent` of `avr/<version>` and nothing else. avar
sends no identifier, no username, no machine name and no information about your
projects. As with any web request, GitHub will see the connection and your IP
address; GitHub's own
[privacy statement](https://docs.github.com/site-policy/privacy-policies/github-general-privacy-statement)
covers that.

If avar was installed with Homebrew or winget, `avr update` makes no requests at
all — it prints the one command that updates it and changes nothing.

Separately, the **first time an environment is created**, the backend downloads a
Linux image: Lima from the distribution's publisher on macOS, WSL from Microsoft
on Windows. Those downloads are made by Lima and WSL, under their own privacy
terms, not by avar.

## What is stored on your computer, and how to remove it

Everything avar remembers is in one directory:

| Host | Location |
| --- | --- |
| Windows | `%LocalAppData%\avar` |
| macOS | `~/.avr` |

Setting `AVR_HOME` puts it somewhere else.

It contains the directories you have used avar in, the environments avar
created for them, which sessions are currently running, your `config.toml`, SSH
keys for reaching the local environments, logs, and — on Windows — the Linux
distributions and any snapshots you captured.

Because that record reveals which directories you work in, the directory and
every file in it are created private to your user account: mode `0700`/`0600` on
macOS, and on Windows an access-control list naming your account, the
Administrators group and SYSTEM, and no one else.

**To remove everything:**

1. `avr destroy --all` removes every Linux environment avar created. Your
   project files are never touched by this.
2. Delete the directory above.
3. Uninstall avar — `winget uninstall olamide226.avar`, or
   `brew uninstall --cask avar`.

On Windows, avar also registers one Task Scheduler task, `avar-idle-check`,
which stops idle environments so they do not consume memory indefinitely. It
reads only avar's own state. Uninstalling removes it; `schtasks /Delete /TN
avar-idle-check /F` removes it by hand.

## Telemetry and analytics

**avar collects none.** There is no usage reporting, no crash reporting, no
analytics SDK and no network call made on your behalf other than the `avr
update` requests described above. You can confirm this: the source is public,
and the only code in it that opens a network connection is the updater.

## Children

avar is a developer tool and is not directed at children.

## Changes to this policy

Changes are made in public, in the
[avar repository](https://github.com/olamide226/avar), with the date above
updated. The history of this page is the history of the policy.

## Contact

Questions or corrections: please open an issue at
[github.com/olamide226/avar/issues](https://github.com/olamide226/avar/issues).
