//go:build linux
// +build linux

package fs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShadowNode_PathRebasing(t *testing.T) {
	ts := NewTestSetup(t)
	defer ts.Cleanup()

	// Test rebasing source path to cache
	sourcePath := filepath.Join(ts.SrcDir, "test.txt")
	cachedPath := ts.Root.RebasePathUsingCache(sourcePath)
	expectedCachedPath := filepath.Join(ts.CacheDir, "test.txt")

	if cachedPath != expectedCachedPath {
		t.Errorf("RebasePathUsingCache() = %v, want %v", cachedPath, expectedCachedPath)
	}

	// Test rebasing cache path to source
	rebasedSource := ts.Root.RebasePathUsingSrc(cachedPath)
	if rebasedSource != sourcePath {
		t.Errorf("RebasePathUsingSrc() = %v, want %v", rebasedSource, sourcePath)
	}
}

func TestShadowNode_CreateMirroredDir(t *testing.T) {
	ts := NewTestSetup(t)
	defer ts.Cleanup()

	// Create a directory structure in source
	sourceDir := filepath.Join(ts.SrcDir, "testdir", "subdir")
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("Failed to create source directory: %v", err)
	}

	// Test creating mirrored directory
	targetPath := filepath.Join(ts.CacheDir, "testdir", "subdir")
	result, err := ts.Root.createMirroredDir(targetPath)
	if err != nil {
		t.Errorf("createMirroredDir() failed: %v", err)
	}
	if result != targetPath {
		t.Errorf("createMirroredDir() = %v, want %v", result, targetPath)
	}

	// Verify directory exists
	if stat, err := os.Stat(targetPath); err != nil {
		t.Errorf("Mirrored directory does not exist: %v", err)
	} else if !stat.IsDir() {
		t.Error("Mirrored path is not a directory")
	}
}

func TestShadowNode_CreateMirroredFileOrDir(t *testing.T) {
	ts := NewTestSetup(t)
	defer ts.Cleanup()

	// Test creating mirrored file
	sourceFile := filepath.Join(ts.SrcDir, "testfile.txt")
	if err := os.WriteFile(sourceFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	targetPath := filepath.Join(ts.CacheDir, "testfile.txt")
	result, err := ts.Root.CreateMirroredFileOrDir(sourceFile)
	if err != nil {
		t.Errorf("CreateMirroredFileOrDir() failed: %v", err)
	}
	if result != targetPath {
		t.Errorf("CreateMirroredFileOrDir() = %v, want %v", result, targetPath)
	}

	// Verify file exists
	if stat, err := os.Stat(targetPath); err != nil {
		t.Errorf("Mirrored file does not exist: %v", err)
	} else if stat.IsDir() {
		t.Error("Mirrored path is a directory, expected file")
	}
}

func TestShadowNode_CreateMirroredFileOrDir_Directory(t *testing.T) {
	ts := NewTestSetup(t)
	defer ts.Cleanup()

	// Test creating mirrored directory
	sourceDir := filepath.Join(ts.SrcDir, "testdir")
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("Failed to create source directory: %v", err)
	}

	targetPath := filepath.Join(ts.CacheDir, "testdir")
	result, err := ts.Root.CreateMirroredFileOrDir(sourceDir)
	if err != nil {
		t.Errorf("CreateMirroredFileOrDir() failed: %v", err)
	}
	if result != targetPath {
		t.Errorf("CreateMirroredFileOrDir() = %v, want %v", result, targetPath)
	}

	// Verify directory exists
	if stat, err := os.Stat(targetPath); err != nil {
		t.Errorf("Mirrored directory does not exist: %v", err)
	} else if !stat.IsDir() {
		t.Error("Mirrored path is not a directory")
	}
}

func TestShadowNode_FileOperations(t *testing.T) {
	ts := NewTestSetup(t)
	defer ts.Cleanup()

	// Create a test file in source
	ts.CreateTestFile("test.txt", "content")

	// Test file operations through cache
	sourceFile := filepath.Join(ts.SrcDir, "test.txt")
	cachedFile := filepath.Join(ts.CacheDir, "test.txt")

	// Initially, file should not exist in cache
	if _, err := os.Stat(cachedFile); err == nil {
		t.Error("File should not exist in cache initially")
	}

	// Create mirrored file
	_, err := ts.Root.CreateMirroredFileOrDir(sourceFile)
	if err != nil {
		t.Fatalf("Failed to create mirrored file: %v", err)
	}

	// Verify file exists in cache
	if _, err := os.Stat(cachedFile); err != nil {
		t.Errorf("File should exist in cache after mirroring: %v", err)
	}

	// Test deletion tracking
	attr := ShadowXAttr{ShadowPathStatus: ShadowPathStatusDeleted}
	errno := SetShadowXAttr(cachedFile, &attr)
	if errno != 0 {
		t.Errorf("SetShadowXAttr() failed: %v", errno)
	}

	// Verify file is marked as deleted
	var retrievedAttr ShadowXAttr
	exists, errno := GetShadowXAttr(cachedFile, &retrievedAttr)
	if errno != 0 {
		t.Errorf("GetShadowXAttr() failed: %v", errno)
	}
	if !exists {
		t.Error("Expected xattr to exist")
	}
	if !IsPathDeleted(retrievedAttr) {
		t.Error("Expected file to be marked as deleted")
	}
}

func TestShadowNode_DirectoryOperations(t *testing.T) {
	ts := NewTestSetup(t)
	defer ts.Cleanup()

	// Create a test directory in source
	ts.CreateTestDir("testdir")

	// Test directory mirroring
	sourceDir := filepath.Join(ts.SrcDir, "testdir")
	cachedDir := filepath.Join(ts.CacheDir, "testdir")

	// Initially, directory should not exist in cache
	if _, err := os.Stat(cachedDir); err == nil {
		t.Error("Directory should not exist in cache initially")
	}

	// Create mirrored directory
	_, err := ts.Root.CreateMirroredFileOrDir(sourceDir)
	if err != nil {
		t.Fatalf("Failed to create mirrored directory: %v", err)
	}

	// Verify directory exists in cache
	if stat, err := os.Stat(cachedDir); err != nil {
		t.Errorf("Directory should exist in cache after mirroring: %v", err)
	} else if !stat.IsDir() {
		t.Error("Mirrored path should be a directory")
	}
}

func TestShadowNode_NestedPathOperations(t *testing.T) {
	ts := NewTestSetup(t)
	defer ts.Cleanup()

	// Create nested structure in source
	nestedFile := filepath.Join("dir1", "dir2", "file.txt")
	ts.CreateTestFile(nestedFile, "nested content")

	// Test path rebasing for nested paths
	sourcePath := filepath.Join(ts.SrcDir, nestedFile)
	cachedPath := ts.Root.RebasePathUsingCache(sourcePath)
	expectedCachedPath := filepath.Join(ts.CacheDir, nestedFile)

	if cachedPath != expectedCachedPath {
		t.Errorf("RebasePathUsingCache() for nested path = %v, want %v", cachedPath, expectedCachedPath)
	}

	// Test creating mirrored nested structure
	_, err := ts.Root.CreateMirroredFileOrDir(sourcePath)
	if err != nil {
		t.Errorf("CreateMirroredFileOrDir() failed for nested path: %v", err)
	}

	// Verify nested file exists in cache
	if _, err := os.Stat(expectedCachedPath); err != nil {
		t.Errorf("Nested file should exist in cache: %v", err)
	}
}

func TestShadowNode_ErrorHandling(t *testing.T) {
	ts := NewTestSetup(t)
	defer ts.Cleanup()

	// Test operations on non-existent paths
	nonExistentSource := filepath.Join(ts.SrcDir, "nonexistent.txt")
	_, err := ts.Root.CreateMirroredFileOrDir(nonExistentSource)
	if err == nil {
		t.Error("Expected error for non-existent source file")
	}

	// Test xattr operations on non-existent files
	nonExistentCache := filepath.Join(ts.CacheDir, "nonexistent.txt")
	var attr ShadowXAttr
	exists, errno := GetShadowXAttr(nonExistentCache, &attr)
	if errno != 0 {
		t.Errorf("GetShadowXAttr() on non-existent file should not error: %v", errno)
	}
	if exists {
		t.Error("Expected xattr to not exist on non-existent file")
	}
}
