# Requirements Document — avar (`avr`)

## Introduction

avar is a zero-configuration, directory-centric shell environment switcher. The MVP targets macOS; post-MVP Windows support uses WSL 2 as a native provider rather than introducing another virtualization runtime. It lets a developer stand in any project directory and drop into a complete Linux environment — same project context, real `sudo`, persistent packages, automatic port forwarding — as easily as opening another shell tab:

```bash
cd ~/code/my-project
avr                  # Linux shell, same directory
avr npm test         # run one command in Linux
avr --arch amd64     # same, but x86_64
avr --distro fedora  # same, but Fedora
```

On macOS, avar is a thin product/UX layer over [Lima](https://lima-vm.io) (Apache 2.0, CNCF incubating). On Windows, the post-MVP WSL2Provider supplies the same product contract over WSL 2. avar deliberately hides each backend's machine- or distribution-centric model: the user never needs to name a VM or WSL distribution, write a mount stanza, edit backend configuration, or configure SSH. The **current directory plus the selected operating environment** is the entire mental model.

avar is explicitly **not** a Docker wrapper, a Dev Container implementation, or a VM manager. It competes on the mental model, not on virtualization.

**Scope phases** (traceability for tasks): Requirements 1–9 are **MVP Phase 1**, Requirements 10–13 are **MVP Phase 2**, Requirements 14–16 and 18 are **Post-MVP**. Requirement 17 (non-functional) applies to all phases.

## Glossary

- **avar**: The product name.
- **avr**: The CLI binary name (`avr --help`).
- **Host**: The user's supported macOS or Windows system. MVP host requirements remain macOS-only.
- **Windows_Host**: A supported Windows system running WSL 2.
- **Guest**: A Linux environment managed by avar.
- **Machine**: A Lima VM instance managed by avar. avar names, creates, and selects machines internally; machine identity is never required in the default UX.
- **WSL_Distribution**: A Linux distribution registered with WSL 2. The WSL2Provider manages only avar-owned distributions and hides their registered names in the default UX.
- **Shared_Machine**: The default long-lived machine a given (distro, arch) pair maps to. All projects share it unless isolation is requested.
- **Isolated_Environment**: A per-project machine derived from a clean base, selected via `--isolate` and remembered per project.
- **Project**: The host directory `avr` is invoked from (the nearest enclosing directory the user is standing in; avar does not require a repository root marker).
- **Project_Identity**: A stable identifier for a project, derived from the SHA-256 hash of the project's resolved absolute path (symlinks resolved).
- **Live_Mount**: The default file-sharing mode — the host project directory is mounted writable inside the guest at the identical absolute path on macOS, or exposed at its canonical WSL path on Windows.
- **Environment_Selector**: The (distro, arch, isolation) triple that determines which machine a command targets.
- **Provider**: The backend that implements environment lifecycle and execution operations. The only MVP provider is Lima; WSL2Provider is a post-MVP Windows provider.
- **State_Dir**: avar's private metadata directory on the host (`~/.avr/` on macOS; a platform-appropriate per-user application-data directory on Windows) containing project records, environment registry, generated connection configuration, and logs.
- **Idle_Timeout**: The period with no active avar sessions after which a machine is automatically stopped.

---

## Requirements

### Requirement 1: Zero-Config Linux Shell Entry — *Phase 1*

**User Story:** As a developer, I want to type `avr` in any project directory and land in a Linux shell at the same directory, so that switching operating environments feels like opening another shell tab rather than operating a VM.

#### Acceptance Criteria

1.1 WHEN a user runs `avr` with no arguments and the target machine is running THEN THE CLI SHALL open an interactive login shell inside the guest with the working directory set to the host's current directory path.

1.2 WHEN a user runs `avr` and no machine exists for the selected environment THEN THE CLI SHALL provision one automatically (default: Ubuntu 24.04 LTS, host-native architecture, resource defaults per Requirement 17.4), display human-readable progress (e.g., "Creating your Linux development environment · Ubuntu 24.04 · ARM64 · 4 CPU · 8 GB RAM"), and then enter the shell — all within the single invocation.

1.3 WHEN a user runs `avr` and the target machine exists but is stopped THEN THE CLI SHALL start it automatically and then enter the shell.

1.4 THE guest session SHALL run as a non-root user matching the host username, with passwordless `sudo`.

1.5 THE default UX SHALL NOT require the user to supply, know, or see machine names, mount configuration, SSH configuration, or image references.

1.6 IF provisioning or startup fails THEN THE CLI SHALL print a single actionable error (what failed, the underlying cause, and the suggested next step), exit non-zero, and leave no half-registered machine in avar's state (either the machine is fully registered or the failure is cleaned up).

1.7 WHEN the interactive shell exits THEN THE CLI SHALL exit with the shell's exit code.

### Requirement 2: One-Shot Command Execution — *Phase 1*

**User Story:** As a developer, I want `avr <command>` to run a single command inside Linux from my current directory, so that I can use Linux toolchains inside normal macOS workflows (scripts, Makefiles, CI-like runs).

#### Acceptance Criteria

2.1 WHEN a user runs `avr <command> [args...]` THEN THE CLI SHALL execute the command inside the guest with the working directory set to the host's current directory path, auto-starting/provisioning the machine as in Requirements 1.2–1.3.

2.2 THE CLI SHALL propagate the guest command's exit code as its own exit code.

2.3 THE CLI SHALL stream stdin, stdout, and stderr between host and guest without buffering-induced reordering, and SHALL allocate a guest PTY if and only if the host stdin is a TTY (so both `avr npm test` interactively and `avr ls | grep foo` in pipelines behave correctly).

2.4 WHEN the user sends SIGINT or SIGTERM to the avr process THEN THE CLI SHALL forward the signal to the guest command.

2.5 THE CLI SHALL treat the first argument that is not a recognized avr flag or subcommand as the start of the guest command, and SHALL support `--` as an explicit separator (`avr -- ls --help` runs `ls --help` in the guest).

2.6 IF a guest command name collides with an avr subcommand (e.g., a project script named `status`) THEN THE CLI SHALL resolve to the avr subcommand and THE user SHALL be able to force guest execution with `avr -- status`.

### Requirement 3: Terminal Fidelity — *Phase 1*

**User Story:** As a developer, I want the Linux shell to behave exactly like a native terminal (colors, resize, signals, editors), so that I forget virtualization exists.

#### Acceptance Criteria

3.1 WHILE an interactive session is active THE CLI SHALL propagate terminal window resize events (SIGWINCH) to the guest PTY.

3.2 THE CLI SHALL pass through the host `TERM` value (and a color-capable default when unset) so that full-screen programs (vim, htop, less) and colored output render correctly.

3.3 WHILE an interactive session is active THE CLI SHALL deliver Ctrl-C, Ctrl-Z, and Ctrl-D to the guest session rather than acting on the avr process itself.

3.4 THE round-trip latency added by avar on top of the underlying transport SHALL be imperceptible in interactive use (no added per-keystroke buffering).

### Requirement 4: Environment Selection (Architecture and Distribution) — *Phase 1*

**User Story:** As a developer, I want to select CPU architecture and Linux distribution per invocation, so that I can test ARM64 vs AMD64 behavior and different distros without managing machines.

#### Acceptance Criteria

4.1 WHEN a user passes `--arch arm64` or `--arch amd64` THEN THE CLI SHALL target a machine of that architecture, provisioning it on first use per Requirement 1.2.

4.2 WHEN a user passes `--distro <name>[:<version>]` (initially supported: `ubuntu`, `debian`, `fedora`, each with a pinned default version) THEN THE CLI SHALL target a machine of that distribution, provisioning it on first use.

4.3 THE CLI SHALL map each distinct (distro, arch) pair to its own persistent Shared_Machine, and packages installed in one environment SHALL persist across sessions of that environment without affecting others.

4.4 IF the user passes an unsupported `--arch` or `--distro` value THEN THE CLI SHALL exit non-zero listing the supported values.

4.5 WHEN `--arch` and/or `--distro` are combined with a one-shot command (e.g., `avr --arch amd64 npm test`) THEN THE CLI SHALL apply the Environment_Selector to that command execution.

4.6 WHERE the host is Apple Silicon THE CLI SHALL provision `--arch amd64` machines via emulation (QEMU) and SHALL warn the user once, at provision time, about the performance difference relative to native ARM64.

4.7 IF `--arch` / `--distro` selection would require provisioning a new machine THEN THE CLI SHALL state which environment is being created before doing so (no silent multi-VM sprawl).

### Requirement 5: Machine Lifecycle Management — *Phase 1 (5.1–5.4), Phase 2 (5.5–5.8)*

**User Story:** As a developer, I want avar to manage machine state invisibly but let me inspect and stop it, so that I keep control without needing to think about VMs day to day.

#### Acceptance Criteria

5.1 WHEN a user runs `avr status` THEN THE CLI SHALL display each avar-managed machine with: environment label (distro, version, arch), state (running/stopped), resource allocation (CPU, memory), disk usage, and which mode (shared/isolated) it serves.

5.2 WHEN a user runs `avr stop` THEN THE CLI SHALL stop the machine for the current Environment_Selector; `avr stop --all` SHALL stop all avar-managed machines.

5.3 WHEN `avr status` is run and no machines exist THEN THE CLI SHALL say so and describe how to get started (run `avr`).

5.4 THE CLI SHALL only ever manage machines it created (identified by an avar naming prefix/label), and SHALL never list, modify, or stop the user's other Lima machines.

5.5 WHILE a machine has had no active avar sessions for the Idle_Timeout (default 2 hours, configurable, disableable) THE system SHALL stop that machine automatically to release memory. *(Phase 2)*

*Criteria 5.6–5.8 added after implementation.* Every other lifecycle verb was
specified — create, start, stop, idle-stop, reset — and removal was not, so nothing
implemented it. The result was that an environment could be created but never
deliberately removed: `avr reset` destroys and immediately recreates, and
`avr isolate off` removes only the current project's machine and only when that
project's directory is the working directory. A user wanting to reclaim the disk an
environment holds had no avar command for it and had to reach for the backend's own
tooling, which is precisely what Requirement 1.5 and the product rule exist to prevent.

5.6 WHEN a user runs `avr destroy` THEN THE CLI SHALL remove the environment for the current Environment_Selector — the machine and everything inside it — after stating what will be destroyed and obtaining interactive confirmation, bypassable with `--yes`. Host project files SHALL never be affected. *(Phase 2)*

5.7 WHEN a user runs `avr destroy --all` THEN THE CLI SHALL remove every avar-managed environment under the same confirmation rules, and SHALL report how many were removed. *(Phase 2)*

5.8 WHEN a user runs `avr destroy --orphaned` THEN THE CLI SHALL remove only those isolated environments whose project directory no longer exists on the host, naming the project each belonged to. THIS is the only path by which such an environment can be removed, because `avr isolate off` requires the project directory it is being run from to exist. *(Phase 2)*

### Requirement 6: Project File Sharing (Live Mount) — *Phase 1*

**User Story:** As a developer, I want my current project visible inside Linux at the same path with changes appearing instantly in both directions, so that macOS editors and Linux toolchains operate on the same files with zero synchronization steps.

#### Acceptance Criteria

6.1 WHEN a session or command starts THEN THE guest SHALL see the project directory writable at the identical absolute path as on the host, using VirtioFS where the virtualization stack supports it.

6.2 WHEN a file is created, modified, or deleted on either side THEN THE change SHALL be visible on the other side without any explicit sync action.

6.3 THE CLI SHALL mount only registered project directories by default — never the entire home directory (see Requirement 9.3).

6.4 WHEN `avr` is invoked from a project directory that is not yet covered by the target machine's mounts THEN THE CLI SHALL register the project and make the mount available automatically; IF this requires a machine restart THEN THE CLI SHALL perform it within the invocation and tell the user why the extra one-time delay occurred.

6.5 IF the current directory cannot be mounted (e.g., network volume, permission failure) THEN THE CLI SHALL exit non-zero with a clear explanation rather than dropping the user into a shell at a wrong or empty path.

6.6 WHEN the user invokes `avr` from a subdirectory of a registered project THEN THE CLI SHALL reuse the existing mount and set the guest working directory to the matching subdirectory path.

### Requirement 7: Automatic Port Forwarding — *Phase 1*

**User Story:** As a developer, I want servers started inside Linux to be reachable at `localhost` on macOS automatically, so that browsers, API clients, and other local tools work with no tunnel setup.

#### Acceptance Criteria

7.1 WHEN a guest process listens on a TCP port on localhost THEN THE port SHALL become reachable at the same port on host `localhost` automatically.

7.2 IF a guest port cannot be forwarded because the host port is already in use THEN THE system SHALL NOT break the session; the conflict SHALL be discoverable via avar's status/diagnostic output.

7.3 WHEN a forwarded guest port is closed THEN THE corresponding host listener SHALL be released.

### Requirement 8: Dependency Management (Lima) — *Phase 1*

**User Story:** As a developer, I want `avr` to handle its Lima dependency for me, so that installation is one step.

#### Acceptance Criteria

8.1 WHEN `avr` runs and a compatible Lima installation is present THEN THE CLI SHALL use it without further prompts.

8.2 WHEN `avr` runs and Lima is missing THEN THE CLI SHALL offer to install it via `brew install lima`, and SHALL proceed with the installation only after user confirmation.

8.3 IF Lima is missing and Homebrew is also unavailable, or the user declines installation THEN THE CLI SHALL print manual installation instructions and exit non-zero.

8.4 IF the installed Lima version is below avar's minimum supported version THEN THE CLI SHALL report the found and required versions and how to upgrade, and SHALL NOT attempt to operate against the unsupported version.

8.5 WHERE avar is distributed via Homebrew THE formula SHALL declare Lima as a dependency so brew users never hit 8.2.

### Requirement 9: Security Defaults — *Phase 1*

**User Story:** As a developer, I want the guest to receive nothing from my Mac that I did not explicitly grant, so that credentials and unrelated files never silently cross the boundary.

#### Acceptance Criteria

9.1 THE CLI SHALL NOT forward host environment variables into guest sessions by default, except a minimal terminal allowlist (`TERM`, locale variables).

9.2 THE CLI SHALL NOT forward the host SSH agent into the guest by default.

9.3 THE CLI SHALL mount only directories registered as projects — never the full home directory, and never anything outside directories the user has run `avr` from.

9.4 THE guest SHALL have no avar-granted access to the host keychain, host credentials files (e.g., `~/.aws`, `~/.ssh`), or host clipboard.

### Requirement 10: Snapshots and Reset — *Phase 2*

**User Story:** As a developer, I want to snapshot, restore, and reset my Linux environment, so that I can experiment fearlessly and recover a clean state in seconds.

#### Acceptance Criteria

*Amended after implementation.* Snapshots depend on the backend: Lima implements
them for QEMU machines only, and every snapshot subcommand exits `unimplemented`
on a `vz` machine. avar runs host-native environments under `vz` deliberately —
that is what supplies VirtioFS speed and Rosetta (see the VM-stack row in
design.md §1) — so on an Apple Silicon Mac the default environment is precisely
the one that cannot be snapshotted, while an emulated `--arch amd64` environment
can. Criteria 10.1, 10.2 and 10.4 therefore apply *where the backend supports
snapshots*; where it does not, the CLI SHALL say so, name `avr reset` as the way
to return to a clean state, and change nothing. Criterion 10.3 (`avr reset`) is
unaffected and works on every environment.

10.1 WHEN a user runs `avr snapshot <name>` THEN THE CLI SHALL capture a named snapshot of the current Environment_Selector's machine while preserving the running workload state or cleanly stopping/restarting as required by the backend, and report what was captured.

10.2 WHEN a user runs `avr restore <name>` THEN THE CLI SHALL restore that snapshot and confirm completion; IF the name does not exist THEN THE CLI SHALL list available snapshots.

10.3 WHEN a user runs `avr reset` THEN THE CLI SHALL return the current environment to a clean base state (fresh OS, no user-installed packages), require interactive confirmation (bypassable with `--yes`), and state clearly beforehand what will be destroyed. Project files on the host SHALL never be affected by reset.

10.4 WHEN a user runs `avr snapshot` with no arguments THEN THE CLI SHALL list existing snapshots for the current environment with creation timestamps.

### Requirement 11: Project Isolation — *Phase 2*

**User Story:** As a developer, I want an isolated per-project Linux environment on demand, so that experimental or conflicting dependencies cannot pollute my shared environment.

#### Acceptance Criteria

11.1 WHEN a user runs `avr --isolate` in a project THEN THE CLI SHALL create (on first use) or enter a per-project Isolated_Environment derived from a clean base image of the selected (distro, arch).

11.2 WHEN a project has an Isolated_Environment THEN subsequent plain `avr` invocations in that project SHALL target the isolated environment automatically — the choice is remembered per Project_Identity in the State_Dir, with no file added to the repository.

11.3 WHEN a user runs `avr --shared` in a project that defaults to isolated THEN THE CLI SHALL target the shared environment for that invocation without changing the remembered default; `avr isolate off` SHALL remove the remembered default (and offer to delete the isolated machine).

11.4 WHEN `avr reset` runs in a project with an Isolated_Environment THEN THE reset SHALL apply to that project's environment only.

11.5 THE isolated environment SHALL mount only its own project directory.

### Requirement 12: Explicit Environment and Credential Forwarding — *Phase 2*

**User Story:** As a developer, I want to opt specific variables, env files, or my SSH agent into the guest per invocation, so that I can authenticate when needed without weakening the default boundary.

#### Acceptance Criteria

12.1 WHEN a user passes `--env NAME` THEN THE CLI SHALL forward that single host variable's value into the guest session/command; `--env NAME=value` SHALL set an explicit value; the flag SHALL be repeatable.

12.2 WHEN a user passes `--env-file <path>` THEN THE CLI SHALL load variables from that file into the guest session/command; IF the file does not exist or cannot be parsed THEN THE CLI SHALL exit non-zero before starting the session.

12.3 WHEN a user passes `--ssh-agent` THEN THE CLI SHALL forward the host SSH agent socket into the guest for the duration of that session only.

12.4 Forwarding SHALL be per-invocation: no `--env`/`--ssh-agent` grant SHALL persist beyond the session it was given to, unless the user configures a persistent allowlist in avar's own (host-side) configuration.

### Requirement 13: Editor Integration (`avr code`) — *Phase 2*

**User Story:** As a developer, I want `avr code` to open VS Code attached to the Linux environment at my project path, so that editing and running both happen in Linux without manual SSH setup.

#### Acceptance Criteria

13.1 WHEN a user runs `avr code` THEN THE CLI SHALL ensure the target machine is running, provision/refresh an SSH host entry for it in avar-managed SSH configuration, and launch VS Code Remote-SSH opening the project at the matching guest path.

13.2 IF the `code` CLI is not on PATH THEN THE CLI SHALL exit non-zero with instructions for enabling it (VS Code "Shell Command: Install 'code' command in PATH").

13.3 THE SSH configuration avar writes SHALL live in an avar-owned file included from the State_Dir and SHALL NOT modify the user's existing `~/.ssh/config` entries.

13.4 `avr code` SHALL respect the same Environment_Selector flags as other commands (`avr --isolate code`, `avr --distro fedora code`).

### Requirement 14: Linux-Native Workspace Mode — *Post-MVP*

**User Story:** As a developer with file-I/O-heavy projects, I want an opt-in mode where the project lives on the Linux filesystem with reviewable sync back to the host, so that dependency-heavy operations run at native speed.

#### Acceptance Criteria

14.1 WHEN a user passes `--native-fs` THEN THE CLI SHALL place/sync the project into the guest's native filesystem (e.g., `~/workspaces/<project>`) and run the session there.

14.2 THE system SHALL provide a way to sync guest-side changes back to the host with the opportunity to review changes before they are applied.

14.3 IF host and guest copies have conflicting changes THEN THE system SHALL surface the conflict and never silently overwrite either side.

### Requirement 15: Optional Project Configuration and Detection — *Post-MVP*

**User Story:** As a team member, I want an optional tiny config file and an `avr init` that proposes an environment from my project's manifests, so that teams get reproducibility without avar ever requiring configuration.

#### Acceptance Criteria

15.1 WHERE a `.avr.toml` exists in the project root THE CLI SHALL apply its settings (distro, arch, cpus, memory, packages, forward_env allowlist) when provisioning/selecting the environment for that project.

15.2 WHEN a user runs `avr init` THEN THE CLI SHALL inspect well-known manifests (`package.json`, `pyproject.toml`, `go.mod`, `Cargo.toml`, `Dockerfile`, `docker-compose.yml`, `.tool-versions`, `mise.toml`), present a human-readable proposal of the detected stack, and only write `.avr.toml` after user confirmation.

15.3 THE CLI SHALL never install packages or change the environment based on detection without an explicit user-confirmed proposal.

15.4 THE absence of `.avr.toml` SHALL never degrade any avar behavior — zero-config remains the primary path.

### Requirement 16: Port Inspection Commands — *Post-MVP*

**User Story:** As a developer, I want to list forwarded ports and open one in my browser, so that I can find my running servers without remembering ports.

#### Acceptance Criteria

16.1 WHEN a user runs `avr ports` THEN THE CLI SHALL list currently forwarded guest ports with the guest process/command where determinable.

16.2 WHEN a user runs `avr open <port>` THEN THE CLI SHALL open `http://localhost:<port>` in the default host browser; IF the port is not currently forwarded THEN THE CLI SHALL say so.

### Requirement 17: Non-Functional Requirements — *All phases*

17.1 **Warm-path latency**: WHEN the target machine is already running THEN entering a shell or starting a one-shot command SHALL add no more than ~500 ms of avar+transport overhead on typical hardware.

17.2 **Distribution**: THE product SHALL ship as a single self-contained binary (Go), installable via Homebrew, with no runtime dependencies beyond Lima and macOS system frameworks.

17.3 **Provider abstraction**: THE codebase SHALL isolate all backend-specific operations behind a Provider interface such that adding an OrbStack, SSH, or cloud provider post-MVP requires no changes to command-layer code.

17.4 **Resource defaults**: New machines SHALL default to conservative, host-proportional resources (e.g., min(4, host/2) CPUs, min(8 GB, host/4) memory, sparse disk) so that provisioning never destabilizes the host.

17.5 **Crash consistency**: IF avar is killed mid-operation THEN a subsequent invocation SHALL detect and recover or clean up partial state (no wedged "unknown" machines requiring manual Lima surgery).

17.6 **Host platform**: MVP SHALL support macOS 13+ on Apple Silicon and Intel. Linux hosts, Windows, GUIs, cloud/remote environments, team policies, and Kubernetes are explicitly out of scope for MVP.

### Requirement 18: Windows Host Support via WSL 2 — *Post-MVP*

**User Story:** As a Windows developer, I want the same `avr` project-centred Linux shell experience backed by WSL 2, so that I can use avar consistently without installing or managing a second VM runtime.

#### Acceptance Criteria

18.1 WHERE `avr` runs on a supported Windows host THE CLI SHALL automatically select the WSL2Provider and preserve the same top-level shell, one-shot command, environment-selector, lifecycle, isolation, forwarding, and editor command grammar used on macOS.

18.2 WHEN WSL 2 and a compatible WSL command-line interface are available THEN THE CLI SHALL use them directly without requiring Lima, Docker Desktop, Hyper-V VM configuration, or another third-party virtualization runtime.

18.3 IF WSL is missing, disabled, outdated, or requires an administrative install, kernel update, or host restart THEN THE CLI SHALL identify the unmet prerequisite, offer an explicit installation or upgrade action where it can do so safely, describe any elevation or restart requirement before acting, and exit without registering a partial avar environment.

18.4 IF the selected distribution is running under WSL 1 THEN THE CLI SHALL refuse to use it as an avar environment, explain that WSL 2 is required, and provide the exact upgrade command or next step.

18.5 WHEN a user invokes `avr` or `avr <command>` from a Windows filesystem path THEN THE WSL2Provider SHALL resolve that path to its canonical WSL-visible path, start the guest process in the corresponding project directory, and preserve live bidirectional file visibility.

18.6 WHEN a user selects a supported distribution THEN THE WSL2Provider SHALL create or reuse an avar-managed WSL 2 distribution for that selector without requiring the user to know its registered name. IF a requested architecture is not supported by the Windows host and WSL 2 runtime THEN THE CLI SHALL fail before provisioning with a provider-capability error listing the supported architecture values.

18.7 THE WSL2Provider SHALL distinguish avar-managed distributions using both an avar-owned registry record and a reserved name prefix, and SHALL never start, stop, export, import, reset, unregister, or otherwise modify a user-managed WSL distribution.

18.8 WHEN the WSL2Provider executes an interactive shell or one-shot command THEN THE CLI SHALL preserve stdin, stdout, stderr, PTY behavior, terminal resizing, signal/interrupt behavior where Windows and WSL expose an equivalent, and guest exit-code propagation consistent with Requirements 1–3.

18.9 WHEN a guest process listens on a TCP port and WSL localhost forwarding is available THEN that port SHALL be reachable from the Windows host at the same `localhost` port. IF forwarding is unavailable or conflicts with an existing listener THEN the session SHALL continue and `avr status` SHALL report an actionable diagnostic.

18.10 WHEN a Windows user runs `avr code` THEN THE CLI SHALL open the project through VS Code's WSL integration for the selected avar-managed distribution and SHALL NOT require or generate SSH configuration for that flow.

18.11 WHEN a project resides on the Windows filesystem and avar detects a workload likely to suffer materially from cross-filesystem I/O THEN THE CLI SHALL display a dismissible, once-per-project recommendation for Linux-native workspace mode; accepting that recommendation SHALL use Requirement 14's reviewable synchronization and conflict-safety rules.

18.12 THE WSL2Provider SHALL implement snapshot, restore, reset, project isolation, and crash recovery without changing or deleting host project files, and SHALL leave either a fully registered usable environment or a recoverable clean state after interruption.

18.13 THE Windows build SHALL store state in a per-user, non-roaming application-data directory, canonicalize Windows project paths case-insensitively for Project_Identity, and prevent the same project path expressed with different drive-letter casing or separators from creating duplicate project records.

18.14 THE Windows implementation SHALL ship as a self-contained `avr.exe` for supported Windows host architectures and SHALL keep all WSL-specific commands, parsing, lifecycle rules, and capability checks behind the Provider and dependency boundaries so that command-layer behavior remains provider-neutral.
