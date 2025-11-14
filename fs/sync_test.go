package fs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	tu "github.com/pleclech/shadowfs/fs/utils/testings"
)

func TestCreateBackup(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "shadowfs-test-")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	sourceDir := filepath.Join(tempDir, "source")
	mountPoint := filepath.Join(tempDir, "mount")

	// Create source directory with some files
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		tu.Failf(t, "Failed to create source dir: %v", err)
	}

	testFile := filepath.Join(sourceDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	// Set backup directory
	backupBaseDir := filepath.Join(tempDir, "backups")
	oldEnv := os.Getenv("SHADOWFS_BACKUP_DIR")
	defer os.Setenv("SHADOWFS_BACKUP_DIR", oldEnv)
	os.Setenv("SHADOWFS_BACKUP_DIR", backupBaseDir)

	// Create backup
	backupID, backupDir, err := CreateBackup(sourceDir, mountPoint)
	if err != nil {
		tu.Failf(t, "CreateBackup failed: %v", err)
	}

	if backupID == "" {
		tu.Failf(t, "Backup ID should not be empty")
	}

	if backupDir == "" {
		tu.Failf(t, "Backup directory should not be empty")
	}

	// Verify backup directory exists
	if _, err := os.Stat(backupDir); err != nil {
		tu.Failf(t, "Backup directory does not exist: %v", err)
	}

	// Verify backup info file exists
	infoFile := filepath.Join(backupDir, ".backup-info")
	if _, err := os.Stat(infoFile); err != nil {
		tu.Failf(t, "Backup info file does not exist: %v", err)
	}

	// Verify test file was backed up
	backedUpFile := filepath.Join(backupDir, "test.txt")
	if _, err := os.Stat(backedUpFile); err != nil {
		tu.Failf(t, "Backed up file does not exist: %v", err)
	}

	// Verify file content
	content, err := os.ReadFile(backedUpFile)
	if err != nil {
		tu.Failf(t, "Failed to read backed up file: %v", err)
	}
	if string(content) != "test content" {
		tu.Failf(t, "Expected content 'test content', got '%s'", string(content))
	}
}

func TestRollbackSync(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "shadowfs-test-")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	sourceDir := filepath.Join(tempDir, "source")
	backupBaseDir := filepath.Join(tempDir, "backups")

	// Create source directory
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		tu.Failf(t, "Failed to create source dir: %v", err)
	}

	// Create initial file
	initialFile := filepath.Join(sourceDir, "initial.txt")
	if err := os.WriteFile(initialFile, []byte("initial content"), 0644); err != nil {
		tu.Failf(t, "Failed to create initial file: %v", err)
	}

	// Create backup
	mountPoint := filepath.Join(tempDir, "mount")
	oldEnv := os.Getenv("SHADOWFS_BACKUP_DIR")
	defer os.Setenv("SHADOWFS_BACKUP_DIR", oldEnv)
	os.Setenv("SHADOWFS_BACKUP_DIR", backupBaseDir)

	backupID, _, err := CreateBackup(sourceDir, mountPoint)
	if err != nil {
		tu.Failf(t, "CreateBackup failed: %v", err)
	}

	// Modify source file
	if err := os.WriteFile(initialFile, []byte("modified content"), 0644); err != nil {
		tu.Failf(t, "Failed to modify file: %v", err)
	}

	// Verify file was modified
	content, err := os.ReadFile(initialFile)
	if err != nil {
		tu.Failf(t, "Failed to read file: %v", err)
	}
	if string(content) != "modified content" {
		tu.Failf(t, "Expected 'modified content', got '%s'", string(content))
	}

	// Rollback
	if err := RollbackSync(backupID, sourceDir); err != nil {
		tu.Failf(t, "RollbackSync failed: %v", err)
	}

	// Verify file was restored
	content, err = os.ReadFile(initialFile)
	if err != nil {
		tu.Failf(t, "Failed to read file after rollback: %v", err)
	}
	if string(content) != "initial content" {
		tu.Failf(t, "Expected 'initial content' after rollback, got '%s'", string(content))
	}
}

func TestSyncOptions(t *testing.T) {
	options := SyncOptions{
		MountPoint: "/test/mount",
		CachePath:  "/test/cache",
		SourcePath: "/test/source",
		Backup:     true,
		DryRun:     false,
		Force:      false,
	}

	if options.MountPoint != "/test/mount" {
		tu.Failf(t, "Expected mount point '/test/mount', got '%s'", options.MountPoint)
	}

	if !options.Backup {
		tu.Failf(t, "Backup should be enabled")
	}
}

func TestBackupInfo(t *testing.T) {
	info := BackupInfo{
		Timestamp:    time.Now(),
		MountID:      "test-mount-id",
		SourcePath:   "/test/source",
		FilesChanged: []string{"file1.txt", "file2.txt"},
	}

	if info.MountID != "test-mount-id" {
		tu.Failf(t, "Expected mount ID 'test-mount-id', got '%s'", info.MountID)
	}

	if len(info.FilesChanged) != 2 {
		tu.Failf(t, "Expected 2 files changed, got %d", len(info.FilesChanged))
	}
}

func TestListBackups(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "shadowfs-test-")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	backupBaseDir := filepath.Join(tempDir, "backups")
	oldEnv := os.Getenv("SHADOWFS_BACKUP_DIR")
	defer os.Setenv("SHADOWFS_BACKUP_DIR", oldEnv)
	os.Setenv("SHADOWFS_BACKUP_DIR", backupBaseDir)

	sourceDir := filepath.Join(tempDir, "source1")
	mountPoint1 := filepath.Join(tempDir, "mount1")
	mountPoint2 := filepath.Join(tempDir, "mount2")

	// Create source directories
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		tu.Failf(t, "Failed to create source dir: %v", err)
	}

	// Create two backups with different mount points
	backupID1, _, err := CreateBackup(sourceDir, mountPoint1)
	if err != nil {
		tu.Failf(t, "CreateBackup failed: %v", err)
	}

	time.Sleep(10 * time.Millisecond) // Ensure different timestamps

	_, _, err = CreateBackup(sourceDir, mountPoint2)
	if err != nil {
		tu.Failf(t, "CreateBackup failed: %v", err)
	}

	// List all backups
	backups, err := ListBackups("")
	if err != nil {
		tu.Failf(t, "ListBackups failed: %v", err)
	}

	if len(backups) < 2 {
		tu.Failf(t, "Expected at least 2 backups, got %d", len(backups))
	}

	// Verify backups are sorted (newest first)
	if len(backups) >= 2 {
		if backups[0].Timestamp.Before(backups[1].Timestamp) {
			tu.Failf(t, "Backups should be sorted newest first")
		}
	}

	// List backups filtered by mount point
	mount1Backups, err := ListBackups(mountPoint1)
	if err != nil {
		tu.Failf(t, "ListBackups failed: %v", err)
	}

	// Verify we found at least one backup for mount point 1
	if len(mount1Backups) == 0 {
		tu.Failf(t, "Should find at least one backup for mount point 1")
	}

	// Verify backup ID1 is in the filtered list
	found := false
	for _, backup := range mount1Backups {
		if backup.ID == backupID1 {
			found = true
			break
		}
	}
	if !found {
		tu.Failf(t, "Backup ID1 (%s) should be found when filtering by mount point 1. Found backups: %v", backupID1, mount1Backups)
	}
}

func TestReadBackupInfo(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "shadowfs-test-")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	backupBaseDir := filepath.Join(tempDir, "backups")
	oldEnv := os.Getenv("SHADOWFS_BACKUP_DIR")
	defer os.Setenv("SHADOWFS_BACKUP_DIR", oldEnv)
	os.Setenv("SHADOWFS_BACKUP_DIR", backupBaseDir)

	sourceDir := filepath.Join(tempDir, "source")
	mountPoint := filepath.Join(tempDir, "mount")

	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		tu.Failf(t, "Failed to create source dir: %v", err)
	}

	backupID, _, err := CreateBackup(sourceDir, mountPoint)
	if err != nil {
		tu.Failf(t, "CreateBackup failed: %v", err)
	}

	// Read backup info
	info, err := ReadBackupInfo(backupID)
	if err != nil {
		tu.Failf(t, "ReadBackupInfo failed: %v", err)
	}

	if info.SourcePath != sourceDir {
		tu.Failf(t, "Expected source path '%s', got '%s'", sourceDir, info.SourcePath)
	}

	if info.MountID == "" {
		tu.Failf(t, "Mount ID should not be empty")
	}

	// Test reading non-existent backup
	_, err = ReadBackupInfo("non-existent-backup-id")
	if err == nil {
		tu.Failf(t, "ReadBackupInfo should fail for non-existent backup")
	}
}

func TestDeleteBackup(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "shadowfs-test-")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	backupBaseDir := filepath.Join(tempDir, "backups")
	oldEnv := os.Getenv("SHADOWFS_BACKUP_DIR")
	defer os.Setenv("SHADOWFS_BACKUP_DIR", oldEnv)
	os.Setenv("SHADOWFS_BACKUP_DIR", backupBaseDir)

	sourceDir := filepath.Join(tempDir, "source")
	mountPoint := filepath.Join(tempDir, "mount")

	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		tu.Failf(t, "Failed to create source dir: %v", err)
	}

	backupID, backupDir, err := CreateBackup(sourceDir, mountPoint)
	if err != nil {
		tu.Failf(t, "CreateBackup failed: %v", err)
	}

	// Verify backup exists
	if _, err := os.Stat(backupDir); err != nil {
		tu.Failf(t, "Backup directory should exist: %v", err)
	}

	// Delete backup
	if err := DeleteBackup(backupID); err != nil {
		tu.Failf(t, "DeleteBackup failed: %v", err)
	}

	// Verify backup is deleted
	if _, err := os.Stat(backupDir); err == nil {
		tu.Failf(t, "Backup directory should be deleted")
	}

	// Test deleting non-existent backup
	err = DeleteBackup("non-existent-backup-id")
	if err == nil {
		tu.Failf(t, "DeleteBackup should fail for non-existent backup")
	}
}

func TestCleanupOldBackups(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "shadowfs-test-")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	backupBaseDir := filepath.Join(tempDir, "backups")
	oldEnv := os.Getenv("SHADOWFS_BACKUP_DIR")
	defer os.Setenv("SHADOWFS_BACKUP_DIR", oldEnv)
	os.Setenv("SHADOWFS_BACKUP_DIR", backupBaseDir)

	sourceDir := filepath.Join(tempDir, "source")
	mountPoint := filepath.Join(tempDir, "mount")

	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		tu.Failf(t, "Failed to create source dir: %v", err)
	}

	// Create a backup
	backupID, _, err := CreateBackup(sourceDir, mountPoint)
	if err != nil {
		tu.Failf(t, "CreateBackup failed: %v", err)
	}

	// Cleanup backups older than 0 days (should delete all)
	deletedCount, err := CleanupOldBackups(0, "")
	if err != nil {
		tu.Failf(t, "CleanupOldBackups failed: %v", err)
	}

	if deletedCount == 0 {
		tu.Failf(t, "At least one backup should be deleted")
	}

	// Verify backup is deleted
	_, err = ReadBackupInfo(backupID)
	if err == nil {
		tu.Failf(t, "Backup should be deleted")
	}
}

func TestBackupListItem(t *testing.T) {
	item := BackupListItem{
		ID:         "test-id",
		Timestamp:  time.Now(),
		SourcePath: "/test/source",
		Size:       1024,
		FileCount:  5,
	}

	if item.ID != "test-id" {
		tu.Failf(t, "Expected ID 'test-id', got '%s'", item.ID)
	}

	if item.Size != 1024 {
		tu.Failf(t, "Expected size 1024, got %d", item.Size)
	}

	if item.FileCount != 5 {
		tu.Failf(t, "Expected file count 5, got %d", item.FileCount)
	}
}
