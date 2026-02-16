package cache

import (
	"os"
	"path/filepath"
	"testing"

	tu "github.com/pleclech/shadowfs/fs/utils/testings"
)

func TestComputeMountID(t *testing.T) {
	mountPoint := "/test/mount"
	srcDir := "/test/source"

	mountID := ComputeMountID(mountPoint, srcDir)

	// Mount ID should be a hex string (SHA256 produces 64 hex characters)
	if len(mountID) != 64 {
		tu.Failf(t, "Expected mount ID length 64, got %d", len(mountID))
	}

	// Same inputs should produce same mount ID
	mountID2 := ComputeMountID(mountPoint, srcDir)
	if mountID != mountID2 {
		tu.Failf(t, "Same inputs should produce same mount ID")
	}

	// Different inputs should produce different mount IDs
	mountID3 := ComputeMountID(mountPoint, "/different/source")
	if mountID == mountID3 {
		tu.Failf(t, "Different inputs should produce different mount IDs")
	}
}

func TestGetCacheBaseDir(t *testing.T) {
	// Save original environment variable
	originalEnv := os.Getenv("SHADOWFS_CACHE_DIR")
	defer os.Setenv("SHADOWFS_CACHE_DIR", originalEnv)

	// Test with environment variable set
	testDir := "/test/cache/dir"
	os.Setenv("SHADOWFS_CACHE_DIR", testDir)
	cacheDir, err := GetCacheBaseDir()
	if err != nil {
		tu.Failf(t, "GetCacheBaseDir failed: %v", err)
	}
	absTestDir, _ := filepath.Abs(testDir)
	if cacheDir != absTestDir {
		tu.Failf(t, "Expected %s, got %s", absTestDir, cacheDir)
	}

	// Test with environment variable unset (should use default)
	os.Unsetenv("SHADOWFS_CACHE_DIR")
	cacheDir, err = GetCacheBaseDir()
	if err != nil {
		tu.Failf(t, "GetCacheBaseDir failed: %v", err)
	}
	homeDir, _ := os.UserHomeDir()
	expectedDir := filepath.Join(homeDir, ".shadowfs")
	if cacheDir != expectedDir {
		tu.Failf(t, "Expected %s, got %s", expectedDir, cacheDir)
	}
}

func TestGetSessionPath(t *testing.T) {
	baseCacheDir := "/test/cache"
	mountID := "abc123def456"

	sessionPath := GetSessionPath(baseCacheDir, mountID)
	expectedPath := filepath.Join(baseCacheDir, mountID)

	if sessionPath != expectedPath {
		tu.Failf(t, "Expected %s, got %s", expectedPath, sessionPath)
	}
}

func TestGetGitDirPath(t *testing.T) {
	sessionPath := "/test/cache/abc123"
	gitDirPath := GetGitDirPath(sessionPath)
	expectedPath := filepath.Join(sessionPath, ".gitofs", ".git")

	if gitDirPath != expectedPath {
		tu.Failf(t, "Expected %s, got %s", expectedPath, gitDirPath)
	}
}

func TestGetCachePath(t *testing.T) {
	sessionPath := "/test/cache/abc123"
	cachePath := GetCachePath(sessionPath)
	expectedPath := filepath.Join(sessionPath, ".root")

	if cachePath != expectedPath {
		tu.Failf(t, "Expected %s, got %s", expectedPath, cachePath)
	}
}

func TestGetTargetFilePath(t *testing.T) {
	sessionPath := "/test/cache/abc123"
	targetPath := GetTargetFilePath(sessionPath)
	expectedPath := filepath.Join(sessionPath, ".target")

	if targetPath != expectedPath {
		tu.Failf(t, "Expected %s, got %s", expectedPath, targetPath)
	}
}

func TestGetDaemonDirPath(t *testing.T) {
	// Save original environment variable
	originalEnv := os.Getenv("SHADOWFS_CACHE_DIR")
	defer os.Setenv("SHADOWFS_CACHE_DIR", originalEnv)

	// Test with environment variable set
	testDir := "/test/cache/dir"
	os.Setenv("SHADOWFS_CACHE_DIR", testDir)
	daemonDir, err := GetDaemonDirPath()
	if err != nil {
		tu.Failf(t, "GetDaemonDirPath failed: %v", err)
	}
	absTestDir, _ := filepath.Abs(testDir)
	expectedDir := filepath.Join(absTestDir, "daemons")
	if daemonDir != expectedDir {
		tu.Failf(t, "Expected %s, got %s", expectedDir, daemonDir)
	}

	// Test with environment variable unset (should use default)
	os.Unsetenv("SHADOWFS_CACHE_DIR")
	daemonDir, err = GetDaemonDirPath()
	if err != nil {
		tu.Failf(t, "GetDaemonDirPath failed: %v", err)
	}
	homeDir, _ := os.UserHomeDir()
	expectedDir = filepath.Join(homeDir, ".shadowfs", "daemons")
	if daemonDir != expectedDir {
		tu.Failf(t, "Expected %s, got %s", expectedDir, daemonDir)
	}
}

func TestGetDaemonPIDFilePath(t *testing.T) {
	// Save original environment variable
	originalEnv := os.Getenv("SHADOWFS_CACHE_DIR")
	defer os.Setenv("SHADOWFS_CACHE_DIR", originalEnv)

	mountID := "abc123def456"

	// Test with environment variable set
	testDir := "/test/cache/dir"
	os.Setenv("SHADOWFS_CACHE_DIR", testDir)
	pidPath, err := GetDaemonPIDFilePath(mountID)
	if err != nil {
		tu.Failf(t, "GetDaemonPIDFilePath failed: %v", err)
	}
	absTestDir, _ := filepath.Abs(testDir)
	expectedPath := filepath.Join(absTestDir, "daemons", mountID+".pid")
	if pidPath != expectedPath {
		tu.Failf(t, "Expected %s, got %s", expectedPath, pidPath)
	}

	// Test with environment variable unset
	os.Unsetenv("SHADOWFS_CACHE_DIR")
	pidPath, err = GetDaemonPIDFilePath(mountID)
	if err != nil {
		tu.Failf(t, "GetDaemonPIDFilePath failed: %v", err)
	}
	homeDir, _ := os.UserHomeDir()
	expectedPath = filepath.Join(homeDir, ".shadowfs", "daemons", mountID+".pid")
	if pidPath != expectedPath {
		tu.Failf(t, "Expected %s, got %s", expectedPath, pidPath)
	}
}

func TestGetDaemonLogFilePath(t *testing.T) {
	// Save original environment variable
	originalEnv := os.Getenv("SHADOWFS_CACHE_DIR")
	defer os.Setenv("SHADOWFS_CACHE_DIR", originalEnv)

	mountID := "abc123def456"

	// Test with environment variable set
	testDir := "/test/cache/dir"
	os.Setenv("SHADOWFS_CACHE_DIR", testDir)
	logPath, err := GetDaemonLogFilePath(mountID)
	if err != nil {
		tu.Failf(t, "GetDaemonLogFilePath failed: %v", err)
	}
	absTestDir, _ := filepath.Abs(testDir)
	expectedPath := filepath.Join(absTestDir, "daemons", mountID+".log")
	if logPath != expectedPath {
		tu.Failf(t, "Expected %s, got %s", expectedPath, logPath)
	}

	// Test with environment variable unset
	os.Unsetenv("SHADOWFS_CACHE_DIR")
	logPath, err = GetDaemonLogFilePath(mountID)
	if err != nil {
		tu.Failf(t, "GetDaemonLogFilePath failed: %v", err)
	}
	homeDir, _ := os.UserHomeDir()
	expectedPath = filepath.Join(homeDir, ".shadowfs", "daemons", mountID+".log")
	if logPath != expectedPath {
		tu.Failf(t, "Expected %s, got %s", expectedPath, logPath)
	}
}

func TestGetSocketPath(t *testing.T) {
	originalEnv := os.Getenv("SHADOWFS_CACHE_DIR")
	defer os.Setenv("SHADOWFS_CACHE_DIR", originalEnv)

	mountID := "abc123def456"

	testDir := "/test/cache/dir"
	os.Setenv("SHADOWFS_CACHE_DIR", testDir)
	socketPath, err := GetSocketPath(mountID)
	if err != nil {
		tu.Failf(t, "GetSocketPath failed: %v", err)
	}
	absTestDir, _ := filepath.Abs(testDir)
	expectedPath := filepath.Join(absTestDir, "daemons", mountID+".sock")
	if socketPath != expectedPath {
		tu.Failf(t, "Expected %s, got %s", expectedPath, socketPath)
	}

	os.Unsetenv("SHADOWFS_CACHE_DIR")
	socketPath, err = GetSocketPath(mountID)
	if err != nil {
		tu.Failf(t, "GetSocketPath failed: %v", err)
	}
	homeDir, _ := os.UserHomeDir()
	expectedPath = filepath.Join(homeDir, ".shadowfs", "daemons", mountID+".sock")
	if socketPath != expectedPath {
		tu.Failf(t, "Expected %s, got %s", expectedPath, socketPath)
	}
}

func TestFindCacheDirectoryForMount_NotFound(t *testing.T) {
	originalEnv := os.Getenv("SHADOWFS_CACHE_DIR")
	defer os.Setenv("SHADOWFS_CACHE_DIR", originalEnv)

	tempDir, err := os.MkdirTemp("", "cache-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	os.Setenv("SHADOWFS_CACHE_DIR", tempDir)

	_, err = FindCacheDirectoryForMount("/nonexistent/mount/point")
	if err == nil {
		tu.Failf(t, "Expected error for non-existent mount point")
	}
}

func TestFindCacheDirectoryForMount_Found(t *testing.T) {
	originalEnv := os.Getenv("SHADOWFS_CACHE_DIR")
	defer os.Setenv("SHADOWFS_CACHE_DIR", originalEnv)

	tempDir, err := os.MkdirTemp("", "cache-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	os.Setenv("SHADOWFS_CACHE_DIR", tempDir)

	mountPoint := "/test/mount"
	srcDir := "/test/source"
	mountID := ComputeMountID(mountPoint, srcDir)

	sessionPath := GetSessionPath(tempDir, mountID)
	os.MkdirAll(sessionPath, 0755)

	targetFile := GetTargetFilePath(sessionPath)
	os.WriteFile(targetFile, []byte(srcDir), 0644)

	cachePath := GetCachePath(sessionPath)
	os.MkdirAll(cachePath, 0755)

	foundSession, err := FindCacheDirectoryForMount(mountPoint)
	if err != nil {
		tu.Failf(t, "FindCacheDirectoryForMount failed: %v", err)
	}
	if foundSession != sessionPath {
		tu.Failf(t, "Expected %s, got %s", sessionPath, foundSession)
	}
}
