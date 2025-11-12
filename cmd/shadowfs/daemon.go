package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/pleclech/shadowfs/fs/cache"
	"github.com/pleclech/shadowfs/fs/rootinit"
)

// DaemonInfo contains information about a running daemon
type DaemonInfo struct {
	PID        int       `json:"pid"`
	MountPoint string    `json:"mount_point"`
	SourceDir  string    `json:"source_dir"`
	StartedAt  time.Time `json:"started_at"`
}

// writePIDFile writes daemon information to a PID file
func writePIDFile(mountID, mountPoint, sourceDir string) error {
	pidFile, err := cache.GetDaemonPIDFilePath(mountID)
	if err != nil {
		return fmt.Errorf("failed to get PID file path: %w", err)
	}

	// Create daemon directory if it doesn't exist
	daemonDir := filepath.Dir(pidFile)
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		return fmt.Errorf("failed to create daemon directory: %w", err)
	}

	info := DaemonInfo{
		PID:        os.Getpid(),
		MountPoint: mountPoint,
		SourceDir:  sourceDir,
		StartedAt:  time.Now(),
	}

	data, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("failed to marshal daemon info: %w", err)
	}

	if err := os.WriteFile(pidFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write PID file: %w", err)
	}

	return nil
}

// removePIDFile removes the PID file for a mount ID
func removePIDFile(mountID string) error {
	pidFile, err := cache.GetDaemonPIDFilePath(mountID)
	if err != nil {
		return err
	}
	return os.Remove(pidFile)
}

// findPIDFileByMountPoint finds the PID file for a given mount point
func findPIDFileByMountPoint(mountPoint string) (string, *DaemonInfo, error) {
	// Normalize mount point
	normalizedMountPoint, err := rootinit.GetMountPoint(mountPoint)
	if err != nil {
		return "", nil, fmt.Errorf("invalid mount point: %w", err)
	}

	daemonDir, err := cache.GetDaemonDirPath()
	if err != nil {
		return "", nil, err
	}

	// Read all PID files and find matching mount point
	entries, err := os.ReadDir(daemonDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, fmt.Errorf("no daemon processes found")
		}
		return "", nil, fmt.Errorf("failed to read daemon directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".pid" {
			continue
		}

		pidFile := filepath.Join(daemonDir, entry.Name())
		data, err := os.ReadFile(pidFile)
		if err != nil {
			continue // Skip invalid files
		}

		var info DaemonInfo
		if err := json.Unmarshal(data, &info); err != nil {
			continue // Skip invalid JSON
		}

		// Normalize stored mount point for comparison
		storedMountPoint, err := rootinit.GetMountPoint(info.MountPoint)
		if err != nil {
			continue
		}

		if storedMountPoint == normalizedMountPoint {
			return pidFile, &info, nil
		}
	}

	return "", nil, fmt.Errorf("daemon not found for mount point: %s", mountPoint)
}

// stopDaemon stops a daemon process by sending SIGTERM
func stopDaemon(mountPoint string) error {
	pidFile, info, err := findPIDFileByMountPoint(mountPoint)
	if err != nil {
		return err
	}

	// Check if process is still running
	process, err := os.FindProcess(info.PID)
	if err != nil {
		// Process not found, remove stale PID file
		os.Remove(pidFile)
		return fmt.Errorf("process %d not found (stale PID file)", info.PID)
	}

	// Send SIGTERM
	if err := process.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("failed to send SIGTERM to process %d: %w", info.PID, err)
	}

	// Wait a bit for graceful shutdown
	time.Sleep(500 * time.Millisecond)

	// Check if process is still running
	if err := process.Signal(syscall.Signal(0)); err != nil {
		// Process terminated, remove PID file
		os.Remove(pidFile)
		return nil
	}

	// Process still running, remove PID file anyway (process will clean up on exit)
	os.Remove(pidFile)
	return nil
}

// checkMountPointAvailable checks if a mount point is already mounted or has an active daemon
func checkMountPointAvailable(mountPoint string) error {
	// Normalize mount point
	normalizedMountPoint, err := rootinit.GetMountPoint(mountPoint)
	if err != nil {
		return fmt.Errorf("invalid mount point: %w", err)
	}

	// Check if mount point is already mounted
	isMounted, err := rootinit.IsMountPointActive(normalizedMountPoint)
	if err != nil {
		return fmt.Errorf("failed to check mount status: %w", err)
	}
	if isMounted {
		return fmt.Errorf("mount point %s is already mounted", normalizedMountPoint)
	}

	// Check if there's already a daemon running for this mount point
	_, daemonInfo, err := findPIDFileByMountPoint(normalizedMountPoint)
	if err == nil {
		// Found a PID file, check if process is still running
		process, err := os.FindProcess(daemonInfo.PID)
		if err == nil {
			// Signal 0 doesn't actually send a signal, just checks if process exists
			if err := process.Signal(syscall.Signal(0)); err == nil {
				return fmt.Errorf("daemon is already running for mount point %s (PID: %d)", normalizedMountPoint, daemonInfo.PID)
			}
		}
		// Process not running but PID file exists - this is a stale PID file
		// We'll allow mounting in this case, but could clean it up
	}

	return nil
}

// daemonize forks the process and runs in background
func daemonize(mountPoint, sourceDir string) error {
	// Normalize mount point and source directory to ensure consistent mount ID
	normalizedMountPoint, err := rootinit.GetMountPoint(mountPoint)
	if err != nil {
		return fmt.Errorf("invalid mount point: %w", err)
	}
	
	// Normalize source directory to absolute path
	normalizedSourceDir, err := filepath.Abs(sourceDir)
	if err != nil {
		return fmt.Errorf("invalid source directory: %w", err)
	}
	
	// Get the current executable path
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// Create a new command with the same arguments, but remove -daemon flag
	args := make([]string, 0, len(os.Args))
	for _, arg := range os.Args[1:] {
		// Skip -daemon flag
		if arg == "-daemon" || arg == "--daemon" {
			continue
		}
		args = append(args, arg)
	}

	// Compute mount ID using normalized paths (needed for log file path)
	mountID := cache.ComputeMountID(normalizedMountPoint, normalizedSourceDir)
	
	// Get log file path and open it for writing
	logFilePath, err := cache.GetDaemonLogFilePath(mountID)
	var logFile *os.File
	if err == nil {
		// Ensure daemon directory exists
		daemonDir, err := cache.GetDaemonDirPath()
		if err == nil {
			os.MkdirAll(daemonDir, 0755)
		}
		
		// Open log file in append mode (create if doesn't exist)
		logFile, err = os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			logFile = nil // Fallback to nil if we can't create log file
		}
	}

	cmd := exec.Command(execPath, args...)
	cmd.Stdin = nil
	if logFile != nil {
		// Redirect both stdout and stderr to log file
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	} else {
		// Fallback to nil if log file couldn't be created
		cmd.Stdout = nil
		cmd.Stderr = nil
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true, // Create new session
	}

	// Set environment variable to mark this as daemon child process
	cmd.Env = append(os.Environ(), "SHADOWFS_DAEMON=1")

	if err := cmd.Start(); err != nil {
		if logFile != nil {
			logFile.Close()
		}
		return fmt.Errorf("failed to start daemon process: %w", err)
	}

	// Don't close logFile here - it needs to stay open for the daemon process
	// The daemon process will close it when it exits

	pidFilePath, err := cache.GetDaemonPIDFilePath(mountID)
	if err != nil {
		// Fallback if we can't get the path
		pidFilePath = "~/.shadowfs/daemons/<mount-id>.pid"
	}

	// Print success message before exiting parent process
	fmt.Printf("Daemon started successfully\n")
	fmt.Printf("  PID: %d\n", cmd.Process.Pid)
	fmt.Printf("  Mount Point: %s\n", normalizedMountPoint)
	fmt.Printf("  Source Directory: %s\n", normalizedSourceDir)
	fmt.Printf("  PID File: %s\n", pidFilePath)
	if logFile != nil {
		fmt.Printf("  Log File: %s\n", logFilePath)
	}
	fmt.Printf("\nUse 'shadowfs list' to see all active mounts\n")
	fmt.Printf("Use 'shadowfs stop --mount-point %s' to stop the daemon\n", normalizedMountPoint)

	// Exit parent process immediately
	os.Exit(0)
	return nil
}

// runStopCommand handles the stop subcommand
func runStopCommand(args []string) {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	mountPoint := fs.String("mount-point", "", "Mount point path (required)")
	fs.Parse(args)

	if *mountPoint == "" {
		log.Fatal("--mount-point is required")
	}

	if err := validateMountPoint(*mountPoint); err != nil {
		log.Fatalf("Invalid mount point: %v", err)
	}

	if err := stopDaemon(*mountPoint); err != nil {
		log.Fatalf("Failed to stop daemon: %v", err)
	}

	fmt.Printf("Daemon stopped for mount point: %s\n", *mountPoint)
}

