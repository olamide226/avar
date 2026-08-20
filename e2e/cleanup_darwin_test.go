//go:build e2e && darwin

package e2e

// cleanupBackend does nothing on macOS.
//
// The Lima half of the suite creates one shared instance and reuses it across
// runs deliberately — provisioning is the slow part, and a suite that deleted
// its machine would pay for it again every time. `avr destroy --all` is the way
// to reclaim it, and z_destroy_test.go exercises exactly that.
func cleanupBackend() {}
