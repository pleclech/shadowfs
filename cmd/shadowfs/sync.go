package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	shadowfs "github.com/pleclech/shadowfs/fs"
	"github.com/pleclech/shadowfs/fs/cache"
)

func runSyncCommand(args []string) {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	mountPoint := fs.String("mount-point", "", "Mount point path (required)")
	dryRun := fs.Bool("dry-run", false, "Show what would be synced without actually syncing")
	backup := fs.Bool("backup", true, "Create backup before syncing (default: true)")
	noBackup := fs.Bool("no-backup", false, "Skip backup (dangerous)")
	rollback := fs.Bool("rollback", false, "Rollback last sync operation")
	backupID := fs.String("backup-id", "", "Backup ID for rollback")
	filePath := fs.String("file", "", "Sync only specific file")
	dirPath := fs.String("dir", "", "Sync only specific directory tree")
	force := fs.Bool("force", false, "Force sync even if conflicts detected")
	fs.Parse(args)

	if *mountPoint == "" {
		log.Fatal("--mount-point is required")
	}

	if err := validateMountPoint(*mountPoint); err != nil {
		log.Fatalf("Invalid mount point: %v", err)
	}

	// Find cache directory
	cacheDir, err := shadowfs.FindCacheDirectory(*mountPoint)
	if err != nil {
		log.Fatalf("Failed to find cache directory: %v", err)
	}

	// Read source directory from .target file
	targetFile := filepath.Join(cacheDir, ".target")
	srcDirData, err := os.ReadFile(targetFile)
	if err != nil {
		log.Fatalf("Failed to read source directory: %v", err)
	}
	sourcePath := strings.TrimSpace(string(srcDirData))
	cachePath := cache.GetCachePath(cacheDir)

	if *rollback {
		// Rollback operation
		if *backupID == "" {
			log.Fatal("--backup-id is required for rollback")
		}
		if err := validateBackupID(*backupID); err != nil {
			log.Fatalf("Invalid backup ID: %v", err)
		}
		if err := shadowfs.RollbackSync(*backupID, sourcePath); err != nil {
			log.Fatalf("Failed to rollback: %v", err)
		}
		fmt.Printf("Successfully rolled back to backup: %s\n", *backupID)
		return
	}

	// Sync operation
	options := shadowfs.SyncOptions{
		MountPoint: *mountPoint,
		CachePath:  cachePath,
		SourcePath: sourcePath,
		Backup:     *backup && !*noBackup,
		DryRun:     *dryRun,
		Force:      *force,
		FilePath:   *filePath,
		DirPath:    *dirPath,
	}

	result, err := shadowfs.SyncCacheToSource(options)
	if err != nil {
		log.Fatalf("Sync failed: %v", err)
	}

	if result.BackupID != "" {
		fmt.Printf("Backup created: %s\n", result.BackupID)
	}
	fmt.Printf("Synced %d files\n", result.FilesSynced)
	if result.FilesFailed > 0 {
		fmt.Printf("Failed: %d files\n", result.FilesFailed)
		for _, e := range result.Errors {
			fmt.Printf("  %s: %v\n", e.File, e.Error)
		}
	}
}

func runBackupsCommand(args []string) {
	if len(args) == 0 {
		printBackupsUsage()
		os.Exit(1)
	}

	command := args[0]
	switch command {
	case "list":
		runBackupsList(args[1:])
	case "info":
		runBackupsInfo(args[1:])
	case "delete":
		runBackupsDelete(args[1:])
	case "cleanup":
		runBackupsCleanup(args[1:])
	default:
		log.Fatalf("Unknown backups command: %s\n\n%s", command, getBackupsUsage())
	}
}

func printBackupsUsage() {
	fmt.Print(getBackupsUsage())
}

func getBackupsUsage() string {
	return `Usage: shadowfs backups <command> [options]

Commands:
  list      List all backups
  info      Show backup details
  delete    Delete a backup
  cleanup   Cleanup old backups

Use "shadowfs backups <command> --help" for command-specific help.
`
}

func runBackupsList(args []string) {
	fs := flag.NewFlagSet("backups list", flag.ExitOnError)
	mountPoint := fs.String("mount-point", "", "Filter backups by mount point")
	fs.Parse(args)

	backups, err := shadowfs.ListBackups(*mountPoint)
	if err != nil {
		log.Fatalf("Failed to list backups: %v", err)
	}

	if len(backups) == 0 {
		fmt.Println("No backups found")
		return
	}

	// Print header
	fmt.Printf("%-20s %-20s %-40s %10s %8s\n", "ID", "Timestamp", "Source Path", "Size", "Files")
	fmt.Println(strings.Repeat("-", 100))

	// Print backups
	for _, backup := range backups {
		sizeStr := formatSize(backup.Size)
		timestampStr := backup.Timestamp.Format("2006-01-02 15:04:05")
		sourcePath := backup.SourcePath
		if len(sourcePath) > 38 {
			sourcePath = "..." + sourcePath[len(sourcePath)-35:]
		}
		fmt.Printf("%-20s %-20s %-40s %10s %8d\n", 
			backup.ID[:20], timestampStr, sourcePath, sizeStr, backup.FileCount)
	}
}

func runBackupsInfo(args []string) {
	fs := flag.NewFlagSet("backups info", flag.ExitOnError)
	fs.Parse(args)

	if len(fs.Args()) == 0 {
		log.Fatal("Backup ID is required")
	}
	backupID := fs.Args()[0]

	info, err := shadowfs.ReadBackupInfo(backupID)
	if err != nil {
		log.Fatalf("Failed to read backup info: %v", err)
	}

	fmt.Printf("Backup ID: %s\n", backupID)
	fmt.Printf("Timestamp: %s\n", info.Timestamp.Format("2006-01-02 15:04:05"))
	fmt.Printf("Source Path: %s\n", info.SourcePath)
	fmt.Printf("Mount ID: %s\n", info.MountID)
	fmt.Printf("Files Changed: %d\n", len(info.FilesChanged))

	if len(info.FilesChanged) > 0 {
		fmt.Println("\nChanged Files:")
		for _, file := range info.FilesChanged {
			if strings.HasPrefix(file, "-") {
				fmt.Printf("  - %s (deleted)\n", file[1:])
			} else {
				fmt.Printf("  + %s\n", file)
			}
		}
	}
}

func runBackupsDelete(args []string) {
	fs := flag.NewFlagSet("backups delete", flag.ExitOnError)
	force := fs.Bool("force", false, "Skip confirmation")
	fs.Parse(args)

	if len(fs.Args()) == 0 {
		log.Fatal("Backup ID is required")
	}
	backupID := fs.Args()[0]

	if err := validateBackupID(backupID); err != nil {
		log.Fatalf("Invalid backup ID: %v", err)
	}

	// Confirm deletion unless --force
	if !*force {
		confirmed, err := readUserConfirmation(fmt.Sprintf("Are you sure you want to delete backup %s? (yes/no): ", backupID))
		if err != nil {
			log.Fatalf("Failed to read confirmation: %v", err)
		}
		if !confirmed {
			fmt.Println("Deletion cancelled")
			return
		}
	}

	if err := shadowfs.DeleteBackup(backupID); err != nil {
		log.Fatalf("Failed to delete backup: %v", err)
	}

	fmt.Printf("Successfully deleted backup: %s\n", backupID)
}

func runBackupsCleanup(args []string) {
	fs := flag.NewFlagSet("backups cleanup", flag.ExitOnError)
	olderThan := fs.Int("older-than", 30, "Delete backups older than N days")
	mountPoint := fs.String("mount-point", "", "Only cleanup backups for this mount point")
	force := fs.Bool("force", false, "Skip confirmation")
	fs.Parse(args)

	// List backups that would be deleted
	backups, err := shadowfs.ListBackups(*mountPoint)
	if err != nil {
		log.Fatalf("Failed to list backups: %v", err)
	}

	cutoffTime := time.Now().AddDate(0, 0, -*olderThan)
	var toDelete []shadowfs.BackupListItem
	for _, backup := range backups {
		if backup.Timestamp.Before(cutoffTime) {
			toDelete = append(toDelete, backup)
		}
	}

	if len(toDelete) == 0 {
		fmt.Println("No backups found to cleanup")
		return
	}

	// Show what would be deleted
	fmt.Printf("Found %d backup(s) older than %d days:\n", len(toDelete), *olderThan)
	for _, backup := range toDelete {
		fmt.Printf("  - %s (from %s)\n", backup.ID, backup.Timestamp.Format("2006-01-02 15:04:05"))
	}

	// Confirm unless --force
	if !*force {
		confirmed, err := readUserConfirmation("\nDelete these backups? (yes/no): ")
		if err != nil {
			log.Fatalf("Failed to read confirmation: %v", err)
		}
		if !confirmed {
			fmt.Println("Cleanup cancelled")
			return
		}
	}

	// Delete backups
	deletedCount, err := shadowfs.CleanupOldBackups(*olderThan, *mountPoint)
	if err != nil {
		log.Fatalf("Failed to cleanup backups: %v", err)
	}

	fmt.Printf("Successfully deleted %d backup(s)\n", deletedCount)
}

// formatSize formats bytes into human-readable size
func formatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
