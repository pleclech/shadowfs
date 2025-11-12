package utils

import (
	"context"
	"os"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fuse"
)

// PreserveOwner sets uid and gid of `path` according to the caller information
// in `ctx`.
func PreserveOwner(ctx context.Context, path string) error {
	if os.Getuid() != 0 {
		return nil
	}
	caller, ok := fuse.FromContext(ctx)
	if !ok {
		return nil
	}
	return syscall.Lchown(path, int(caller.Uid), int(caller.Gid))
}

// EnsureDirPermissions ensures a directory has proper execute permissions.
// It checks if the path is a directory and ensures it has at least 0755 permissions.
// Always chmods to ensure kernel sees correct permissions immediately.
func EnsureDirPermissions(path string) error {
	var st syscall.Stat_t
	if err := syscall.Lstat(path, &st); err != nil {
		return err
	}

	// Check if it's actually a directory
	if st.Mode&syscall.S_IFDIR == 0 {
		return syscall.ENOTDIR
	}

	// Get current permissions (only permission bits, not file type)
	currentMode := st.Mode & 0777

	// Ensure at least 0755 (rwxr-xr-x) - directories need execute bits to be traversable
	// Preserve higher permissions if they exist, but always ensure execute bits
	requiredMode := currentMode | 0755

	// Always chmod to ensure kernel sees correct permissions immediately
	// This is important for FUSE filesystems where kernel might cache wrong attributes
	if err := syscall.Chmod(path, requiredMode); err != nil {
		return err
	}

	return nil
}

