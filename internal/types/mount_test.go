package types

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// hostPath renders a POSIX-shaped test path in the host's own vocabulary.
//
// MountSpec.HostPath is absolute *in the host's syntax* by definition, so a
// table that hard-codes "/Users/dev/code/app" asserts something that is only
// true on macOS: on Windows that string is a relative path and Validate is
// right to reject it. Prefixing the drive keeps each case testing the mount
// rule it was written for rather than the platform's idea of "absolute"
// (REQ-18.13).
//
// It concatenates rather than joining, because filepath.Join would normalize
// the result and the cases below depend on being able to write one that is not
// normalized.
func hostPath(posix string) string {
	if runtime.GOOS != "windows" {
		return posix
	}
	return `C:` + filepath.FromSlash(posix)
}

// A mount is only usable if both halves of it are: an absolute host directory
// and an absolute, normalized Linux path in the guest (design §4).
func TestMountSpec_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mount   MountSpec
		wantErr bool
	}{
		{
			name:  "identity mapping, as Lima produces",
			mount: MountSpec{HostPath: hostPath("/Users/dev/code/app"), GuestPath: "/Users/dev/code/app", Writable: true},
		},
		{
			name:  "distinct guest path, as WSL produces",
			mount: MountSpec{ProjectID: "3fa9c2b1d0", HostPath: hostPath("/Users/dev/code/app"), GuestPath: "/mnt/avr/projects/3fa9c2b1d0", Writable: true},
		},
		{name: "read-only is a legal mapping", mount: MountSpec{HostPath: hostPath("/a"), GuestPath: "/a"}},
		{name: "no host path", mount: MountSpec{GuestPath: "/a"}, wantErr: true},
		{name: "no guest path", mount: MountSpec{HostPath: hostPath("/a")}, wantErr: true},
		{name: "relative host path", mount: MountSpec{HostPath: filepath.FromSlash("code/app"), GuestPath: "/a"}, wantErr: true},
		{name: "unnormalized host path", mount: MountSpec{HostPath: hostPath("/a/../a"), GuestPath: "/a"}, wantErr: true},
		{name: "relative guest path", mount: MountSpec{HostPath: hostPath("/a"), GuestPath: "mnt/avr"}, wantErr: true},
		{name: "unnormalized guest path", mount: MountSpec{HostPath: hostPath("/a"), GuestPath: "/mnt/avr/../avr"}, wantErr: true},
		{name: "trailing separator is not normalized", mount: MountSpec{HostPath: hostPath("/a"), GuestPath: "/mnt/avr/"}, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.mount.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("Validate(%s) = nil, want error", tc.mount)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("Validate(%s) = %v, want nil", tc.mount, err)
			}
		})
	}
}

// A guest path is a Linux path whatever the host is. Validation must not go
// through the host's own path syntax, or a Windows build would reject every
// guest path avar produces and accept a Windows one.
func TestMountSpec_GuestPathIsJudgedAsALinuxPath_REQ_18_5(t *testing.T) {
	t.Parallel()

	if err := (MountSpec{HostPath: hostPath("/a"), GuestPath: "/mnt/avr/projects/abc"}).Validate(); err != nil {
		t.Errorf("an absolute Linux guest path was rejected: %v", err)
	}
	if err := (MountSpec{HostPath: hostPath("/a"), GuestPath: `C:\Users\ola\code\app`}).Validate(); err == nil {
		t.Error("a Windows path was accepted as a guest path")
	}
}

func TestNormalizeMounts_CanonicalisesAndOrdersTheSet(t *testing.T) {
	t.Parallel()

	got, err := NormalizeMounts([]MountSpec{
		{HostPath: hostPath("/b/../b/two/"), GuestPath: "/b/two/", Writable: true},
		{HostPath: hostPath("/a/one"), GuestPath: "/a/one", Writable: true},
		{HostPath: hostPath("/a/one"), GuestPath: "/a/one", Writable: true},
	})
	if err != nil {
		t.Fatalf("NormalizeMounts: %v", err)
	}
	want := []MountSpec{
		{HostPath: hostPath("/a/one"), GuestPath: "/a/one", Writable: true},
		{HostPath: hostPath("/b/two"), GuestPath: "/b/two", Writable: true},
	}
	if !EqualMounts(got, want) {
		t.Errorf("NormalizeMounts = %v, want %v", got, want)
	}
	if got, err := NormalizeMounts(nil); err != nil || got != nil {
		t.Errorf("NormalizeMounts(nil) = (%v, %v), want (nil, nil)", got, err)
	}
}

// Two host directories behind one guest path would hide one of them, so the
// configured set would no longer describe what the guest can reach.
func TestNormalizeMounts_RefusesASetThatContradictsItself_PROP_5(t *testing.T) {
	t.Parallel()

	_, err := NormalizeMounts([]MountSpec{
		{HostPath: hostPath("/a"), GuestPath: "/mnt/avr/projects/x", Writable: true},
		{HostPath: hostPath("/b"), GuestPath: "/mnt/avr/projects/x", Writable: true},
	})
	if err == nil {
		t.Fatal("two host directories were allowed to claim one guest path")
	}
	if !strings.Contains(err.Error(), "/mnt/avr/projects/x") {
		t.Errorf("the error does not name the contested guest path: %v", err)
	}

	if _, err := NormalizeMounts([]MountSpec{
		{HostPath: hostPath("/a"), GuestPath: "/one", Writable: true},
		{HostPath: hostPath("/a"), GuestPath: "/two", Writable: true},
	}); err == nil {
		t.Error("one host directory was allowed two guest paths")
	}
}

func TestMountHostPaths_KeepsOrder(t *testing.T) {
	t.Parallel()

	got := MountHostPaths([]MountSpec{
		{HostPath: hostPath("/a"), GuestPath: "/mnt/a"},
		{HostPath: hostPath("/b"), GuestPath: "/mnt/b"},
	})
	if len(got) != 2 || got[0] != hostPath("/a") || got[1] != hostPath("/b") {
		t.Errorf("MountHostPaths = %v", got)
	}
}
