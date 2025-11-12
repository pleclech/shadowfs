package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	shadowfs "github.com/pleclech/shadowfs/fs"
)

// validateMountPoint checks if mount point exists and is a directory
func validateMountPoint(mountPoint string) error {
	if mountPoint == "" {
		return fmt.Errorf("mount point is required")
	}

	absPath, err := filepath.Abs(mountPoint)
	if err != nil {
		return fmt.Errorf("invalid mount point path: %w", err)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("mount point does not exist: %s", absPath)
		}
		return fmt.Errorf("cannot access mount point: %w", err)
	}

	if !info.IsDir() {
		return fmt.Errorf("mount point is not a directory: %s", absPath)
	}

	return nil
}

// validateCommitHash checks if a commit hash exists in the Git repository
func validateCommitHash(gm *shadowfs.GitManager, hash string) error {
	if hash == "" {
		return fmt.Errorf("commit hash is required")
	}

	// Basic format validation (Git hashes are hex strings, typically 7-40 chars)
	matched, err := regexp.MatchString(`^[0-9a-fA-F]{7,40}$`, hash)
	if err != nil {
		return fmt.Errorf("invalid commit hash format: %w", err)
	}
	if !matched {
		return fmt.Errorf("invalid commit hash format (must be 7-40 hex characters): %s", hash)
	}

	// Check if commit exists in repository
	workspacePath := gm.GetWorkspacePath()
	// git init creates .gitofs/.git/, so use .gitofs/.git for --git-dir
	gitDir := filepath.Join(workspacePath, ".gitofs", ".git")
	cmd := exec.Command("git", "--git-dir", gitDir, "-C", workspacePath, "cat-file", "-e", hash)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("commit hash does not exist in repository: %s", hash)
	}

	return nil
}

// validateFilePath checks if a file path exists (optional validation)
func validateFilePath(filePath string, mustExist bool) error {
	if filePath == "" {
		return nil // Empty path is valid (means "all files")
	}

	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("invalid file path: %w", err)
	}

	if mustExist {
		info, err := os.Stat(absPath)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("file does not exist: %s", absPath)
			}
			return fmt.Errorf("cannot access file: %w", err)
		}
		if info.IsDir() {
			return fmt.Errorf("path is a directory, not a file: %s", absPath)
		}
	}

	return nil
}

// validateBackupID checks if a backup ID exists
func validateBackupID(backupID string) error {
	if backupID == "" {
		return fmt.Errorf("backup ID is required")
	}

	// Basic format validation (backup IDs are hex strings)
	matched, err := regexp.MatchString(`^[0-9a-fA-F]{32,64}$`, backupID)
	if err != nil {
		return fmt.Errorf("invalid backup ID format: %w", err)
	}
	if !matched {
		return fmt.Errorf("invalid backup ID format (must be hex string): %s", backupID)
	}

	// Check if backup exists
	_, err = shadowfs.ReadBackupInfo(backupID)
	if err != nil {
		return fmt.Errorf("backup does not exist: %s", backupID)
	}

	return nil
}

// readUserConfirmation reads user confirmation with better error handling
func readUserConfirmation(prompt string) (bool, error) {
	fmt.Print(prompt)
	
	// Use bufio.Scanner for better EOF handling
	var response string
	_, err := fmt.Scanln(&response)
	if err != nil {
		// EOF or other error - default to "no" for safety
		return false, nil
	}

	response = strings.ToLower(strings.TrimSpace(response))
	return response == "yes" || response == "y", nil
}

