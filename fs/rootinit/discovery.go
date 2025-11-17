package rootinit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pleclech/shadowfs/fs/cache"
)

// FindCacheDirectory finds the cache directory for a given mount point
// This is a wrapper that normalizes the mount point and calls cache.FindCacheDirectoryForMount
func FindCacheDirectory(mountPoint string) (string, error) {
	// Normalize mount point path using same method as NewShadowRoot for consistency
	absMountPoint, err := GetMountPoint(mountPoint)
	if err != nil {
		return "", fmt.Errorf("invalid mount point: %w", err)
	}

	// Use the shared function from cache package
	return cache.FindCacheDirectoryForMount(absMountPoint)
}

// findSourceDirectory finds the source directory for a mount point
func findSourceDirectory(mountPoint string) (string, string, error) {
	// Try to read from .target file in cache directories
	// First, try common cache locations
	cacheBaseDirs := []string{}
	if envDir := os.Getenv("SHADOWFS_CACHE_DIR"); envDir != "" {
		if absEnvDir, err := filepath.Abs(envDir); err == nil {
			cacheBaseDirs = append(cacheBaseDirs, absEnvDir)
		}
	}

	// Add default cache directory using centralized function
	if defaultDir, err := cache.GetCacheBaseDir(); err == nil {
		cacheBaseDirs = append(cacheBaseDirs, defaultDir)
	}

	// Try to find any session directory that has this mount point
	for _, baseDir := range cacheBaseDirs {
		if baseDir == "" {
			continue
		}
		entries, err := os.ReadDir(baseDir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			sessionPath := cache.GetSessionPath(baseDir, entry.Name())
			targetFile := filepath.Join(sessionPath, ".target")
			if data, err := os.ReadFile(targetFile); err == nil {
				srcDir := strings.TrimSpace(string(data))
				// Verify this session matches the mount point using centralized function
				mountID := cache.ComputeMountID(mountPoint, srcDir)
				if entry.Name() == mountID {
					return srcDir, mountID, nil
				}
			}
		}
	}

	return "", "", fmt.Errorf("source directory not found for mount point: %s", mountPoint)
}
