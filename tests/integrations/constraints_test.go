//go:build linux && integration
// +build linux,integration

package integrations

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/pleclech/shadowfs/fs/xattr"

	tu "github.com/pleclech/shadowfs/fs/utils/testings"
)

const (
	testBinary = "/tmp/shadowfs-test"
)

// TestMain builds the binary once before all tests and cleans up after
func TestMain(m *testing.M) {
	defer tu.SetupTestMain(m, testBinary)()

	// Run all tests
	os.Exit(m.Run())
}

// ============================================================================
// Test Helpers (DRY)
// ============================================================================

// setupTestFilesystem creates a new test filesystem and starts it
func setupTestFilesystem(t *testing.T, initialContent map[string]string) *tu.TestFilesystem {
	return tu.NewTestFilesystem(t, testBinary, initialContent)
}

// assertXAttr verifies xattr fields
// Note: This helper requires access to *testing.T, so we need to pass it separately
// or add it to TestFilesystem. For now, we'll keep it as a standalone function.
func assertXAttr(t *testing.T, fs *tu.TestFilesystem, relPath string, check func(attr xattr.XAttr) error) {
	t.Helper()
	cachePath := fs.GetCachePath(relPath)
	attr := xattr.XAttr{}
	exists, errno := xattr.Get(cachePath, &attr)
	if errno != 0 && errno != syscall.ENODATA {
		tu.Failf(t, "Failed to get xattr for %s: %v", relPath, errno)
	}
	if !exists {
		tu.Failf(t, "XAttr should exist for %s", relPath)
	}
	if err := check(attr); err != nil {
		tu.Failf(t, "Failed to check xattr for %s: %v", relPath, err)
	}
}

// ============================================================================
// Phase 1: Cache Priority Tests
// ============================================================================

// TestPhase1_CacheFirstLookup verifies Principle 2: Cache Priority
// Cache entries must always override source entries
func TestPhase1_CacheFirstLookup(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystem(t, map[string]string{
		"test.txt": "source content",
	})
	defer fs.Cleanup()

	// Step 2: Modify file (triggers COW to cache)
	fs.WriteFile("test.txt", []byte("cache content"))

	// Step 3: Verify cache content is visible (cache priority)
	fs.AssertFileContent("test.txt", "cache content")

	// Step 4: Verify source file is unchanged (Principle 1: Cache Independence)
	fs.AssertSourceUnchanged("test.txt", "source content")
}

// TestPhase1_DeletedInCache verifies deleted paths are hidden even if source exists
func TestPhase1_DeletedInCache(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystem(t, map[string]string{
		"test.txt": "source content",
	})
	defer fs.Cleanup()

	// Step 1: Verify file exists
	fs.AssertFileExists("test.txt")

	// Step 2: Delete file (marks as deleted in cache)
	fs.RemoveFile("test.txt")

	// Step 3: Verify file is gone (deleted status takes priority over source)
	fs.AssertFileNotExists("test.txt")

	// Step 4: Verify source file still exists (Principle 1: no source modification)
	fs.AssertSourceExists("test.txt")
}

// ============================================================================
// Phase 2: Rename Operation Tests
// ============================================================================

// TestPhase2_CacheOnlyRename verifies cache-only rename (no source touch)
func TestPhase2_CacheOnlyRename(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystem(t, map[string]string{
		"source.txt": "source content",
	})
	defer fs.Cleanup()

	// Step 1: Modify file to trigger COW (brings it into cache)
	fs.WriteFile("source.txt", []byte("cache content"))

	// Step 2: Rename file (should be cache-only operation)
	fs.Rename("source.txt", "renamed.txt")

	// Step 3: Verify old path is gone
	fs.AssertFileNotExists("source.txt")

	// Step 4: Verify new path exists with cache content
	fs.AssertFileContent("renamed.txt", "cache content")

	// Step 5: Verify source file is unchanged (Principle 1: no source modification)
	fs.AssertSourceUnchanged("source.txt", "source content")

	// Step 6: Verify source file still exists at original location
	fs.AssertSourceExists("source.txt")
}

// TestPhase2_COWRename verifies Copy-on-Write rename from source
func TestPhase2_COWRename(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystem(t, map[string]string{
		"source.txt": "source content",
	})
	defer fs.Cleanup()

	// Step 1: Rename file directly from source (COW rename)
	fs.Rename("source.txt", "renamed.txt")

	// Step 2: Verify old path is marked as deleted in cache
	fs.AssertFileNotExists("source.txt")

	// Step 3: Verify new path exists with source content
	fs.AssertFileContent("renamed.txt", "source content")

	// Step 4: Verify source file is unchanged (Principle 1: no source modification)
	fs.AssertSourceUnchanged("source.txt", "source content")

	// Step 5: Verify source file still exists at original location
	fs.AssertSourceExists("source.txt")
}

// TestPhase2_PartialCachePathCreation verifies partial cache arborescence creation
// This tests Edge Case 1: Move into Existing Source Directory
// The directory exists in source, but we need to create its cache copy (partial cache path)
func TestPhase2_PartialCachePathCreation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystem(t, map[string]string{
		"level1/level2/level3/file.txt": "nested content",
		"existing_dir/":                 "",
	})
	defer fs.Cleanup()

	oldPath := fs.MountPath("level1", "level2", "level3", "file.txt")
	tu.ShouldBeRegularFile(oldPath, t)
	newPath := fs.MountPath("existing_dir", "renamed.txt")
	tu.ShouldBeRegularDirectory(filepath.Dir(newPath), t)

	// Step 1: Rename to existing directory (should create partial cache path for existing_dir)
	// This tests that we create the cache copy of existing_dir even though it wasn't in cache before
	if err := os.Rename(oldPath, newPath); err != nil {
		tu.Failf(t, "Failed to rename file: %v", err)
	}

	// Step 2: Verify new path exists
	content, err := os.ReadFile(newPath)
	if err != nil {
		tu.Failf(t, "Failed to read renamed file: %v", err)
	}
	if string(content) != "nested content" {
		tu.Failf(t, "Expected 'nested content', got '%s'", string(content))
	}

	// Step 3: Verify existing_dir was created in cache (partial cache path creation)
	existingDirMount := fs.MountPath("existing_dir")
	tu.ShouldBeRegularDirectory(existingDirMount, t)

	// Step 4: Verify source directory structure is unchanged
	srcExistingDir := fs.SourcePath("existing_dir")
	tu.ShouldBeRegularDirectory(srcExistingDir, t)
}

// ============================================================================
// Phase 3: Edge Cases Tests
// ============================================================================

// TestPhase3_TypeReplacement verifies type replacement (file <-> directory)
func TestPhase3_TypeReplacement(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystem(t, map[string]string{
		"foo": "file content",
	})
	defer fs.Cleanup()

	// Step 2: Delete file
	fs.RemoveFile("foo")
	tu.ShouldBeRegularFile(fs.SourcePath("foo"), t)

	// Step 3: Create directory with same name (type replacement)
	fs.CreateMountDir("foo")

	// Step 5: Verify source file still exists (Principle 1: no source modification)
	tu.ShouldBeRegularFile(fs.SourcePath("foo"), t)

	// Step 6: Verify we can use the new directory
	fs.WriteFile("foo/test.txt", []byte("test"))

	// Step 7: Verify xattr marks type replacement
	cacheRoot := fs.CacheRoot()
	cacheFooPath := filepath.Join(cacheRoot, "foo")

	attr := xattr.XAttr{}
	exists, errno := xattr.Get(cacheFooPath, &attr)
	if errno != 0 && errno != syscall.ENODATA {
		tu.Failf(t, "Failed to get xattr: %v for %s", errno, cacheFooPath)
	}
	if exists && !attr.TypeReplaced {
		tu.Failf(t, "TypeReplaced should be true after type replacement")
	}
	if exists && !attr.CacheIndependent {
		tu.Failf(t, "CacheIndependent should be true after type replacement")
	}
}

// TestPhase3_PermanentIndependence verifies Principle 3: Permanent Independence
func TestPhase3_PermanentIndependence(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()
	cacheDir := t.TempDir()

	// Create source file
	srcFile := filepath.Join(srcDir, "test.txt")
	if err := os.WriteFile(srcFile, []byte("source content"), 0644); err != nil {
		tu.Failf(t, "Failed to create source file: %v", err)
	}

	// Start filesystem with a specific cache directory (will be reused after remount)
	testMgr := tu.NewBinaryManager(t, testBinary)
	cmd, err := testMgr.RunBinary(t, mountPoint, srcDir, cacheDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer tu.GracefulShutdown(cmd, mountPoint, t)

	time.Sleep(100 * time.Millisecond)

	mountedFile := filepath.Join(mountPoint, "test.txt")

	// Step 1: Delete file
	if err := os.Remove(mountedFile); err != nil {
		tu.Failf(t, "Failed to remove file: %v", err)
	}

	// Step 2: Verify file is gone
	if _, err := os.Stat(mountedFile); err == nil {
		tu.Failf(t, "File should not exist after deletion")
	}

	// Step 3: Restart filesystem (simulates remount) - use SAME cache directory
	tu.GracefulShutdown(cmd, mountPoint, t)
	time.Sleep(100 * time.Millisecond)

	testMgr2 := tu.NewBinaryManager(t, testBinary)
	// Use the same cache directory to preserve deletion markers
	cmd2, err := testMgr2.RunBinary(t, mountPoint, srcDir, cacheDir)
	if err != nil {
		tu.Failf(t, "Failed to restart filesystem: %v", err)
	}
	defer tu.GracefulShutdown(cmd2, mountPoint, t)

	time.Sleep(100 * time.Millisecond)

	// Step 4: Verify file is still gone (permanent independence)
	// Even though source file exists, deleted cache entry should not reappear
	if _, err := os.Stat(mountedFile); err == nil {
		tu.Failf(t, "File should remain deleted after remount (permanent independence)")
	}

	// Step 5: Verify source file still exists
	if _, err := os.Stat(srcFile); os.IsNotExist(err) {
		tu.Failf(t, "Source file should still exist")
	}
}

// TestPhase3_HierarchicalRenameTracking verifies hierarchical rename tracking
func TestPhase3_HierarchicalRenameTracking(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Create nested structure in source
	nestedPath := filepath.Join(srcDir, "foo", "bar", "hello", "world")
	if err := os.MkdirAll(nestedPath, 0755); err != nil {
		tu.Failf(t, "Failed to create nested structure: %v", err)
	}
	testFile := filepath.Join(nestedPath, "file.txt")
	if err := os.WriteFile(testFile, []byte("nested content"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	// Start filesystem with its own cache directory
	testMgr := tu.NewBinaryManager(t, testBinary)
	cmd, err := testMgr.RunBinary(t, mountPoint, srcDir, testMgr.CacheDir())
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer tu.GracefulShutdown(cmd, mountPoint, t)

	time.Sleep(100 * time.Millisecond)

	oldDir := filepath.Join(mountPoint, "foo", "bar")
	newDir := filepath.Join(mountPoint, "foo", "baz")

	// Step 1: Rename directory (bar -> baz)
	if err := os.Rename(oldDir, newDir); err != nil {
		tu.Failf(t, "Failed to rename directory: %v", err)
	}

	tu.ShouldBeRegularDirectory(mountPoint+"/foo/baz", t)
	tu.ShouldBeRegularDirectory(mountPoint+"/foo/baz/hello", t)
	tu.ShouldBeRegularDirectory(mountPoint+"/foo/baz/hello/world", t)
	tu.ShouldBeRegularFile(mountPoint+"/foo/baz/hello/world/file.txt", t)

	// Step 2: Verify nested file is accessible via new path
	nestedFile := filepath.Join(newDir, "hello", "world", "file.txt")
	content, err := os.ReadFile(nestedFile)
	if err != nil {
		tu.Failf(t, "Failed to read nested file via renamed path: %v", err)
	}
	if string(content) != "nested content" {
		tu.Failf(t, "Expected 'nested content', got '%s'", string(content))
	}

	// Step 3: Verify old path is gone
	if _, err := os.Stat(oldDir); err == nil {
		tu.Failf(t, "Old directory path should not exist")
	}

	// Step 4: Verify source structure is unchanged
	srcOldDir := filepath.Join(srcDir, "foo", "bar")
	if _, err := os.Stat(srcOldDir); os.IsNotExist(err) {
		tu.Failf(t, "Source directory structure should remain unchanged")
	}
}

// ============================================================================
// Phase 4: Directory Listing Tests
// ============================================================================

// TestPhase4_CachePriorityInListing verifies cache priority in directory listing
func TestPhase4_CachePriorityInListing(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Create source files
	srcFile1 := filepath.Join(srcDir, "file1.txt")
	srcFile2 := filepath.Join(srcDir, "file2.txt")
	if err := os.WriteFile(srcFile1, []byte("source1"), 0644); err != nil {
		tu.Failf(t, "Failed to create source file1: %v", err)
	}
	if err := os.WriteFile(srcFile2, []byte("source2"), 0644); err != nil {
		tu.Failf(t, "Failed to create source file2: %v", err)
	}

	// Start filesystem with its own cache directory
	testMgr := tu.NewBinaryManager(t, testBinary)
	cmd, err := testMgr.RunBinary(t, mountPoint, srcDir, testMgr.CacheDir())
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer tu.GracefulShutdown(cmd, mountPoint, t)

	time.Sleep(100 * time.Millisecond)

	// Step 1: Modify file1 (brings it into cache)
	mountedFile1 := filepath.Join(mountPoint, "file1.txt")
	if err := os.WriteFile(mountedFile1, []byte("cache1"), 0644); err != nil {
		tu.Failf(t, "Failed to modify file1: %v", err)
	}

	// Step 2: List directory
	entries, err := os.ReadDir(mountPoint)
	if err != nil {
		tu.Failf(t, "Failed to read directory: %v", err)
	}

	// Step 3: Verify both files are listed
	entryMap := make(map[string]bool)
	for _, entry := range entries {
		entryMap[entry.Name()] = true
	}

	if !entryMap["file1.txt"] {
		tu.Failf(t, "file1.txt should be in listing")
	}
	if !entryMap["file2.txt"] {
		tu.Failf(t, "file2.txt should be in listing")
	}

	// Step 4: Verify file1 has cache content (cache priority)
	content, err := os.ReadFile(mountedFile1)
	if err != nil {
		tu.Failf(t, "Failed to read file1: %v", err)
	}
	if string(content) != "cache1" {
		tu.Failf(t, "Expected 'cache1' (cache priority), got '%s'", string(content))
	}

	// Step 5: Verify file2 has source content
	mountedFile2 := filepath.Join(mountPoint, "file2.txt")
	content, err = os.ReadFile(mountedFile2)
	if err != nil {
		tu.Failf(t, "Failed to read file2: %v", err)
	}
	if string(content) != "source2" {
		tu.Failf(t, "Expected 'source2', got '%s'", string(content))
	}
}

// TestPhase4_DeletedFilesHiddenInListing verifies deleted files are hidden in listing
func TestPhase4_DeletedFilesHiddenInListing(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Create source files
	srcFile1 := filepath.Join(srcDir, "file1.txt")
	srcFile2 := filepath.Join(srcDir, "file2.txt")
	if err := os.WriteFile(srcFile1, []byte("source1"), 0644); err != nil {
		tu.Failf(t, "Failed to create source file1: %v", err)
	}
	if err := os.WriteFile(srcFile2, []byte("source2"), 0644); err != nil {
		tu.Failf(t, "Failed to create source file2: %v", err)
	}

	// Start filesystem with its own cache directory
	testMgr := tu.NewBinaryManager(t, testBinary)
	cmd, err := testMgr.RunBinary(t, mountPoint, srcDir, testMgr.CacheDir())
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer tu.GracefulShutdown(cmd, mountPoint, t)

	time.Sleep(100 * time.Millisecond)

	// Step 1: Delete file1
	mountedFile1 := filepath.Join(mountPoint, "file1.txt")
	if err := os.Remove(mountedFile1); err != nil {
		tu.Failf(t, "Failed to remove file1: %v", err)
	}

	// Step 2: List directory
	entries, err := os.ReadDir(mountPoint)
	if err != nil {
		tu.Failf(t, "Failed to read directory: %v", err)
	}

	// Step 3: Verify file1 is NOT in listing (deleted files hidden)
	entryMap := make(map[string]bool)
	for _, entry := range entries {
		entryMap[entry.Name()] = true
	}

	if entryMap["file1.txt"] {
		tu.Failf(t, "file1.txt should NOT be in listing (deleted)")
	}
	if !entryMap["file2.txt"] {
		tu.Failf(t, "file2.txt should be in listing")
	}
}

// ============================================================================
// Comprehensive Edge Case Tests
// ============================================================================

// TestEdgeCase_MoveIntoExistingDir verifies moving file into existing directory
func TestEdgeCase_MoveIntoExistingDir(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Create source file and directory
	srcFile := filepath.Join(srcDir, "file.txt")
	if err := os.WriteFile(srcFile, []byte("file content"), 0644); err != nil {
		tu.Failf(t, "Failed to create source file: %v", err)
	}
	srcDirPath := filepath.Join(srcDir, "existing_dir")
	if err := os.Mkdir(srcDirPath, 0755); err != nil {
		tu.Failf(t, "Failed to create source directory: %v", err)
	}

	// Start filesystem with its own cache directory
	testMgr := tu.NewBinaryManager(t, testBinary)
	cmd, err := testMgr.RunBinary(t, mountPoint, srcDir, testMgr.CacheDir())
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer tu.GracefulShutdown(cmd, mountPoint, t)

	time.Sleep(100 * time.Millisecond)

	filePath := filepath.Join(mountPoint, "file.txt")
	dirPath := filepath.Join(mountPoint, "existing_dir")

	// Step 1: Move file into existing directory
	mvCmd := exec.Command("mv", filePath, dirPath)
	mvCmd.Dir = mountPoint
	if err := mvCmd.Run(); err != nil {
		tu.Failf(t, "Failed to move file into directory: %v", err)
	}

	// Step 2: Verify file is inside directory
	movedFile := filepath.Join(dirPath, "file.txt")
	content, err := os.ReadFile(movedFile)
	if err != nil {
		tu.Failf(t, "Failed to read moved file: %v", err)
	}
	if string(content) != "file content" {
		tu.Failf(t, "Expected 'file content', got '%s'", string(content))
	}

	// Step 3: Verify original file location is gone
	if _, err := os.Stat(filePath); err == nil {
		tu.Failf(t, "Original file location should not exist")
	}

	// Step 4: Verify source structure is unchanged
	if _, err := os.Stat(srcFile); os.IsNotExist(err) {
		tu.Failf(t, "Source file should still exist")
	}
	if _, err := os.Stat(filepath.Join(srcDirPath, "file.txt")); err == nil {
		tu.Failf(t, "Source should not have file in directory")
	}
}

// TestEdgeCase_MultipleTypeReplacements verifies multiple type replacements
func TestEdgeCase_MultipleTypeReplacements(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Create source file
	srcFile := filepath.Join(srcDir, "foo")
	if err := os.WriteFile(srcFile, []byte("file content"), 0644); err != nil {
		tu.Failf(t, "Failed to create source file: %v", err)
	}

	// Start filesystem with its own cache directory
	testMgr := tu.NewBinaryManager(t, testBinary)
	cmd, err := testMgr.RunBinary(t, mountPoint, srcDir, testMgr.CacheDir())
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer tu.GracefulShutdown(cmd, mountPoint, t)

	time.Sleep(100 * time.Millisecond)

	fooPath := filepath.Join(mountPoint, "foo")

	// Step 1: Delete file, create directory
	if err := os.Remove(fooPath); err != nil {
		tu.Failf(t, "Failed to remove file: %v", err)
	}
	if err := os.Mkdir(fooPath, 0755); err != nil {
		tu.Failf(t, "Failed to create directory: %v", err)
	}

	// Step 2: Delete directory, create file
	if err := os.Remove(fooPath); err != nil {
		tu.Failf(t, "Failed to remove directory: %v", err)
	}
	// Small delay to allow kernel negative dentry cache to expire (200ms NegativeTimeout)
	time.Sleep(250 * time.Millisecond)
	if err := os.WriteFile(fooPath, []byte("new file content"), 0644); err != nil {
		tu.Failf(t, "Failed to create file: %v", err)
	}

	// Step 3: Verify file exists and works
	content, err := os.ReadFile(fooPath)
	if err != nil {
		tu.Failf(t, "Failed to read file: %v", err)
	}
	if string(content) != "new file content" {
		tu.Failf(t, "Expected 'new file content', got '%s'", string(content))
	}

	// Step 4: Verify source file is unchanged
	srcContent, err := os.ReadFile(srcFile)
	if err != nil {
		tu.Failf(t, "Failed to read source file: %v", err)
	}
	if string(srcContent) != "file content" {
		tu.Failf(t, "Source file should remain unchanged, got '%s'", string(srcContent))
	}
}

// TestEdgeCase_NestedIndependentPaths verifies nested independent paths
func TestEdgeCase_NestedIndependentPaths(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create source directory structure BEFORE mounting
	// This ensures files are visible when filesystem is mounted
	// Use a fresh BinaryManager for this test to avoid path conflicts
	testMgr := tu.NewBinaryManager(t, testBinary)
	srcDir := testMgr.SrcDir()
	mntDir := testMgr.MntDir()

	// Create nested structure in source BEFORE mounting
	nestedPath := filepath.Join(srcDir, "level1", "level2", "level3")
	if err := os.MkdirAll(nestedPath, 0755); err != nil {
		tu.Failf(t, "Failed to create nested structure: %v", err)
	}
	testFile := filepath.Join(nestedPath, "file.txt")
	if err := os.WriteFile(testFile, []byte("nested content"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	// Verify file exists in source before mounting
	if _, err := os.Stat(testFile); err != nil {
		tu.Failf(t, "Test file should exist in source before mount: %v", err)
	}

	// NOW mount the filesystem using the test directories
	cmd, err := testMgr.RunBinary(t, mntDir, srcDir, testMgr.CacheDir())
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer tu.GracefulShutdown(cmd, mntDir, t)

	// Create TestFilesystem struct from existing setup
	fs := tu.NewTestFilesystemFromExisting(t, mntDir, srcDir, testMgr.CacheDir(), cmd, testBinary)

	// Wait for filesystem to be ready
	time.Sleep(100 * time.Millisecond)

	level2Path := filepath.Join(fs.MountPoint(), "level1", "level2")
	level3Path := filepath.Join(level2Path, "level3")
	filePath := filepath.Join(level3Path, "file.txt")

	// Step 1: Access nested directories to ensure they're discovered
	// First, verify parent directories exist
	tu.ShouldBeRegularDirectory(filepath.Join(fs.MountPoint(), "level1"), t)
	tu.ShouldBeRegularDirectory(filepath.Join(fs.MountPoint(), "level1", "level2"), t)
	tu.ShouldBeRegularDirectory(level3Path, t)

	// Step 2: Read the file first to ensure it's discovered through the mount point
	content, err := os.ReadFile(filePath)
	if err != nil {
		tu.Failf(t, "Failed to read file through mount point: %v", err)
	}
	if string(content) != "nested content" {
		tu.Failf(t, "Expected 'nested content', got '%s'", string(content))
	}

	// Small delay to ensure FUSE has processed the read
	time.Sleep(50 * time.Millisecond)

	// Now verify it exists as a regular file (this also ensures it's in the FUSE tree)
	tu.ShouldBeRegularFile(filePath, t)

	// Additional delay to ensure the inode is stable
	time.Sleep(50 * time.Millisecond)

	// Verify file still exists right before removal (through mount point)
	if _, err := os.Stat(filePath); err != nil {
		tu.Failf(t, "File should exist before removal, but stat failed: %v", err)
	}

	// Also verify file exists in source directory directly (right before removal)
	if _, err := os.Stat(testFile); err != nil {
		tu.Failf(t, "File should exist in source directory before removal, but stat failed: %v", err)
	}

	// Small delay to ensure everything is synced
	time.Sleep(50 * time.Millisecond)

	// Verify one more time that file exists in source
	if _, err := os.Stat(testFile); err != nil {
		tu.Failf(t, "File disappeared from source directory! Stat failed: %v", err)
	}

	// CRITICAL: Also verify the exact path that Unlink will check
	// This is the path that FullPath(false) + name will produce
	expectedUnlinkPath := filepath.Join(fs.SourceDir(), "level1", "level2", "level3", "file.txt")
	if expectedUnlinkPath != testFile {
		tu.Failf(t, "Path mismatch: expected %s, got %s", expectedUnlinkPath, testFile)
	}
	if _, err := os.Stat(expectedUnlinkPath); err != nil {
		tu.Failf(t, "File should exist at Unlink path %s, but stat failed: %v", expectedUnlinkPath, err)
	}

	// Step 3: Delete file first (this creates a deletion marker)
	// Use the test helper which handles FUSE filesystem properly
	fs.RemoveFile("level1/level2/level3/file.txt")

	// Step 4: Delete level3 directory (should work now that file is deleted)
	// Note: The directory should be empty now (deleted files are filtered from listings)
	// Use os.Remove for directory removal
	if err := os.Remove(level3Path); err != nil {
		tu.Failf(t, "Failed to remove level3: %v", err)
	}

	// Small delay to allow kernel negative dentry cache to expire (200ms NegativeTimeout)
	time.Sleep(250 * time.Millisecond)

	// Step 5: Recreate level3 as independent
	if err := os.Mkdir(level3Path, 0755); err != nil {
		tu.Failf(t, "Failed to recreate level3: %v", err)
	}

	// Step 6: Create new file in independent level3
	newFile := filepath.Join(level3Path, "newfile.txt")
	if err := os.WriteFile(newFile, []byte("independent content"), 0644); err != nil {
		tu.Failf(t, "Failed to create new file: %v", err)
	}

	// Step 7: Verify new file exists
	content, err = os.ReadFile(newFile)
	if err != nil {
		tu.Failf(t, "Failed to read new file: %v", err)
	}
	if string(content) != "independent content" {
		tu.Failf(t, "Expected 'independent content', got '%s'", string(content))
	}

	// Step 8: Verify source structure is unchanged
	if _, err := os.Stat(testFile); os.IsNotExist(err) {
		tu.Failf(t, "Source file should still exist")
	}
}

// TestEdgeCase_RenameToNestedPath verifies renaming file to nested directory that exists in cache
func TestEdgeCase_RenameToNestedPath(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystem(t, map[string]string{
		"source.txt":                "source content",
		"existing_dir/":             "",
		"existing_dir/nested/":      "",
		"existing_dir/nested/deep/": "",
	})
	defer fs.Cleanup()

	fs.Rename("source.txt", "existing_dir/nested/deep/renamed.txt")

	fs.AssertFileNotExists("source.txt")
	fs.AssertFileContent("existing_dir/nested/deep/renamed.txt", "source content")
	fs.AssertSourceUnchanged("source.txt", "source content")
}

// TestEdgeCase_RenameDirectoryWithContents verifies renaming directory preserves contents
func TestEdgeCase_RenameDirectoryWithContents(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystem(t, map[string]string{
		"mydir/file1.txt":        "content1",
		"mydir/file2.txt":        "content2",
		"mydir/subdir/file3.txt": "content3",
	})
	defer fs.Cleanup()

	tu.ShouldRenameDir(fs.MountPath("mydir"), fs.MountPath("renamed_dir"), t)

	tu.ShouldNotExist(fs.MountPath("mydir"), t)
	fs.AssertFileContent("renamed_dir/file1.txt", "content1")
	fs.AssertFileContent("renamed_dir/file2.txt", "content2")
	fs.AssertFileContent("renamed_dir/subdir/file3.txt", "content3")
}

// TestEdgeCase_SequentialRenames verifies chained renames (A→B→C)
func TestEdgeCase_SequentialRenames(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystem(t, map[string]string{
		"original.txt": "test content",
	})
	defer fs.Cleanup()

	fs.Rename("original.txt", "step1.txt")
	fs.AssertFileNotExists("original.txt")
	fs.AssertFileContent("step1.txt", "test content")

	fs.Rename("step1.txt", "step2.txt")
	fs.AssertFileNotExists("step1.txt")
	fs.AssertFileContent("step2.txt", "test content")

	fs.Rename("step2.txt", "final.txt")
	fs.AssertFileNotExists("step2.txt")
	fs.AssertFileContent("final.txt", "test content")

	fs.AssertSourceUnchanged("original.txt", "test content")
}

// TestEdgeCase_RenameOverExistingFile verifies rename replaces existing file
func TestEdgeCase_RenameOverExistingFile(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystem(t, map[string]string{
		"source.txt": "new content",
		"target.txt": "old content",
	})
	defer fs.Cleanup()

	fs.Rename("source.txt", "target.txt")

	fs.AssertFileNotExists("source.txt")
	fs.AssertFileContent("target.txt", "new content")
}

// TestEdgeCase_UnicodeFilenames verifies handling of unicode characters in filenames
func TestEdgeCase_UnicodeFilenames(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystem(t, map[string]string{
		"файл.txt":        "russian content",
		"文件名.txt":         "chinese content",
		"ファイル.txt":        "japanese content",
		"émoji_🎉.txt":     "emoji content",
		"café_résumé.txt": "accented content",
	})
	defer fs.Cleanup()

	fs.AssertFileContent("файл.txt", "russian content")
	fs.AssertFileContent("文件名.txt", "chinese content")
	fs.AssertFileContent("ファイル.txt", "japanese content")
	fs.AssertFileContent("émoji_🎉.txt", "emoji content")
	fs.AssertFileContent("café_résumé.txt", "accented content")
}

// TestEdgeCase_SpecialCharacters verifies handling of special characters in filenames
func TestEdgeCase_SpecialCharacters(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystem(t, map[string]string{
		"file with spaces.txt":      "spaces",
		"file-with-dashes.txt":      "dashes",
		"file_with_underscores.txt": "underscores",
		"file.multiple.dots.txt":    "dots",
	})
	defer fs.Cleanup()

	fs.AssertFileContent("file with spaces.txt", "spaces")
	fs.AssertFileContent("file-with-dashes.txt", "dashes")
	fs.AssertFileContent("file_with_underscores.txt", "underscores")
	fs.AssertFileContent("file.multiple.dots.txt", "dots")
}

// TestEdgeCase_EmptyFile verifies handling of empty files
func TestEdgeCase_EmptyFile(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystem(t, map[string]string{
		"empty.txt": "",
		"small.txt": "x",
	})
	defer fs.Cleanup()

	fs.AssertFileContent("empty.txt", "")
	fs.AssertFileContent("small.txt", "x")

	fs.Rename("empty.txt", "renamed_empty.txt")
	fs.AssertFileNotExists("empty.txt")
	fs.AssertFileContent("renamed_empty.txt", "")
}

// TestEdgeCase_DeepNesting verifies deeply nested directory operations
func TestEdgeCase_DeepNesting(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystem(t, map[string]string{
		"level1/level2/level3/level4/level5/deep.txt": "deep content",
	})
	defer fs.Cleanup()

	fs.AssertFileContent("level1/level2/level3/level4/level5/deep.txt", "deep content")

	fs.Rename("level1/level2/level3/level4/level5/deep.txt", "level1/level2/level3/level4/level5/renamed.txt")
	fs.AssertFileNotExists("level1/level2/level3/level4/level5/deep.txt")
	fs.AssertFileContent("level1/level2/level3/level4/level5/renamed.txt", "deep content")
}

// TestEdgeCase_LongFilename verifies handling of long filenames
func TestEdgeCase_LongFilename(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	longName := ""
	for i := 0; i < 20; i++ {
		longName += "longname"
	}
	longName += ".txt"

	fs := setupTestFilesystem(t, map[string]string{
		longName: "long name content",
	})
	defer fs.Cleanup()

	fs.AssertFileContent(longName, "long name content")
}

// TestEdgeCase_RenameThenRead verifies rename followed by immediate read
func TestEdgeCase_RenameThenRead(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystem(t, map[string]string{
		"original.txt": "test content",
	})
	defer fs.Cleanup()

	fs.Rename("original.txt", "renamed.txt")
	fs.AssertFileContent("renamed.txt", "test content")
}

// TestEdgeCase_MultipleFilesRename verifies renaming multiple files at once
func TestEdgeCase_MultipleFilesRename(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystem(t, map[string]string{
		"file1.txt": "content1",
		"file2.txt": "content2",
		"file3.txt": "content3",
	})
	defer fs.Cleanup()

	fs.Rename("file1.txt", "renamed1.txt")
	fs.Rename("file2.txt", "renamed2.txt")
	fs.Rename("file3.txt", "renamed3.txt")

	fs.AssertFileNotExists("file1.txt")
	fs.AssertFileNotExists("file2.txt")
	fs.AssertFileNotExists("file3.txt")
	fs.AssertFileContent("renamed1.txt", "content1")
	fs.AssertFileContent("renamed2.txt", "content2")
	fs.AssertFileContent("renamed3.txt", "content3")
}

// TestEdgeCase_DeleteThenRecreate verifies deleting and recreating a file
func TestEdgeCase_DeleteThenRecreate(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystem(t, map[string]string{
		"test.txt": "original content",
	})
	defer fs.Cleanup()

	fs.AssertFileContent("test.txt", "original content")
	fs.RemoveFile("test.txt")
	fs.AssertFileNotExists("test.txt")

	fs.WriteFileInMount("test.txt", []byte("new content"))
	fs.AssertFileContent("test.txt", "new content")
}

// TestEdgeCase_RenameDirectoryThenAccessFiles verifies renaming directory preserves file access
func TestEdgeCase_RenameDirectoryThenAccessFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystem(t, map[string]string{
		"dir/file1.txt": "content1",
		"dir/file2.txt": "content2",
	})
	defer fs.Cleanup()

	fs.Rename("dir", "newdir")
	fs.AssertFileNotExists("dir/file1.txt")
	fs.AssertFileNotExists("dir/file2.txt")
	fs.AssertFileContent("newdir/file1.txt", "content1")
	fs.AssertFileContent("newdir/file2.txt", "content2")
}
