package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	shadowfs "github.com/pleclech/shadowfs/fs"
	"github.com/hanwen/go-fuse/v2/fs"
)

func unmount(mountPoint string) error {
	cmd := exec.Command("umount", mountPoint)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return err
	}

	return nil
}

func main() {
	// get name of the binary
	binaryName := filepath.Base(os.Args[0])

	// Check for --help flag before subcommand parsing
	if len(os.Args) > 1 && (os.Args[1] == "--help" || os.Args[1] == "-h") {
		printMainUsage(binaryName)
		return
	}

	// Check for subcommands BEFORE parsing flags
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			runVersionCommand(os.Args[2:])
			return
		case "checkpoint":
			runCheckpointCommand(os.Args[2:])
			return
		case "sync":
			runSyncCommand(os.Args[2:])
			return
		case "backups":
			runBackupsCommand(os.Args[2:])
			return
		case "help":
			if len(os.Args) > 2 {
				printSubcommandHelp(os.Args[2], binaryName)
			} else {
				printMainUsage(binaryName)
			}
			return
		}
	}

	// Parse flags for mount command
	debug := flag.Bool("debug", false, "print debug data")
	autoGit := flag.Bool("auto-git", false, "enable automatic Git versioning")
	gitIdleTimeout := flag.Duration("git-idle-timeout", 30*time.Second, "idle timeout for auto-commits")
	gitSafetyWindow := flag.Duration("git-safety-window", 5*time.Second, "safety window delay after last write before committing")
	cacheDir := flag.String("cache-dir", "", "custom cache directory (default: ~/.shadowfs, or $SHADOWFS_CACHE_DIR)")
	flag.Parse()
	
	if len(flag.Args()) < 2 {
		log.Fatalf("Usage:\n %s MOUNTPOINT SRCDIR", binaryName)
	}

	// Resolve cache directory: flag > environment variable > default
	cacheDirPath := *cacheDir
	if cacheDirPath == "" {
		cacheDirPath = os.Getenv("SHADOWFS_CACHE_DIR")
	}

	rootNode, err := shadowfs.NewShadowRoot(flag.Arg(0), flag.Arg(1), cacheDirPath)
	if err != nil {
		log.Fatalf("NewShadowRoot error:\n%v", err)
	}
	root := rootNode.(*shadowfs.ShadowNode)

	zeroDuration := time.Duration(0)
	opts := &fs.Options{
		// NullPermissions: true, // Leave file permissions on "000" files as-is
		EntryTimeout:    &zeroDuration, // Disable entry caching
		AttrTimeout:     &zeroDuration, // Disable attribute caching
		NegativeTimeout: &zeroDuration, // Disable negative entry caching
	}
	// Take full control of kernel caching to prevent permission issues
	opts.MountOptions.ExplicitDataCacheControl = true

	opts.Debug = *debug

	server, err := fs.Mount(root.GetMountPoint(), root, opts)
	if err != nil {
		log.Fatalf("Mount fail: %v\n", err)
	}

	// Initialize Git functionality after successful mount
	if *autoGit {
		config := shadowfs.GitConfig{
			IdleTimeout:  *gitIdleTimeout,
			SafetyWindow: *gitSafetyWindow,
			AutoCommit:   true,
		}

		if err := root.InitGitManager(config); err != nil {
			log.Printf("Git initialization failed, continuing without Git: %v", err)
		} else {
			log.Printf("Git auto-versioning enabled (idle timeout: %v, safety window: %v)", *gitIdleTimeout, *gitSafetyWindow)
		}
	}

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		log.Printf("Received shutdown signal, cleaning up...")
		
		// Cleanup Git resources and commit all pending changes
		// CleanupGitManager calls CommitAllPending() which commits all uncommitted changes
		root.CleanupGitManager()
		
		log.Printf("Git cleanup completed, unmounting filesystem...")
		
		// Unmount the filesystem - this will make server.Wait() return
		err := unmount(root.GetMountPoint())
		if err != nil {
			// Try server.Unmount() as fallback
			if err := server.Unmount(); err != nil {
				log.Printf("Warning: Unmount failed: %v", err)
			}
		} else {
			// Also call server.Unmount() to ensure server.Wait() returns
			server.Unmount()
		}
		
		log.Printf("Shutdown complete")
	}()

	server.Wait()
	log.Printf("Server stopped, exiting...")
}

func printMainUsage(binaryName string) {
	fmt.Printf(`%s - Shadow filesystem with automatic versioning

Usage:
  %s [flags] MOUNTPOINT SRCDIR    Mount shadow filesystem
  %s <command> [options]          Run a command

Commands:
  version     Manage version history (list, diff, restore, log)
  checkpoint  Create a manual checkpoint of current changes
  sync        Sync cache to source directory with backup/rollback
  backups     Manage backups (list, info, delete, cleanup)
  help        Show help for a command

Flags for mount:
  -debug                  Print debug data
  -auto-git               Enable automatic Git versioning
  -git-idle-timeout       Idle timeout for auto-commits (default: 30s)
  -git-safety-window      Safety window delay after last write before committing (default: 5s)
  -cache-dir              Custom cache directory (default: ~/.shadowfs, or $SHADOWFS_CACHE_DIR)

Examples:
  %s /mnt/shadow /home/user/source
  %s version list --mount-point /mnt/shadow
  %s checkpoint --mount-point /mnt/shadow
  %s sync --mount-point /mnt/shadow --dry-run
  %s backups list

Use "%s help <command>" for command-specific help.
`, binaryName, binaryName, binaryName, binaryName, binaryName, binaryName, binaryName, binaryName, binaryName)
}

func printSubcommandHelp(command string, binaryName string) {
	switch command {
	case "version":
		fmt.Print(`Usage: ` + binaryName + ` version <command> [options]

Commands:
  list      List version history (commits)
  diff      Show diff between versions
  restore   Restore files/directories/workspace to a previous version
  log       Enhanced version history with more options

Examples:
  ` + binaryName + ` version list --mount-point /mnt/shadow
  ` + binaryName + ` version list --mount-point /mnt/shadow --path "*.txt"
  ` + binaryName + ` version list --mount-point /mnt/shadow --path "*.txt,*.go"
  ` + binaryName + ` version diff --mount-point /mnt/shadow HEAD~1 HEAD --path "src/**/*.go"
  ` + binaryName + ` version log --mount-point /mnt/shadow --path "*.txt"

Use "` + binaryName + ` version <command> --help" for command-specific help.
`)
	case "checkpoint":
		fmt.Print(`Usage: ` + binaryName + ` checkpoint [options]

Options:
  -mount-point string   Mount point path (required)
  -file string          Create checkpoint for specific file

Examples:
  ` + binaryName + ` checkpoint --mount-point /mnt/shadow
  ` + binaryName + ` checkpoint --mount-point /mnt/shadow --file path/to/file.txt
`)
	case "sync":
		fmt.Print(`Usage: ` + binaryName + ` sync [options]

Options:
  -mount-point string   Mount point path (required)
  -dry-run             Show what would be synced without actually syncing
  -backup              Create backup before syncing (default: true)
  -no-backup           Skip backup (dangerous)
  -rollback            Rollback last sync operation
  -backup-id string    Backup ID for rollback
  -file string         Sync only specific file
  -dir string          Sync only specific directory tree
  -force               Force sync even if conflicts detected

Examples:
  ` + binaryName + ` sync --mount-point /mnt/shadow --dry-run
  ` + binaryName + ` sync --mount-point /mnt/shadow --backup
  ` + binaryName + ` sync --mount-point /mnt/shadow --rollback --backup-id <id>
`)
	case "backups":
		fmt.Print(`Usage: ` + binaryName + ` backups <command> [options]

Commands:
  list      List all backups
  info      Show backup details
  delete    Delete a backup
  cleanup   Cleanup old backups

Use "` + binaryName + ` backups <command> --help" for command-specific help.
`)
	default:
		fmt.Printf("Unknown command: %s\n\n", command)
		printMainUsage(binaryName)
	}
}
