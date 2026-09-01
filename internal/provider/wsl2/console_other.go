//go:build !windows

package wsl2

// holdInterrupts does nothing off Windows.
//
// This backend drives wsl.exe, which exists only on Windows, so a session can
// only ever be attached there. The stub exists so that the package compiles and
// its pure logic — path mapping, mount planning, listing, the environment
// boundary — is testable on a developer's Mac, which is where most of avar is
// written. See console_windows.go for what the real one does and why.
func holdInterrupts() (stop func()) { return func() {} }
