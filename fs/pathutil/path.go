package pathutil

import (
	"path/filepath"
	"strings"
)

// RebaseToCache converts a source path to cache path
func RebaseToCache(path, cachePath, srcDir string) string {
	if strings.HasPrefix(path, cachePath) {
		return path
	}
	return filepath.Join(cachePath, strings.TrimPrefix(path, srcDir))
}

// RebaseToMountPoint converts a cache path to mount point path
func RebaseToMountPoint(path, mountPoint, cachePath string) string {
	if strings.HasPrefix(path, mountPoint) {
		return path
	}
	return filepath.Join(mountPoint, strings.TrimPrefix(path, cachePath))
}

// RebaseToSource converts a cache path to source path
func RebaseToSource(path, srcDir, cachePath string) string {
	if strings.HasPrefix(path, srcDir) {
		return path
	}
	return filepath.Join(srcDir, strings.TrimPrefix(path, cachePath))
}

