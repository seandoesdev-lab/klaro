//go:build linux

package scanner

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// tmpfsMagic is the statfs f_type for tmpfs (Linux).
const tmpfsMagic = 0x01021994

// AssertTmpfs verifies that path is backed by tmpfs (RAM), failing fast otherwise.
// This is the startup guard for [EPHEM-01]: source work must never land on disk.
// Bypass via SCAN_WORK_TMPFS_ASSERT=0 is handled by the caller (test/non-Linux).
func AssertTmpfs(path string) error {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return fmt.Errorf("statfs %q: %w", path, err)
	}
	if int64(st.Type) != tmpfsMagic {
		return fmt.Errorf("scan work dir %q is not tmpfs (f_type=0x%x, want 0x%x) — refusing to run to honor [EPHEM-01]", path, st.Type, tmpfsMagic)
	}
	return nil
}
