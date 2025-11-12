package rootinit

import (
	"fmt"
	"os"
	"path/filepath"
)

// GetMountPoint validates and normalizes a mount point path
func GetMountPoint(mountPoint string) (string, error) {
	mountPoint = filepath.Clean(mountPoint)
	if !filepath.IsAbs(mountPoint) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		mountPoint = filepath.Clean(filepath.Join(cwd, mountPoint))
	}

	// Resolve symlinks to get canonical path
	// This ensures that symlinks and real paths pointing to the same directory
	// normalize to the same canonical path, preventing mountID mismatches
	resolvedPath, err := filepath.EvalSymlinks(mountPoint)
	if err != nil {
		// If path doesn't exist, EvalSymlinks will fail
		// We'll let os.Stat below provide a clearer error message
		if os.IsNotExist(err) {
			// Continue to os.Stat check which will return clearer error
		} else {
			return "", fmt.Errorf("failed to resolve symlinks: %w", err)
		}
	} else {
		mountPoint = resolvedPath
	}

	// check if mountPoint exists and is a directory
	stat, err := os.Stat(mountPoint)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("mount point does not exist:\n%w", err)
		}
		return "", fmt.Errorf("stat error:\n%w", err)
	}

	if !stat.IsDir() {
		return "", fmt.Errorf("mount point(%s) must be a directory", mountPoint)
	}

	return mountPoint, nil
}
