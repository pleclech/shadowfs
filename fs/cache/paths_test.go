package cache

import (
	"os"
	"path/filepath"
	"testing"
)

func TestComputeMountID(t *testing.T) {
	mountPoint := "/test/mount"
	srcDir := "/test/source"

	mountID := ComputeMountID(mountPoint, srcDir)

	// Mount ID should be a hex string (SHA256 produces 64 hex characters)
	if len(mountID) != 64 {
		t.Errorf("Expected mount ID length 64, got %d", len(mountID))
	}

	// Same inputs should produce same mount ID
	mountID2 := ComputeMountID(mountPoint, srcDir)
	if mountID != mountID2 {
		t.Error("Same inputs should produce same mount ID")
	}

	// Different inputs should produce different mount IDs
	mountID3 := ComputeMountID(mountPoint, "/different/source")
	if mountID == mountID3 {
		t.Error("Different inputs should produce different mount IDs")
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
		t.Fatalf("GetCacheBaseDir failed: %v", err)
	}
	absTestDir, _ := filepath.Abs(testDir)
	if cacheDir != absTestDir {
		t.Errorf("Expected %s, got %s", absTestDir, cacheDir)
	}

	// Test with environment variable unset (should use default)
	os.Unsetenv("SHADOWFS_CACHE_DIR")
	cacheDir, err = GetCacheBaseDir()
	if err != nil {
		t.Fatalf("GetCacheBaseDir failed: %v", err)
	}
	homeDir, _ := os.UserHomeDir()
	expectedDir := filepath.Join(homeDir, ".shadowfs")
	if cacheDir != expectedDir {
		t.Errorf("Expected %s, got %s", expectedDir, cacheDir)
	}
}

func TestGetSessionPath(t *testing.T) {
	baseCacheDir := "/test/cache"
	mountID := "abc123def456"

	sessionPath := GetSessionPath(baseCacheDir, mountID)
	expectedPath := filepath.Join(baseCacheDir, mountID)

	if sessionPath != expectedPath {
		t.Errorf("Expected %s, got %s", expectedPath, sessionPath)
	}
}

