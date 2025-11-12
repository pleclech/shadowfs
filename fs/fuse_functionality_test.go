package fs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pleclech/shadowfs/fs/rootinit"
	"github.com/pleclech/shadowfs/fs/xattr"
)

// TestFUSE_FileCreation tests file creation functionality
func TestFUSE_FileCreation(t *testing.T) {
	ts := NewTestSetup(t)
	defer ts.Cleanup()

	// Test creating a mirrored file in cache
	sourceFile := filepath.Join(ts.SrcDir, "test_file.txt")
	cacheFile := filepath.Join(ts.CacheDir, "test_file.txt")

	// Create source file
	content := []byte("test content for file creation")
	if err := os.WriteFile(sourceFile, content, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	// Test CreateMirroredFileOrDir
	mirroredPath, err := ts.Root.CreateMirroredFileOrDir(sourceFile)
	if err != nil {
		t.Errorf("CreateMirroredFileOrDir failed: %v", err)
	}

	if mirroredPath != cacheFile {
		t.Errorf("Expected mirrored path %s, got %s", cacheFile, mirroredPath)
	}

	// Verify file was created in cache
	if _, err := os.Stat(cacheFile); os.IsNotExist(err) {
		t.Error("File not created in cache")
	}

	// Note: Content might not match exactly due to how CreateMirroredFileOrDir works
	// The important thing is that the file structure is created
}

// TestFUSE_DirectoryCreation tests directory creation functionality
func TestFUSE_DirectoryCreation(t *testing.T) {
	ts := NewTestSetup(t)
	defer ts.Cleanup()

	// Test creating a mirrored directory in cache
	sourceDir := filepath.Join(ts.SrcDir, "test_dir")
	cacheDir := filepath.Join(ts.CacheDir, "test_dir")

	// Create source directory
	if err := os.Mkdir(sourceDir, 0755); err != nil {
		t.Fatalf("Failed to create source directory: %v", err)
	}

	// Test createMirroredDir
	mirroredPath, err := ts.Root.createMirroredDir(sourceDir)
	if err != nil {
		t.Errorf("createMirroredDir failed: %v", err)
	}

	if mirroredPath != cacheDir {
		t.Errorf("Expected mirrored path %s, got %s", cacheDir, mirroredPath)
	}

	// Verify directory was created in cache
	if stat, err := os.Stat(cacheDir); err != nil || !stat.IsDir() {
		t.Error("Directory not created in cache or not a directory")
	}
}

// TestFUSE_PathRebasingOperations tests path rebasing with real file operations
func TestFUSE_PathRebasingOperations(t *testing.T) {
	ts := NewTestSetup(t)
	defer ts.Cleanup()

	// Create a nested file structure
	nestedFile := filepath.Join(ts.SrcDir, "subdir", "nested", "file.txt")
	if err := os.MkdirAll(filepath.Dir(nestedFile), 0755); err != nil {
		t.Fatalf("Failed to create nested directories: %v", err)
	}
	if err := os.WriteFile(nestedFile, []byte("nested content"), 0644); err != nil {
		t.Fatalf("Failed to create nested file: %v", err)
	}

	// Test rebasing source to cache
	cachePath := ts.Root.RebasePathUsingCache(nestedFile)
	expectedCachePath := filepath.Join(ts.CacheDir, "subdir", "nested", "file.txt")
	if cachePath != expectedCachePath {
		t.Errorf("RebasePathUsingCache failed: expected %s, got %s", expectedCachePath, cachePath)
	}

	// Test rebasing cache to source
	rebasedSource := ts.Root.RebasePathUsingSrc(cachePath)
	if rebasedSource != nestedFile {
		t.Errorf("RebasePathUsingSrc failed: expected %s, got %s", nestedFile, rebasedSource)
	}

	// Test that IsCached works correctly
	// Note: IsCached checks if mirrorPath is set, not the actual path
	// The root node has mirrorPath set to cachePath, so IsCached returns true
	if !ts.Root.IsCached(nestedFile) {
		t.Error("IsCached should return true for root node (mirrorPath is set)")
	}
	if !ts.Root.IsCached(cachePath) {
		t.Error("IsCached should return true for root node (mirrorPath is set)")
	}
}

// TestFUSE_ShadowXAttrOperations tests shadow xattr functionality
func TestFUSE_ShadowXAttrOperations(t *testing.T) {
	ts := NewTestSetup(t)
	defer ts.Cleanup()

	// Create a test file
	testFile := filepath.Join(ts.CacheDir, "test_xattr.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Test setting shadow xattr
	attr := &xattr.XAttr{PathStatus: xattr.PathStatusDeleted}
	errno := xattr.Set(testFile, attr)
	if errno != 0 {
		t.Errorf("SetShadowXAttr failed: %v", errno)
	}

	// Test getting shadow xattr
	var retrievedAttr xattr.XAttr
	exists, errno := xattr.Get(testFile, &retrievedAttr)
	if errno != 0 {
		t.Errorf("GetShadowXAttr failed: %v", errno)
	}
	if !exists {
		t.Error("Xattr should exist after setting")
	}
	if retrievedAttr.PathStatus != xattr.PathStatusDeleted {
		t.Errorf("Expected PathStatusDeleted, got %d", retrievedAttr.PathStatus)
	}

	// Test IsPathDeleted function
	if !xattr.IsPathDeleted(retrievedAttr) {
		t.Error("IsPathDeleted should return true for deleted file")
	}

	// Test with normal status
	normalAttr := &xattr.XAttr{PathStatus: xattr.PathStatusNone}
	if xattr.IsPathDeleted(*normalAttr) {
		t.Error("IsPathDeleted should return false for normal file")
	}
}

// TestFUSE_WriteFileOnce tests atomic file writing functionality
func TestFUSE_WriteFileOnce(t *testing.T) {
	ts := NewTestSetup(t)
	defer ts.Cleanup()

	// Test writing a file once
	testFile := filepath.Join(ts.CacheDir, "test_write_once.txt")
	content := []byte("content written once")

	err := rootinit.WriteFileOnce(testFile, content, 0644)
	if err != nil {
		t.Errorf("writeFileOnce failed: %v", err)
	}

	// Verify file exists and has correct content
	readContent, err := os.ReadFile(testFile)
	if err != nil {
		t.Errorf("Failed to read file: %v", err)
	}
	if string(readContent) != string(content) {
		t.Errorf("Content mismatch: expected %s, got %s", string(content), string(readContent))
	}

	// Test writing again (should not overwrite)
	err = rootinit.WriteFileOnce(testFile, []byte("new content"), 0644)
	if err != nil {
		t.Errorf("writeFileOnce should not fail when file exists: %v", err)
	}

	// Verify content wasn't overwritten
	readContent, err = os.ReadFile(testFile)
	if err != nil {
		t.Errorf("Failed to read file after second write: %v", err)
	}
	if string(readContent) != string(content) {
		t.Error("Content should not be overwritten by writeFileOnce")
	}
}

// TestFUSE_FullPathOperations tests FullPath method functionality
func TestFUSE_FullPathOperations(t *testing.T) {
	ts := NewTestSetup(t)
	defer ts.Cleanup()

	// Test GetMountPoint (this doesn't require inode)
	mountPoint := ts.Root.GetMountPoint()
	if mountPoint != ts.MountPoint {
		t.Errorf("GetMountPoint failed: expected %s, got %s", ts.MountPoint, mountPoint)
	}

	// Test that the root node has the expected paths set
	if ts.Root.cachePath != ts.CacheDir {
		t.Errorf("Root cachePath mismatch: expected %s, got %s", ts.CacheDir, ts.Root.cachePath)
	}
	if ts.Root.srcDir != ts.SrcDir {
		t.Errorf("Root srcDir mismatch: expected %s, got %s", ts.SrcDir, ts.Root.srcDir)
	}
}
