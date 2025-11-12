package cache

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
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

