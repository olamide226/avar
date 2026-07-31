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

### A file manifest bounds what a task writes, not what it finishes

`internal/state.Reconcile` was built and tested to 91% coverage, and nothing called
it. Crash recovery had never run. Task 13's `_writes:` manifest listed no caller and
no later task claimed one, so it fell in the gap between them — discovered only when
a failed provision left a running machine with no record and nothing repaired it.

A task is not done when its files exist. It is done when something invokes them. When
writing a task, name the caller; when finishing one, check that the feature is
reachable from `main.go`.
