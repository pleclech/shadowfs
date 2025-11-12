package fs

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/pleclech/shadowfs/fs/xattr"
)

// SyncOptions contains options for sync operation
type SyncOptions struct {
	MountPoint string
	CachePath  string
	SourcePath string
	Backup     bool
	DryRun     bool
	Force      bool
	FilePath   string // Sync single file only
	DirPath    string // Sync directory tree only
}

// SyncResult contains result of sync operation
type SyncResult struct {
	Success           bool
	BackupID          string
	FilesSynced       int
	FilesFailed       int
	FilesSkipped      int
	Errors            []SyncError
	RollbackAvailable bool
}

// SyncError represents an error during sync
type SyncError struct {
	File      string
	Operation string
	Error     error
}

// BackupInfo contains metadata about a backup
type BackupInfo struct {
	Timestamp   time.Time `json:"timestamp"`
	MountID     string    `json:"mount_id"`
	MountPoint  string    `json:"mount_point"` // Store mount point for filtering
	SourcePath  string    `json:"source_path"`
	FilesChanged []string  `json:"files_changed"`
}

// SyncCacheToSource syncs cache to source with full safety mechanisms
func SyncCacheToSource(options SyncOptions) (*SyncResult, error) {
	result := &SyncResult{
		Errors: make([]SyncError, 0),
	}

	// Validate inputs
	if options.SourcePath == "" {
		return nil, fmt.Errorf("source path is required")
	}
	if options.CachePath == "" {
		return nil, fmt.Errorf("cache path is required")
	}

	// Ensure source directory exists
	if err := os.MkdirAll(options.SourcePath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create source directory: %w", err)
	}

	// Create backup if enabled
	var backupID string
	if options.Backup {
		var err error
		backupID, _, err = CreateBackup(options.SourcePath, options.MountPoint)
		if err != nil {
			return nil, fmt.Errorf("failed to create backup: %w", err)
		}
		result.BackupID = backupID
		result.RollbackAvailable = true
	}

	// Get list of files to sync
	filesToSync, deletedFiles, err := getFilesToSync(options.CachePath, options.SourcePath, options.FilePath, options.DirPath)
	if err != nil {
		return nil, fmt.Errorf("failed to discover files: %w", err)
	}

	if options.DryRun {
		// Just report what would be synced
		fmt.Printf("Would sync %d files:\n", len(filesToSync))
		for _, f := range filesToSync {
			fmt.Printf("  + %s\n", f)
		}
		fmt.Printf("Would delete %d files:\n", len(deletedFiles))
		for _, f := range deletedFiles {
			fmt.Printf("  - %s\n", f)
		}
		result.Success = true
		return result, nil
	}

	// Perform sync operations
	transactionLog := make([]string, 0) // Track operations for rollback

	// Sync files from cache to source
	for _, relPath := range filesToSync {
		cacheFile := filepath.Join(options.CachePath, relPath)
		sourceFile := filepath.Join(options.SourcePath, relPath)

		// Check for conflicts (source file modified while mounted)
		if !options.Force {
			if conflict, err := detectConflict(cacheFile, sourceFile); err == nil && conflict {
				result.Errors = append(result.Errors, SyncError{
					File:      relPath,
					Operation: "sync",
					Error:     fmt.Errorf("conflict detected: source file was modified"),
				})
				result.FilesSkipped++
				continue
			}
		}

		// Sync file atomically
		if err := syncFileAtomically(cacheFile, sourceFile); err != nil {
			result.Errors = append(result.Errors, SyncError{
				File:      relPath,
				Operation: "sync",
				Error:     err,
			})
			result.FilesFailed++
			continue
		}

		transactionLog = append(transactionLog, relPath)
		result.FilesSynced++
	}

	// Delete files marked as deleted in cache
	for _, relPath := range deletedFiles {
		sourceFile := filepath.Join(options.SourcePath, relPath)

		// Check if file exists in source
		if _, err := os.Stat(sourceFile); os.IsNotExist(err) {
			// Already deleted, skip
			continue
		}

		// Delete file atomically
		if err := deleteFileAtomically(sourceFile); err != nil {
			result.Errors = append(result.Errors, SyncError{
				File:      relPath,
				Operation: "delete",
				Error:     err,
			})
			result.FilesFailed++
			continue
		}

		transactionLog = append(transactionLog, "-"+relPath) // Mark as deletion
		result.FilesSynced++
	}

	// Update backup info with transaction log if backup was created
	if backupID != "" && len(transactionLog) > 0 {
		updateBackupInfo(backupID, transactionLog)
	}

	result.Success = len(result.Errors) == 0
	return result, nil
}

// CreateBackup creates a timestamped backup of the source directory
func CreateBackup(sourcePath, mountPoint string) (string, string, error) {
	// Compute mount ID
	mountID := fmt.Sprintf("%x", sha256.Sum256([]byte(mountPoint+sourcePath)))
	
	// Create backup directory name
	timestamp := time.Now().Format("20060102-150405")
	backupID := fmt.Sprintf("%s-%s", timestamp, mountID[:12])
	
	// Determine backup base directory
	backupBaseDir := os.Getenv("SHADOWFS_BACKUP_DIR")
	if backupBaseDir == "" {
		if homeDir, err := os.UserHomeDir(); err == nil {
			backupBaseDir = filepath.Join(homeDir, ".shadowfs", "backups")
		} else {
			return "", "", fmt.Errorf("cannot determine backup directory: %w", err)
		}
	}
	
	backupDir := filepath.Join(backupBaseDir, backupID)
	
	// Create backup directory
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return "", "", fmt.Errorf("failed to create backup directory: %w", err)
	}
	
	// Copy source directory to backup
	if err := copyDirectory(sourcePath, backupDir); err != nil {
		return "", "", fmt.Errorf("failed to copy source to backup: %w", err)
	}
	
	// Create backup info file
	info := BackupInfo{
		Timestamp:  time.Now(),
		MountID:     mountID,
		MountPoint:  mountPoint, // Store mount point for filtering
		SourcePath:  sourcePath,
		FilesChanged: []string{}, // Will be populated during sync
	}
	
	infoData, err := json.Marshal(info)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal backup info: %w", err)
	}
	
	infoFile := filepath.Join(backupDir, ".backup-info")
	if err := os.WriteFile(infoFile, infoData, 0644); err != nil {
		return "", "", fmt.Errorf("failed to write backup info: %w", err)
	}
	
	return backupID, backupDir, nil
}

// RollbackSync restores source from backup
func RollbackSync(backupID, sourcePath string) error {
	// Find backup directory
	backupBaseDir := os.Getenv("SHADOWFS_BACKUP_DIR")
	if backupBaseDir == "" {
		if homeDir, err := os.UserHomeDir(); err == nil {
			backupBaseDir = filepath.Join(homeDir, ".shadowfs", "backups")
		} else {
			return fmt.Errorf("cannot determine backup directory: %w", err)
		}
	}
	
	backupDir := filepath.Join(backupBaseDir, backupID)
	if _, err := os.Stat(backupDir); err != nil {
		return fmt.Errorf("backup not found: %w", err)
	}
	
	// Read backup info
	infoFile := filepath.Join(backupDir, ".backup-info")
	infoData, err := os.ReadFile(infoFile)
	if err != nil {
		return fmt.Errorf("failed to read backup info: %w", err)
	}
	
	var info BackupInfo
	if err := json.Unmarshal(infoData, &info); err != nil {
		return fmt.Errorf("failed to parse backup info: %w", err)
	}
	
	// Restore from backup using transaction log to restore only changed files
	if len(info.FilesChanged) == 0 {
		// No transaction log available - fallback to full restore
		if err := copyDirectory(backupDir, sourcePath); err != nil {
			return fmt.Errorf("failed to restore from backup: %w", err)
		}
		return nil
	}

	// Restore only files that were changed (more efficient and safer)
	for _, fileEntry := range info.FilesChanged {
		relPath := fileEntry
		isDeleted := false
		
		// Check if this is a deletion entry (prefixed with "-")
		if strings.HasPrefix(relPath, "-") {
			isDeleted = true
			relPath = relPath[1:] // Remove "-" prefix
		}
		
		sourceFile := filepath.Join(sourcePath, relPath)
		backupFile := filepath.Join(backupDir, relPath)
		
		if isDeleted {
			// File was deleted during sync - restore it from backup
			if err := os.MkdirAll(filepath.Dir(sourceFile), 0755); err != nil {
				return fmt.Errorf("failed to create directory for %s: %w", relPath, err)
			}
			if err := copyFileWithMetadata(backupFile, sourceFile, 0644); err != nil {
				// If backup file doesn't exist, it means the file was created during sync
				// In that case, we should delete it from source
				if os.IsNotExist(err) {
					if err := os.Remove(sourceFile); err != nil && !os.IsNotExist(err) {
						return fmt.Errorf("failed to remove file %s: %w", relPath, err)
					}
				} else {
					return fmt.Errorf("failed to restore file %s: %w", relPath, err)
				}
			}
		} else {
			// File was added or modified during sync - restore from backup
			if err := os.MkdirAll(filepath.Dir(sourceFile), 0755); err != nil {
				return fmt.Errorf("failed to create directory for %s: %w", relPath, err)
			}
			
			// Get file mode from backup file
			backupInfo, err := os.Stat(backupFile)
			if err != nil {
				if os.IsNotExist(err) {
					// File was created during sync - remove it from source
					if err := os.Remove(sourceFile); err != nil && !os.IsNotExist(err) {
						return fmt.Errorf("failed to remove file %s: %w", relPath, err)
					}
					continue
				}
				return fmt.Errorf("failed to stat backup file %s: %w", relPath, err)
			}
			
			if err := copyFileWithMetadata(backupFile, sourceFile, backupInfo.Mode()); err != nil {
				return fmt.Errorf("failed to restore file %s: %w", relPath, err)
			}
		}
	}
	
	return nil
}

// Helper functions

// FileSyncInfo contains information about a file to sync
type FileSyncInfo struct {
	RelPath     string
	IsDeleted   bool
	CachePath   string
	SourcePath  string
}

func getFilesToSync(cachePath, sourcePath, filePath, dirPath string) ([]string, []string, error) {
	var filesToSync []string
	var deletedFiles []string

	// Determine base path for walking
	walkBase := cachePath
	if dirPath != "" {
		walkBase = filepath.Join(cachePath, dirPath)
	}

	// Walk cache directory
	err := filepath.Walk(walkBase, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip hidden/system directories
		if info.IsDir() {
			base := filepath.Base(path)
			if strings.HasPrefix(base, ".") && base != "." && base != ".." {
				// Skip .gitofs, .root, etc.
				if base == ".gitofs" || base == ".root" || base == ".backup-info" {
					return filepath.SkipDir
				}
			}
			return nil
		}

		// Calculate relative path from cache root
		relPath, err := filepath.Rel(cachePath, path)
		if err != nil {
			return err
		}

		// Skip if filtering by specific file
		if filePath != "" && relPath != filePath {
			return nil
		}

		// Check if file is marked as deleted
		var attr xattr.XAttr
		exists, errno := xattr.Get(path, &attr)
		if errno != 0 && errno != syscall.ENODATA {
			// Error reading xattr, but continue
			exists = false
		}

		if exists && xattr.IsPathDeleted(attr) {
			// File is marked as deleted in cache
			deletedFiles = append(deletedFiles, relPath)
			return nil
		}

		// File exists in cache and is not deleted - add to sync list
		filesToSync = append(filesToSync, relPath)
		return nil
	})

	if err != nil {
		return nil, nil, err
	}

	return filesToSync, deletedFiles, nil
}

func copyDirectory(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		
		// Skip backup info file
		if info.Name() == ".backup-info" {
			return nil
		}
		
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		
		dstPath := filepath.Join(dst, relPath)
		
		if info.IsDir() {
			return os.MkdirAll(dstPath, info.Mode())
		}
		
		return copyFile(path, dstPath, info.Mode())
	})
}

// syncFileAtomically syncs a file from cache to source using atomic operations
func syncFileAtomically(cacheFile, sourceFile string) error {
	// Get source file info for metadata
	var cacheStat syscall.Stat_t
	if err := syscall.Lstat(cacheFile, &cacheStat); err != nil {
		return fmt.Errorf("failed to stat cache file: %w", err)
	}

	// Create parent directory if needed
	parentDir := filepath.Dir(sourceFile)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return fmt.Errorf("failed to create parent directory: %w", err)
	}

	// Copy to temporary file first (atomic operation)
	tmpFile := sourceFile + ".shadowfs.tmp"
	defer os.Remove(tmpFile) // Cleanup temp file on error

	// Copy file content and metadata
	if err := copyFileWithMetadata(cacheFile, tmpFile, os.FileMode(cacheStat.Mode)); err != nil {
		return fmt.Errorf("failed to copy file: %w", err)
	}

	// Verify copy integrity (compare sizes)
	var tmpStat syscall.Stat_t
	if err := syscall.Lstat(tmpFile, &tmpStat); err != nil {
		return fmt.Errorf("failed to verify copy: %w", err)
	}
	if tmpStat.Size != cacheStat.Size {
		return fmt.Errorf("copy verification failed: size mismatch")
	}

	// Atomic rename (replaces existing file atomically)
	if err := syscall.Rename(tmpFile, sourceFile); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	// Copy ownership and timestamps
	if err := syscall.Chown(sourceFile, int(cacheStat.Uid), int(cacheStat.Gid)); err != nil {
		// Ignore chown errors (may not have permission)
	}

	var times [2]syscall.Timespec
	times[0] = syscall.Timespec{Sec: cacheStat.Atim.Sec, Nsec: cacheStat.Atim.Nsec}
	times[1] = syscall.Timespec{Sec: cacheStat.Mtim.Sec, Nsec: cacheStat.Mtim.Nsec}
	if err := syscall.UtimesNano(sourceFile, times[:]); err != nil {
		// Ignore timestamp errors (may not have permission)
	}

	return nil
}

// deleteFileAtomically deletes a file atomically
func deleteFileAtomically(sourceFile string) error {
	// Use syscall.Unlink for atomic deletion
	if err := syscall.Unlink(sourceFile); err != nil {
		return fmt.Errorf("failed to delete file: %w", err)
	}
	return nil
}

// detectConflict checks if source file was modified while mounted
func detectConflict(cacheFile, sourceFile string) (bool, error) {
	// If source file doesn't exist, no conflict
	if _, err := os.Stat(sourceFile); os.IsNotExist(err) {
		return false, nil
	}

	var cacheStat, sourceStat syscall.Stat_t
	if err := syscall.Lstat(cacheFile, &cacheStat); err != nil {
		return false, err
	}
	if err := syscall.Lstat(sourceFile, &sourceStat); err != nil {
		return false, err
	}

	// Compare modification times
	// If source was modified more recently than cache, there's a conflict
	// (This is a simple heuristic - in practice, we'd need to track mount time)
	cacheMtime := cacheStat.Mtim.Sec*1e9 + int64(cacheStat.Mtim.Nsec)
	sourceMtime := sourceStat.Mtim.Sec*1e9 + int64(sourceStat.Mtim.Nsec)

	// If source is newer, potential conflict
	// Allow small differences for filesystem timestamp precision
	if sourceMtime > cacheMtime+1e9 { // 1 second tolerance
		return true, nil
	}

	return false, nil
}

// updateBackupInfo updates backup info file with transaction log
func updateBackupInfo(backupID string, transactionLog []string) {
	backupBaseDir := os.Getenv("SHADOWFS_BACKUP_DIR")
	if backupBaseDir == "" {
		if homeDir, err := os.UserHomeDir(); err == nil {
			backupBaseDir = filepath.Join(homeDir, ".shadowfs", "backups")
		} else {
			return // Can't update backup info
		}
	}

	backupDir := filepath.Join(backupBaseDir, backupID)
	infoFile := filepath.Join(backupDir, ".backup-info")

	// Read existing info
	infoData, err := os.ReadFile(infoFile)
	var info BackupInfo
	if err == nil {
		json.Unmarshal(infoData, &info)
	} else {
		info = BackupInfo{
			Timestamp: time.Now(),
		}
	}

	// Update files changed list
	info.FilesChanged = transactionLog

	// Write updated info
	infoData, err = json.Marshal(info)
	if err == nil {
		os.WriteFile(infoFile, infoData, 0644)
	}
}

func copyFile(src, dst string, mode os.FileMode) error {
	return copyFileWithMetadata(src, dst, mode)
}

func copyFileWithMetadata(src, dst string, mode os.FileMode) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()
	
	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer dstFile.Close()
	
	_, err = io.Copy(dstFile, srcFile)
	if err != nil {
		return err
	}
	
	// Copy file metadata
	if stat, err := srcFile.Stat(); err == nil {
		if sysStat, ok := stat.Sys().(*syscall.Stat_t); ok {
			if err := syscall.Chown(dst, int(sysStat.Uid), int(sysStat.Gid)); err != nil {
				// Ignore chown errors (may not have permission)
			}
		}
	}
	
	return nil
}

// getBackupBaseDir returns the base directory for backups
func getBackupBaseDir() (string, error) {
	backupBaseDir := os.Getenv("SHADOWFS_BACKUP_DIR")
	if backupBaseDir == "" {
		if homeDir, err := os.UserHomeDir(); err == nil {
			backupBaseDir = filepath.Join(homeDir, ".shadowfs", "backups")
		} else {
			return "", fmt.Errorf("cannot determine backup directory: %w", err)
		}
	}
	return backupBaseDir, nil
}

// ListBackups returns a list of all backup IDs
func ListBackups(mountPoint string) ([]BackupListItem, error) {
	backupBaseDir, err := getBackupBaseDir()
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(backupBaseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []BackupListItem{}, nil // No backups yet
		}
		return nil, fmt.Errorf("failed to read backup directory: %w", err)
	}

	var backups []BackupListItem
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		backupID := entry.Name()
		backupDir := filepath.Join(backupBaseDir, backupID)

		// Read backup info
		info, err := ReadBackupInfo(backupID)
		if err != nil {
			// Skip invalid backups
			continue
		}

		// Filter by mount point if specified
		if mountPoint != "" && info.MountPoint != mountPoint {
			continue
		}

		// Get backup size
		size, err := getBackupSize(backupDir)
		if err != nil {
			size = 0
		}

		backups = append(backups, BackupListItem{
			ID:         backupID,
			Timestamp:  info.Timestamp,
			SourcePath: info.SourcePath,
			Size:       size,
			FileCount:  len(info.FilesChanged),
		})
	}

	// Sort by timestamp (newest first)
	for i := 0; i < len(backups)-1; i++ {
		for j := i + 1; j < len(backups); j++ {
			if backups[i].Timestamp.Before(backups[j].Timestamp) {
				backups[i], backups[j] = backups[j], backups[i]
			}
		}
	}

	return backups, nil
}

// BackupListItem represents a backup in the list
type BackupListItem struct {
	ID         string
	Timestamp  time.Time
	SourcePath string
	Size       int64
	FileCount  int
}

// ReadBackupInfo reads backup info for a given backup ID
func ReadBackupInfo(backupID string) (*BackupInfo, error) {
	backupBaseDir, err := getBackupBaseDir()
	if err != nil {
		return nil, err
	}

	backupDir := filepath.Join(backupBaseDir, backupID)
	infoFile := filepath.Join(backupDir, ".backup-info")

	infoData, err := os.ReadFile(infoFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read backup info: %w", err)
	}

	var info BackupInfo
	if err := json.Unmarshal(infoData, &info); err != nil {
		return nil, fmt.Errorf("failed to parse backup info: %w", err)
	}

	return &info, nil
}

// DeleteBackup deletes a backup by ID
func DeleteBackup(backupID string) error {
	backupBaseDir, err := getBackupBaseDir()
	if err != nil {
		return err
	}

	backupDir := filepath.Join(backupBaseDir, backupID)

	// Verify backup exists
	if _, err := os.Stat(backupDir); err != nil {
		return fmt.Errorf("backup not found: %w", err)
	}

	// Delete entire backup directory
	if err := os.RemoveAll(backupDir); err != nil {
		return fmt.Errorf("failed to delete backup: %w", err)
	}

	return nil
}

// CleanupOldBackups deletes backups older than specified days
func CleanupOldBackups(olderThanDays int, mountPoint string) (int, error) {
	backups, err := ListBackups(mountPoint)
	if err != nil {
		return 0, err
	}

	cutoffTime := time.Now().AddDate(0, 0, -olderThanDays)
	deletedCount := 0

	for _, backup := range backups {
		if backup.Timestamp.Before(cutoffTime) {
			if err := DeleteBackup(backup.ID); err != nil {
				// Log error but continue
				fmt.Printf("Warning: Failed to delete backup %s: %v\n", backup.ID, err)
				continue
			}
			deletedCount++
		}
	}

	return deletedCount, nil
}

// getBackupSize calculates the total size of a backup directory
func getBackupSize(backupDir string) (int64, error) {
	var size int64
	err := filepath.Walk(backupDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size, err
}

