//go:build !windows

package wsl2

// holdInterrupts does nothing off Windows.
//
// This backend drives wsl.exe, which exists only on Windows, so a session can
// only ever be attached there. The stub is what lets the package compile on a
// developer's Mac, which matters because cmd/app.go links both backends into one
// binary — not because the backend does anything there.
//
// It compiles there; it is not exercised there. Its Windows paths are Windows
// paths, and path/filepath answers differently about them off Windows, so the
// package's behaviour tests carry a `windows` build constraint for the same
// reason the Lima backend's carry a `unix` one. See the header of shell_test.go.
func holdInterrupts() (stop func()) { return func() {} }
