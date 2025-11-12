package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pleclech/shadowfs/fs/cache"
)

// FindCacheDirectory finds the cache directory for a given mount point
func FindCacheDirectory(mountPoint string) (string, error) {
	// 1. Normalize mount point path
	absMountPoint, err := filepath.Abs(mountPoint)
	if err != nil {
		return "", fmt.Errorf("invalid mount point: %w", err)
	}

	// 2. Try to find source directory from mount info or .target file
	srcDir, err := findSourceDirectory(absMountPoint)
	if err != nil {
		return "", fmt.Errorf("cannot find source directory: %w", err)
	}

	// 3. Compute mountID using centralized function
	mountID := cache.ComputeMountID(absMountPoint, srcDir)

	// 4. Search cache directories
	// Try environment variable first
	cacheBaseDirs := []string{}
	if envDir := os.Getenv("SHADOWFS_CACHE_DIR"); envDir != "" {
		cacheBaseDirs = append(cacheBaseDirs, envDir)
	}
	
	// Add default cache directory using centralized function
	if defaultDir, err := cache.GetCacheBaseDir(); err == nil {
		cacheBaseDirs = append(cacheBaseDirs, defaultDir)
	}

	for _, baseDir := range cacheBaseDirs {
		if baseDir == "" {
			continue
		}
		sessionPath := cache.GetSessionPath(baseDir, mountID)
		gitDir := filepath.Join(sessionPath, GitofsName)
		if _, err := os.Stat(gitDir); err == nil {
			return sessionPath, nil
		}
	}

	return "", fmt.Errorf("cache directory not found for mount point: %s", mountPoint)
}

// findSourceDirectory finds the source directory for a mount point
func findSourceDirectory(mountPoint string) (string, error) {
	// Try to read from .target file in cache directories
	// First, try common cache locations
	cacheBaseDirs := []string{
		os.Getenv("SHADOWFS_CACHE_DIR"),
	}
	
	// Add default cache directory
	if homeDir, err := os.UserHomeDir(); err == nil {
		cacheBaseDirs = append(cacheBaseDirs, filepath.Join(homeDir, ".shadowfs"))
	}

	// Try to find any session directory that has this mount point
	for _, baseDir := range cacheBaseDirs {
		if baseDir == "" {
			continue
		}
		entries, err := os.ReadDir(baseDir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			sessionPath := cache.GetSessionPath(baseDir, entry.Name())
			targetFile := filepath.Join(sessionPath, TargetFileName)
			if data, err := os.ReadFile(targetFile); err == nil {
				srcDir := strings.TrimSpace(string(data))
				// Verify this session matches the mount point using centralized function
				mountID := cache.ComputeMountID(mountPoint, srcDir)
				if entry.Name() == mountID {
					return srcDir, nil
				}
			}
		}
	}

	// Fallback: try to read from /proc/mounts
	return findSourceFromProcMounts(mountPoint)
}

// findSourceFromProcMounts attempts to find source directory from /proc/mounts
func findSourceFromProcMounts(mountPoint string) (string, error) {
	// This is a simplified implementation
	// In practice, we'd parse /proc/mounts to find the source
	// For now, return an error - user should provide source dir explicitly
	return "", fmt.Errorf("cannot determine source directory automatically, please specify --source-dir")
}

// GetGitRepository creates a GitManager for an existing cache directory
func GetGitRepository(cacheDir string) (*GitManager, error) {
	// git init creates .gitofs/.git/, so check for .gitofs/.git
	gitDir := cache.GetGitDirPath(cacheDir)
	if _, err := os.Stat(gitDir); err != nil {
		return nil, fmt.Errorf("git repository not found in cache directory: %w", err)
	}

	// Read source directory from .target file
	targetFile := cache.GetTargetFilePath(cacheDir)
	srcDirData, err := os.ReadFile(targetFile)
	if err != nil {
		return nil, fmt.Errorf("cannot read source directory from .target file: %w", err)
	}
	srcDir := strings.TrimSpace(string(srcDirData))

	// Create GitManager (Git is already initialized)
	config := GitConfig{
		AutoCommit: true, // Assume enabled if repo exists
	}
	gm := NewGitManager(cacheDir, srcDir, config)
	gm.enabled = true // Mark as enabled

	return gm, nil
}

// ValidateGitRepository checks if a Git repository exists and is valid
func ValidateGitRepository(cacheDir string) error {
	// git init creates .gitofs/.git/, so check for .gitofs/.git
	gitDir := cache.GetGitDirPath(cacheDir)
	if _, err := os.Stat(gitDir); err != nil {
		return fmt.Errorf("git repository not found: %w", err)
	}

	// Check if it's a valid git repository by checking for HEAD
	headFile := filepath.Join(gitDir, "HEAD")
	if _, err := os.Stat(headFile); err != nil {
		return fmt.Errorf("invalid git repository (HEAD not found): %w", err)
	}

	return nil
}

// ExpandGlobPatterns expands glob patterns and returns a list of matching files
// relative to the workspace root. Handles both glob patterns and regular paths.
// Note: `**` patterns (recursive globbing) are passed directly to Git without expansion,
// as Go's filepath.Glob doesn't support them, but Git does.
func ExpandGlobPatterns(workspacePath string, patterns []string) ([]string, error) {
	if len(patterns) == 0 {
		return nil, nil
	}

	var expandedPaths []string
	seen := make(map[string]bool) // Deduplicate paths

	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}

		// Check if pattern contains recursive glob (**) - pass directly to Git
		if strings.Contains(pattern, "**") {
			normalized := filepath.ToSlash(pattern)
			if !seen[normalized] {
				expandedPaths = append(expandedPaths, normalized)
				seen[normalized] = true
			}
			continue
		}

		// Check if pattern contains glob characters
		hasGlob := strings.Contains(pattern, "*") || strings.Contains(pattern, "?") || strings.Contains(pattern, "[")
		
		if hasGlob {
			// Expand glob pattern relative to workspace
			matches, err := filepath.Glob(filepath.Join(workspacePath, pattern))
			if err != nil {
				return nil, fmt.Errorf("invalid glob pattern %s: %w", pattern, err)
			}
			
			// Convert absolute paths to relative paths
			for _, match := range matches {
				relPath, err := filepath.Rel(workspacePath, match)
				if err != nil {
					continue // Skip if can't make relative
				}
				
				// Normalize path separators
				relPath = filepath.ToSlash(relPath)
				if !seen[relPath] {
					expandedPaths = append(expandedPaths, relPath)
					seen[relPath] = true
				}
			}
		} else {
			// Regular path - validate it exists and make it relative
			fullPath := filepath.Join(workspacePath, pattern)
			if _, err := os.Stat(fullPath); err == nil {
				relPath, err := filepath.Rel(workspacePath, fullPath)
				if err == nil {
					relPath = filepath.ToSlash(relPath)
					if !seen[relPath] {
						expandedPaths = append(expandedPaths, relPath)
						seen[relPath] = true
					}
				}
			}
			// If path doesn't exist, still add it (Git will handle it)
			// This allows filtering by paths that existed in history but not now
			normalized := filepath.ToSlash(pattern)
			if !seen[normalized] {
				expandedPaths = append(expandedPaths, normalized)
				seen[normalized] = true
			}
		}
	}

	return expandedPaths, nil
}

