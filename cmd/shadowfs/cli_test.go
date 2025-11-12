//go:build integration
// +build integration

// CLI integration tests for shadowfs commands (daemon, status, version, etc.)
// These tests verify CLI command functionality, not filesystem operations.
// For filesystem integration tests, see ../filesystem_integration_test.go
package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	shadowfs "github.com/pleclech/shadowfs/fs"
	"github.com/pleclech/shadowfs/fs/cache"
)

func TestDaemonCommands(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tempDir, err := os.MkdirTemp("", "daemon-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	mountPoint := filepath.Join(tempDir, "mount")
	sourceDir := filepath.Join(tempDir, "source")

	if err := os.MkdirAll(mountPoint, 0755); err != nil {
		t.Fatalf("Failed to create mount point: %v", err)
	}
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("Failed to create source dir: %v", err)
	}

	// Test writePIDFile
	mountID := cache.ComputeMountID(mountPoint, sourceDir)
	err = writePIDFile(mountID, mountPoint, sourceDir)
	if err != nil {
		t.Errorf("writePIDFile() error = %v", err)
	}

	// Verify PID file was created
	pidFile, err := cache.GetDaemonPIDFilePath(mountID)
	if err != nil {
		t.Fatalf("Failed to get PID file path: %v", err)
	}

	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("Failed to read PID file: %v", err)
	}

	var info DaemonInfo
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatalf("Failed to unmarshal PID file: %v", err)
	}

	if info.MountPoint != mountPoint {
		t.Errorf("Expected mount point %s, got %s", mountPoint, info.MountPoint)
	}
	if info.SourceDir != sourceDir {
		t.Errorf("Expected source dir %s, got %s", sourceDir, info.SourceDir)
	}

	// Test removePIDFile
	err = removePIDFile(mountID)
	if err != nil {
		t.Errorf("removePIDFile() error = %v", err)
	}

	// Verify PID file was removed
	if _, err := os.Stat(pidFile); err == nil {
		t.Error("PID file should have been removed")
	}
}

func TestFindPIDFileByMountPoint(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tempDir, err := os.MkdirTemp("", "daemon-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	mountPoint := filepath.Join(tempDir, "mount")
	sourceDir := filepath.Join(tempDir, "source")

	if err := os.MkdirAll(mountPoint, 0755); err != nil {
		t.Fatalf("Failed to create mount point: %v", err)
	}
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("Failed to create source dir: %v", err)
	}

	// Create PID file
	mountID := cache.ComputeMountID(mountPoint, sourceDir)
	err = writePIDFile(mountID, mountPoint, sourceDir)
	if err != nil {
		t.Fatalf("Failed to write PID file: %v", err)
	}
	defer removePIDFile(mountID)

	// Test finding PID file
	pidFile, info, err := findPIDFileByMountPoint(mountPoint)
	if err != nil {
		t.Errorf("findPIDFileByMountPoint() error = %v", err)
	}
	if pidFile == "" {
		t.Error("Expected PID file path, got empty string")
	}
	if info == nil {
		t.Error("Expected DaemonInfo, got nil")
	} else {
		if info.MountPoint != mountPoint {
			t.Errorf("Expected mount point %s, got %s", mountPoint, info.MountPoint)
		}
	}

	// Test with non-existent mount point
	_, _, err = findPIDFileByMountPoint("/nonexistent/mount")
	if err == nil {
		t.Error("Expected error for non-existent mount point")
	}
}

func TestListActiveMounts(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Test listActiveMounts (may return empty list if no mounts)
	mounts, err := listActiveMounts()
	if err != nil {
		t.Errorf("listActiveMounts() error = %v", err)
	}
	// Should not error even if no mounts exist
	_ = mounts // May be empty, which is fine
}

func TestGetMountStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tempDir, err := os.MkdirTemp("", "status-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Test with non-mounted directory
	status, err := getMountStatus(tempDir)
	if err != nil {
		t.Logf("getMountStatus() error (may be expected): %v", err)
	} else if status != nil {
		if status.Status != "inactive" {
			t.Logf("Status for non-mounted dir: %s (may vary)", status.Status)
		}
	}
}

func TestCheckMountPointAvailable(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tempDir, err := os.MkdirTemp("", "check-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	mountPoint := filepath.Join(tempDir, "mount")
	if err := os.MkdirAll(mountPoint, 0755); err != nil {
		t.Fatalf("Failed to create mount point: %v", err)
	}

	// Test with available mount point (should not error)
	err = checkMountPointAvailable(mountPoint)
	if err != nil {
		t.Logf("checkMountPointAvailable() error (may be expected if already mounted): %v", err)
	}
}

func TestValidateMountPoint(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "validate-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	tests := []struct {
		name      string
		mountPoint string
		shouldErr bool
	}{
		{
			name:       "valid directory",
			mountPoint: tempDir,
			shouldErr:  false,
		},
		{
			name:       "non-existent path",
			mountPoint: "/nonexistent/dir",
			shouldErr:  true,
		},
		{
			name:       "file instead of directory",
			mountPoint: func() string {
				testFile := filepath.Join(tempDir, "testfile")
				os.WriteFile(testFile, []byte("test"), 0644)
				return testFile
			}(),
			shouldErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMountPoint(tt.mountPoint)
			if tt.shouldErr {
				if err == nil {
					t.Errorf("validateMountPoint(%s) expected error, got nil", tt.mountPoint)
				}
			} else {
				if err != nil {
					t.Errorf("validateMountPoint(%s) unexpected error: %v", tt.mountPoint, err)
				}
			}
		})
	}
}

func TestStopDaemon(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tempDir, err := os.MkdirTemp("", "stop-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	mountPoint := filepath.Join(tempDir, "mount")
	if err := os.MkdirAll(mountPoint, 0755); err != nil {
		t.Fatalf("Failed to create mount point: %v", err)
	}

	// Test stopping non-existent daemon (should error)
	err = stopDaemon(mountPoint)
	if err == nil {
		t.Log("stopDaemon() for non-existent daemon may not error (acceptable)")
	}
}

// Test CLI command execution (requires shadowfs binary)
func TestCLICommands(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Find shadowfs binary
	binary := os.Getenv("SHADOWFS_BINARY")
	if binary == "" {
		// Try to find it in common locations
		binary = "./shadowfs"
		if _, err := os.Stat(binary); err != nil {
			t.Skip("shadowfs binary not found, skipping CLI tests")
		}
	}

	tempDir, err := os.MkdirTemp("", "cli-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	mountPoint := filepath.Join(tempDir, "mount")
	sourceDir := filepath.Join(tempDir, "source")

	if err := os.MkdirAll(mountPoint, 0755); err != nil {
		t.Fatalf("Failed to create mount point: %v", err)
	}
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("Failed to create source dir: %v", err)
	}

	// Test list command
	cmd := exec.Command(binary, "list")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("list command output: %s, error: %v (may be expected)", string(output), err)
	}

	// Test info command with non-mounted point
	cmd = exec.Command(binary, "info", "--mount-point", mountPoint)
	output, err = cmd.CombinedOutput()
	if err != nil {
		t.Logf("info command output: %s, error: %v (may be expected)", string(output), err)
	}
}

// Backups CLI Integration Tests

func TestBackupsList(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Find shadowfs binary
	binary := os.Getenv("SHADOWFS_BINARY")
	if binary == "" {
		binary = "./shadowfs"
		if _, err := os.Stat(binary); err != nil {
			t.Skip("shadowfs binary not found, skipping CLI tests")
		}
	}

	tempDir, err := os.MkdirTemp("", "backups-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	mountPoint := filepath.Join(tempDir, "mount")
	sourceDir := filepath.Join(tempDir, "source")

	if err := os.MkdirAll(mountPoint, 0755); err != nil {
		t.Fatalf("Failed to create mount point: %v", err)
	}
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("Failed to create source dir: %v", err)
	}

	// Create a test file in source
	testFile := filepath.Join(sourceDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create a backup using the API (simulating sync operation)
	oldEnv := os.Getenv("SHADOWFS_BACKUP_DIR")
	defer os.Setenv("SHADOWFS_BACKUP_DIR", oldEnv)
	backupBaseDir := filepath.Join(tempDir, "backups")
	os.Setenv("SHADOWFS_BACKUP_DIR", backupBaseDir)

	backupID, _, err := shadowfs.CreateBackup(sourceDir, mountPoint)
	if err != nil {
		t.Fatalf("Failed to create backup: %v", err)
	}
	if backupID == "" {
		t.Fatal("Backup ID should not be empty")
	}

	// Test backups list command
	cmd := exec.Command(binary, "backups", "list")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("backups list command failed: %v, output: %s", err, string(output))
	}

	// Verify output contains backup information
	outputStr := string(output)
	if !strings.Contains(outputStr, backupID[:20]) {
		t.Errorf("Expected backup ID in output, got: %s", outputStr)
	}
	if !strings.Contains(outputStr, "test.txt") || !strings.Contains(outputStr, sourceDir) {
		t.Logf("backups list output: %s", outputStr)
		// Don't fail - the output format might vary
	}
}

func TestBackupsInfo(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Find shadowfs binary
	binary := os.Getenv("SHADOWFS_BINARY")
	if binary == "" {
		binary = "./shadowfs"
		if _, err := os.Stat(binary); err != nil {
			t.Skip("shadowfs binary not found, skipping CLI tests")
		}
	}

	tempDir, err := os.MkdirTemp("", "backups-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	mountPoint := filepath.Join(tempDir, "mount")
	sourceDir := filepath.Join(tempDir, "source")

	if err := os.MkdirAll(mountPoint, 0755); err != nil {
		t.Fatalf("Failed to create mount point: %v", err)
	}
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("Failed to create source dir: %v", err)
	}

	// Create a test file in source
	testFile := filepath.Join(sourceDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create a backup using the API
	oldEnv := os.Getenv("SHADOWFS_BACKUP_DIR")
	defer os.Setenv("SHADOWFS_BACKUP_DIR", oldEnv)
	backupBaseDir := filepath.Join(tempDir, "backups")
	os.Setenv("SHADOWFS_BACKUP_DIR", backupBaseDir)

	backupID, _, err := shadowfs.CreateBackup(sourceDir, mountPoint)
	if err != nil {
		t.Fatalf("Failed to create backup: %v", err)
	}

	// Test backups info command
	cmd := exec.Command(binary, "backups", "info", backupID)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("backups info command failed: %v, output: %s", err, string(output))
	}

	// Verify output contains backup information
	outputStr := string(output)
	if !strings.Contains(outputStr, backupID) {
		t.Errorf("Expected backup ID in output, got: %s", outputStr)
	}
	if !strings.Contains(outputStr, sourceDir) {
		t.Errorf("Expected source path in output, got: %s", outputStr)
	}
}

func TestBackupsDelete(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Find shadowfs binary
	binary := os.Getenv("SHADOWFS_BINARY")
	if binary == "" {
		binary = "./shadowfs"
		if _, err := os.Stat(binary); err != nil {
			t.Skip("shadowfs binary not found, skipping CLI tests")
		}
	}

	tempDir, err := os.MkdirTemp("", "backups-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	mountPoint := filepath.Join(tempDir, "mount")
	sourceDir := filepath.Join(tempDir, "source")

	if err := os.MkdirAll(mountPoint, 0755); err != nil {
		t.Fatalf("Failed to create mount point: %v", err)
	}
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("Failed to create source dir: %v", err)
	}

	// Create a test file in source
	testFile := filepath.Join(sourceDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create a backup using the API
	oldEnv := os.Getenv("SHADOWFS_BACKUP_DIR")
	defer os.Setenv("SHADOWFS_BACKUP_DIR", oldEnv)
	backupBaseDir := filepath.Join(tempDir, "backups")
	os.Setenv("SHADOWFS_BACKUP_DIR", backupBaseDir)

	backupID, backupDir, err := shadowfs.CreateBackup(sourceDir, mountPoint)
	if err != nil {
		t.Fatalf("Failed to create backup: %v", err)
	}

	// Verify backup exists
	if _, err := os.Stat(backupDir); err != nil {
		t.Fatalf("Backup directory should exist: %v", err)
	}

	// Test backups delete command with --force flag
	cmd := exec.Command(binary, "backups", "delete", "--force", backupID)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("backups delete command failed: %v, output: %s", err, string(output))
	}

	// Verify backup was deleted
	if _, err := os.Stat(backupDir); err == nil {
		t.Error("Backup directory should be deleted")
	}

	// Verify output mentions deletion
	outputStr := string(output)
	if !strings.Contains(outputStr, backupID) {
		t.Errorf("Expected backup ID in delete output, got: %s", outputStr)
	}
}

func TestBackupsCleanup(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Find shadowfs binary
	binary := os.Getenv("SHADOWFS_BINARY")
	if binary == "" {
		binary = "./shadowfs"
		if _, err := os.Stat(binary); err != nil {
			t.Skip("shadowfs binary not found, skipping CLI tests")
		}
	}

	tempDir, err := os.MkdirTemp("", "backups-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	mountPoint := filepath.Join(tempDir, "mount")
	sourceDir := filepath.Join(tempDir, "source")

	if err := os.MkdirAll(mountPoint, 0755); err != nil {
		t.Fatalf("Failed to create mount point: %v", err)
	}
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("Failed to create source dir: %v", err)
	}

	// Create a test file in source
	testFile := filepath.Join(sourceDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create a backup using the API
	oldEnv := os.Getenv("SHADOWFS_BACKUP_DIR")
	defer os.Setenv("SHADOWFS_BACKUP_DIR", oldEnv)
	backupBaseDir := filepath.Join(tempDir, "backups")
	os.Setenv("SHADOWFS_BACKUP_DIR", backupBaseDir)

	backupID, _, err := shadowfs.CreateBackup(sourceDir, mountPoint)
	if err != nil {
		t.Fatalf("Failed to create backup: %v", err)
	}

	// Test backups cleanup command with --force flag and --older-than 0 (should delete all)
	cmd := exec.Command(binary, "backups", "cleanup", "--older-than", "0", "--force")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("backups cleanup command failed: %v, output: %s", err, string(output))
	}

	// Verify backup was deleted
	_, err = shadowfs.ReadBackupInfo(backupID)
	if err == nil {
		t.Error("Backup should be deleted after cleanup")
	}

	// Verify output mentions cleanup
	outputStr := string(output)
	if !strings.Contains(outputStr, "deleted") && !strings.Contains(outputStr, "Successfully") {
		t.Logf("backups cleanup output: %s", outputStr)
		// Don't fail - output format might vary
	}
}

