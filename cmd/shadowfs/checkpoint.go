package main

import (
	"flag"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	shadowfs "github.com/pleclech/shadowfs/fs"
	"github.com/pleclech/shadowfs/fs/cache"
	"github.com/pleclech/shadowfs/fs/rootinit"
)

func runCheckpointCommand(args []string) {
	fs := flag.NewFlagSet("checkpoint", flag.ExitOnError)
	mountPoint := fs.String("mount-point", "", "Mount point path (required)")
	filePath := fs.String("file", "", "Create checkpoint for specific file")
	fs.Parse(args)

	if *mountPoint == "" {
		log.Fatal("--mount-point is required")
	}

	if err := validateMountPoint(*mountPoint); err != nil {
		log.Fatalf("Invalid mount point: %v", err)
	}

	gm, err := shadowfs.GetGitRepository(*mountPoint)
	if err != nil {
		// Add helpful suggestion for git repository not found
		if strings.Contains(err.Error(), "git repository not found") {
			log.Fatalf("Failed to get git repository: %v\n\nTip: Did you enable git with -auto-git flag? Try: shadowfs -auto-git %s <srcdir>", err, *mountPoint)
		}
		log.Fatalf("Failed to get git repository: %v", err)
	}

	if !gm.IsEnabled() {
		log.Fatal("Git auto-versioning is not enabled for this mount point")
	}

	// Find cache directory to get cache path
	cacheDir, err := rootinit.FindCacheDirectory(*mountPoint)
	if err != nil {
		// Add helpful suggestion for cache directory not found
		if strings.Contains(err.Error(), "cache directory not found") {
			log.Fatalf("Failed to find cache directory: %v\n\nTip: Is the filesystem mounted? Try: shadowfs %s <srcdir>", err, *mountPoint)
		}
		log.Fatalf("Failed to find cache directory: %v", err)
	}

	// Get cache path using centralized function
	cachePath := cache.GetCachePath(cacheDir)

	if *filePath != "" {
		// Validate file path if specified
		fullPath := filepath.Join(cachePath, *filePath)
		if err := validateFilePath(fullPath, true); err != nil {
			log.Fatalf("Invalid file path: %v", err)
		}

		// Create checkpoint for specific file
		if err := gm.CommitFileCheckpoint(fullPath, "Manual checkpoint"); err != nil {
			log.Fatalf("Failed to create checkpoint: %v", err)
		}
		fmt.Printf("Checkpoint created for file: %s\n", *filePath)
	} else {
		// Create checkpoint for all files with uncommitted changes
		changedFiles, err := discoverChangedFiles(gm)
		if err != nil {
			log.Fatalf("Failed to discover changed files: %v", err)
		}

		if len(changedFiles) == 0 {
			fmt.Println("No uncommitted changes found. Nothing to checkpoint.")
			return
		}

		// Convert relative paths to absolute paths
		workspacePath := gm.GetWorkspacePath()
		absolutePaths := make([]string, 0, len(changedFiles))
		for _, relPath := range changedFiles {
			absolutePaths = append(absolutePaths, filepath.Join(workspacePath, relPath))
		}

		if err := gm.CommitFilesBatchCheckpoint(absolutePaths, "Manual checkpoint"); err != nil {
			log.Fatalf("Failed to create checkpoint: %v", err)
		}
		fmt.Printf("Checkpoint created for %d file(s)\n", len(changedFiles))
	}
}

// discoverChangedFiles discovers all files with uncommitted changes using git status
func discoverChangedFiles(gm *shadowfs.GitManager) ([]string, error) {
	// Use GitManager's StatusPorcelain method
	return gm.StatusPorcelain()
}
