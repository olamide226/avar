# Implementation Plan: avar (`avr`)

## Overview

Implementation is bottom-up: pure logic first (resolver, grammar, state) so the riskiest UX invariants get tests before any VM exists; then the LimaProvider; then the user-facing flows. **Phase 1** delivers the MVP core loop plus arch/distro selection. **Phase 2** completes the MVP with lifecycle (snapshots/reset/idle-stop), isolation, editor integration, and explicit forwarding. **Phase 3** is post-MVP. Every task builds on prior tasks; nothing is orphaned. E2E tests against real Lima live behind `make e2e` and are added as soon as the shell path exists (task 8), then extended per feature.

Module path: `github.com/<owner>/avar`, binary `avr`.

## Phase 1 — MVP core: shell loop + environment selection

- [x] 1. Scaffold project and CLI skeleton  _(PR #1)_
  - Go module, cobra root command, `--version`, styled `avr --help` reflecting the full grammar (flag parsing moved to internal/cli in task 2 — see design.md §3.1)
  - CI: lint + unit tests on macOS runner; `make e2e` target stubbed
  - _Requirements: 17.2_
  - _writes: go.mod, main.go, cmd/root.go, Makefile, .github/workflows/ci.yml_

- [x] 2. Implement argv grammar resolution  _(PR #2)_
  - [x] 2.1 Selector-flag parsing and subcommand vs guest-command split  _(PR #2)_
    - Table-driven tests: `avr`, `avr npm test`, `avr status`, `avr -- status`, `avr --arch amd64 npm test`, `avr --distro fedora code`
    - _Requirements: 2.5, 2.6, 4.5_
    - _writes: cmd/root.go, internal/cli/grammar.go, internal/cli/grammar_test.go_
  - [x] 2.2 Fuzz/property tests for the grammar (Property 9)  _(PR #2)_
    - _Requirements: 2.5, 2.6_
    - _writes: internal/cli/grammar_fuzz_test.go_

- [x] 3. Implement state store  _(PR #5)_
  - Types (`ProjectRecord`, `MachineRecord`, `SessionRecord`), atomic write (temp+rename), advisory file lock, Project_Identity hashing with symlink resolution
  - Unit tests incl. concurrent-writer lock test
  - _Requirements: 11.2 (schema), 17.5_
  - _writes: internal/state/store.go, internal/state/lock.go, internal/state/project.go, internal/state/session.go, + tests (records live in internal/types, so no state/types.go)_

- [x] 4. Implement Resolver  _(PR #8)_
  - Precedence: flags > project record > config.toml > defaults; deterministic machine naming (`avr-<distro>-<ver>-<arch>`, `avr-prj-<hash10>`); supported distro/arch matrix with pinned versions; unsupported values error listing options
  - Unit tests for Property 2 (determinism, no name collisions across selectors)
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 1.5_
  - _writes: internal/resolve/resolver.go, internal/resolve/matrix.go, internal/resolve/resolver_test.go_

- [x] 5. Implement dependency manager (Lima)  _(PR #3)_
  - Detect `limactl`, parse version, compare to pinned minimum; interactive `brew install lima` offer; manual-instructions fallback; refuse unsupported versions
  - Unit tests with fake exec
  - _Requirements: 8.1, 8.2, 8.3, 8.4_
  - _writes: internal/deps/lima.go, internal/deps/lima_test.go_

- [x] 6. Define Provider interface + FakeProvider test double  _(PR #4)_
  - `Provider`, `MachineSpec`, `ShellOpts`, `ProgressSink`; FakeProvider records call sequences for flow tests
  - _Requirements: 17.3_
  - _writes: internal/provider/provider.go, internal/provider/fake/fake.go_

- [x] 7. Implement LimaProvider: machine provisioning and lifecycle  _(PR #11)_
  - [x] 7.1 Lima config generation per (distro, arch)  _(PR #11)_
    - Embedded templates: pinned+checksummed images; vz+VirtioFS+Rosetta for native arch, qemu for foreign; resource defaults min(4, host/2) CPU, min(8GB, host/4) mem; unit tests golden-file the generated YAML
    - _Requirements: 1.2, 4.6, 17.4_
    - _writes: internal/provider/lima/template.go, internal/provider/lima/templates/*.yaml.tmpl, internal/provider/lima/template_test.go_
  - [x] 7.2 EnsureMachine / Stop / Delete / Status via limactl  _(PR #11)_
    - `limactl start --tty=false` create+start, JSON list parsing against fixtures, `avr-` prefix + machines.json ownership filter, progress events, create-failure cleanup (delete partial instance, no record written), provision logs to `~/.avr/logs/`
    - _Requirements: 1.2, 1.3, 1.6, 4.7, 5.4_
    - _writes: internal/provider/lima/lima.go, internal/provider/lima/status.go, internal/provider/lima/lima_test.go, internal/provider/lima/testdata/*_

- [x] 8. Implement shell attachment (interactive + one-shot)  _(PR #20)_
  - `limactl shell --workdir` wiring; PTY iff stdin is TTY; SIGINT/SIGTERM/SIGWINCH forwarding; TERM passthrough with color-capable fallback; exit-code propagation; env policy applied with base allowlist only (TERM, LANG, LC_*)
  - First e2e tests: cold-start `avr true`, `avr sh -c 'exit 42'` → 42, `avr pwd` == host pwd, guest `env` contains no marker var exported on host (Properties 1, 3, 4, 8)
  - _Requirements: 1.1, 1.4, 1.7, 2.1, 2.2, 2.3, 2.4, 3.1, 3.2, 3.3, 3.4, 9.1, 17.1_
  - _writes: internal/provider/lima/shell.go, internal/envpolicy/policy.go, internal/envpolicy/policy_test.go, cmd/shell.go, e2e/shell_test.go_

- [x] 9. Implement mount management  _(PR #29)_
  - Project registration on first use; read applied mounts from `limactl list --json`; `limactl edit --set` + explained restart when adding a project (ProgressSink message); subdirectory reuse of existing mounts; pre-flight stat + in-guest mount verification, hard error instead of wrong-path shell; live-session restart prompt (abort when non-interactive)
  - FakeProvider flow tests for Property 5 (mount confinement); e2e: new project one-time restart, second visit instant, file visible both directions
  - _Requirements: 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 9.3, 9.4_
  - _writes: internal/mounts/mounts.go, internal/mounts/mounts_test.go, e2e/mounts_test.go_

- [x] 10. Implement `avr status` and `avr stop`  _(PR #19)_
  - Status: env label, state, resources, disk, mode, registered mounts, port-forward diagnostics from hostagent log (7.2); empty-state onboarding message; `stop` current selector / `--all`; never touches non-avar Lima machines
  - _Requirements: 5.1, 5.2, 5.3, 5.4, 7.2_
  - _writes: cmd/status.go, cmd/stop.go, internal/provider/lima/portdiag.go, cmd/status_test.go_

- [x] 29. Call the reconciler on startup  _(PR #26)_
  - `internal/state.Reconcile` is built and tested (task 13, PR #9) but **nothing calls it**. Every `avr` invocation should reconcile before resolving, so a machine left by a killed provision is adopted or cleaned up instead of persisting. Found while debugging task 8: a failed run left a running machine with no record and no automatic repair.
  - Decide where it runs (likely `cmd/app.go` before the first provider use), what it prints when it acts, and how it stays cheap on the warm path — design §3.3 requires the consistent case to do no writes
  - _Requirements: 1.6, 17.5_
  - _writes: cmd/app.go, cmd/reconcile.go, cmd/reconcile_test.go_

- [x] 11. Wire `--arch` / `--distro` end to end  _(PR #28)_
  - New-environment provision notice (4.7); one-time amd64 emulation warning on Apple Silicon (4.6); package persistence per environment verified in e2e (install pkg in ubuntu-arm64, absent in fedora-arm64)
  - _Requirements: 4.1–4.7_
  - _writes: cmd/root.go, internal/resolve/resolver.go, e2e/matrix_test.go_

- [x] 12. Port forwarding verification (e2e)  _(PR #27)_
  - Guest server on :3000 reachable at host localhost:3000; listener released after guest close; occupied-host-port conflict surfaces in `avr status`
  - _Requirements: 7.1, 7.2, 7.3_
  - _writes: e2e/ports_test.go_

- [x] 13. Crash-consistency reconciler  _(PR #9)_
  - On startup: adopt healthy orphan `avr-` instances, delete broken ones with notice, drop dangling records; decision-table unit tests (Property 7); Ctrl-C during provisioning cleans up and exits 130
  - _Requirements: 1.6, 17.5_
  - _writes: internal/state/reconcile.go, internal/state/reconcile_test.go_

- [x] 14. Packaging and distribution  _(PR #1)_
  - GoReleaser config (darwin arm64/amd64), Homebrew tap formula with `depends_on "lima"`, release workflow
  - _Requirements: 8.5, 17.2, 17.6_
  - _writes: .goreleaser.yaml, .github/workflows/release.yml_

## Phase 1b — provider-neutral contracts (spec revision)

The Requirement 18 / design revision makes the backend contract provider-neutral so a
post-MVP WSL2Provider can satisfy it unchanged. These land **before task 9**, because
task 9 is the first consumer of the new mount shape. They refactor merged code rather
than adding behaviour, so they are one coherent change, not a per-package guess.

- [x] 27. Make the backend contract provider-neutral  _(PR #15)_
  - `internal/types`: add `ProviderID` and `MountSpec{ProjectID, HostPath, GuestPath, Writable}`; `MachineRecord.Mounts` becomes `[]MountSpec`, `VMType` becomes `Runtime`, plus a `Provider` field
  - `internal/provider`: `Provider` gains `ID()` and `MapProjectPath(projectID, hostRoot, hostCwd) (MountSpec, guestCwd, error)`; `AppliedMounts`/`SetMounts` move to `[]MountSpec`; `SSHConfigProvider` becomes the transport-neutral `EditorTargetProvider` so WSL is not forced through SSH
  - `internal/provider/fake` and `internal/provider/lima` follow; `internal/resolve.Resolve` takes a `ProviderID`
  - `internal/state`: schema v2 — migrate `Mounts []string` to `MountSpec{HostPath: p, GuestPath: p}`, `VMType` to `Runtime`, and stamp `Provider: "lima"` on pre-existing records; migration is covered by a round-trip test from a committed v1 fixture
  - LimaProvider's `MapProjectPath` is the identity mapping, which is exactly what makes the abstraction honest rather than speculative
  - _Requirements: 17.3, 18.14, 6.1, 18.5_
  - _writes: internal/types/*, internal/provider/provider.go, internal/provider/fake/*, internal/provider/lima/*, internal/resolve/*, internal/state/*_

- [x] 28. Route provider selection by host platform  _(PR #17)_
  - `darwin` selects LimaProvider; unsupported hosts fail before any dependency work with a clear message; no user-visible provider flag
  - Keeps Windows-specific branching out of `cmd/` from the start (REQ-18.1, REQ-18.14)
  - _Requirements: 18.1, 18.14, 17.6_
  - _writes: internal/provider/select.go, internal/provider/select_test.go, cmd/root.go_

## Phase 2 — MVP completion: lifecycle, isolation, editor, forwarding, release

- [x] 15. Implement snapshots and restore  _(PR #34)_
  - `avr snapshot <name>` / bare `avr snapshot` list with timestamps / `avr restore <name>`; stop-if-needed-then-resume orchestration; unknown name lists available
  - _Requirements: 10.1, 10.2, 10.4_
  - _writes: cmd/snapshot.go, internal/provider/lima/snapshot.go, internal/provider/lima/snapshot_test.go, e2e/snapshot_test.go_

- [x] 16. Implement `avr reset`  _(PR #32)_
  - Delete + re-provision from template/base; interactive confirmation with explicit destruction summary, `--yes` bypass; e2e asserts host project files untouched (Property 10)
  - _Requirements: 10.3_
  - _writes: cmd/reset.go, e2e/reset_test.go_ — shipped as `e2e/z_reset_test.go`; the `z_` prefix orders the destructive tests last within the package, so they cannot pull the environment out from under the tests that follow

- [x] 17. Implement project isolation  _(PR #36)_
  - [x] 17.1 Base-machine + clone fast path  _(PR #36)_
    - Pristine stopped `avr-base-*` per used (distro, arch); `limactl clone` for isolated create; full-provision fallback
    - _Requirements: 11.1_
    - _writes: internal/provider/lima/clone.go, internal/provider/lima/clone_test.go_
  - [x] 17.2 `--isolate` / `--shared` / `avr isolate off` flows  _(PR #36)_
    - Remembered default in ProjectRecord (no repo file); per-invocation `--shared` override; `isolate off` clears default and offers machine deletion; isolated machine mounts only its project; reset scopes to the project machine
    - FakeProvider flow tests (Properties 5, 10); e2e isolation smoke test
    - _Requirements: 11.1, 11.2, 11.3, 11.4, 11.5_
    - _writes: cmd/isolate.go, internal/resolve/resolver.go, e2e/isolate_test.go_

- [x] 18. Implement explicit env/credential forwarding  _(PR #31, #38)_
  - Repeatable `--env NAME[=V]`, `--env-file` (pre-flight validation), `--ssh-agent` session-scoped socket forwarding; optional persistent `forward_env` allowlist in config.toml; property tests that nothing outside grants leaks (Property 4)
  - _Requirements: 12.1, 12.2, 12.3, 12.4, 9.2_
  - _writes: internal/envpolicy/policy.go, internal/envpolicy/policy_test.go, cmd/root.go_

- [ ] 45. Forward the granted environment into an interactive shell on macOS
  - `--env`, `--env-file`, `forward_env` and approved `.avr.toml` variables reached `avr <command>` and were silently dropped from `avr`. `internal/provider/lima.shellArgv` returned `limactl shell --workdir … <machine>` with no argv when `ShellOpts.Argv` was empty, and the environment was composed only in `guestArgv`, which that shape never reached. A code comment asserted the interactive form "has no equivalent", so the gap read as a decision rather than a defect.
  - Interactive sessions now pass an argv of their own: `sh -c 'exec env -- "$@" "$SHELL" -l' sh NAME=value …`. Assignments are positional arguments to a constant script, so no value is ever parsed as shell syntax; `"$SHELL"` is expanded before the grant applies, so the account's own login shell is exec'd (REQ-1.1) even when the grant names SHELL; `-l` keeps it a login shell.
  - Verified against Lima 2.2.0 and Ubuntu 24.04 through a pseudo-terminal, before and after. Lima runs any argv as `exec "$SHELL" -l -c '<argv>'`, so the profile is read by that shell and again by the interactive one — which means a profile that assigns a granted name unconditionally wins in an interactive session. That is stated in design §3.5 and on the site rather than worked around.
  - WSL 2 does not have the same defect: `WSLENV` is set on the `wsl.exe` process rather than in the argv, so an interactive launch already carries the grant. A test now asserts it instead of leaving it to be true by accident.
  - _Requirements: 12.1, 12.2, 12.4, 1.1, 9.1_
  - _Properties: 4_
  - _writes: internal/provider/lima/shell.go, internal/provider/lima/shell_test.go, internal/provider/wsl2/shell_test.go, e2e/shell_test.go, site/commands/avr.md, site/syntax/index.md, docs/lessons.md, .kiro/specs/avar-cli/design.md, .kiro/specs/avar-cli/tasks.md_

- [x] 19. Implement `avr code`  _(PR #33, #38)_
  - avar-owned `~/.avr/ssh/config` from `limactl show-ssh`; one-time approved `Include` line in user ssh config; launch `code --remote ssh-remote+avr-<machine> <path>`; missing-`code` guidance; honors selector flags
  - _Requirements: 13.1, 13.2, 13.3, 13.4_
  - _writes: cmd/code.go, internal/editor/vscode.go, internal/editor/sshconfig.go, internal/editor/sshconfig_test.go_

- [x] 20. Implement session tracking and idle auto-stop  _(PR #35)_
  - `sessions.json` attach/detach records with stale-pid pruning; `avr internal idle-check`; launchd agent install with one-time notice; config.toml `idle_timeout` (default 2h, "0" disables); never stops machines with live sessions (Property 11)
  - _Requirements: 5.5_
  - _writes: internal/session/session.go, internal/session/idle.go, cmd/internal_idle.go, internal/session/session_test.go_

- [x] 30. Implement `avr destroy`  _(PR #45)_
  - `avr destroy` removes the current environment; `--all` removes every avar-managed environment; `--orphaned` removes isolated environments whose project directory no longer exists, naming the project each served. Destruction summary then interactive confirmation, bypassable with `--yes`, exactly as `avr reset` does it. Host project files untouched (Property 10). The machine's SSH host entry goes with it (`forgetSSHHost`), and its records with it.
  - Add `destroy` to the argv grammar's subcommand set — the split a user sees must not change as commands land, so the name is reserved even before the handler exists.
  - Found after Phase 2 was otherwise complete: every other lifecycle verb was specified and removal was not, so an environment could be created but never deliberately removed, and reclaiming its disk meant reaching for `limactl` (REQ-1.5).
  - FakeProvider flow tests for each scope; e2e proves the machine is gone, the record is gone, and the host project directory is intact
  - _Requirements: 5.6, 5.7, 5.8_
  - _writes: cmd/destroy.go, cmd/destroy_test.go, internal/cli/grammar.go, internal/cli/grammar_test.go, e2e/z_destroy_test.go_

- [x] 31. Exercise the release pipeline before tagging  _(PR #44, #47, #48)_
  - `.goreleaser.yaml` and `.github/workflows/release.yml` exist (task 14) and have never run. Three things are missing and would each fail a real release: the `homebrew-tap` repository does not exist, the `HOMEBREW_TAP_TOKEN` secret is not configured, and no tag has ever been pushed.
  - Order: `goreleaser release --snapshot --clean` locally (builds every artifact, publishes nothing) → create the tap repository and PAT secret → tag a prerelease (`v0.1.0-rc.1`) and verify the GitHub release, the archive, the checksums and the generated cask → only then tag `v0.1.0`
  - Decide and document the signing posture: released binaries are unsigned and unnotarised, so a direct tarball download hits Gatekeeper. The cask strips the quarantine attribute, which covers Homebrew users; anyone else needs to be told.
  - _Requirements: 17.2, 8.5_
  - _writes: docs/releasing.md, .goreleaser.yaml (only if the dry run finds a defect)_

- [ ] 32. Public-facing documentation
  - [x] 32.1 Rewrite the README for a public audience  _(PR #41, #42, #46)_
    - It currently reads as a project-status note. Needs: one-line pitch, install (Homebrew and direct), a sixty-second quickstart, a command table covering the whole surface, requirements, how it works, honest limitations, contributing, licence.
    - **Platform support must be stated honestly.** macOS is supported today; Windows via WSL 2 is Requirement 18, Phase 4, and not started. The two must be visibly separated so no reader concludes Windows works. *(Superseded by Phase 4: Windows now works and the README says so, stating that it has had far less mileage than the macOS path rather than claiming parity. The rule this bullet expresses — describe each platform as it actually is — is what kept the README honest through the whole of Phase 4, in both directions.)*
    - Limitations to state rather than omit: snapshots need an emulated environment (Lima's snapshots are QEMU-only), sixteen project directories per environment, unsigned binaries.
    - _Requirements: 17.2_
    - _writes: README.md, CONTRIBUTING.md_
  - [ ] 32.2 Publish a documentation site  _(after v0.1.0)_
    - GitHub Pages with the just-the-docs Jekyll theme: free for public repositories, no build toolchain beyond what Pages provides, and no third-party service.
    - The README stays canonical for install and quickstart; the site carries what outgrows one page — command reference, configuration, troubleshooting, the environment matrix.
    - **On a custom subdomain:** Pages serves `<owner>.github.io/avar` free, and a `CNAME` subdomain is free to configure — but only against a domain that is already owned and paid for. If there is no domain, the `github.io` URL costs nothing and needs no decision.
    - Deferred until after v0.1.0 deliberately: a docs site that forks from the README before the README is settled produces two sources of truth.
    - **Source in `site/`, not `docs/`** (decided while building it). `docs/` holds developer documents (`lessons.md`, `releasing.md`) that README, CLAUDE.md and CONTRIBUTING.md link to. Serving the site from there would publish any future file added beside them unless an exclude list remembered it; a separate directory publishes only what was written for the site, and moves nothing.
    - **Scope widened at review (2026-09-17):** a page per command (help, examples, exit statuses, errors), a Syntax section (the command-line grammar, a full `.avr.toml` and `config.toml` reference, precedence), and a Design decisions page giving each choice, its reason and its trade-off.
    - **No second source of truth.** Whatever the code defines is generated between markers or checked against the code by a test, and `make docs` rewrites the generated parts:
      - `cmd/docsite_test.go`: each command page's help block, rendered through the functions `Execute` calls; the command index; the reserved names; the environment matrix; that every command has a page and no page names a command that does not exist; that every `avr` example on a command page is a command line the grammar accepts and shows the command it documents; that every link into the repository names a file that exists.
      - `internal/cli/docsite_test.go`: the command-line page's flag table lists exactly the flags the grammar parses.
      - `internal/projconfig/docsite_test.go`: the `.avr.toml` key table lists exactly the reader's keys in order; every example file on the page is parsed and must be accepted or refused as the page says, with the error text it shows; the manifests `avr init` reads and the packages it proposes per runtime are generated from detection's tables.
      - The per-host availability the matrix page states is held by a test in each backend that every environment the resolver accepts has an image (Lima) or a registry entry (WSL).
    - Prose about behaviour the code does not define as data (exit statuses, messages, the design rationale) is written by hand, and was checked against the code when written.
    - Built and deployed by `.github/workflows/pages.yml`: pull requests build without deploying and upload the built site as a preview artifact, and only `main` deploys. Pages must be enabled with GitHub Actions as its source before the first deploy.
    - _Requirements: 17.2_
    - _writes: site/**, .github/workflows/pages.yml, cmd/docsite_test.go, internal/cli/docsite_test.go, internal/projconfig/docsite_test.go_
    - _also wrote: README.md (one line linking the site), Makefile (`make docs`), .gitignore (the site's build output and local bundle), internal/provider/lima/images_test.go and internal/provider/wsl2/images_test.go (backend coverage of the resolver's matrix, which the site's per-host claims rest on)_

- [x] 41. Harden host-agent reaping, and make `avr stop` say what it did  _(PR #78; the QEMU question moves to task 43)_
  - PR #50 made `avr stop` reap the orphaned Lima host agents that a stopped instance can leave behind. The detector is right and was earned from a real leak; the parts around it have three defects, found by review of the merged code rather than by a failure.
  - **A kill race turns a successful stop into a reported failure.** `signalPID` treats `/bin/kill`'s exit status as the verdict (`internal/provider/lima/hostagent.go:82`), and `/bin/kill -TERM <exited pid>` exits 1 with "No such process" — verified. `reapHostAgents` returns that error (`:25`, `:47`) and `stopMachine` wraps it into a failed stop (`internal/provider/lima/lima.go:377`). The window is not narrow: the escalation pass at `:47` sends KILL to pids that received TERM 200 ms earlier, which are exactly the ones most likely to have exited in between, and `avr stop --all` records one `failures` entry per machine it happens to. **The signal's exit status is not the verdict — tolerate "no such process" and let a re-scan decide.**
  - **The constraint on that fix, which is easy to walk into:** `internal/provider/lima` compiles on Windows and must keep doing so (task 38). `hostagent.go` carries no build tag, and `syscall.Kill` does not exist there — the package confines it to `signals_unix.go` for exactly this reason. Either keep `/bin/kill` and ignore its exit status, or split the file on a build tag. Reaching for `syscall.Kill` reintroduces the breakage task 38 fixed.
  - **`avr stop` is the only command that discards its progress.** All four call sites pass `types.DiscardProgress` (`cmd/stop.go:91,133,169`, `cmd/internal_idle.go:85`) while every other command uses `progressTo(app.Err)`. So the force-stop warning at `lima.go:371` — *"Unsaved work inside it may be lost"* — is written to a sink nobody reads. That is a safety message, not telemetry. Route stop through the same sink, and give `reapHostAgents` a way to report what it killed.
  - **Idle auto-stop never reaps.** `cmd/internal_idle.go:79` skips machines that are not running, and an instance Lima reports as Stopped with an escaped agent is precisely that state, so only an explicit `avr stop` ever self-heals. `avr status` also renders a flat "stopped", giving the user no reason to run one.
  - **To verify rather than assume: does killing the agent take an emulated instance's VM process with it?** The match is `limactl hostagent … <machine>` only, and a `qemu` instance runs `qemu-system-*` as a separate process. If it survives, the leak this task exists to fix is only half fixed on `--arch amd64`. Check on a real emulated machine before deciding whether the detector needs a second predicate; do not add one on suspicion.
    - *Observed while fixing the rest of this task, on Lima 2.2.0 / macOS 26.5.2.* On a `vz` machine the VM runs in a separate `com.apple.Virtualization.VirtualMachine` XPC process, and it exits within a second of its host agent being sent SIGKILL; Lima then reports the instance Stopped. So on `vz` the agent is the only process to reap. **The QEMU half is still unverified:** the verification host has no QEMU installed, so no emulated machine could be created, and no second predicate was added. One observation bears on it without answering it: `limactl list --json` reports each instance's `hostAgentPID` and `driverPID`, and while a machine was starting it read Stopped (neither), then Broken (agent pid, no driver pid), then Running (both). If Lima applies the same rule in the other direction — not yet checked — a `qemu-system-*` that outlived its agent would be listed as Broken, which `avr stop` leaves alone, rather than as Stopped. Settle it on a host with QEMU by starting an `--arch amd64` machine, sending its agent SIGKILL, and reading both the process table and `limactl list --json`.
  - Lower priority, and hardening rather than defects: `ps -axo` truncating a long command line would make `HasSuffix(command, " "+machine)` miss an agent **silently**, which is the direction that matters — `ps -axww -o` is cheap insurance; and `--all` runs one `ps` per machine where one scan would do.
    - *Measured before deciding.* `ps -axww` shipped. The single scan did not: one `ps -axww -o pid=,command=` took 67 ms on the verification host, and each machine `avr stop --all` visits also pays a `limactl list --json` inside `Stop` (100 ms) that one scan would not remove. Sharing a scan across machines needs either state held between `Stop` calls, which the Lima provider deliberately does not keep, or a batch operation on the `Provider` interface; neither is worth 67 ms per stopped environment on a command that is not the warm path. Also observed: macOS `ps` did not truncate even without `ww` when its output was a pipe, so the truncation risk was not live on that host.
  - The reaping has no automated coverage: the parser is unit-tested and the killing is not, because the tests stub the reaper. A failing test comes first, per the testing section of `CLAUDE.md`.
  - _Requirements: 5.2, 5.5, 1.5_
  - _writes: internal/provider/lima/hostagent.go, internal/provider/lima/lima.go, cmd/stop.go, cmd/internal_idle.go, internal/provider/lima/hostagent_test.go_ — and, found necessary while doing it, internal/provider/lima/runner_test.go (the fake runner answers `ps` and `kill`, since reaping now goes through the provider's Runner), internal/provider/fake/fake.go (a backend warning raised on stop), cmd/stop_test.go, cmd/internal_idle_test.go

## Phase 3 — Post-MVP

- [x] 21. Linux-native workspace mode (`--native-fs`)  _(21.1 shipped; 21.2 retired)_
  - Project sync into guest `~/workspaces/<project>` (Lima `--sync` substrate), reviewable sync-back, conflict surfacing without silent overwrite
  - _Requirements: 14.1, 14.2, 14.3_
  - _writes: internal/workspace/native.go, cmd/root.go, e2e/nativefs_test.go_

  - [x] 21.1 The provider-neutral half, and the WSL implementation  _(PR #65, #67)_
    - `--native-fs` runs a session in a copy of the project on the distribution's own filesystem at `~/workspaces/<name>-<hash>`; `avr sync [--to-host|--to-guest] [--yes]` reviews and applies changes between the two copies. Requirement 14 is satisfied on Windows.
    - Everything above the backend is host-neutral: `internal/workspace/native.go` is a pure three-way planner over content hashes, `types.NativeWorkspace`/`WorkspaceScan` carry the vocabulary, and `provider.NativeWorkspacer` is an optional capability with no WSL concept in any signature. `--native-fs` on a backend that does not implement it says so plainly rather than failing.
    - Content hashes and never timestamps: two filesystems, two clocks, two granularities, across a translation layer. The baseline advances **per file**, which is what makes a one-directional sync correct while changes are outstanding the other way. A conflict refuses the whole apply, because a partial one leaves a destination that is neither copy.
    - `sync` joins the reserved subcommand names, so a project script of that name needs `avr -- sync`. Documented in the README's reserved-names list and in `avr sync --help`, and the list is checked against `cli.Subcommands()` by a test rather than maintained by hand (#67).
    - _Requirements: 14.1, 14.2, 14.3, 18.11_
    - _Properties: 22_
    - _writes: internal/workspace/native.go, internal/types/workspace.go, internal/provider/provider.go, internal/provider/wsl2/native.go, internal/provider/fake/native.go, cmd/native.go, cmd/root.go, internal/cli/grammar.go, internal/workspace/advise.go, e2e/nativefs_test.go, .kiro/specs/avar-cli/design.md_

  - 21.2 The Lima implementation: **retired, not built** (2026-09-16)
    - Native workspace mode fixes a cost that Lima does not have. WSL reaches a Windows directory through a translation layer, so a dependency-heavy command pays on every file operation; a Lima machine shares the project over VirtioFS at native speed. A second copy on Lima would bring the divergence Requirement 14 exists to manage and none of the speed that justifies it.
    - What replaces it is REQ-14.4: on a backend that reaches the project directly, `--native-fs` says the flag is unnecessary, and says so **before any machine work**. Checking what that path did turned up a defect. `avr --native-fs` booted, or even provisioned, the environment before refusing, so a user could wait minutes for the answer. The shell path now refuses first. `avr --native-fs code`, `cursor` and `zed` had the same ordering, which was fixed once task 24's rewrite of `cmd/code.go` landed (#76). `avr sync` already refused first.
    - The shape 21.1 was built for still holds: a future backend with a filesystem boundary implements the three `NativeWorkspacer` methods and nothing above the Provider boundary changes.
    - _Requirements: 14.4_
    - _writes: cmd/shell.go, cmd/native_test.go_

- [x] 22. `.avr.toml` support and `avr init` detection  _(PR #82, #85, #86)_
  - Config schema (distro/arch/cpus/memory/packages/forward_env) applied at resolve time; manifest scanners (package.json, pyproject.toml, go.mod, Cargo.toml, Dockerfile, docker-compose.yml, .tool-versions, mise.toml); confirm-before-write proposal UX; zero-config path unchanged
  - Design: design.md §3.11, Properties 23 and 24. The original manifest named no caller for the parsed settings and no state for approvals; the sub-tasks below name both.
  - _Requirements: 15.1, 15.2, 15.3, 15.4_
  - [x] 22.1 Strict `.avr.toml` reader and distro/arch at resolve time  _(PR #82)_
    - `projconfig.Parse`/`Load` for the flat TOML subset (strings only at this stage), `resolve.Options.ProjectConfig` as an injected reader, the precedence layer between the project record and global config, wired into `App.Resolve` so every resolving command honours it
    - _Requirements: 15.1 (distro, arch), 15.4_
    - _Properties: 24 (first clause)_
    - _writes: internal/projconfig/config.go, internal/projconfig/config_test.go, internal/resolve/resolver.go, internal/resolve/resolver_test.go, cmd/app.go, cmd/projconfig_test.go, README.md_
  - [x] 22.2 cpus, memory, packages and forward_env  _(PR #85; verified on real Lima: sizing, approval, apt-get install; dnf and WSL in task 43)_
    - Integers and string arrays in the reader; `provider.MachineSizer` (Lima implements it; the clone path applies sizes and the base no longer inherits them); approval prompt and approved sets in `ProjectRecord`; installed packages in `MachineRecord`; `projconfig.InstallCommands`; project-approved names into `envpolicy.Input.Allowlist`
    - _Requirements: 15.1 (cpus, memory, packages, forward_env), 15.3, 9.1, 12.4_
    - _Properties: 23_
    - _writes: internal/projconfig/config.go, internal/projconfig/install.go, internal/projconfig/*_test.go, internal/types/records.go, internal/state/store.go, internal/state/*_test.go, internal/provider/provider.go, internal/provider/lima/clone.go, internal/provider/lima/lima.go, internal/provider/lima/*_test.go, internal/provider/fake/fake.go, internal/envpolicy/policy.go, cmd/projconfig.go, cmd/app.go, cmd/shell.go, cmd/code.go, cmd/projconfig_test.go, cmd/projgrants_test.go, README.md, .kiro/specs/avar-cli/tasks.md_
  - [x] 22.3 `avr init`  _(PR #86)_
    - `projconfig.Detect`/`Propose`/`Render`; `cmd/init.go` shows the detected stack and the exact file, asks, writes with exclusive creation; no terminal writes nothing; `init` reserved in the grammar, help, and README
    - _Requirements: 15.2, 15.3, 2.6_
    - _Properties: 24 (second clause)_
    - _writes: internal/projconfig/detect.go, internal/projconfig/detect_test.go, internal/projconfig/render.go, cmd/init.go, cmd/init_test.go, cmd/app.go, cmd/root.go, internal/cli/grammar.go, README.md, .kiro/specs/avar-cli/design.md, .kiro/specs/avar-cli/tasks.md_
  - [ ] 22.4 Refuse cpus and memory larger than the host
    - Maintainer decision (2026-09-17): a `.avr.toml` whose `cpus` exceed the host's logical CPUs or whose `memory` exceeds its physical memory is refused where the size would apply (creating the project's isolated environment on a `MachineSizer`), before any machine work, in one message naming the file, lines, values and what the host has. Where the size cannot apply (shared environment, existing isolated environment, WSL) it blocks nothing, and the existing one-time notice says it also exceeds the host.
    - `provider.MachineSizer` reports `HostCapacity` instead of being a bare marker; LimaProvider reuses its `sysctl hw.memsize` probe without the fallback; `types.HostCapacity`; `projconfig.Config.ExceedsHost` and the line each size was set on; the check wired into the shell, editor, `sync` and `reset` paths. Callers: `runGuest`, `openInEditor`, `runSync`, `runReset`.
    - Also recorded in design §3.11: the maintainer's confirmation that project configuration is read only from the Project's own `.avr.toml` and the global `config.toml`, with no parent search.
    - Not verified on real Lima in this task: a refused file is proven against the fake, and the probe against the real `sysctl`.
    - _Requirements: 15.5, 15.1, 17.4_
    - _Properties: 25_
    - _writes: internal/types/env.go, internal/provider/provider.go, internal/provider/lima/host.go, internal/provider/lima/lima.go, internal/provider/lima/lima_test.go, internal/provider/fake/fake.go, internal/projconfig/config.go, internal/projconfig/render.go, internal/projconfig/config_test.go, internal/projconfig/detect_test.go, internal/projconfig/capacity_test.go, cmd/projconfig.go, cmd/shell.go, cmd/code.go, cmd/native.go, cmd/reset.go, cmd/projsize_test.go, README.md, .kiro/specs/avar-cli/requirements.md, .kiro/specs/avar-cli/design.md, .kiro/specs/avar-cli/tasks.md_

- [x] 23. `avr ports` and `avr open`  _(PR #79; Lima e2e run and passing; Windows behaviour in task 43)_
  - Forwarded-port listing with guest process attribution where determinable; `avr open <port>` browser launch with not-forwarded message
  - _Requirements: 16.1, 16.2_
  - _writes: cmd/ports.go, internal/provider/lima/portdiag.go_
  - _also wrote: cmd/{app,root,ports_test}.go, internal/cli/grammar{,_test}.go, internal/provider/provider.go, internal/provider/listeners/* (guest listener script and parser shared by both backends), internal/provider/wsl2/portdiag{,_test}.go, internal/provider/wsl2/wsl2_test.go, internal/provider/lima/{portdiag,runner}_test.go, internal/browser/*, e2e/ports_test.go, README.md, .kiro/specs/avar-cli/design.md_

- [x] 24. Additional editors (`avr cursor`, `avr zed`) reusing the SSH plumbing  _(PR #76, #77; launching the real editors in task 43)_
  - _Requirements: 13.5, 13.6, 13.7, 13.8, 13.9 (criteria added with this task; 13.x pattern)_
  - _writes: internal/editor/cursor.go, internal/editor/zed.go, cmd/code.go_

- [x] 25. Second provider (OrbStack or SSH) behind the Provider interface  _(retired: satisfied by the WSL2Provider, Phase 4)_
  - Proves 17.3: no command-layer changes permitted by the task's definition of done
  - **Retired 2026-09-16 rather than built.** The second backend exists: Phase 4's WSL2Provider. It shows what this task set out to show, with one honest qualification. Phase 4 did change `cmd/`, but for Windows host concerns (the Task Scheduler branch in `cmd/internal_idle.go`) and for new capabilities (`avr sync`), not to branch on a backend. The one backend branch that did creep in (`cmd/shell.go` testing `ProviderWSL2`) was removed in task 38b. After that, `cmd/app.go`, the composition root, is the only file in `cmd/` that names a backend. Task 39 turns that from a reviewer's observation into a test.
  - OrbStack, SSH, and cloud providers are unplanned, not rejected. Any of them needs its own requirement first.
  - _Requirements: 17.3_
  - _writes: none_

- 26. VS Code extension: terminal profile picker invoking `avr`: **deferred to the backlog** (2026-09-16)
  - It has no EARS requirement, and publishing it needs a VS Code Marketplace publisher account. Write the requirement before planning any work. Until then it is not on the roadmap and not part of finishing this spec.
  - _Requirements: (product backlog — no EARS requirement yet; spec before build)_
  - _writes: editors/vscode-extension/*_

## Phase 4 — Windows host support via WSL 2 (Requirement 18, post-MVP)

Gated on the MVP shipping. Every task below sits behind the Provider boundary
established in tasks 27 and 28: none of them may add a Windows branch to `cmd/`.

**Status: shipped** (PRs #52–#60). `avr.exe` provisions and drives avar-owned WSL 2
distributions, and the boundary held — **no command's behaviour branches on which
backend is in use.** The only backend names in `cmd/` are the composition root's
in `app.go`, which imports both packages and maps a provider ID to an
implementation; something has to, and Property 21 names that exempt set. What
task 38b had to restore was the real rule: `cmd/shell.go` was the last place a
command's behaviour turned on `p.ID() == types.ProviderWSL2`, and it now reads
the condition off the `MountSpec` instead.

**Requirement 18 is now complete.** Its last open clause was REQ-18.11's second
half — that accepting the cross-filesystem recommendation routes to Requirement
14's reviewable synchronization — which could not be honoured while Requirement 14
did not exist. Task 21.1 built it, so the advisory names `--native-fs` and
`avr sync` rather than giving generic advice, and `TestMessage_IsActionableToday_REQ_18_11`
inverted from forbidding that mention to requiring it.

Everything Requirement 18 asks for is implemented and has been exercised against a
real WSL 2 installation (task 38c), not only against a fake.

- [x] 38. Make the existing packages build and pass their tests on a Windows host
  - `go build ./...` does not compile on `windows/amd64` today: `internal/state` uses `syscall.Flock` for the advisory lock and `syscall.Kill(pid, 0)` for stale-session detection, and `internal/provider/lima` names `syscall.SIGWINCH` and `syscall.Kill`. None of that is Windows work in disguise — it is POSIX leaking through packages that are otherwise portable — but every task below is unreachable until it is fixed, because none of their tests can even be compiled.
  - Split each on a build tag rather than branching at runtime: an exclusive-sharing file handle for the lock (released by the OS if `avr` is killed, which is what `flock` gives on the other side), `OpenProcess`/`GetExitCodeProcess` for process liveness, and a signal relay that forwards what Windows actually has. `internal/provider/lima` already carries `diskusage_other.go`, so compiling off-unix is an established intent of that package rather than a new one.
  - Add a `windows-latest` CI job running lint, build and unit tests, so the Windows build cannot regress between Phase 4 tasks. Lima remains macOS-only and `provider.SupportedHost` still refuses Windows: what this task claims is that the code compiles and its pure logic is proven on both hosts, not that avar runs on Windows yet.
  - Found on the first `go test ./...` of a Windows host, which did not reach a single test.
  - _Requirements: 18.14, 17.5_
  - _writes: internal/state/{lock,lock_unix,lock_windows,session,session_unix,session_windows,store,atomic_unix,atomic_windows}.go, internal/provider/lima/{shell,signals_unix,signals_other}.go, go.mod, .github/workflows/ci.yml, docs/lessons.md, and the test files that hard-code POSIX host paths or macOS-only behaviour: internal/{types,resolve,mounts,state,deps,editor}/*_test.go, internal/provider/{fake,lima}/*_test.go, cmd/status_test.go_

- [x] 33. WSL capability detection and prerequisites
  - Probe WSL presence, version, and WSL 1 vs 2; offer install/upgrade where safe; describe elevation or restart requirements before acting; never register a partial environment
  - _Requirements: 18.2, 18.3, 18.4_
  - _writes: internal/deps/wsl.go, internal/deps/wsl_test.go_

- [x] 34. WSL2Provider: distribution lifecycle
  - Import avar-owned root filesystems with a reserved name prefix plus a registry record; never touch a user-managed distribution; reject unsupported architectures before provisioning
  - _Requirements: 18.6, 18.7, 18.12_
  - _writes: internal/provider/wsl2/*_

- [x] 35. WSL2Provider: path mapping and execution
  - `MapProjectPath` to `/mnt/avr/projects/<Project_Identity>` via DrvFS with automatic drive mounting disabled; `wsl.exe --distribution … --cd … --exec …` preserving streams, PTY, resize, signals and exit codes
  - Shipped as `/mnt/avr/projects/<name>-<Project_Identity prefix>` — `app-3fa9c2b1d0`, not sixty-four hexadecimal characters. This path is the user's working directory and appears in their prompt, their editor's title bar and every error any tool prints, so it is named for the project before it is identified by its hash. The hash half is never dropped, which is what keeps two `api` directories distinct (PROP-14). design §3.2 and §3.6 amended in the same PR (#55).
  - There is no 128+signal case in exit-status handling: a Windows process has an exit code and no signal status, so 18.8's "where Windows and WSL expose an equivalent" is what licenses the omission rather than it being a gap.
  - _Requirements: 18.5, 18.8_
  - _writes: internal/provider/wsl2/path.go, internal/provider/wsl2/shell.go, + tests_
  - _also wrote: internal/provider/wsl2/{mounts,console_windows,console_other}.go, internal/types/mount.go (EqualMappings), internal/provider/select.go, cmd/app.go, internal/state/store.go (DistrosDir), .kiro/specs/avar-cli/design.md_

- [x] 36. Windows state, ports, and editor integration
  - Per-user non-roaming state dir; case-insensitive `PathKey` so drive-letter and separator spellings cannot duplicate a project; localhost forwarding diagnostics; `avr code` through the `wsl+<distro>` remote authority with no SSH config
  - Two manifest deviations, both deliberate. The path key shipped as `internal/state/pathkey_{windows,unix}.go` rather than `path_windows.go`, alongside `dir_{windows,unix}.go` and `perm_{windows,unix}.go`, because the state directory's location and its access control are separate host questions from path identity and mode bits are not how Windows expresses permissions. And the editor target shipped as `internal/provider/wsl2/editor.go` rather than `internal/editor/wsl.go`: `avr code` needed no command-layer change at all, because the backend returns `wsl+<distro>` and no SSH material and `cmd/code.go` writes SSH configuration only when a backend hands it some (PROP-17). Putting it under `internal/editor` would have created the Windows branch REQ-18.14 forbids.
  - The POSIX `PathKey` is the resolved path with nothing added, which is not an inconsistency with design §3.2's prefixed Windows key: every project identity avar has written on macOS is the hash of exactly that string, and prefixing it would orphan every existing record for nothing gained. The Windows key has no history to keep.
  - _Requirements: 18.9, 18.10, 18.13_
  - _writes: internal/state/path_windows.go, internal/provider/wsl2/portdiag.go, internal/editor/wsl.go, + tests_
  - _also wrote: internal/state/{pathkey,dir,perm}_{windows,unix}.go, internal/provider/wsl2/editor.go, cmd/internal_idle.go (Task Scheduler), internal/types/records.go, internal/state/project.go_

- [x] 37. Windows packaging and cross-filesystem guidance
  - Self-contained `avr.exe` for supported Windows architectures; once-per-project dismissible recommendation for Linux-native workspace mode when a workload would suffer from cross-filesystem I/O
  - **REQ-18.11 was satisfied in one half only when this shipped, and is now whole.** The advisory was detected, shown once per project and dismissible; its second clause — "accepting that recommendation SHALL use Requirement 14's reviewable synchronization" — could not be honoured while Linux-native workspace mode did not exist. Rather than name a flag avar did not have, the message carried Microsoft's own advice, and `TestMessage_IsActionableToday_REQ_18_11` asserted it did *not* say `--native-fs`. **Task 21.1 closed it**, and that test now requires the mention it used to forbid — which is why the constraint was written as a test rather than left as an intention.
  - _Requirements: 18.11, 18.14_
  - _writes: .goreleaser.yaml, .github/workflows/release.yml, internal/workspace/advise.go_
  - _also wrote: README.md, cmd/shell.go, internal/types/records.go_

Three pieces of Phase 4 shipped without a task describing them. They are recorded
here so the phase's history matches what is on `main`.

- [x] 38a. WSL2Provider: snapshot and restore
  - REQ-18.12 names snapshot and restore among what the WSL backend must implement, and no task description mentioned them — task 34 cited the requirement and built only the lifecycle, so `avr snapshot` on Windows reported a gap nobody had chosen.
  - A snapshot is the distribution's disk exported as a VHD rather than a tar, so permissions, symbolic links, sparse files and extended attributes survive. design §3.6 amended: the export flag is `--format vhd`; `--vhd` is reserved for an import.
  - design §5's pre-restore rollback export is **deliberately not implemented**, and §5 is amended to say so. It is a full copy of the disk paid on every restore to insure against a rare import failure whose recovery can fail identically. Restore is made retryable instead — it does not require the distribution to exist — which costs nothing. The residual risk is stated in the design, the code and the error message.
  - Restore verifies the imported disk (marker, account, sudo, confinement) but not its release, since a snapshot holds the release it held.
  - _Requirements: 10.1, 10.2, 10.4, 18.12_
  - _writes: internal/provider/wsl2/snapshot.go, internal/provider/wsl2/provision.go, cmd/app.go, internal/state/store.go, .kiro/specs/avar-cli/design.md, + tests_

- [x] 38b. Bring the Windows warm path inside REQ-17.1's budget
  - Measured at ~570–670 ms against a ~500 ms budget: `wsl --list` costs ~130 ms per sweep and was paid three times, because `Status`, `EnsureMachine` and `Shell` each built their own view milliseconds apart, plus two `schtasks` subprocesses on every invocation. The view is now one per invocation, held on the Provider and dropped by `forget()` after any mutation; scheduler registration became a stat of a stamp file. ~310–410 ms removed.
  - Also fixed two defects: every project share was handed to root (`id -u` with no argument in a script that already runs as root, so `uid=0,gid=0` reached both the mount and `/etc/fstab`), and `cmd/shell.go` branched on `p.ID() == types.ProviderWSL2` — a backend name in the one layer that may not know one (REQ-17.3). The condition is now read off the mapping: a project crosses a boundary exactly when `HostPath != GuestPath`.
  - _Requirements: 17.1, 17.3, 18.5, 18.11, 18.14, 9.3_
  - _writes: cmd/{internal_idle,shell}.go, internal/provider/progress.go, internal/provider/lima/{progress,clone,lima}.go, internal/provider/wsl2/*, internal/deps/wsl.go_

- [x] 38c. E2E tests against real WSL 2
  - Sixteen tests behind the `e2e` tag, run by `make e2e` on a Windows host, including a cold start that downloads a root filesystem and provisions it. Three cover a machine with no WSL or one too old, reached through a stub `wsl.exe` earlier on PATH, and assert both what the user is told and that nothing was registered.
  - Found four defects unit tests could not: `/proc/mounts` reports DrvFS as `9p` with `aname=drvfs` and not as type `drvfs` — which made `AppliedMounts` see nothing *and* would have passed a guest with all of `C:` mounted; `/mnt/wsl` and `/mnt/wslg` are WSL's own tmpfs and were being refused as Windows drives; `wsl --install` needs retrying against an intermittently reset distribution-list fetch; and a refused update named neither the version found nor `wsl --update`.
  - Two lessons recorded in `docs/lessons.md`, both about test doubles and over-broad checks.
  - _Requirements: 18.3, 18.5, 18.6, 9.3, 1.2, 1.4, 2.5, 5.1, 6.4, 10.3, 17.1_
  - _writes: e2e/{harness,wsl_shell,wsl_prereq,cleanup_darwin}_test.go, e2e/*_test.go (build tags), Makefile, README.md, docs/lessons.md, internal/provider/wsl2/*, internal/deps/wsl.go_

- [x] 39. Enforce Property 21's provider-purity rule with an actual static check  _(PR #73)_
  - Property 21 says *"static dependency checks SHALL find no WSL-specific imports outside the WSL provider, Windows dependency checker, platform adapter, scheduler adapter, or Windows-only terminal files"*, and design §7 lists it among the tests. **No such check exists** — grepping for one returns nothing, and the property is enforced today only by a reviewer noticing.
  - That is the shape `docs/lessons.md` already records for `Reconcile`: a thing the spec says happens, that nothing makes happen. It is not hypothetical here — Phase 4 broke this boundary (`cmd/shell.go` branching on `types.ProviderWSL2`), it survived into a merged PR, and task 38b restored it only because a human read the diff.
  - A test in `cmd/` that walks its own package's imports with `go/parser` or `golang.org/x/tools/go/packages` and fails on any `internal/provider/<backend>` import outside the exempt set is enough, and needs no new dependency if the standard library form is used. The exempt set is Property 21's own list; `cmd/app.go` is the composition root and is exempt by design.
  - _Requirements: 17.3, 18.14_
  - _Properties: 21_
  - _writes: cmd/purity_test.go_

- [x] 40. Run the WSL end-to-end suite in CI  _(PR #64)_
  - The suite is what turned Phase 4 from unit-tested-against-a-fake into exercised-against-the-tool, and it found four defects that twenty-nine unit tests had agreed did not exist. It runs only when somebody remembers, which is how that happens again.
  - **Nightly and on demand, never per push, and that is a measurement rather than caution.** On a developer machine the whole suite is 68–88 seconds, of which the cold-start test is 34 and every other test is under three. **On a hosted runner the same suite took 19.4 minutes, and installing the foreign distribution the PROP-6 check needs took another 13 — about 33 minutes against roughly one locally.** Nested virtualization on a hosted runner is that much slower and nothing is cached there. Per push that is half an hour of Windows runner time, billed at 2×, on every commit. Nightly catches the same regression within a day; `workflow_dispatch` lets anyone touching the WSL backend ask for it immediately.
  - **This corrects an earlier estimate in this task that said a runner would be slower "but not by an order of magnitude".** It is by an order of magnitude, and the first version of this job ran per push on the strength of that guess. The number came from actually running it.
  - If it ever needs to run per push, the 13-minute half is the one to attack first: PROP-6 needs a distribution avar does not own, not specifically Ubuntu, so importing a minimal busybox rootfs would do the same job in seconds.
  - A separate `windows-latest` job, `continue-on-error` at first: WSL 2 on a hosted runner rests on nested virtualization that GitHub provides but does not support. It works — verified, the job passed — and carries no promise of continuing to. Drop `continue-on-error` once it has proven itself, and it becomes a real gate.
  - **The job must install a distribution avar does not own.** PROP-6 — that avar never stops somebody else's environment — cannot be checked without one, and a runner has none. `AVR_E2E_REQUIRE_FOREIGN` turns that test's skip into a failure so the arrangement breaking is visible rather than silent.
  - _Requirements: 18.1, 18.14, 17.5_
  - _Properties: 6_
  - _writes: .github/workflows/ci.yml, e2e/wsl_shell_test.go_

- [ ] 42. Publish the Windows build to winget
  - `winget install olamide226.avar` installs the release's own zip archives as a portable package. GoReleaser's `winget` publisher generates the manifests on every release, pushes them to `olamide226/winget-pkgs` (a fork), and opens a pull request against `microsoft/winget-pkgs`.
  - **Publishing does not end when the workflow does.** A version reaches `winget install` only after Microsoft's validation merges that pull request, and the first submission also gets a human review. The README says so rather than implying the Homebrew tap's immediacy.
  - **No `Microsoft.WSL` dependency, though winget carries that package.** Installing it needs elevation and would prompt for it mid-install with nothing explaining why, and it does not do all that `wsl --install` does. REQ-18.3 requires avar to describe elevation or restart before acting, and a package dependency would act first.
  - **Minor and major releases only** (`skip_upload` also requires `.Patch == 0`). Releasing on every `feat:`/`fix:` merge opened five pull requests in 45 minutes on 16 September while the first was still in review. Four were closed as superseded, leaving #435996 (0.9.1).
  - **Releases must not fail for want of the token.** `skip_upload` is templated on `WINGET_TOKEN` being set: without it the manifests are written to `dist/winget/` and nothing is submitted. With it, `auto` still keeps prereleases out.
  - Outside the repository, and required before a submission happens: the `olamide226/winget-pkgs` fork, and a `WINGET_TOKEN` secret (classic PAT, `public_repo`). Done when a stable release's pull request has merged upstream and `winget install olamide226.avar` installs a working `avr`.
  - _Requirements: 18.15, 18.3, 18.14_
  - _writes: .goreleaser.yaml, .github/workflows/release.yml, README.md, docs/releasing.md, .kiro/specs/avar-cli/requirements.md, .kiro/specs/avar-cli/design.md_

- [ ] 43. Verify on real hosts what CI and this Mac could not
  - These are the unverified items the tasks above left behind, gathered in one place. Their code is merged and unit- or fake-tested. Each item needs a specific host or tool that CI and the development Mac lack, so it goes here rather than keeping a task open.
  - **Cursor and Zed launching (task 24, REQ-13.5/13.6).** On macOS, check that Cursor ≥ 1.6.26 opens over Remote-SSH at the project path, and that Zed connects over SSH, including a project path containing a space. Also find out whether `zed` returns straight away or waits for the connection. On Windows, check that Cursor opens the Editor Window (not Agent Window) on `wsl+<distro>`, and that `zed --wsl <distro>` connects, including whether Zed's remote-server upload works in avar's distributions with automount disabled. An old Zed without `--wsl` should show its own error to the user. `--wsl` is not in Zed's public CLI reference, so a Zed release could break it.
  - **`avr open` on Windows (task 23, REQ-16.2).** `ShellExecuteW` has never run on a real Windows host.
  - **WSL loopback probing (task 23, REQ-18.9).** Listeners bound only to guest loopback are now probed on the strength of Microsoft's `localhostForwarding` documentation, and that has not been measured on real WSL. Add a WSL e2e test for `avr ports` while doing it.
  - **Emulated Lima machines and the host agent (task 41).** Find out whether killing `limactl hostagent` also ends `qemu-system-*` on an `--arch amd64` machine. It needs a host with QEMU installed. Task 41's own text says how to settle it, and a second detector predicate is only warranted if the process survives.
  - **`.avr.toml` packages on Fedora and on WSL (task 22.2, REQ-15.1, REQ-15.3).** The approval prompt, `apt-get` install, isolated sizing and the no-terminal refusal were run on real Lima 2.2.0 (PR #85). `dnf install` on Fedora 43 has only been unit-tested for its shape. On WSL, packages and forward_env have never been run, and WSL2Provider has no `MachineSizer`, so check that cpus/memory produce the "cannot apply" notice there instead of being ignored silently.
  - Tick each item as it is checked, with the host and versions used. A failure found here becomes its own fix task.
  - _Requirements: 13.5, 13.6, 15.1, 15.3, 16.2, 18.9, 5.2_
  - _writes: e2e/** (tests that capture what was verified), this file_

- [ ] 44. Read `config.toml` with the strict reader
  - Maintainer decision (2026-09-17): replace the lenient `state.parseConfigList` and `session.parseTOMLKey` with the strict TOML-subset reader `.avr.toml` uses. Each silently misread a file: a misspelt key was ignored, `idle_timeout="0"` without spaces left auto-stop on, and a comma inside a quoted `forward_env` name split it into two grants. A failing flow test for each was written against the old code first.
  - Extract `.avr.toml`'s lexical layer into `internal/tomlsubset` (no avar imports; closed `Schema`, near-miss key suggestions, unquoted-string hint, `ReadFile` with the 64 KiB cap) without changing what `.avr.toml` accepts. `config.toml`'s schema (`idle_timeout`, `forward_env`) lives in `internal/state` as `Store.Config`/`ParseConfig`; `distro`, `arch`, `cpus`, `memory` and `packages` are refused as not supported there. `types.CheckVariableName` is shared by both files. The `tomllib` agreement test covers both files through `internal/tomlsubset/tomltest`.
  - A file that cannot be read exactly refuses every command in `cmd` dispatch before resolving or machine work, except `status`, `stop` and `destroy`, which say so and run; `help` and `version` never read it. The scheduled idle check stops nothing and exits non-zero. Callers: `dispatch` (`checkUserConfig`), `runGuest` (`forward_env`), `runIdleCheck` (`idle_timeout`).
  - Design §3.11's precedence table listed a global `config.toml` distro/arch layer that the code never filled; the table is corrected to the code and the choice of adding one is left to the maintainer.
  - _Requirements: 17.7, 5.5, 12.4_
  - _writes: internal/tomlsubset/**, internal/projconfig/config.go, internal/projconfig/config_test.go, internal/projconfig/docsite_test.go, internal/state/config.go, internal/state/config_test.go, internal/state/store.go, internal/session/idle.go, internal/session/session_test.go, internal/types/variable.go, cmd/app.go, cmd/dispatch.go, cmd/dispatch_test.go, cmd/userconfig.go, cmd/userconfig_test.go, cmd/shell.go, cmd/internal_idle.go, README.md, site/syntax/config-toml.md, site/design.md, site/troubleshooting.md, docs/lessons.md, .kiro/specs/avar-cli/requirements.md, .kiro/specs/avar-cli/design.md, .kiro/specs/avar-cli/tasks.md_

## Notes

- Each task includes a `_writes:` manifest for file conflict detection.
- E2E tests (tasks 8, 9, 11, 12, 15–17) require a macOS machine with virtualization; the Windows half (task 38c) requires WSL 2. Both run via `make e2e`, which selects the half that applies by build tag. Neither runs per push; the WSL half runs in CI nightly and on demand (task 38c).
- Backlog explicitly deferred beyond Phase 4 (out of scope per Req 17.6, or not yet planned): cloud/remote environments, collaboration, team policies, Kubernetes, marketplace, desktop GUI, Linux hosts.
- Distro image versions and the minimum Lima version are pinned in one file (`internal/resolve/matrix.go` / `internal/deps/lima.go`) so upgrades are single-point changes.
- Development targets **Lima 2.x**. The pinned minimum in `internal/deps` is `2.0.0` (PR #12), matching the version avar's generated configurations are actually validated against.
