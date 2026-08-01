# Contributing to avar

Thank you for considering a contribution. avar is a small, opinionated tool with an
unusual process behind it, and the process is the part worth reading before you write
any code: **avar is spec-driven**, and a pull request that ignores the spec will be
sent back regardless of how good the code is.

This document explains how the project actually works — the spec, requirement
traceability, the test layers, and what a reviewable pull request looks like. The
[README](README.md) covers what avar is and how to use it; start there if you have not.

## Code of conduct

This project follows the [Contributor Covenant](https://www.contributor-covenant.org/version/2/1/code_of_conduct/)
(version 2.1). Be direct about code and generous about people. Report unacceptable
behaviour by opening an issue, or privately to the maintainers if the issue itself
would be inappropriate to make public.

## The spec is the source of truth

The specification lives at [`.kiro/specs/avar-cli/`](.kiro/specs/avar-cli/) and is
three files:

| File | Contents |
| --- | --- |
| `requirements.md` | Numbered requirements, each with EARS acceptance criteria and a glossary of the project's vocabulary |
| `design.md` | Architecture, component interfaces, correctness properties, the error table |
| `tasks.md` | The phased implementation checklist, with a `_writes:` file manifest per task |

Read all three before writing code. They are long, but they answer most of the
questions a new contributor would otherwise ask in review: what a `Project` is, why
`Selector` is a three-part thing, which layer is allowed to print, what happens when a
machine crashes mid-create.

**If the code and an acceptance criterion disagree, the criterion wins** — or the spec
is amended in the same pull request, with the amendment called out in the PR body.
What must never happen is silent drift, where the code does one thing, the spec says
another, and nobody knows which is intended.

Amending the spec is a normal and expected outcome, not an admission of failure. When
Lima turned out to implement snapshots for QEMU machines only — so the default
Apple Silicon environment, which runs under `vz`, cannot be snapshotted at all —
Requirement 10 was amended to say that criteria 10.1, 10.2 and 10.4 apply *where the
backend supports snapshots*, and the CLI was made to say so and point at `avr reset`
instead. That amendment is still visible in `requirements.md` under Requirement 10. If
you find that a criterion cannot be satisfied as written, say so and propose the
amendment; do not work around it quietly and do not make the code lie to match.

### Requirement traceability: `REQ-<n>.<m>` and `PROP-<n>`

Acceptance criteria are cited as `REQ-<requirement>.<criterion>` — `REQ-1.2`,
`REQ-6.4`, `REQ-17.1`. Correctness properties from `design.md` are cited as
`PROP-<n>`. These citations are what let a reviewer check your diff against the spec
rather than against their own assumptions.

Citations belong in exactly three places:

1. **The pull request body** — a "Requirements covered" section listing every `REQ-x.y`
   the PR implements, quoting the criterion and noting in one line how the code
   satisfies it. The [PR template](.github/pull_request_template.md) has the table
   ready. Also list criteria in scope that this PR deliberately does *not* finish.
2. **Commit messages** — a `Requirements:` trailer naming the ids the commit advances.
3. **Test names** — a test that exists to prove a criterion names it:
   `TestStop_StopsOnlyTheResolvedTarget_REQ_5_2`. A property-based test names the
   property: `TestProp_EnvironmentIsolationByDefault_PROP_4`.

Citations do **not** belong scattered through production code. A `REQ-` id in a
comment is justified only where the code looks wrong without it — a deliberate,
non-obvious constraint the criterion imposes. Do not annotate ordinary code with the
requirement that motivated it; the pull request and the tests carry that.

### The `_writes:` manifest

Each task in `tasks.md` lists the files it is expected to touch. Keeping that honest
is how parallel branches avoid colliding. If your change has to touch files outside
its task's manifest, that is allowed — say so in the PR body.

## Development setup

You need:

- **macOS.** It is the only supported host platform today (Windows via WSL 2 is
  Requirement 18 and not started). Unit and integration tests run on macOS in CI.
- **Go**, at the version in [`go.mod`](go.mod) — currently 1.23. CI pins itself to
  that file, so the toolchain you build with matches the one that reviews you.
- **[Lima](https://lima-vm.io) 2.0.0 or newer**, but only for end-to-end tests and for
  running `avr` against real machines. `make test` needs no VM and no Lima. The
  minimum version lives in one place, `internal/deps.MinLimaVersion`.

Then, once per clone:

```bash
make hooks
```

This points `core.hooksPath` at [`.githooks/`](.githooks/), whose `pre-push` hook
refuses direct pushes to `main`. Linked worktrees share the repository config, so one
install covers them all. Skipping this step is how a commit reaches `main` without
review.

### Make targets

```
make build        # compile bin/avr
make install      # go install
make test         # unit + integration, with -race
make lint         # gofmt -s check plus go vet
make tidy-check   # go mod tidy -diff — fails if go.mod/go.sum are stale
make cover        # test with a coverage profile, prints the total
make e2e          # real-Lima end-to-end tests (see below)
make hooks        # install the pre-push hook
make clean        # remove bin/ and coverage.out
```

CI runs `make lint`, `make tidy-check`, `make build`, and `make test` on macOS. It
does not run `make e2e`.

## Testing

There are three layers, described in `design.md` §7.

**Unit** — pure decision logic, table-driven, no VMs. The argv grammar in
`internal/cli`, the resolver precedence table in `internal/resolve`, the environment
allowlist in `internal/envpolicy`, the state store's atomic writes and locking. This
layer is deliberately free of I/O so it can be exhaustive and fast.

**Integration** — full command flows against the in-process `FakeProvider` in
`internal/provider/fake`, asserting the *sequence of provider calls* a command makes.
First run, mount-add, `stop --all`, isolation being remembered, reset scoping. No VMs
here either, so these run in CI on every push.

**End-to-end** — real Lima, behind the `e2e` build tag, run with `make e2e`. These
provision actual virtual machines: they need macOS with virtualization and `limactl`
installed, they are **not** part of default CI, and a full run takes on the order of
twelve minutes (individual tests allow up to fifteen minutes for a cold provision, and
the target's overall timeout is thirty). Run them before submitting anything that
touches the Lima provider, mounting, ports, or the shell path. If you cannot run them,
say so in the PR under Verification rather than leaving the box ticked.

Two rules that come from real defects in this repo:

- **Write the test that proves the acceptance criterion, not the test that mirrors the
  implementation.** A unit test once asserted the exact argv avar built for the guest
  environment. It passed, and the command was broken for every user, because asserting
  the arguments a function builds cannot confirm that the receiving program accepts
  them. Where the value of the code is that an *external* tool understands it, at
  least one test must put it in front of that tool.
- **Every bug fix starts with a failing test.**

## Engineering standards

These are summarised from [`CLAUDE.md`](CLAUDE.md), which is the full working
agreement and worth reading in its own right.

**The `Provider` interface is a hard boundary.** Nothing in `cmd/` or
`internal/resolve` may reference Lima, `limactl`, or any backend concept (REQ-17.3). A
second backend must be addable without touching command-layer code. Dependencies point
inward toward `internal/types`, which holds shared vocabulary and never imports another
avar package.

**`cmd/` owns every byte of user-facing output.** Packages below it return values and
errors and never print. Pure decision logic stays free of I/O; side effects live behind
interfaces a test double can replace.

**Standard library first.** The sanctioned third-party set is
`github.com/spf13/cobra` and `golang.org/x/term`. Anything else needs justification in
the PR body.

**Errors name what avar was attempting**, wrap the cause with `%w`, and where the user
can act, say what to do next. No `panic` on a recoverable path, and never swallow an
error to keep going. Match the error table in `design.md` §6.

**Security.** The guest gets nothing from the host that the user did not explicitly
grant (REQ-9): no host environment variables beyond the terminal allowlist, no SSH
agent, no home-directory mount, no credential files. Mount only registered project
directories. Never log secrets. Only ever operate on machines carrying avar's `avr-`
prefix *and* present in avar's own records.

**Naming.** Use the glossary in `requirements.md`. A `Project` is a host directory, a
`Machine` is a VM, a `Selector` is a (distro, arch, isolation) triple. Do not invent
synonyms.

**No speculative abstraction.** No dead code, no interface for a second implementation
that does not exist yet. The `Provider` interface is the one sanctioned exception, and
it is required.

## Making a change

**Everything arrives by pull request** — including changes that only tick a checkbox in
`tasks.md`. A ticked checkbox is a claim that a requirement is now satisfied, and the
pull request is where that claim gets checked against the diff. Several corrections to
`design.md` came out of exactly those reviews.

1. **Branch.** `feat/<area>-<short-description>`, `fix/<area>-<short-description>`,
   `chore/…`, `docs/…`. One task, or one coherent group of sub-tasks, per branch.
2. **Commit** in conventional-commit form: `feat(resolve): map each selector to its own
   machine`. The subject says what changed; the body says why, what was considered, and
   anything a reviewer would otherwise have to reconstruct. End with a `Requirements:`
   trailer.

   ```
   feat(state): store project and machine records atomically

   Records are written to a temp file, fsynced, and renamed so an interrupted
   write can never leave a partial record behind. All mutations take the advisory
   lock at ~/.avr/lock, which makes concurrent avr invocations serialize instead
   of racing on create.

   Requirements: REQ-17.5, REQ-11.2
   Properties: PROP-7
   ```

3. **Do not tick `tasks.md` checkboxes inside a feature branch.** Checkboxes are ticked
   when work merges, in their own pull request, so that parallel branches do not
   conflict on that one file.
4. **Open the pull request** and fill in the template: the spec task, the requirements
   covered, the correctness properties, what you deliberately did not finish, and a
   Verification section saying what you *ran* and what the result *was*. A verification
   checkbox is a claim that a command was executed and its output observed. "It passed
   earlier" is not a result — if it was not run against the state you are submitting,
   leave it unticked and say why.
5. **Wait for CI to reach a terminal state.** `gh pr checks` reporting `pending` is not
   a signal to proceed; the job takes under a minute. A pull request has been merged on
   a pending check before, it failed, and `main` went red.

## Releases

[`docs/releasing.md`](docs/releasing.md) explains the automated release policy,
the Conventional Commit types that create releases, and how GoReleaser publishes
the GitHub archive and Homebrew cask after CI passes on `main`.

## `docs/lessons.md`

[`docs/lessons.md`](docs/lessons.md) records mistakes that changed how this project
works — a passing test that asserted a broken command, a correctness property that
quantified over only half its design, a feature that was built, tested to 91% coverage,
and never called. Read it before you start; most entries describe a trap that is easy
to walk into twice.

Keep it current: when a mistake teaches something structural, add the entry in the same
pull request that fixes the mistake. The bar is deliberately high. Exploration that
went nowhere, a wrong first guess later corrected, or a build that failed once are the
ordinary cost of working and do not belong there. An entry earns its place only if
repeating the mistake would cost real time or ship a real defect.

## Reporting a bug

Open a GitHub issue with:

- **What you ran**, exactly — the full `avr` command line and the directory you were
  standing in.
- **What you expected**, and what happened instead. Paste the error verbatim.
- **`avr status` output.** This is the single most useful thing in a report: it shows
  which environments exist, what they cost, and what is forwarded.
- **`avr --version`**, or the commit you built from.
- **Your macOS version** (`sw_vers -productVersion`) and **architecture**
  (`uname -m`) — Apple Silicon and Intel take different paths, as do native and
  emulated environments.
- **Your Lima version** (`limactl --version`). Several past defects were version
  behaviour, not avar behaviour.

If the problem involves a guest environment, say which selector it was: distro, arch,
and whether it was `--isolate`d.

Security issues should not be filed as public issues. Contact the maintainers
privately.

## Licence

avar is Apache-2.0 licensed. Contributions are accepted under the same licence; see
[`LICENSE`](LICENSE).
