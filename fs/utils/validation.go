package utils

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ValidateName validates FUSE name parameter to prevent path traversal
// Fast check - only validates the name component, not the full path
func ValidateName(name string) bool {
	// Reject names containing path traversal sequences
	if strings.Contains(name, "..") {
		return false
	}
	// Reject absolute paths (shouldn't happen from FUSE, but be safe)
	if filepath.IsAbs(name) {
		return false
	}
	// Reject empty names
	if name == "" {
		return false
	}
	return true
}

// ValidatePathWithinRoot validates that a path stays within the specified root directory
// Returns the cleaned path and an error if the path escapes the root boundary
// Only use this for paths that may have been constructed from user input
func ValidatePathWithinRoot(path, root string) (string, error) {
	cleaned := filepath.Clean(path)

	// Convert to absolute paths for comparison
	var absPath string
	var err error
	if filepath.IsAbs(cleaned) {
		absPath = cleaned
	} else {
		absPath, err = filepath.Abs(cleaned)
		if err != nil {
			return "", err
		}
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}

	// Ensure the path is within the root directory
	// Check if absPath is exactly absRoot or is a subdirectory of absRoot
	if absPath != absRoot && !strings.HasPrefix(absPath+string(os.PathSeparator), absRoot+string(os.PathSeparator)) {
		return "", syscall.EPERM
	}

	return cleaned, nil
}

