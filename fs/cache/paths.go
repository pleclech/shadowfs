package cache

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ComputeMountID calculates the mount ID from mount point and source directory
// The mount ID is a SHA256 hash of the concatenated mount point and source directory paths
func ComputeMountID(mountPoint, srcDir string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(mountPoint+srcDir)))
}

// GetCacheBaseDir returns the cache base directory
// It checks SHADOWFS_CACHE_DIR environment variable first, then defaults to ~/.shadowfs
func GetCacheBaseDir() (string, error) {
	// Check environment variable first
	if cacheDir := os.Getenv("SHADOWFS_CACHE_DIR"); cacheDir != "" {
		absPath, err := filepath.Abs(cacheDir)
		if err != nil {
			return "", fmt.Errorf("invalid cache directory path: %w", err)
		}
		return absPath, nil
	}

	// Default to ~/.shadowfs
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	return filepath.Join(homeDir, ".shadowfs"), nil
}

// GetSessionPath constructs the session path from cache base directory and mount ID
func GetSessionPath(baseCacheDir, mountID string) string {
	return filepath.Join(baseCacheDir, mountID)
}

// GetGitDirPath returns the git directory path (.gitofs/.git) for a session path
func GetGitDirPath(sessionPath string) string {
	return filepath.Join(sessionPath, ".gitofs", ".git")
}

// GetCachePath returns the cache path (.root) for a session path
func GetCachePath(sessionPath string) string {
	return filepath.Join(sessionPath, ".root")
}

// GetTargetFilePath returns the target file path (.target) for a session path
func GetTargetFilePath(sessionPath string) string {
	return filepath.Join(sessionPath, ".target")
}

// GetDaemonDirPath returns the daemon directory path for storing PID files
func GetDaemonDirPath() (string, error) {
	baseDir, err := GetCacheBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(baseDir, "daemons"), nil
}

// GetDaemonPIDFilePath returns the PID file path for a mount ID
func GetDaemonPIDFilePath(mountID string) (string, error) {
	daemonDir, err := GetDaemonDirPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(daemonDir, mountID+".pid"), nil
}

// GetDaemonLogFilePath returns the log file path for a mount ID
func GetDaemonLogFilePath(mountID string) (string, error) {
	daemonDir, err := GetDaemonDirPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(daemonDir, mountID+".log"), nil
}

// GetSocketPath returns the Unix socket path for a mount ID
// Uses shortened filename to avoid Unix socket path length limits (~108 chars on Linux)
func GetSocketPath(mountID string) (string, error) {
	daemonDir, err := GetDaemonDirPath()
	if err != nil {
		return "", err
	}
	// Use just the mountID as filename (without "shadowfs-" prefix) to keep path shorter
	// mountID is already unique (SHA256 hash), so no prefix needed
	return filepath.Join(daemonDir, mountID+".sock"), nil
}

// FindCacheDirectoryForMount finds the cache directory for a given mount point
// This function searches cache directories to find the session that matches the mount point.
// It accepts a normalized mount point (absolute, symlinks resolved) and searches for matching sessions.
// This is in the cache package to avoid import cycles - both rootinit and testings can use it.
func FindCacheDirectoryForMount(normalizedMountPoint string) (string, error) {
	// Search cache directories
	cacheBaseDirs := []string{}
	if envDir := os.Getenv("SHADOWFS_CACHE_DIR"); envDir != "" {
		if absEnvDir, err := filepath.Abs(envDir); err == nil {
			cacheBaseDirs = append(cacheBaseDirs, absEnvDir)
		}
	}

	// Add default cache directory
	if defaultDir, err := GetCacheBaseDir(); err == nil {
		cacheBaseDirs = append(cacheBaseDirs, defaultDir)
	}

	// Try to find session directory that matches this mount point
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
			sessionPath := GetSessionPath(baseDir, entry.Name())
			targetFile := GetTargetFilePath(sessionPath)
			if data, err := os.ReadFile(targetFile); err == nil {
				srcDir := strings.TrimSpace(string(data))
				// Verify this session matches the mount point
				mountID := ComputeMountID(normalizedMountPoint, srcDir)
				if entry.Name() == mountID {
					// Verify cache directory exists
					if _, err := os.Stat(targetFile); err == nil {
						return sessionPath, nil
					}
					cachePath := GetCachePath(sessionPath)
					if _, err := os.Stat(cachePath); err == nil {
						return sessionPath, nil
					}
				}
			}
		}
	}

	return "", fmt.Errorf("cache directory not found for mount point: %s", normalizedMountPoint)
}

