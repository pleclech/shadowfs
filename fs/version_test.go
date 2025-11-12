package fs

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pleclech/shadowfs/fs/cache"
	"github.com/pleclech/shadowfs/fs/rootinit"
)

func TestFindCacheDirectory(t *testing.T) {
	// Create a temporary directory structure
	tempDir, err := os.MkdirTemp("", "shadowfs-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create cache structure
	mountPoint := filepath.Join(tempDir, "mount")
	sourceDir := filepath.Join(tempDir, "source")
	cacheBaseDir := filepath.Join(tempDir, ".shadowfs")

	if err := os.MkdirAll(mountPoint, 0755); err != nil {
		t.Fatalf("Failed to create mount point: %v", err)
	}
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("Failed to create source dir: %v", err)
	}

	// Compute mount ID using centralized function
	mountID := cache.ComputeMountID(mountPoint, sourceDir)
	sessionPath := filepath.Join(cacheBaseDir, mountID)
	if err := os.MkdirAll(sessionPath, 0755); err != nil {
		t.Fatalf("Failed to create session path: %v", err)
	}

	// Create .gitofs directory (git init creates .gitofs/.git/ inside it)
	gitDir := filepath.Join(sessionPath, ".gitofs")
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		t.Fatalf("Failed to create git dir: %v", err)
	}
	// Create .git subdirectory to simulate git init
	gitGitDir := filepath.Join(gitDir, ".git")
	if err := os.MkdirAll(gitGitDir, 0755); err != nil {
		t.Fatalf("Failed to create .git subdirectory: %v", err)
	}

	// Create .target file
	targetFile := filepath.Join(sessionPath, ".target")
	if err := os.WriteFile(targetFile, []byte(sourceDir), 0444); err != nil {
		t.Fatalf("Failed to create target file: %v", err)
	}

	// Set environment variable for cache directory
	oldEnv := os.Getenv("SHADOWFS_CACHE_DIR")
	defer os.Setenv("SHADOWFS_CACHE_DIR", oldEnv)
	os.Setenv("SHADOWFS_CACHE_DIR", cacheBaseDir)

	// Test FindCacheDirectory
	foundCacheDir, err := rootinit.FindCacheDirectory(mountPoint)
	if err != nil {
		t.Fatalf("FindCacheDirectory failed: %v", err)
	}

	if foundCacheDir != sessionPath {
		t.Errorf("Expected cache dir %s, got %s", sessionPath, foundCacheDir)
	}
}

func TestValidateGitRepository(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "shadowfs-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Test with non-existent directory
	if err := ValidateGitRepository(tempDir); err == nil {
		t.Error("Expected error for non-existent git repository")
	}

	// Create .gitofs/.git directory structure (as created by git init)
	gitDir := filepath.Join(tempDir, ".gitofs", ".git")
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		t.Fatalf("Failed to create git dir: %v", err)
	}

	// Test with missing HEAD
	if err := ValidateGitRepository(tempDir); err == nil {
		t.Error("Expected error for missing HEAD file")
	}

	// Create HEAD file
	headFile := filepath.Join(gitDir, "HEAD")
	if err := os.WriteFile(headFile, []byte("ref: refs/heads/main\n"), 0644); err != nil {
		t.Fatalf("Failed to create HEAD file: %v", err)
	}

	// Test with valid repository
	if err := ValidateGitRepository(tempDir); err != nil {
		t.Errorf("Expected no error for valid repository, got: %v", err)
	}
}

func TestGetGitRepository(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "shadowfs-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	mountPoint := filepath.Join(tempDir, "mount")
	sourceDir := filepath.Join(tempDir, "source")
	cacheBaseDir := filepath.Join(tempDir, ".shadowfs")

	// Create mount point and source directories
	if err := os.MkdirAll(mountPoint, 0755); err != nil {
		t.Fatalf("Failed to create mount point: %v", err)
	}
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("Failed to create source dir: %v", err)
	}

	// Create .gitofs/.git directory structure in mount point (as created by git init)
	gitDir := filepath.Join(mountPoint, ".gitofs", ".git")
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		t.Fatalf("Failed to create git dir: %v", err)
	}

	// Create HEAD file
	headFile := filepath.Join(gitDir, "HEAD")
	if err := os.WriteFile(headFile, []byte("ref: refs/heads/main\n"), 0644); err != nil {
		t.Fatalf("Failed to create HEAD file: %v", err)
	}

	// Create cache directory structure
	mountID := cache.ComputeMountID(mountPoint, sourceDir)
	sessionPath := filepath.Join(cacheBaseDir, mountID)
	if err := os.MkdirAll(sessionPath, 0755); err != nil {
		t.Fatalf("Failed to create session path: %v", err)
	}

	// Create .target file in cache directory
	targetFile := cache.GetTargetFilePath(sessionPath)
	if err := os.WriteFile(targetFile, []byte(sourceDir), 0444); err != nil {
		t.Fatalf("Failed to create target file: %v", err)
	}

	// Create .root directory so FindCacheDirectory can find it
	// (FindCacheDirectory currently checks for .gitofs, but we'll create .root as fallback)
	cachePath := cache.GetCachePath(sessionPath)
	if err := os.MkdirAll(cachePath, 0755); err != nil {
		t.Fatalf("Failed to create cache path: %v", err)
	}

	// Set environment variable for cache directory
	oldEnv := os.Getenv("SHADOWFS_CACHE_DIR")
	defer os.Setenv("SHADOWFS_CACHE_DIR", oldEnv)
	os.Setenv("SHADOWFS_CACHE_DIR", cacheBaseDir)

	// Test GetGitRepository with mount point
	gm, err := GetGitRepository(mountPoint)
	if err != nil {
		t.Fatalf("GetGitRepository failed: %v", err)
	}

	if gm == nil {
		t.Fatal("GetGitRepository returned nil")
	}

	if !gm.IsEnabled() {
		t.Error("GitManager should be enabled")
	}

	// Normalize mount point for comparison (GetGitRepository normalizes it)
	normalizedMountPoint, err := rootinit.GetMountPoint(mountPoint)
	if err != nil {
		t.Fatalf("Failed to normalize mount point: %v", err)
	}
	if gm.GetWorkspacePath() != normalizedMountPoint {
		t.Errorf("Expected workspace path %s, got %s", normalizedMountPoint, gm.GetWorkspacePath())
	}
}

func TestExpandGlobPatterns_SingleFile(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "shadowfs-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a test file
	testFile := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	patterns := []string{"test.txt"}
	result, err := ExpandGlobPatterns(tempDir, patterns)
	if err != nil {
		t.Fatalf("ExpandGlobPatterns failed: %v", err)
	}

	expected := []string{"test.txt"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("Expected %v, got %v", expected, result)
	}
}

func TestExpandGlobPatterns_MultipleFiles(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "shadowfs-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create test files
	files := []string{"file1.txt", "file2.txt", "file3.go"}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(tempDir, f), []byte("content"), 0644); err != nil {
			t.Fatalf("Failed to create test file %s: %v", f, err)
		}
	}

	patterns := []string{"file1.txt", "file2.txt"}
	result, err := ExpandGlobPatterns(tempDir, patterns)
	if err != nil {
		t.Fatalf("ExpandGlobPatterns failed: %v", err)
	}

	expected := []string{"file1.txt", "file2.txt"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("Expected %v, got %v", expected, result)
	}
}

func TestExpandGlobPatterns_GlobPattern(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "shadowfs-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create test files
	files := []string{"file1.txt", "file2.txt", "file3.go", "readme.md"}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(tempDir, f), []byte("content"), 0644); err != nil {
			t.Fatalf("Failed to create test file %s: %v", f, err)
		}
	}

	patterns := []string{"*.txt"}
	result, err := ExpandGlobPatterns(tempDir, patterns)
	if err != nil {
		t.Fatalf("ExpandGlobPatterns failed: %v", err)
	}

	// Should match file1.txt and file2.txt
	if len(result) != 2 {
		t.Errorf("Expected 2 matches, got %d: %v", len(result), result)
	}

	// Check that both .txt files are present
	hasFile1 := false
	hasFile2 := false
	for _, r := range result {
		if r == "file1.txt" {
			hasFile1 = true
		}
		if r == "file2.txt" {
			hasFile2 = true
		}
	}
	if !hasFile1 || !hasFile2 {
		t.Errorf("Expected file1.txt and file2.txt, got %v", result)
	}
}

func TestExpandGlobPatterns_RecursiveGlob(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "shadowfs-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Recursive glob patterns should pass through unchanged
	patterns := []string{"**/*.go", "src/**/*.txt"}
	result, err := ExpandGlobPatterns(tempDir, patterns)
	if err != nil {
		t.Fatalf("ExpandGlobPatterns failed: %v", err)
	}

	expected := []string{"**/*.go", "src/**/*.txt"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("Expected %v, got %v", expected, result)
	}
}

func TestExpandGlobPatterns_Directory(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "shadowfs-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a directory
	testDir := filepath.Join(tempDir, "subdir")
	if err := os.MkdirAll(testDir, 0755); err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}

	patterns := []string{"subdir"}
	result, err := ExpandGlobPatterns(tempDir, patterns)
	if err != nil {
		t.Fatalf("ExpandGlobPatterns failed: %v", err)
	}

	expected := []string{"subdir"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("Expected %v, got %v", expected, result)
	}
}

func TestExpandGlobPatterns_CommaSeparated(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "shadowfs-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create test files
	files := []string{"file1.txt", "file2.go", "file3.md"}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(tempDir, f), []byte("content"), 0644); err != nil {
			t.Fatalf("Failed to create test file %s: %v", f, err)
		}
	}

	// Test multiple patterns (simulating comma-separated input)
	patterns := []string{"file1.txt", "file2.go"}
	result, err := ExpandGlobPatterns(tempDir, patterns)
	if err != nil {
		t.Fatalf("ExpandGlobPatterns failed: %v", err)
	}

	if len(result) != 2 {
		t.Errorf("Expected 2 matches, got %d: %v", len(result), result)
	}
}

func TestExpandGlobPatterns_EmptyPatterns(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "shadowfs-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Test empty patterns
	result, err := ExpandGlobPatterns(tempDir, []string{})
	if err != nil {
		t.Fatalf("ExpandGlobPatterns failed: %v", err)
	}

	if len(result) != 0 {
		t.Errorf("Expected empty result, got %v", result)
	}

	// Test with empty string pattern
	result, err = ExpandGlobPatterns(tempDir, []string{""})
	if err != nil {
		t.Fatalf("ExpandGlobPatterns failed: %v", err)
	}

	if len(result) != 0 {
		t.Errorf("Expected empty result for empty string pattern, got %v", result)
	}
}

func TestExpandGlobPatterns_NoMatches(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "shadowfs-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Test glob pattern with no matches
	patterns := []string{"*.nonexistent"}
	result, err := ExpandGlobPatterns(tempDir, patterns)
	if err != nil {
		t.Fatalf("ExpandGlobPatterns failed: %v", err)
	}

	// Should return empty list, not error
	if len(result) != 0 {
		t.Errorf("Expected empty result for no matches, got %v", result)
	}
}

func TestExpandGlobPatterns_Deduplication(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "shadowfs-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a test file
	testFile := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Test duplicate patterns
	patterns := []string{"test.txt", "test.txt", "*.txt"}
	result, err := ExpandGlobPatterns(tempDir, patterns)
	if err != nil {
		t.Fatalf("ExpandGlobPatterns failed: %v", err)
	}

	// Should have only one instance of test.txt
	count := 0
	for _, r := range result {
		if r == "test.txt" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("Expected test.txt to appear once, got %d times: %v", count, result)
	}
}

func TestExpandGlobPatterns_NonexistentFiles(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "shadowfs-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Test pattern for file that doesn't exist
	// Should still add it (Git will handle it)
	patterns := []string{"nonexistent.txt"}
	result, err := ExpandGlobPatterns(tempDir, patterns)
	if err != nil {
		t.Fatalf("ExpandGlobPatterns failed: %v", err)
	}

	expected := []string{"nonexistent.txt"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("Expected %v, got %v", expected, result)
	}
}

func TestExpandGlobPatterns_PathNormalization(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "shadowfs-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create nested directory structure
	subDir := filepath.Join(tempDir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("Failed to create subdirectory: %v", err)
	}

	testFile := filepath.Join(subDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Test with relative path
	patterns := []string{"subdir/test.txt"}
	result, err := ExpandGlobPatterns(tempDir, patterns)
	if err != nil {
		t.Fatalf("ExpandGlobPatterns failed: %v", err)
	}

	// Path should be normalized to use forward slashes
	expected := []string{"subdir/test.txt"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("Expected %v, got %v", expected, result)
	}
}

func TestExpandGlobPatterns_MixedPatterns(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "shadowfs-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create test files
	files := []string{"file1.txt", "file2.go", "readme.md"}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(tempDir, f), []byte("content"), 0644); err != nil {
			t.Fatalf("Failed to create test file %s: %v", f, err)
		}
	}

	// Test mixed patterns: glob, specific file, recursive glob
	patterns := []string{"*.txt", "file2.go", "**/*.md"}
	result, err := ExpandGlobPatterns(tempDir, patterns)
	if err != nil {
		t.Fatalf("ExpandGlobPatterns failed: %v", err)
	}

	// Should have file1.txt (from glob), file2.go (specific), and **/*.md (recursive glob passed through)
	hasFile1 := false
	hasFile2 := false
	hasRecursiveGlob := false
	for _, r := range result {
		if r == "file1.txt" {
			hasFile1 = true
		}
		if r == "file2.go" {
			hasFile2 = true
		}
		if r == "**/*.md" {
			hasRecursiveGlob = true
		}
	}

	if !hasFile1 {
		t.Errorf("Expected file1.txt in result, got %v", result)
	}
	if !hasFile2 {
		t.Errorf("Expected file2.go in result, got %v", result)
	}
	if !hasRecursiveGlob {
		t.Errorf("Expected **/*.md in result, got %v", result)
	}
}

func TestExpandGlobPatterns_NestedDirectories(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "shadowfs-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create nested directory structure
	subDir1 := filepath.Join(tempDir, "dir1", "subdir")
	if err := os.MkdirAll(subDir1, 0755); err != nil {
		t.Fatalf("Failed to create nested directory: %v", err)
	}

	testFile1 := filepath.Join(tempDir, "file1.txt")
	testFile2 := filepath.Join(tempDir, "dir1", "file2.txt")
	testFile3 := filepath.Join(subDir1, "file3.txt")

	for _, f := range []string{testFile1, testFile2, testFile3} {
		if err := os.WriteFile(f, []byte("content"), 0644); err != nil {
			t.Fatalf("Failed to create test file %s: %v", f, err)
		}
	}

	// Test glob pattern matching files in nested directories
	patterns := []string{"dir1/*.txt"}
	result, err := ExpandGlobPatterns(tempDir, patterns)
	if err != nil {
		t.Fatalf("ExpandGlobPatterns failed: %v", err)
	}

	// Should match file2.txt in dir1
	expected := []string{"dir1/file2.txt"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("Expected %v, got %v", expected, result)
	}
}
