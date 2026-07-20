//go:build !linux

package scanner

// AssertTmpfs is a no-op on non-Linux hosts (Windows/macOS dev machines) where
// tmpfs semantics differ and statfs magic isn't available. The production runtime
// is the Linux worker container, which uses the Linux build that enforces the
// [EPHEM-01] tmpfs guard. Local `go test` therefore never blocks on this.
func AssertTmpfs(path string) error { return nil }
