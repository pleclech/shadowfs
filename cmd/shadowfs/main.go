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
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	shadowfs "github.com/pleclech/shadowfs/fs"
	"github.com/pleclech/shadowfs/fs/rootinit"
)

func unmount(mountPoint string) error {
	// Try standard unmount first
	cmd := exec.Command("umount", mountPoint)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return nil
	}

	// If standard unmount fails, try lazy unmount
	log.Printf("Standard unmount failed, trying lazy unmount...")
	cmd = exec.Command("umount", "-l", mountPoint)
	stderr.Reset()
	cmd.Stderr = &stderr
	lazyErr := cmd.Run()
	if lazyErr == nil {
		return nil
	}

	// If both fail, return the original error
	return fmt.Errorf("unmount failed: %v\nLazy unmount also failed: %v", stderr.String(), lazyErr)
}

func initLogger() {
	logLevel := shadowfs.LogLevelFromString(os.Getenv("SHADOWFS_LOG_LEVEL"))
	shadowfs.InitLogger(logLevel)
}

func main() {
	// get name of the binary
	binaryName := filepath.Base(os.Args[0])

	// Check for --help flag before subcommand parsing
	if len(os.Args) > 1 && (os.Args[1] == "--help" || os.Args[1] == "-h") {
		printMainUsage(binaryName)
		return
	}

	// Check for --version flag before subcommand parsing
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		PrintVersion()
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
		case "stop":
			runStopCommand(os.Args[2:])
			return
		case "list":
			runListCommand(os.Args[2:])
			return
		case "info":
			runInfoCommand(os.Args[2:])
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
	debugFuse := flag.Bool("debug-fuse", false, "print FUSE debug data")
	autoGit := flag.Bool("auto-git", false, "enable automatic Git versioning")
	gitIdleTimeout := flag.Duration("git-idle-timeout", 30*time.Second, "idle timeout for auto-commits")
	gitSafetyWindow := flag.Duration("git-safety-window", 5*time.Second, "safety window delay after last write before committing")
	cacheDir := flag.String("cache-dir", "", "custom cache directory (default: ~/.shadowfs, or $SHADOWFS_CACHE_DIR)")
	daemon := flag.Bool("daemon", false, "run as daemon in background")
	allowOther := flag.Bool("allow-other", false, "allow other users to access the mount (required for VS Code compatibility)")
	flag.Parse()

	if len(flag.Args()) < 2 {
		log.Fatalf("Usage:\n %s MOUNTPOINT SRCDIR", binaryName)
	}

	// Daemonize if requested (must be done before mounting)
	if *daemon {
		mountPoint := flag.Arg(0)
		sourceDir := flag.Arg(1)

		// Check if mount point is already in use
		if err := checkMountPointAvailable(mountPoint); err != nil {
			log.Fatalf("Cannot start daemon: %v", err)
		}

		if err := daemonize(mountPoint, sourceDir); err != nil {
			log.Fatalf("Failed to daemonize: %v", err)
		}
		// Parent process exits here, child continues
	}

	// Resolve cache directory: flag > environment variable > default
	cacheDirPath := *cacheDir
	if cacheDirPath == "" {
		cacheDirPath = os.Getenv("SHADOWFS_CACHE_DIR")
	}

	// Check if running as daemon child (set by daemonize)
	isDaemonChild := os.Getenv("SHADOWFS_DAEMON") == "1"

	// Initialize logger: Debug level if debug flag is set, Info level in daemon mode, otherwise default
	logLevel := shadowfs.LogLevelFromString(os.Getenv("SHADOWFS_LOG_LEVEL"))

	if *debug {
		logLevel = shadowfs.LogLevelDebug
	}

	if os.Getenv("SHADOWFS_DEBUG_FUSE") == "1" {
		*debugFuse = true
	}

	shadowfs.InitLogger(logLevel)

	mountPoint := flag.Arg(0)

	// Check if mount point is already mounted (for non-daemon mode)
	if !*daemon {
		isMounted, err := rootinit.IsMountPointActive(mountPoint)
		if err == nil && isMounted {
			log.Fatalf("Mount point %s is already mounted", mountPoint)
		}
	}

	rootNode, err := shadowfs.NewShadowRoot(mountPoint, flag.Arg(1), cacheDirPath)
	if err != nil {
		log.Fatalf("NewShadowRoot error:\n%v", err)
	}
	root := rootNode.(*shadowfs.ShadowNode)

	// Battle-tested mount options from rclone, restic, goofys, sioyek (2025)
	// These timeouts prevent stale cached dentries without deadlock risk
	entryTimeout := 500 * time.Millisecond    // Positive dentries
	attrTimeout := 500 * time.Millisecond     // Attributes
	negativeTimeout := 200 * time.Millisecond // Negative dentries (crucial for deleted files)
	opts := &fs.Options{
		EntryTimeout:    &entryTimeout,
		AttrTimeout:     &attrTimeout,
		NegativeTimeout: &negativeTimeout,
	}
	// Take full control of kernel caching to prevent permission issues
	opts.MountOptions.ExplicitDataCacheControl = true

	// Add default_permissions for better permission handling and error messages
	opts.MountOptions.Options = append(opts.MountOptions.Options, "default_permissions")

	// Allow other users to access the mount (required for VS Code and other applications)
	// Note: This requires user_allow_other to be enabled in /etc/fuse.conf for non-root users
	if *allowOther {
		opts.MountOptions.AllowOther = true
	}

	opts.Debug = *debugFuse

	server, err := fs.Mount(root.GetMountPoint(), root, opts)
	if err != nil {
		log.Fatalf("Mount fail: %v\n", err)
	}

	// Set server reference in root node for safe entry invalidation (battle-tested approach)
	// This allows all nodes to safely call server.EntryNotify/ NegativeEntryNotify
	// without deadlock risk, even from inside Lookup, Unlink, Rename, etc.
	root.SetServer(server)

	// Start IPC server for CLI communication (works in both daemon and non-daemon mode)
	if err := root.StartIPCServer(); err != nil {
		log.Printf("Warning: Failed to start IPC server: %v (continuing)", err)
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

	// Write PID file if running as daemon
	if *daemon || isDaemonChild {
		mountID := root.GetMountID()
		// Normalize source directory to absolute path for consistency
		sourceDir := flag.Arg(1)
		normalizedSourceDir, err := filepath.Abs(sourceDir)
		if err != nil {
			normalizedSourceDir = sourceDir // Fallback to original if normalization fails
		}
		if err := writePIDFile(mountID, root.GetMountPoint(), normalizedSourceDir); err != nil {
			log.Printf("Warning: Failed to write PID file: %v", err)
		} else {
			log.Printf("Daemon PID file written: %s", mountID)
		}
	}

	c := make(chan os.Signal, 2) // Buffer for multiple signals
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	shutdownComplete := make(chan bool, 1)

	cleanUpDone := false

	cleanup := func() {
		if cleanUpDone {
			return
		}
		cleanUpDone = true

		// Stop IPC server and remove socket file
		if err := root.StopIPCServer(); err != nil {
			log.Printf("Warning: Failed to stop IPC server: %v", err)
		}

		// Cleanup Git resources and commit all pending changes
		// CleanupGitManager calls CommitAllPending() which commits all uncommitted changes
		root.CleanupGitManager()

		log.Printf("Git cleanup completed, unmounting filesystem...")

		isDaemonChild := os.Getenv("SHADOWFS_DAEMON") == "1"
		if isDaemonChild {
			mountID := root.GetMountID()
			if err := removePIDFile(mountID); err != nil {
				log.Printf("Warning: Failed to remove PID file: %v", err)
			}
		}

		log.Printf("Unmounting filesystem...")
		err := server.Unmount()
		if err != nil {
			err = unmount(root.GetMountPoint())
			if err != nil {
				log.Printf("Warning: Unmount failed: %v", err)
			}
		}
	}

	// on panic try to cleanup
	defer func() {
		if !cleanUpDone {
			if r := recover(); r != nil {
				log.Printf("Panic: %v, trying to cleanup...", r)
				cleanup()
			}
		}
	}()

	go func() {
		sig := <-c
		log.Printf("Received %v signal, cleaning up...", sig)

		go func() {
			time.Sleep(200 * time.Millisecond)
			// Handle second signal for forceful shutdown
			sig := <-c
			log.Printf("Received second %v signal, forcing exit...", sig)
			os.Exit(1)
		}()

		cleanup()

		// Unmount the filesystem - this will make server.Wait() return
		log.Printf("Shutdown complete")
		shutdownComplete <- true
	}()

	// Wait for server to stop (should run indefinitely until interrupted)
	server.Wait()
	log.Printf("Server stopped, exiting...")
}

func printWithBinaryName(binaryName string, msg string) {
	fmt.Print(strings.ReplaceAll(msg, "{binaryname}", binaryName))
}

func printMainUsage(binaryName string) {
	cmd := `{binaryname} - Shadow filesystem with automatic versioning

Usage:
  {binaryname} [flags] MOUNTPOINT SRCDIR    Mount shadow filesystem
  {binaryname} <command> [options]          Run a command

Commands:
  version     Manage version history (list, diff, restore, log)
  checkpoint  Create a manual checkpoint of current changes
  sync        Sync cache to source directory with backup/rollback
  backups     Manage backups (list, info, delete, cleanup)
  list        List all active mounts
  info        Show detailed statistics for a mount point
  stop        Stop a running daemon process
  help        Show help for a command

Flags for mount:
  -v, --version           Print version information and exit
  --debug                 Print ShadowFS debug data (filesystem-specific logging)
  --debug-fuse            Print FUSE debug data (low-level FUSE operation logging)
  --auto-git              Enable automatic Git versioning
  --git-idle-timeout      Idle timeout for auto-commits (default: 30s)
  --git-safety-window     Safety window delay after last write before committing (default: 5s)
  --cache-dir             Custom cache directory (default: ~/.shadowfs, or $SHADOWFS_CACHE_DIR)
  --daemon                Run as daemon in background
  --allow-other           Allow other users to access the mount (required for VS Code compatibility)

Examples:
  {binaryname} /mnt/shadow /home/user/source
  {binaryname} --daemon /mnt/shadow /home/user/source
  {binaryname} --allow-other /mnt/shadow /home/user/source
  {binaryname} list
  {binaryname} info --mount-point /mnt/shadow
  {binaryname} stop --mount-point /mnt/shadow
  {binaryname} version list --mount-point /mnt/shadow
  {binaryname} checkpoint --mount-point /mnt/shadow
  {binaryname} sync --mount-point /mnt/shadow --dry-run
  {binaryname} backups list

Use "{binaryname} help <command>" for command-specific help.
`

	printWithBinaryName(binaryName, cmd)
}

func printSubcommandHelp(command string, binaryName string) {
	switch command {
	case "version":
		msg := `Usage: {binaryname} version <command> [options]

Commands:
  list      List version history (commits)
  diff      Show diff between versions
  restore   Restore files/directories/workspace to a previous version
  log       Enhanced version history with more options

Examples:
  {binaryname} version list --mount-point /mnt/shadow
  {binaryname} version list --mount-point /mnt/shadow --path "*.txt"
  {binaryname} version list --mount-point /mnt/shadow --path "*.txt,*.go"
  {binaryname} version diff --mount-point /mnt/shadow HEAD~1 HEAD --path "src/**/*.go"
  {binaryname} version log --mount-point /mnt/shadow --path "*.txt"

Use "{binaryname} version <command> --help" for command-specific help.
`
		printWithBinaryName(binaryName, msg)
	case "checkpoint":
		msg := `Usage: {binaryname} checkpoint [options]

Options:
  --mount-point string   Mount point path (required)
  --file string          Create checkpoint for specific file

Examples:
  {binaryname} checkpoint --mount-point /mnt/shadow
  {binaryname} checkpoint --mount-point /mnt/shadow --file path/to/file.txt
`
		printWithBinaryName(binaryName, msg)
	case "sync":
		msg := `Usage: {binaryname} sync [options]

Options:
  --mount-point string   Mount point path (required)
  --dry-run              Show what would be synced without actually syncing
  --backup               Create backup before syncing (default: true)
  --no-backup            Skip backup (dangerous)
  --rollback             Rollback last sync operation
  --backup-id string     Backup ID for rollback
  --file string          Sync only specific file
  --dir string           Sync only specific directory tree
  --force                Force sync even if conflicts detected

Examples:
  {binaryname} sync --mount-point /mnt/shadow --dry-run
  {binaryname} sync --mount-point /mnt/shadow --backup
  {binaryname} sync --mount-point /mnt/shadow --rollback --backup-id <id>
`
		printWithBinaryName(binaryName, msg)
	case "backups":
		msg := `Usage: {binaryname} backups <command> [options]

Commands:
  list      List all backups
  info      Show backup details
  delete    Delete a backup
  cleanup   Cleanup old backups

Use "{binaryname} backups <command> --help" for command-specific help.
`
		printWithBinaryName(binaryName, msg)
	case "list":
		msg := `Usage: {binaryname} list

Lists all active mounts (both foreground and daemon processes).

The output shows:
  - Mount Point: The mount point path
  - Source Directory: The source directory being shadowed
  - Status: active (foreground), daemon (background), or stale (process not running)
  - PID: Process ID (for daemon processes)
  - Started: When the mount was started

Examples:
  {binaryname} list
`
		printWithBinaryName(binaryName, msg)
	case "info":
		msg := `Usage: {binaryname} info [options]

Shows detailed statistics for a specific mount point.

Options:
  --mount-point string   Mount point path (required)

The output includes:
  - Mount information (mount point, source directory, cache directory, status)
  - Cache statistics (size, file count, directory count)
  - Git status (if enabled): last commit info, uncommitted changes

Examples:
  {binaryname} info --mount-point /mnt/shadow
`
		printWithBinaryName(binaryName, msg)
	case "stop":
		msg := `Usage: {binaryname} stop [options]

Stops a running daemon process for a mount point.

Options:
  --mount-point string   Mount point path (required)

Examples:
  {binaryname} stop --mount-point /mnt/shadow
`
		printWithBinaryName(binaryName, msg)
	default:
		fmt.Printf("Unknown command: %s\n\n", command)
		printMainUsage(binaryName)
	}
}
