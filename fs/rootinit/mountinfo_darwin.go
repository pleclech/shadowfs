//go:build darwin
// +build darwin

package rootinit

// UNTESTED: macOS implementation - requires testing on macOS hardware

import (
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// FindSourceFromMounts attempts to find source directory from mount information
// This is a macOS-specific implementation using getfsstat syscall
func FindSourceFromMounts(mountPoint string) (string, error) {
	// Normalize mount point for comparison
	normalizedMountPoint, err := filepath.Abs(mountPoint)
	if err != nil {
		return "", fmt.Errorf("failed to normalize mount point: %w", err)
	}
	normalizedMountPoint = filepath.Clean(normalizedMountPoint)

	// Get filesystem statistics using getfsstat
	// First call with nil buffer to get the size needed
	buf := make([]unix.Statfs_t, 0)
	count, err := unix.Getfsstat(buf, unix.MNT_NOWAIT)
	if err != nil {
		return "", fmt.Errorf("failed to get filesystem statistics: %w", err)
	}

	// Allocate buffer with the correct size
	buf = make([]unix.Statfs_t, count)
	count, err = unix.Getfsstat(buf, unix.MNT_NOWAIT)
	if err != nil {
		return "", fmt.Errorf("failed to get filesystem statistics: %w", err)
	}

	// Iterate through mount points
	for i := 0; i < count; i++ {
		statfs := buf[i]
		mountPointPath := unix.ByteSliceToString(statfs.Mntonname[:])

		// Normalize mount point path for comparison
		mountPointPath = filepath.Clean(mountPointPath)

		// Check if this mount point matches
		if mountPointPath == normalizedMountPoint {
			// For FUSE filesystems on macOS, check the filesystem type
			fsType := unix.ByteSliceToString(statfs.Fstypename[:])
			if strings.HasPrefix(fsType, "osxfuse") || strings.HasPrefix(fsType, "macfuse") {
				// On macOS, the source directory might be in Mntfromname
				source := unix.ByteSliceToString(statfs.Mntfromname[:])
				if source != "" {
					return source, nil
				}
			}
			// For other filesystems, return the device name
			return unix.ByteSliceToString(statfs.Mntfromname[:]), nil
		}
	}

	return "", fmt.Errorf("mount point not found: %s", mountPoint)
}

// IsMountPointActive checks if a mount point is currently mounted by checking getfsstat
func IsMountPointActive(mountPoint string) (bool, error) {
	// Normalize mount point for comparison
	normalizedMountPoint, err := filepath.Abs(mountPoint)
	if err != nil {
		return false, fmt.Errorf("failed to normalize mount point: %w", err)
	}
	normalizedMountPoint = filepath.Clean(normalizedMountPoint)

	// Get filesystem statistics
	buf := make([]unix.Statfs_t, 0)
	count, err := unix.Getfsstat(buf, unix.MNT_NOWAIT)
	if err != nil {
		return false, fmt.Errorf("failed to get filesystem statistics: %w", err)
	}

	// Allocate buffer with the correct size
	buf = make([]unix.Statfs_t, count)
	count, err = unix.Getfsstat(buf, unix.MNT_NOWAIT)
	if err != nil {
		return false, fmt.Errorf("failed to get filesystem statistics: %w", err)
	}

	// Iterate through mount points
	for i := 0; i < count; i++ {
		statfs := buf[i]
		mountPointPath := unix.ByteSliceToString(statfs.Mntonname[:])

		// Normalize mount point path for comparison
		mountPointPath = filepath.Clean(mountPointPath)

		// Check if this mount point matches and is a FUSE filesystem
		if mountPointPath == normalizedMountPoint {
			fsType := unix.ByteSliceToString(statfs.Fstypename[:])
			if strings.HasPrefix(fsType, "osxfuse") || strings.HasPrefix(fsType, "macfuse") || strings.HasPrefix(fsType, "fuse") {
				return true, nil
			}
		}
	}

	return false, nil
}

