package rootinit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pleclech/shadowfs/fs/cache"
)

// FindCacheDirectory finds the cache directory for a given mount point
func FindCacheDirectory(mountPoint string) (string, error) {
	// 1. Normalize mount point path using same method as NewShadowRoot for consistency
	absMountPoint, err := GetMountPoint(mountPoint)
	if err != nil {
		return "", fmt.Errorf("invalid mount point: %w", err)
	}

	// 2. Try to find source directory from mount info or .target file
	_, mountID, err := findSourceDirectory(absMountPoint)
	if err != nil {
		return "", fmt.Errorf("cannot find source directory: %w", err)
	}

	// 4. Search cache directories
	// Try environment variable first
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

	for _, baseDir := range cacheBaseDirs {
		if baseDir == "" {
			continue
		}
		sessionPath := cache.GetSessionPath(baseDir, mountID)
		fmt.Printf("Debug: in FindCacheDirectory, sessionPath: %s, mountID: %s\n", sessionPath, mountID)
		// Check if cache directory exists by checking for .target or .root
		targetFile := cache.GetTargetFilePath(sessionPath)
		if _, err := os.Stat(targetFile); err == nil {
			return sessionPath, nil
		}
		cachePath := cache.GetCachePath(sessionPath)
		if _, err := os.Stat(cachePath); err == nil {
			return sessionPath, nil
		}
	}

	return "", fmt.Errorf("cache directory not found for mount point: %s", mountPoint)
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
