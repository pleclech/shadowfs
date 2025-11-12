//go:build linux
// +build linux

package rootinit

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FindSourceFromMounts attempts to find source directory from /proc/mounts
// This is a Linux-specific implementation that parses /proc/mounts
func FindSourceFromMounts(mountPoint string) (string, error) {
	// Normalize mount point for comparison
	normalizedMountPoint, err := filepath.Abs(mountPoint)
	if err != nil {
		return "", fmt.Errorf("failed to normalize mount point: %w", err)
	}
	normalizedMountPoint = filepath.Clean(normalizedMountPoint)

	// Open /proc/mounts
	file, err := os.Open("/proc/mounts")
	if err != nil {
		return "", fmt.Errorf("failed to open /proc/mounts: %w", err)
	}
	defer file.Close()

	// Parse /proc/mounts format: device mount_point filesystem options dump pass
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		device := fields[0]
		mountPointPath := fields[1]
		filesystem := fields[2]

		fmt.Printf("Debug: in FindSourceFromMounts, device: %s, mountPointPath: %s, filesystem: %s\n", device, mountPointPath, filesystem)

		// Normalize mount point path for comparison
		mountPointPath = filepath.Clean(mountPointPath)

		// Check if this mount point matches
		if mountPointPath == normalizedMountPoint {
			// For FUSE filesystems, the device field contains the source directory
			// Format is typically: source_dir#fuse or similar
			// For shadowfs, we need to extract the source directory
			if strings.HasPrefix(filesystem, "fuse") {
				// The device field for FUSE typically contains the source
				// Try to extract it (may need adjustment based on actual FUSE mount format)
				if strings.Contains(device, "#") {
					parts := strings.Split(device, "#")
					if len(parts) > 0 {
						return parts[0], nil
					}
				}
				// If no # separator, the device itself might be the source
				// But for FUSE, device is usually the mount command, not the source
				// We'll return an error and let the caller handle it
				return "", fmt.Errorf("found FUSE mount but cannot extract source directory from device field: %s", device)
			}
			// For non-FUSE filesystems, device field is the source
			return device, nil
		}
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("error reading /proc/mounts: %w", err)
	}

	return "", fmt.Errorf("mount point not found in /proc/mounts: %s", mountPoint)
}
