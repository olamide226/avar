# Handoff

State of avar as of the end of the first working session. Read `CLAUDE.md` first —
it is the binding working agreement. This file is the situational context that
agreement does not carry: what is done, what is next, what is broken, and which
mistakes have already been made so they are not repeated.

## Where things stand

`main` is green: 21 PRs merged, ten packages passing with `-race`, CI green, and
the end-to-end suite passing against real Lima 2.2.0.

**avar works.** From a deleted machine and a deleted `~/.avr`:

```
$ cd ~/code/demo && avr sh -c 'echo "I am $(whoami) on $(uname -sm) in $(pwd)"'
Creating your Linux development environment · Ubuntu 24.04 · arm64 · 4 CPU · 8 GB RAM
I am olamide on Linux aarch64 in /Users/olamide/code/demo
                                                    14.248 total
```

Warm path measured at **205 ms mean / 202 ms best** over five runs, against
REQ-17.1's ~500 ms budget — and that is the whole invocation, not just avar's share.

### Done

Phase 1: tasks 1–8, 10, 13, 14. Phase 1b: tasks 27, 28.

`avr` and `avr <command>` work end to end; argv grammar; state store with atomic
writes and locking; resolver; Lima detection and version gate; the `Provider`
contract plus a fake; the Lima backend; the reconciler (**built but not wired —
see task 29**); `avr status` and `avr stop`; packaging; provider-neutral contracts;
host→provider routing.

### Next, in order

| Task | Why it is next |
| --- | --- |
| **29** | The reconciler has no caller. Crash recovery is written, tested at 91%, and never runs. Small, and it closes a correctness hole. |
| **9** | Mount management. Replaces the deliberately minimal version inline in `cmd/shell.go`, and adds the restart prompt when other sessions are attached (REQ-6.4). |
| **11** | Wire `--arch` / `--distro` end to end, plus the one-time emulation warning. |
| **12** | Port-forwarding e2e. Also the first real confirmation of the host-agent log format the port diagnostics parse. |

That finishes the MVP core. Phase 2 (tasks 15–20) completes the MVP: snapshots,
reset, isolation, forwarding flags, `avr code`, idle auto-stop. Phase 3 (21–26) and
Phase 4 (33–37, Windows/WSL 2) are post-MVP.

## Known debt and open questions

- **Task 29 above** is the most important. A failed provision currently leaves a
  running machine with no record and nothing repairs it.
- **`avr code` cannot use `limactl show-ssh`** — deprecated in Lima 2.x, verified.
  design §3.5 now says to read `~/.lima/<instance>/ssh.config` instead. Task 19.
- **Port-forward log format is unconfirmed** against a real conflict. The parser is
  deliberately tolerant; task 12 settles it.
- **PROP-2 wording** reads as though the provider belongs in the machine name, while
  §3.2 says naming is deterministic *within* a provider and shows no provider token.
  Machine names were left alone (they were fixed in PR #8 after a real bug) and the
  distinction is carried by `MachineRecord.Provider`. Worth settling before a second
  backend exists.
- **Lima 1.x is unevidenced, not disproven.** The floor is 2.0.0 because 2.x is what
  `limactl validate` and the e2e suite actually exercise. Lowering it again is a
  one-constant change once 1.x is genuinely tested.
- **Interactive Ctrl-C/Ctrl-Z fidelity (REQ-3.3)** is implemented but has no automated
  test; driving a PTY through a harness is its own piece of work.
- **Homebrew publishing needs `olamide226/homebrew-tap` to exist** and a
  `HOMEBREW_TAP_TOKEN` secret with `contents:write`. Nothing breaks until a `v*` tag.

## How to work here

```
make hooks    # once per clone — refuses direct pushes to main
make build    # ./bin/avr
make test     # unit + integration, no VMs
make lint     # gofmt -s + go vet
make e2e      # real Lima; provisions actual VMs
make tidy-check
```

Everything arrives by PR, including checkbox ticks — see CLAUDE.md. The pre-push
hook enforces it.

## Gotchas that cost time in this session

- **Stale editor diagnostics from agent worktrees.** Worktrees under
  `.claude/worktrees/` are separate modules; the language server reports their
  mid-edit state as errors in files that are fine. Several "compile errors" were
  phantom. **Verify by building the branch in isolation** — `git archive <ref> | tar
  -x -C <tmpdir>` then `go build ./...` — not by trusting the diagnostics *or* an
  agent's report. Merged worktrees have been pruned; keep pruning them.
- **`limactl list --json` prints zero bytes when there are no instances.** The
  warning goes to stderr and the exit code is 0. Empty stdout means "no machines",
  not failure — this is the `avr status` first-run path.
- **Lima 2.x, not 1.x.** `show-ssh` is deprecated. `limactl validate` exists and is
  used inside the test suite to check generated configs against real Lima.
- **`env` argument order.** `env` stops reading options at its first non-option
  argument, so `--` must precede the `NAME=value` assignments. `env FOO=bar -- cmd`
  fails with `env: '--': No such file or directory`.
- **zsh does not word-split unquoted parameters.** A shell loop like
  `for a in "--arch amd64"; do ./bin/avr $a; done` passes one argument, not two, and
  makes correct behaviour look broken.

## Mistakes made in this session

Recorded because the next agent is likely to be tempted by the same shortcuts.

1. **Merged a PR while CI was still `pending`.** It then failed, so a red PR reached
   `main` (PR #20 → fixed by PR #21). Wait for `gh pr checks` to report a terminal
   state. The job takes under a minute.
2. **Ticked a verification checkbox without running it.** `make tidy-check` was
   claimed in PR #20's body from memory of an earlier run, before the import that
   broke it. A checkbox is a claim that something was executed.
3. **Committed spec bookkeeping directly to `main`**, breaking the repository's own
   rule (commit `80d6b40`). Not rewritten — PR #13 wrote the rule down properly and
   PR #14 added the hook that now prevents it.
4. **Trusted agent reports over verification, twice**, before switching to building
   branches in isolation. Both times the reports were broadly accurate, but the
   habit is what matters: two real bugs in task 8 were found only by running the
   thing, and one of them had a *passing unit test asserting the broken behaviour*.

That last point generalises: a test that asserts the shape of a command without
executing it can confirm only that the command is what you wrote, never that it
works. The e2e suite found both task 8 bugs on its first run.

## Where the design decisions live

`.kiro/specs/avar-cli/` is the source of truth — `requirements.md` (EARS criteria,
cited as `REQ-x.y`), `design.md` (architecture, 11 correctness properties cited as
`PROP-n`, error handling), `tasks.md` (the phased plan with `_writes:` manifests).

Every merged PR body carries a requirements table, so any `REQ-x.y` can be traced
from criterion to code to test. Several design defects were found *by implementing
against them* and corrected in PRs #6, #10 and #16 — if the spec and the code
disagree, that has usually meant the spec was wrong, and it gets amended in the same
PR rather than left to drift.
