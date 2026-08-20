// Package deps makes avar's backend dependency invisible.
//
// avar drives its backend as a subprocess, so the backend's command-line tool
// is a hard requirement: `limactl` on macOS, `wsl.exe` on Windows. This package
// locates the one this host needs, gates it on a pinned minimum version, and —
// only with the user's explicit consent — installs or updates it. A user who
// has never heard of Lima or of WSL should never have to read a README to get
// past a missing dependency (REQ-8, REQ-18.2, REQ-18.3).
//
// One host, one dependency. A Windows invocation checks WSL and never mentions
// Lima, Docker Desktop, or any other virtualization runtime, and a macOS
// invocation is the mirror image (PROP-13). The two managers therefore share no
// state and no decisions: they share only the vocabulary below — Runner,
// Version, and the consent prompt — because "ask before changing the user's
// machine" is the one rule both obey.
//
// Nothing here builds a command line for a shell to interpret. Every subprocess
// is executed as an argv, so no path, version string, or tool output can turn
// into shell syntax.
//
// Nothing here creates an environment, either. A dependency manager's job ends
// when it can say "the backend is usable" or "here is what to do about it";
// registering a machine or a distribution is the provider's, which is what
// makes an interrupted or restart-blocked setup leave nothing half-made behind
// (REQ-18.3, PROP-13).
package deps
