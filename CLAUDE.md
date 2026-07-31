# CLAUDE.md — working agreement for the avar repository

avar is a zero-configuration, directory-centric Linux shell environment switcher for
macOS, shipped as a single Go binary named `avr`. It is a thin UX layer over
[Lima](https://lima-vm.io); it is **not** a Docker wrapper, a Dev Container
implementation, or a VM manager.

The product rule every decision answers to:

> The user thinks in terms of "current directory + selected operating environment".
> Machines, mounts, SSH, and images are avar's problem, never the user's.

## Spec-driven development

The spec is the source of truth and lives at `.kiro/specs/avar-cli/`:

| File | Contents |
| --- | --- |
| `requirements.md` | Numbered requirements with EARS acceptance criteria |
| `design.md` | Architecture, component interfaces, correctness properties, error handling |
| `tasks.md` | Phased implementation checklist with per-task `_writes:` manifests |

Read all three before writing code. If an acceptance criterion and the code
disagree, the criterion wins — or the spec gets amended in the same PR, with the
amendment called out in the PR body. Never let them drift silently.

## Requirement traceability: `REQ-<n>.<m>`

Acceptance criteria are cited everywhere as `REQ-<requirement>.<criterion>` —
`REQ-1.2`, `REQ-6.4`, `REQ-17.1`. Correctness properties from `design.md` are cited
as `PROP-<n>`. This is what lets a reviewer check the diff against the spec instead
of against their own assumptions.

**Where citations are required:**

1. **PR body** — a "Requirements covered" section listing every `REQ-x.y` the PR
   implements, each with the criterion's text and a one-line note on how the code
   satisfies it. Also list requirements the PR deliberately does *not* finish.
2. **Commit messages** — a `Requirements:` trailer naming the ids the commit
   advances.
3. **Test names** — tests that exist to prove a criterion name it, e.g.
   `TestResolve_SharedMachinePerSelector_REQ_4_3`. Property-based tests name the
   property: `TestProp_ExitCodeTransparency_PROP_3`.

**Where citations do not belong:** scattered through production code. A `REQ-` id in
a comment is justified only where the code looks wrong without it — a deliberate
non-obvious constraint the criterion imposes. Do not annotate ordinary code with the
requirement that motivated it; the PR and the tests carry that.

## Lessons

[`docs/lessons.md`](docs/lessons.md) records mistakes that changed how we work here —
a passing test that asserted a broken command, a correctness property that covered
only half its design, a feature built and never wired up. Read it before starting;
most entries describe a trap that is easy to walk into twice.

**Keep it current.** When a mistake teaches something structural, add it in the same
PR that fixes the mistake. The bar is deliberately high: exploration that went
nowhere, a wrong first guess later corrected, or a build that failed once are the
ordinary cost of working and do not belong there. An entry earns its place only if
repeating it would cost real time or ship a real defect.

## Engineering standards

**Design.** Follow the interfaces in `design.md` §3. The `Provider` interface is a
hard boundary: nothing in `cmd/` or `internal/resolve` may reference Lima, `limactl`,
or any backend concept (REQ-17.3) — a second backend must be addable without touching
command-layer code. Dependencies point inward toward `internal/types`, which holds
shared vocabulary and never imports another avar package.

**Separation of concerns.** `cmd/` owns every byte of user-facing output; packages
below it return values and errors and never print. Pure decision logic (`internal/cli`
grammar, `internal/resolve`, `internal/envpolicy`) is kept free of I/O so it is
testable without a VM. Side effects live behind interfaces that a test double can
replace.

**Clean code.** Small functions with one job. Names from the glossary in
`requirements.md` — a `Project` is a host directory, a `Machine` is a VM, a
`Selector` is a (distro, arch, isolation) triple; do not invent synonyms. No dead
code, no speculative abstraction for a second implementation that does not exist yet
(the `Provider` interface is the one sanctioned exception, and it is required).

**Errors.** Every error names what avar was attempting, wraps the underlying cause
with `%w`, and where the user can act, says what to do next. No `panic` on a
recoverable path. Never swallow an error to keep going. Match the error table in
`design.md` §6.

**Security.** The guest gets nothing from the host that the user did not explicitly
grant (REQ-9): no host environment variables beyond the terminal allowlist, no SSH
agent, no home-directory mount, no credential files. Mount only registered project
directories. Never log secrets or full environment dumps. Only ever operate on
machines carrying avar's `avr-` prefix *and* present in avar's own records
(REQ-5.4, PROP-6).

**Efficiency.** The warm path — machine already running — must add no more than
~500 ms of overhead (REQ-17.1). Do not shell out to `limactl` more than necessary
per invocation; cache within an invocation, never across. Stream guest I/O rather
than buffering it (REQ-2.3).

**Concurrency.** All state mutation goes through the advisory lock in
`internal/state`; concurrent `avr` invocations must serialize rather than corrupt.
Writes are atomic (temp file, fsync, rename) so a crash never leaves half a record
(REQ-17.5).

**Dependencies.** Standard library first. The sanctioned third-party set is
`github.com/spf13/cobra` and `golang.org/x/term`. Anything else needs justification
in the PR body.

## Testing

Three layers, per `design.md` §7:

- **Unit** — pure logic, table-driven, no VMs. Runs in CI on every push.
- **Integration** — command flows against the in-process `FakeProvider`, asserting the
  sequence of provider calls. No VMs. Runs in CI.
- **E2E** — real Lima, behind the `e2e` build tag and `make e2e`. Requires macOS with
  virtualization and `limactl` installed. Not part of default CI.

Write the test that proves the acceptance criterion, not the test that mirrors the
implementation. Every bug fix starts with a failing test.

```
make build    # compile avr
make test     # unit + integration
make lint     # gofmt check + go vet
make e2e      # real-Lima end-to-end (macOS + Lima required)
```

## Git and pull requests

- `main` is protected in practice: **all feature work arrives by PR.** The only
  direct-to-main commit is the initial scaffold.
- **Spec changes arrive by PR too**, including ones that only tick `tasks.md`
  checkboxes. The temptation is to treat bookkeeping as too small to review, but a
  checkbox is a claim that a requirement is now satisfied, and the PR is where that
  claim gets checked against the diff. Every correction to `design.md` so far came
  out of exactly that review. If a bookkeeping commit does reach `main` directly,
  do not rewrite published history to hide it — say so in the next PR.
- Run **`make hooks`** once per clone. It points `core.hooksPath` at `.githooks`,
  whose `pre-push` hook refuses direct pushes to `main` — the rule above enforced
  rather than remembered. Linked worktrees share the repository config, so one
  install covers them. `AVR_ALLOW_MAIN_PUSH=1` overrides it for a genuine
  emergency; disclose any use in the next PR.
- Branch names: `feat/<area>-<short-description>`, `fix/<area>-<short-description>`,
  `chore/…`, `docs/…`. One task (or one coherent sub-task group) per branch.
- Conventional commits: `feat(resolve): map each selector to its own machine`.
  Commits are self-contained and detailed — the subject says what changed, the body
  says why, what was considered, and anything a reviewer would otherwise have to
  reconstruct. A `Requirements:` trailer lists the `REQ-` ids.

```
feat(state): store project and machine records atomically

Records are written to a temp file, fsynced, and renamed so an interrupted
write can never leave a partial record behind. All mutations take the advisory
lock at ~/.avr/lock, which makes concurrent avr invocations serialize instead
of racing on create.

MachineRecord is written only after the backend confirms the machine started,
and removed only after deletion succeeds, so the store never describes a
machine that never worked.

Requirements: REQ-17.5, REQ-11.2
Properties: PROP-7
```

- Do not edit `.kiro/specs/avar-cli/tasks.md` checkboxes inside a feature branch;
  checkboxes are ticked when work merges, to keep parallel branches from conflicting
  on that file.
- Keep the `_writes:` manifest in `tasks.md` honest: touching files outside your
  task's manifest is how parallel branches collide. If you must, say so in the PR body.

## Repository layout

```
main.go                     Entry point; delegates to cmd
cmd/                        Cobra commands; the only layer that prints
internal/types/             Shared vocabulary: selectors, records, progress contract
internal/cli/               Argv grammar: selector flags vs subcommand vs guest command
internal/state/             ~/.avr state store: atomic writes, locking, reconciliation
internal/resolve/           (cwd, flags, state) -> target machine; the distro/arch matrix
internal/deps/              Lima detection, version gate, brew install offer
internal/provider/          Provider interface (backend-agnostic)
internal/provider/fake/     Test double recording provider calls
internal/provider/lima/     LimaProvider: the only real backend
internal/envpolicy/         What crosses into the guest environment
e2e/                        Real-Lima tests, build tag `e2e`
.kiro/specs/avar-cli/       The spec
```
