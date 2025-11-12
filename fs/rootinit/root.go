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

