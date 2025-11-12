package init

import (
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
)

// CreateDir creates a directory if it doesn't exist, or validates it exists and is a directory
func CreateDir(path string, perm iofs.FileMode) error {
	stat, err := os.Stat(path)

	if err != nil {
		if os.IsNotExist(err) {
			if err := os.MkdirAll(path, perm); err != nil {
				return fmt.Errorf("creating directory error:\n%w", err)
			}
			return nil
		}
		return fmt.Errorf("stat error:\n%w", err)
	}

	if !stat.IsDir() {
		return fmt.Errorf("path must be a directory(%s)", path)
	}

	return nil
}

// WriteFileOnce writes a file if it doesn't exist, or validates it exists and is a file
func WriteFileOnce(path string, content []byte, perm iofs.FileMode) error {
	stat, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			if err := os.WriteFile(path, content, perm); err != nil {
				return fmt.Errorf("writing file error:\n%w", err)
			}
			return nil
		}
		return fmt.Errorf("stat error:\n%w", err)
	}
	// path must be a file
	if stat.IsDir() {
		return fmt.Errorf("path must be a file(%s)", path)
	}
	return nil
}

// ValidateCacheDirectory validates and prepares a cache directory
func ValidateCacheDirectory(path string) error {
	// Normalize to absolute path
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("cannot resolve cache directory path: %w", err)
	}

	// Check if exists
	info, err := os.Stat(absPath)
	if os.IsNotExist(err) {
		// Create directory
		if err := os.MkdirAll(absPath, 0755); err != nil {
			return fmt.Errorf("failed to create cache directory: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot access cache directory: %w", err)
	}

	// Check is directory
	if !info.IsDir() {
		return fmt.Errorf("cache path is not a directory: %s", absPath)
	}

	// Check writable (try to create a test file)
	testFile := filepath.Join(absPath, ".test-write")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		return fmt.Errorf("cache directory is not writable: %w", err)
	}
	os.Remove(testFile)

	return nil
}

