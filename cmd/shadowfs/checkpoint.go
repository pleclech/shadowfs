package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"

	shadowfs "github.com/pleclech/shadowfs/fs"
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

	cacheDir, err := shadowfs.FindCacheDirectory(*mountPoint)
	if err != nil {
		log.Fatalf("Failed to find cache directory: %v", err)
	}

	gm, err := shadowfs.GetGitRepository(cacheDir)
	if err != nil {
		log.Fatalf("Failed to get git repository: %v", err)
	}

	if !gm.IsEnabled() {
		log.Fatal("Git auto-versioning is not enabled for this mount point")
	}

	// Get cache path
	cachePath := filepath.Join(cacheDir, ".root")

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
	workspacePath := gm.GetWorkspacePath()
	gitDir := filepath.Join(workspacePath, ".gitofs")

	// Use git status --porcelain to get list of changed files
	cmd := exec.Command("git", "--git-dir", gitDir, "-C", workspacePath, "status", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to run git status: %w", err)
	}

	var changedFiles []string
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// git status --porcelain format: XY filename
		// X = status of index, Y = status of work tree
		// We want files that are modified (M), added (A), or untracked (?)
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		status := parts[0]
		filePath := parts[1]

		// Check if file has changes (M=modified, A=added, ??=untracked)
		if strings.Contains(status, "M") || strings.Contains(status, "A") || status == "??" {
			changedFiles = append(changedFiles, filePath)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to parse git status output: %w", err)
	}

	return changedFiles, nil
}
