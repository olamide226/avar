# Lessons

Mistakes that changed how we work on avar. The bar for this file is high: an entry
earns its place only if repeating it would cost real time or ship a real defect.
Exploration that went nowhere, a wrong first guess later corrected, a build that
failed once — those are the ordinary cost of working and do not belong here.

Add to this file when a mistake teaches something structural. Keep entries concrete:
what happened, why it matters, what to do instead.

---

## Tests

### A test that asserts a command's *shape* proves only that you wrote what you wrote

`internal/provider/lima` built the guest environment as `env NAME=value -- command`,
and a unit test asserted that exact argv. It passed. The command was broken: `env`
stops reading options at its first non-option argument, so a `--` after the
assignments is not an option terminator, it is the command name. Every `avr <command>`
failed with `env: '--': No such file or directory`. The end-to-end suite found it on
its first run.

Asserting the arguments a function builds confirms the function builds those
arguments. It cannot confirm the receiving program accepts them. Where the value of
the code is that an *external* tool understands it — a CLI invocation, a config file,
a wire format — at least one test must put it in front of that tool. `limactl
validate` in the Lima config tests is the pattern to copy.

### A property that quantifies over part of the design gives false confidence

Property 2 originally read "distinct (distro, arch) pairs SHALL never map to the same
**shared** machine name". The word *shared* exempted isolated machines — which were
named per project only, so `avr --isolate` followed by `avr --isolate --distro fedora`
resolved to the same machine and silently gave the user Ubuntu. The property was
green throughout.

A property covering the easy half of a design is worse than no property: the suite is
green and nobody looks again. When writing one, ask which cases the quantifier
excludes, and whether that exclusion is a real boundary or an accident of how the
sentence came out.

---

## Verification

### Only tick a check you actually ran

`make tidy-check` was ticked in PR #20's verification table from memory of an earlier
run — before the import that broke it. The PR body said the build was verified; the
build was red, and it reached `main`.

A verification checkbox is a claim that a command was executed and its result
observed. If it was not run in the state being submitted, it is unticked, or the PR
says why it was skipped. "It passed earlier" is not a result.

### Wait for CI to reach a terminal state before merging

PR #20 was merged while its check still read `pending`. It failed, so `main` went red
and needed a follow-up PR to repair. The job takes under a minute.

`gh pr checks` reporting `pending` is not a signal to proceed. Wait for `pass` or
`fail`.

### Verify against the real tool, not against priors or documentation

Several assumptions about Lima were wrong in ways only the installed binary revealed:
the version in use is 2.x, not the 1.x the version gate was written for;
`limactl show-ssh` is deprecated and the design's `avr code` approach was built on it;
`limactl list --json` prints zero bytes for an empty instance list rather than an
empty array, which is the first-run path of `avr status`.

Each was cheap to check and expensive to assume. When behaviour of an external tool
matters, run it and paste the output into the PR.

### Do not trust a reported result you can cheaply reproduce

Agent reports and editor diagnostics were both wrong at times — diagnostics
repeatedly showed compile errors that were another worktree's mid-edit state, and
several were chased down before the pattern was recognised. Building the branch in
isolation settles it:

```
git archive <ref> | tar -x -C <tmpdir> && cd <tmpdir> && go build ./... && go test ./...
```

---

## Specification

### The spec can be wrong in ways only implementation reveals

Two defects were found by building against the design, not by reading it. `Status`
was specified to filter on the `avr-` prefix *and* membership in `machines.json`;
taken literally, a machine created but never recorded — exactly what a crash
mid-create leaves — is invisible, which makes the adoption reconciliation exists for
impossible. Separately, isolated machine naming omitted the environment, so two
isolated environments in one project collided.

When an implementer says the spec cannot be satisfied as written, that is evidence
about the spec, not about the implementer. Amend it in the same PR and say so — do
not work around it silently, and do not make the code lie to match.

### Parallel agents converge on the same wheel, and each reinvents it worse

Six Phase 2 tasks ran as independent agents against disjoint file sets. The file sets
held — no collisions — but four of the six independently hand-rolled an atomic file
write, a yes/no prompt, or a state-directory path that `internal/state` already owned.
Every copy was worse than the original: two dropped the fsync while their doc comments
still claimed REQ-17.5, one used a fixed `.tmp` path two concurrent runs would collide
on, and one ignored `$AVR_HOME` so `avr code` wrote outside the state directory every
other command was using.

Disjoint *files* do not make work disjoint. An agent that cannot see the other branches
also cannot see that a helper it needs is being written next door, and it will not go
looking for one in a package its task never mentions. When splitting work, name the
shared helpers each task must use — or expect to spend the review consolidating them.

### A test suite that leaks resources eventually fails tests it has nothing to do with

The e2e suite created a machine per isolated project and deleted none. Twenty-four
accumulated across parallel agent runs. The first symptom was not "too many VMs" — it
was an unrelated test failing because `limactl stop` timed out, on a host too busy to
shut a machine down. Time went into the stop path before the cause was visible.

A suite that allocates something expensive must free it in `t.Cleanup`, and if the
product has no unattended way to free it, that is a missing feature rather than a
reason for the test to skip cleanup: `avr isolate off` could only delete behind an
interactive prompt, so nothing in CI could ever have cleaned up after itself.

### Verify against the tool, then verify the verification

`limactl shell` has no agent-forwarding flag but documents `$SSH` as a way to
substitute the ssh command, so `SSH="ssh -A"` looked like the answer and was tested
directly rather than assumed. It did nothing: Lima keeps a persistent `ControlMaster`,
the connection was already open, and agent forwarding is negotiated only when the
master is established. `-A` on a later invocation is silently ignored. The working
form disables multiplexing as well.

The lesson is not "test against the real tool" — that already happened. It is that a
mechanism can be correctly documented, correctly invoked, and still defeated by
unrelated state the documentation never mentions. When something plausible does not
work, find out why before trying the next plausible thing; the reason here was the
whole answer.

### `--ssh-agent` was accepted, plumbed, and did nothing

`ShellOpts.ForwardSSHAgent` was set from the flag and read by no backend. `avr
--ssh-agent` printed nothing, failed nothing, and gave the user a session with no
agent. Three independent reviewers found it; `grep -rn ForwardSSHAgent` returns the
field declaration and one assignment.

This is [the manifest lesson](#a-file-manifest-bounds-what-a-task-writes-not-what-it-finishes)
again, one layer down: the task wired the flag end to end *except* the end that does
the work, and a field being set looks exactly like a field being used. For a flag that
weakens or strengthens a security boundary, silence is the worst failure mode — the
user believes the grant happened. Until a backend implements it, the flag is refused.

When a feature crosses a layer, the test that proves it belongs at the far side: assert
the backend *acts*, not that the caller *asked*.

### A file manifest bounds what a task writes, not what it finishes

`internal/state.Reconcile` was built and tested to 91% coverage, and nothing called
it. Crash recovery had never run. Task 13's `_writes:` manifest listed no caller and
no later task claimed one, so it fell in the gap between them — discovered only when
a failed provision left a running machine with no record and nothing repaired it.

A task is not done when its files exist. It is done when something invokes them. When
writing a task, name the caller; when finishing one, check that the feature is
reachable from `main.go`.
