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

### Squash-merging the bottom of a stack breaks every branch above it

Phase 4 arrived as nine stacked pull requests, #52 through #60, each based on the one
below it. All nine reported mergeable. Merging them bottom-up with squash produced two
distinct failures, neither of which is visible from the queue beforehand.

**The branches above lose their shared ancestor.** A squash replaces the parent PR's
commits with one new commit that is not an ancestor of anything. The branches above it
still carry the originals, so the merge base for each drops back to before the series
began, and git three-way merges the whole stack against a `main` that already contains
its squashed equivalent. Files a lower PR *added* and a higher one *modified* come back
as add/add conflicts. #55 conflicted this way after #52-#54 landed, having been
mergeable an hour earlier, and #56 through #60 each needed
`git rebase --onto main <parent-tip>` to replay their own single commit.

**Deleting the merged branch closes the PR based on it.** GitHub retargets a child PR
to `main` when the base branch disappears, but not always, and when it does not it
*closes* the PR — and it cannot be reopened while the base ref is missing. Recovering
#53 meant recreating `chore/windows-portability` at its old SHA, reopening, retargeting
to `main`, and deleting the branch again. Note that `delete_branch_on_merge` does this
whether or not `--delete-branch` is passed.

So, when merging a stack:

- **Retarget each child to `main` before merging its parent**, not after. A PR already
  based on `main` is indifferent to its old base branch being deleted.
- **Expect to rebase every remaining branch**, `--onto main`, dropping the commits that
  the squash already landed. This is not optional cleanup: without it the PR's diff
  double-counts its parents' changes.
- **Prove the rebase changed nothing before force-pushing.** `git diff <rebased>
  <original-branch>` must be empty. A rebase that silently drops a hunk looks exactly
  like one that did not, and force-pushing is the point of no return.

None of this argues against squash — the history it leaves is the one this repository
wants. It argues that a stack is merged deliberately, in order, with the child
retargeted first, rather than by merging what the queue says is mergeable.

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

### Measure the limit; three plausible explanations were all wrong

The shared machine stopped starting once the end-to-end suite had used it for a
while. Three hypotheses were formed and each was tested: a ceiling around seventeen
mounts (eighteen started fine), a system-wide device limit across concurrent VMs (it
failed with nothing else running), and mounts pointing at deleted directories (all
twenty existed). The fourth held — macOS caps directory-share devices per VM, and the
boundary is exactly nineteen.

Each wrong hypothesis was plausible enough to have been implemented without checking.
The first would have shipped an eviction scheme tuned to a number that does not exist.
What made the difference was that every guess was cheap to falsify and was falsified
before any code was written for it. When a limit is the thing being designed against,
the limit is the thing to measure — and the measured value belongs in the code with
the version it was measured against, or the next person re-derives it.

### An interface a backend implements is not a capability it always has

`limactl snapshot` exits with the single word `unimplemented` on a `vz` machine:
Lima's snapshots are a QEMU feature. avar runs host-native environments under `vz`
deliberately, so on an Apple Silicon Mac the everyday environment is precisely the one
that cannot be snapshotted — and the design, the code, and the tests had all assumed
otherwise, because the tests used a fake and the design was written from documentation.

Capability segregation was already right: `Snapshotter` exists because snapshot support
is a real difference between backends. What was missing is that a successful type
assertion answers a compile-time question, while whether an operation is possible can
depend on how one machine was built. The same provider snapshots an emulated machine
and refuses a native one, which is why `ErrUnsupportedCapability` has to exist and be
handled by callers that have already asserted the interface.

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
user believes the grant happened. The flag was refused outright until a backend
implemented forwarding, which the Lima backend now does.

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

### A boundary is provider-neutral when a second host compiles it, not when it reads that way

Tasks 27 and 28 made the backend contract provider-neutral for the WSL2Provider that
Requirement 18 specifies. `MountSpec` grew separate host and guest halves,
`MapProjectPath` was introduced so no caller does path arithmetic, `EditorTarget`
replaced the SSH stanza, and the interface documentation explains at length what WSL
needs and why Lima's answer is only Lima's. Every word of that is right.

The first `go build ./...` on a Windows host failed in two packages before reaching a
single test: `internal/state` locks with `flock(2)` and probes liveness with
`kill(2)`, and `internal/provider/lima` names `SIGWINCH`. Once it compiled, `go test
./...` failed in six more — a directory `fsync` that Windows answers with
`ERROR_ACCESS_DENIED`, and several dozen fixtures asserting that `/Users/dev/code/app`
is an absolute path, which on Windows it is not.

None of that contradicts the abstraction; it is all *below* it. That is the point. The
neutrality of an interface says nothing about the portability of the code on either
side of it, and reviewing a diff cannot tell the difference — a compiler on the other
platform can, in seconds. `GOOS=windows go build ./...` costs nothing and would have
been evidence; reading the interface again was not.

When a task's stated purpose is to make something work elsewhere, compile it
elsewhere in the same PR, even when nothing there runs yet.

### A test double that shares the code's assumption confirms the assumption, not the behaviour

The WSL backend reads applied mounts out of `/proc/mounts` and filters them with
`awk '$3 == "drvfs"'`. Twenty-nine unit tests passed. The fake `wsl.exe` they run
against emitted the already-filtered two columns avar wanted, so every one of
them agreed that the filter worked.

WSL 2 serves DrvFS over 9P. Real `/proc/mounts` reports the *type* as `9p` and
names DrvFS only inside the options, as `aname=drvfs`. The filter therefore
matched nothing on a real machine, and it failed in two directions at once:
`AppliedMounts` reported nothing applied, so `SetMounts` failed its own
verification on a mount that had landed perfectly — and the same predicate in the
provisioning check meant a guest with the whole of `C:` mounted would have passed
the confinement check written to prevent exactly that. The first real
provisioning run found both in one command.

The fake was not wrong to be simple. It was wrong to answer in avar's vocabulary
rather than the tool's: it rendered what avar hoped to parse instead of what
`wsl.exe` writes. A double built that way can only ever confirm that the parser
parses its author's idea of the format.

When a double stands in for an external tool, its fixtures are the contract under
test. Take them from the tool — paste a real line in — and where the parse itself
is the value of the code, put it in front of the real thing at least once. This
is the same rule as *"a test that asserts a command's shape proves only that you
wrote what you wrote"*, arriving from the other end: there the assertion was the
author's, here the input was.

### `/mnt` on WSL is not the user's drives

The same provisioning run refused a perfectly good environment because the
confinement check treated everything under `/mnt` as a Windows drive. `/mnt/wsl`
and `/mnt/wslg` are WSL's own tmpfs mounts — shared utility-VM state and GUI
plumbing — present in every distribution and nothing to do with the host's
filesystem.

A confinement check has to name what it forbids precisely. "Anything under /mnt"
was a guess at what automount does; what it actually does is mount the drives,
and that is what the check should look for. A guess that is too broad fails
honest environments, and the fix for it — narrowing — is exactly how a check
becomes too narrow to catch anything.

### A test that reports a security failure it did not observe

The end-to-end check for PROP-6 — that `avr stop --all` never touches a
distribution avar does not own — skipped whenever no such distribution happened
to be running. Silently, on the machine it was written on, and *always* on a CI
runner, where nothing of the user's is ever running. Fixing that was
straightforward: find one, and start it if it is registered but stopped.

The obvious way to start it is wrong, and the way it is wrong is the lesson.
`wsl --exec true` returns immediately and leaves nothing running inside the
distribution, and WSL reaps an idle distribution about thirty seconds later —
measured, not assumed:

```
t=10s  ubuntu_running=1
t=20s  ubuntu_running=1
t=30s  ubuntu_running=0   <- WSL reaped it, with avar nowhere near it
```

The test provisions an environment before it asserts, which takes longer than
that. So the distribution vanished mid-test, and the assertion reported that
`avr stop --all` had stopped somebody's environment. It accused avar of the
exact violation the property exists to prevent, on no evidence at all. Running
`avr stop --all` by hand against a fresh state directory showed avar printing
"avar is not managing any Linux environments" and leaving the distribution
alone, which is what turned a plausible bug report into a known false one.

Two things generalise past WSL.

**A false report of a security failure is worse than no report.** A skip is
visibly nothing; a red cross that names a property is evidence, and evidence
that is wrong costs whoever chases it. The bar for a test that can accuse the
code of a security violation is higher than for one that cannot.

**A runtime that reaps idle resources will reap the one your test is
measuring.** Anything with an idle timeout — a container runtime, a VM manager,
a connection pool, a scheduler — will do this, and the failure surfaces as the
system under test having done something. If a test needs a resource alive, it
has to hold it alive with something real and not merely bring it into existence.

The same fix has a second edge. Having found a foreign distribution, the cleanup
terminated it unconditionally — including one the developer already had open
with work in it. In the test for "never stop a distribution you do not own".
Clean up what you started, and record which that was at the moment you know it,
because starting a process inside a distribution changes the answer.
