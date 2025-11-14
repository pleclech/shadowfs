package utils

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	tu "github.com/pleclech/shadowfs/fs/utils/testings"
)

func TestEnsureDirPermissions(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "permissions-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	testDir := filepath.Join(tempDir, "testdir")
	if err := os.Mkdir(testDir, 0700); err != nil {
		tu.Failf(t, "Failed to create test directory: %v", err)
	}

	// Test ensuring permissions
	err = EnsureDirPermissions(testDir)
	if err != nil {
		tu.Failf(t, "EnsureDirPermissions() error = %v", err)
	}

	// Verify permissions were set correctly
	var st syscall.Stat_t
	if err := syscall.Lstat(testDir, &st); err != nil {
		tu.Failf(t, "Failed to stat directory: %v", err)
	}

	// Check that execute bits are set (at least 0755)
	mode := st.Mode & 0777
	if mode&0111 == 0 {
		tu.Failf(t, "Directory should have execute permissions, got mode %o", mode)
	}
}

func TestEnsureDirPermissions_NotDirectory(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "permissions-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	testFile := filepath.Join(tempDir, "testfile")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	// Should return ENOTDIR for non-directory
	err = EnsureDirPermissions(testFile)
	if err != syscall.ENOTDIR {
		tu.Failf(t, "EnsureDirPermissions() on file should return ENOTDIR, got %v", err)
	}
}

func TestEnsureDirPermissions_NonExistent(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "permissions-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	nonExistent := filepath.Join(tempDir, "nonexistent")

	// Should return error for non-existent path
	err = EnsureDirPermissions(nonExistent)
	if err == nil {
		tu.Failf(t, "EnsureDirPermissions() on non-existent path should return error")
	}
}

func TestPreserveOwner(t *testing.T) {
	// PreserveOwner only works when running as root
	// For non-root users, it should return nil without error
	ctx := context.Background()
	tempDir, err := os.MkdirTemp("", "preserve-owner-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	testFile := filepath.Join(tempDir, "testfile")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	// Should not error for non-root users
	err = PreserveOwner(ctx, testFile)
	if err != nil {
		tu.Failf(t, "PreserveOwner() error = %v (expected nil for non-root)", err)
	}
}

func TestPreserveOwner_WithFuseContext(t *testing.T) {
	// PreserveOwner checks for caller in context via fuse.FromContext
	// For non-root users, it returns nil immediately
	// For root users with valid context, it sets ownership
	// We can't easily create a fuse.Caller in tests, so we test the non-root path
	ctx := context.Background()

	tempDir, err := os.MkdirTemp("", "preserve-owner-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	testFile := filepath.Join(tempDir, "testfile")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	// Should not error (for non-root users, returns nil immediately)
	err = PreserveOwner(ctx, testFile)
	if err != nil {
		tu.Failf(t, "PreserveOwner() error = %v (expected nil for non-root)", err)
	}
}
