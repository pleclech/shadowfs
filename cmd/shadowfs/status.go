package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/pleclech/shadowfs/fs"
	"github.com/pleclech/shadowfs/fs/cache"
	"github.com/pleclech/shadowfs/fs/rootinit"
)

// MountStatus represents the status of a mount point
type MountStatus struct {
	MountPoint  string
	SourceDir   string
	Status      string // "active", "daemon", "stale"
	PID         int
	StartedAt   time.Time
	MountID     string
	CacheDir    string
	IsMounted   bool
}

// MountStats contains detailed statistics for a mount point
type MountStats struct {
	MountPoint      string
	SourceDir       string
	CacheDir        string
	Status          string
	PID             int
	StartedAt       time.Time
	MountID         string
	CacheSize       int64
	FileCount       int
	DirCount        int
	GitEnabled      bool
	LastCommitHash  string
	LastCommitTime  time.Time
	LastCommitMsg   string
	UncommittedFiles []string
}

// listActiveMounts finds all active mounts (both foreground and daemon)
func listActiveMounts() ([]MountStatus, error) {
	var mounts []MountStatus
	mountMap := make(map[string]*MountStatus)

	// 1. Check daemon PID files
	daemonDir, err := cache.GetDaemonDirPath()
	if err == nil {
		entries, err := os.ReadDir(daemonDir)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() || filepath.Ext(entry.Name()) != ".pid" {
					continue
				}

				pidFile := filepath.Join(daemonDir, entry.Name())
				data, err := os.ReadFile(pidFile)
				if err != nil {
					continue
				}

				var daemonInfo DaemonInfo
				if err := json.Unmarshal(data, &daemonInfo); err != nil {
					continue
				}

				// Check if process is still running
				process, err := os.FindProcess(daemonInfo.PID)
				isRunning := false
				if err == nil {
					// Signal 0 doesn't actually send a signal, just checks if process exists
					if err := process.Signal(syscall.Signal(0)); err == nil {
						isRunning = true
					}
				}

				// Check if mount point is actually mounted
				isMounted, _ := rootinit.IsMountPointActive(daemonInfo.MountPoint)

				status := "stale"
				if isRunning && isMounted {
					status = "daemon"
				} else if isMounted {
					status = "active"
				}

				// Get mount ID from PID filename
				mountID := strings.TrimSuffix(entry.Name(), ".pid")

				// Get cache directory
				baseDir, _ := cache.GetCacheBaseDir()
				cacheDir := cache.GetSessionPath(baseDir, mountID)

				mountMap[daemonInfo.MountPoint] = &MountStatus{
					MountPoint: daemonInfo.MountPoint,
					SourceDir:  daemonInfo.SourceDir,
					Status:     status,
					PID:        daemonInfo.PID,
					StartedAt:  daemonInfo.StartedAt,
					MountID:    mountID,
					CacheDir:   cacheDir,
					IsMounted:  isMounted,
				}
			}
		}
	}

	// 2. Check /proc/mounts for any shadowfs mounts that might not have PID files
	// (foreground mounts)
	mountsFromProc, err := findMountsFromProc()
	if err == nil {
		for _, mount := range mountsFromProc {
			// Only add if not already in map (from PID file)
			if _, exists := mountMap[mount.MountPoint]; !exists {
				mountMap[mount.MountPoint] = mount
			}
		}
	}

	// Convert map to slice
	for _, mount := range mountMap {
		mounts = append(mounts, *mount)
	}

	// Sort by mount point
	sort.Slice(mounts, func(i, j int) bool {
		return mounts[i].MountPoint < mounts[j].MountPoint
	})

	return mounts, nil
}

// findMountsFromProc finds mounts from /proc/mounts (Linux-specific)
func findMountsFromProc() ([]*MountStatus, error) {
	var mounts []*MountStatus

	// This is Linux-specific, so we'll use the rootinit function
	// For now, we'll just return empty slice on non-Linux
	// In a real implementation, we'd parse /proc/mounts here
	// But since we already have IsMountPointActive, we can use that
	// to verify mounts found from PID files

	return mounts, nil
}

// getMountStatus gets status for a specific mount point
func getMountStatus(mountPoint string) (*MountStatus, error) {
	// Normalize mount point
	normalizedMountPoint, err := rootinit.GetMountPoint(mountPoint)
	if err != nil {
		return nil, fmt.Errorf("invalid mount point: %w", err)
	}

	// Try to find from PID files first
	_, daemonInfo, err := findPIDFileByMountPoint(normalizedMountPoint)
	if err == nil {
		// Check if process is still running
		process, err := os.FindProcess(daemonInfo.PID)
		isRunning := false
		if err == nil {
			if err := process.Signal(syscall.Signal(0)); err == nil {
				isRunning = true
			}
		}

		// Check if mount point is actually mounted
		isMounted, _ := rootinit.IsMountPointActive(normalizedMountPoint)

		status := "stale"
		if isRunning && isMounted {
			status = "daemon"
		} else if isMounted {
			status = "active"
		}

		// Get mount ID
		mountID := cache.ComputeMountID(normalizedMountPoint, daemonInfo.SourceDir)

		// Get cache directory
		baseDir, _ := cache.GetCacheBaseDir()
		cacheDir := cache.GetSessionPath(baseDir, mountID)

		return &MountStatus{
			MountPoint: normalizedMountPoint,
			SourceDir:  daemonInfo.SourceDir,
			Status:     status,
			PID:        daemonInfo.PID,
			StartedAt:  daemonInfo.StartedAt,
			MountID:    mountID,
			CacheDir:   cacheDir,
			IsMounted:  isMounted,
		}, nil
	}

	// If not found in PID files, check if it's mounted
	isMounted, err := rootinit.IsMountPointActive(normalizedMountPoint)
	if err != nil {
		return nil, fmt.Errorf("failed to check mount status: %w", err)
	}

	if !isMounted {
		return nil, fmt.Errorf("mount point not found: %s", normalizedMountPoint)
	}

	// Try to find source directory and cache
	cacheDir, err := rootinit.FindCacheDirectory(normalizedMountPoint)
	if err != nil {
		return nil, fmt.Errorf("failed to find cache directory: %w", err)
	}

	// Read source directory from .target file
	targetFile := cache.GetTargetFilePath(cacheDir)
	srcDirData, err := os.ReadFile(targetFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read source directory: %w", err)
	}
	srcDir := strings.TrimSpace(string(srcDirData))

	mountID := cache.ComputeMountID(normalizedMountPoint, srcDir)

	return &MountStatus{
		MountPoint: normalizedMountPoint,
		SourceDir:  srcDir,
		Status:     "active",
		PID:        0,
		StartedAt:  time.Time{},
		MountID:    mountID,
		CacheDir:   cacheDir,
		IsMounted:  true,
	}, nil
}

// collectStats gathers detailed statistics for a mount point
func collectStats(mountPoint string) (*MountStats, error) {
	status, err := getMountStatus(mountPoint)
	if err != nil {
		return nil, err
	}

	stats := &MountStats{
		MountPoint: status.MountPoint,
		SourceDir:  status.SourceDir,
		CacheDir:   status.CacheDir,
		Status:     status.Status,
		PID:        status.PID,
		StartedAt:  status.StartedAt,
		MountID:    status.MountID,
	}

	// Calculate cache size and file counts
	cachePath := cache.GetCachePath(status.CacheDir)
	var cacheSize int64
	var fileCount int
	var dirCount int

	err = filepath.Walk(cachePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors
		}
		if info.IsDir() {
			dirCount++
		} else {
			fileCount++
			cacheSize += info.Size()
		}
		return nil
	})
	if err != nil {
		// Log but don't fail
		log.Printf("Warning: failed to walk cache directory: %v", err)
	}

	stats.CacheSize = cacheSize
	stats.FileCount = fileCount
	stats.DirCount = dirCount

	// Check Git status
	gitManager, err := fs.GetGitRepository(mountPoint)
	if err == nil {
		stats.GitEnabled = true

		// Get uncommitted files
		uncommitted, err := gitManager.StatusPorcelain()
		if err == nil {
			stats.UncommittedFiles = uncommitted
		}

		// Get last commit info
		var lastCommitOutput strings.Builder
		err = gitManager.Log(fs.LogOptions{
			Limit:  1,
			Format: "%H|%ai|%s",
		}, &lastCommitOutput)
		if err == nil {
			commitLine := strings.TrimSpace(lastCommitOutput.String())
			if commitLine != "" {
				parts := strings.SplitN(commitLine, "|", 3)
				if len(parts) >= 3 {
					stats.LastCommitHash = parts[0]
					stats.LastCommitMsg = parts[2]
					// Parse time
					if t, err := time.Parse("2006-01-02 15:04:05 -0700", parts[1]); err == nil {
						stats.LastCommitTime = t
					}
				}
			}
		}
	} else {
		stats.GitEnabled = false
	}

	return stats, nil
}

// runListCommand handles the list subcommand
func runListCommand(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	fs.Parse(args)

	mounts, err := listActiveMounts()
	if err != nil {
		log.Fatalf("Failed to list mounts: %v", err)
	}

	if len(mounts) == 0 {
		fmt.Println("No active mounts found.")
		return
	}

	// Print header
	fmt.Printf("%-40s %-40s %-10s %-8s %-20s\n", "Mount Point", "Source Directory", "Status", "PID", "Started")
	fmt.Println(strings.Repeat("-", 118))

	// Print mounts
	for _, mount := range mounts {
		pidStr := "-"
		if mount.PID > 0 {
			pidStr = fmt.Sprintf("%d", mount.PID)
		}

		startedStr := "-"
		if !mount.StartedAt.IsZero() {
			startedStr = mount.StartedAt.Format("2006-01-02 15:04:05")
		}

		// Show full paths (no truncation)
		fmt.Printf("%-40s %-40s %-10s %-8s %-20s\n", mount.MountPoint, mount.SourceDir, mount.Status, pidStr, startedStr)
	}
}

// runInfoCommand handles the info subcommand
func runInfoCommand(args []string) {
	fs := flag.NewFlagSet("info", flag.ExitOnError)
	mountPoint := fs.String("mount-point", "", "Mount point path (required)")
	fs.Parse(args)

	if *mountPoint == "" {
		log.Fatal("--mount-point is required")
	}

	stats, err := collectStats(*mountPoint)
	if err != nil {
		log.Fatalf("Failed to collect stats: %v", err)
	}

	// Format cache size
	cacheSizeStr := formatSize(stats.CacheSize)

	// Print information
	fmt.Printf("Mount Point: %s\n", stats.MountPoint)
	fmt.Printf("Source Directory: %s\n", stats.SourceDir)
	fmt.Printf("Cache Directory: %s\n", stats.CacheDir)
	fmt.Printf("Status: %s\n", stats.Status)
	if stats.PID > 0 {
		fmt.Printf("PID: %d\n", stats.PID)
	}
	if !stats.StartedAt.IsZero() {
		fmt.Printf("Started: %s\n", stats.StartedAt.Format("2006-01-02 15:04:05"))
	}
	fmt.Println()

	fmt.Println("Cache Statistics:")
	fmt.Printf("  Size: %s\n", cacheSizeStr)
	fmt.Printf("  Files: %d\n", stats.FileCount)
	fmt.Printf("  Directories: %d\n", stats.DirCount)
	fmt.Println()

	if stats.GitEnabled {
		fmt.Println("Git Status:")
		fmt.Printf("  Enabled: Yes\n")
		if stats.LastCommitHash != "" {
			hashShort := stats.LastCommitHash
			if len(hashShort) > 7 {
				hashShort = hashShort[:7]
			}
			fmt.Printf("  Last Commit: %s", hashShort)
			if !stats.LastCommitTime.IsZero() {
				fmt.Printf(" (%s)", stats.LastCommitTime.Format("2006-01-02 15:04:05"))
			}
			fmt.Println()
			if stats.LastCommitMsg != "" {
				fmt.Printf("  Message: %s\n", stats.LastCommitMsg)
			}
		}
		if len(stats.UncommittedFiles) > 0 {
			fmt.Printf("  Uncommitted Changes: %d file(s)\n", len(stats.UncommittedFiles))
			if len(stats.UncommittedFiles) <= 10 {
				for _, file := range stats.UncommittedFiles {
					fmt.Printf("    - %s\n", file)
				}
			} else {
				for i := 0; i < 10; i++ {
					fmt.Printf("    - %s\n", stats.UncommittedFiles[i])
				}
				fmt.Printf("    ... and %d more\n", len(stats.UncommittedFiles)-10)
			}
		} else {
			fmt.Printf("  Uncommitted Changes: None\n")
		}
	} else {
		fmt.Println("Git Status:")
		fmt.Printf("  Enabled: No\n")
	}
}


