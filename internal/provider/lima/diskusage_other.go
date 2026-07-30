//go:build !unix

package lima

// diskUsedGB has no portable answer off Unix: the allocated-block count a sparse
// disk image is measured by is not exposed by os.FileInfo. avar targets macOS
// (REQ-17.6), so this exists to keep the package building elsewhere rather than
// to be correct there.
func diskUsedGB(string) float64 { return 0 }
