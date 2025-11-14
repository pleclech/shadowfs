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
	relPath := strings.TrimPrefix(path, srcDir)
	// Remove leading slash if present (filepath.Join treats it as absolute)
	relPath = strings.TrimPrefix(relPath, "/")
	return filepath.Join(cachePath, relPath)
}

// RebaseToMountPoint converts a cache path to mount point path
func RebaseToMountPoint(path, mountPoint, cachePath string) string {
	if strings.HasPrefix(path, mountPoint) {
		return path
	}
	relPath := strings.TrimPrefix(path, cachePath)
	// Remove leading slash if present (filepath.Join treats it as absolute)
	relPath = strings.TrimPrefix(relPath, "/")
	return filepath.Join(mountPoint, relPath)
}

// RebaseToSource converts a cache path to source path
func RebaseToSource(path, srcDir, cachePath string) string {
	if strings.HasPrefix(path, srcDir) {
		return path
	}
	relPath := strings.TrimPrefix(path, cachePath)
	// Remove leading slash if present (filepath.Join treats it as absolute)
	relPath = strings.TrimPrefix(relPath, "/")
	return filepath.Join(srcDir, relPath)
}

