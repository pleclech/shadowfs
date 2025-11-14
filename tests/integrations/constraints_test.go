//go:build linux && integration
// +build linux,integration

package integrations

import (
	"crypto/sha256"
	"fmt"
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
	defer tu.SetupTestMain(m, testBinary, "info")()

	// Run all tests
	os.Exit(m.Run())
}

// ============================================================================
// Test Helpers (DRY)
// ============================================================================

// testFilesystem represents a test filesystem setup
type testFilesystem struct {
	mountPoint string
	srcDir     string
	cmd        *exec.Cmd
	t          *testing.T
}

// setupTestFilesystem creates a new test filesystem and starts it
func setupTestFilesystem(t *testing.T) *testFilesystem {
	t.Helper()

	// Each test gets its own BinaryManager with its own cache directory
	testMgr := tu.NewBinaryManager(t, testBinary)

	cmd, err := testMgr.RunBinary(t, testMgr.MntDir(), testMgr.SrcDir(), testMgr.CacheDir())
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	return &testFilesystem{
		mountPoint: testMgr.MntDir(),
		srcDir:     testMgr.SrcDir(),
		cmd:        cmd,
		t:          t,
	}
}

// cleanup shuts down the test filesystem
func (fs *testFilesystem) cleanup() {
	tu.GracefulShutdown(fs.cmd, fs.mountPoint, fs.t)
}

// createSourceFile creates a file in the source directory
func (fs *testFilesystem) createSourceFile(relPath string, content []byte) string {
	fullPath := fs.sourcePath(relPath)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		tu.Failf(fs.t, "Failed to create source directory %s: %v", dir, err)
	}
	tu.ShouldCreateFile(fullPath, string(content), fs.t)
	return fullPath
}

// createSourceDir creates a directory in the source directory
func (fs *testFilesystem) createSourceDir(relPath string) string {
	fullPath := fs.sourcePath(relPath)
	tu.ShouldCreateDir(fullPath, fs.t)
	return fullPath
}

// mountPath returns the mount point path for a relative path
func (fs *testFilesystem) mountPath(relPath string) string {
	return filepath.Join(fs.mountPoint, relPath)
}

// sourcePath returns the source directory path for a relative path
func (fs *testFilesystem) sourcePath(relPath string) string {
	return filepath.Join(fs.srcDir, relPath)
}

// readFile reads a file from the mount point and returns its content
func (fs *testFilesystem) readFile(relPath string) string {
	return tu.ReadFileContent(fs.mountPath(relPath), fs.t)
}

// writeFile writes content to a file in the mount point
// This overwrites existing files (truncates first)
// Note: The filesystem strips O_TRUNC flag and does copy-on-write in Write() when off==0,
// which can leave trailing bytes. We work around this by writing in a way that avoids COW.
func (fs *testFilesystem) writeFile(relPath string, content []byte) {
	fs.t.Helper()
	fullPath := fs.mountPath(relPath)

	// Open file for writing (filesystem strips O_TRUNC)
	file, err := os.OpenFile(fullPath, os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		tu.Failf(fs.t, "Failed to open file %s for writing: %v", relPath, err)
	}

	// Manually truncate to 0 to clear any existing content
	// This must happen BEFORE any write to avoid COW copying source content
	if err := file.Truncate(0); err != nil {
		file.Close()
		tu.Failf(fs.t, "Failed to truncate file %s: %v", relPath, err)
	}

	// Seek to beginning
	if _, err := file.Seek(0, 0); err != nil {
		file.Close()
		tu.Failf(fs.t, "Failed to seek file %s: %v", relPath, err)
	}

	// Write all content
	written, err := file.Write(content)
	if err != nil {
		file.Close()
		tu.Failf(fs.t, "Failed to write file %s: %v", relPath, err)
	}

	// Truncate again after write to ensure no trailing bytes
	// This handles the case where COW copied source content
	if err := file.Truncate(int64(len(content))); err != nil {
		file.Close()
		tu.Failf(fs.t, "Failed to truncate file %s after write: %v", relPath, err)
	}

	// Sync and close
	if err := file.Sync(); err != nil {
		file.Close()
		tu.Failf(fs.t, "Failed to sync file %s: %v", relPath, err)
	}
	if err := file.Close(); err != nil {
		tu.Failf(fs.t, "Failed to close file %s: %v", relPath, err)
	}

	if written != len(content) {
		tu.Failf(fs.t, "Failed to write all content to %s: wrote %d of %d bytes", relPath, written, len(content))
	}

	// Small delay to ensure FUSE has processed the write
	time.Sleep(50 * time.Millisecond)

	// Verify the write succeeded by reading back
	actualContent := fs.readFile(relPath)
	if actualContent != string(content) {
		tu.Failf(fs.t, "File content mismatch after write: expected %q (%d bytes), got %q (%d bytes)",
			string(content), len(content), actualContent, len(actualContent))
	}
}

// removeFile removes a file from the mount point
func (fs *testFilesystem) removeFile(relPath string) {
	tu.ShouldRemoveFile(fs.mountPath(relPath), fs.t)
}

// mkdir creates a directory in the mount point
func (fs *testFilesystem) mkdir(relPath string) {
	tu.ShouldCreateDir(fs.mountPath(relPath), fs.t)
}

// rename renames a file/directory in the mount point
func (fs *testFilesystem) rename(oldPath, newPath string) {
	fs.t.Helper()
	oldFullPath := fs.mountPath(oldPath)
	newFullPath := fs.mountPath(newPath)

	// Check if it's a directory to use appropriate helper
	if info, err := os.Stat(oldFullPath); err == nil && info.IsDir() {
		tu.ShouldRenameDir(oldFullPath, newFullPath, fs.t)
	} else {
		tu.ShouldRenameFile(oldFullPath, newFullPath, fs.t)
	}
}

// assertFileExists verifies a file exists in the mount point
func (fs *testFilesystem) assertFileExists(relPath string) {
	tu.ShouldExist(fs.mountPath(relPath), fs.t)
}

// assertFileNotExists verifies a file does not exist in the mount point
func (fs *testFilesystem) assertFileNotExists(relPath string) {
	tu.ShouldNotExist(fs.mountPath(relPath), fs.t)
}

// assertFileContent verifies file content matches expected
func (fs *testFilesystem) assertFileContent(relPath, expected string) {
	tu.ShouldHaveSameContent(fs.mountPath(relPath), expected, fs.t)
}

// assertSourceUnchanged verifies source file content is unchanged
func (fs *testFilesystem) assertSourceUnchanged(relPath, expected string) {
	tu.ShouldHaveSameContent(fs.sourcePath(relPath), expected, fs.t)
}

// assertSourceExists verifies source file exists
func (fs *testFilesystem) assertSourceExists(relPath string) {
	tu.ShouldExist(fs.sourcePath(relPath), fs.t)
}

// assertSourceNotExists verifies source file does not exist
func (fs *testFilesystem) assertSourceNotExists(relPath string) {
	tu.ShouldNotExist(fs.sourcePath(relPath), fs.t)
}

// listDir lists directory entries from mount point
func (fs *testFilesystem) listDir(relPath string) []os.DirEntry {
	return tu.ListDir(fs.mountPath(relPath), fs.t)
}

// getCachePath returns the cache path for a given mount point and source directory
func (fs *testFilesystem) getCachePath(relPath string) string {
	return tu.GetCachePath(fs.mountPoint, fs.srcDir, relPath)
}

// assertXAttr verifies xattr fields
func (fs *testFilesystem) assertXAttr(relPath string, check func(attr xattr.XAttr) error) {
	fs.t.Helper()
	cachePath := fs.getCachePath(relPath)
	attr := xattr.XAttr{}
	exists, errno := xattr.Get(cachePath, &attr)
	if errno != 0 && errno != syscall.ENODATA {
		tu.Failf(
			fs.t, "Failed to get xattr for %s: %v", relPath, errno)
	}
	if !exists {
		tu.Failf(
			fs.t, "XAttr should exist for %s", relPath)
	}
	if err := check(attr); err != nil {
		tu.Failf(
			fs.t, "Failed to check xattr for %s: %v", relPath, err)
	}
}

// restart restarts the filesystem
func (fs *testFilesystem) restart() {
	fs.t.Helper()
	fs.cleanup()
	tu.WaitForFilesystemReady(0)

	testMgr := tu.NewBinaryManager(fs.t, testBinary)
	cmd, err := testMgr.RunBinary(fs.t, fs.mountPoint, fs.srcDir, testMgr.CacheDir())
	if err != nil {
		tu.Failf(
			fs.t, "Failed to restart filesystem: %v", err)
	}
	fs.cmd = cmd
	tu.WaitForFilesystemReady(0)
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

	fs := setupTestFilesystem(t)
	defer fs.cleanup()

	// Create source file
	fs.createSourceFile("test.txt", []byte("source content"))

	// Step 1: Verify source file is visible
	fs.assertFileContent("test.txt", "source content")

	// Step 2: Modify file (triggers COW to cache)
	fs.writeFile("test.txt", []byte("cache content"))

	// Step 3: Verify cache content is visible (cache priority)
	fs.assertFileContent("test.txt", "cache content")

	// Step 4: Verify source file is unchanged (Principle 1: Cache Independence)
	fs.assertSourceUnchanged("test.txt", "source content")
}

// TestPhase1_DeletedInCache verifies deleted paths are hidden even if source exists
func TestPhase1_DeletedInCache(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystem(t)
	defer fs.cleanup()

	// Create source file
	fs.createSourceFile("test.txt", []byte("source content"))

	// Step 1: Verify file exists
	fs.assertFileExists("test.txt")

	// Step 2: Delete file (marks as deleted in cache)
	fs.removeFile("test.txt")

	// Step 3: Verify file is gone (deleted status takes priority over source)
	fs.assertFileNotExists("test.txt")

	// Step 4: Verify source file still exists (Principle 1: no source modification)
	fs.assertSourceExists("test.txt")
}

// ============================================================================
// Phase 2: Rename Operation Tests
// ============================================================================

// TestPhase2_CacheOnlyRename verifies cache-only rename (no source touch)
func TestPhase2_CacheOnlyRename(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystem(t)
	defer fs.cleanup()

	// Create source file
	fs.createSourceFile("source.txt", []byte("source content"))

	// Step 1: Modify file to trigger COW (brings it into cache)
	fs.writeFile("source.txt", []byte("cache content"))

	// Step 2: Rename file (should be cache-only operation)
	fs.rename("source.txt", "renamed.txt")

	// Step 3: Verify old path is gone
	fs.assertFileNotExists("source.txt")

	// Step 4: Verify new path exists with cache content
	fs.assertFileContent("renamed.txt", "cache content")

	// Step 5: Verify source file is unchanged (Principle 1: no source modification)
	fs.assertSourceUnchanged("source.txt", "source content")

	// Step 6: Verify source file still exists at original location
	fs.assertSourceExists("source.txt")
}

// TestPhase2_COWRename verifies Copy-on-Write rename from source
func TestPhase2_COWRename(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystem(t)
	defer fs.cleanup()

	// Create source file
	fs.createSourceFile("source.txt", []byte("source content"))

	// Step 1: Rename file directly from source (COW rename)
	fs.rename("source.txt", "renamed.txt")

	// Step 2: Verify old path is marked as deleted in cache
	fs.assertFileNotExists("source.txt")

	// Step 3: Verify new path exists with source content
	fs.assertFileContent("renamed.txt", "source content")

	// Step 4: Verify source file is unchanged (Principle 1: no source modification)
	fs.assertSourceUnchanged("source.txt", "source content")

	// Step 5: Verify source file still exists at original location
	fs.assertSourceExists("source.txt")
}

// TestPhase2_PartialCachePathCreation verifies partial cache arborescence creation
// This tests Edge Case 1: Move into Existing Source Directory
// The directory exists in source, but we need to create its cache copy (partial cache path)
func TestPhase2_PartialCachePathCreation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Create source file in nested directory
	nestedDir := filepath.Join(srcDir, "level1", "level2", "level3")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		tu.Failf(t, "Failed to create nested directory: %v", err)
	}
	srcFile := filepath.Join(nestedDir, "file.txt")
	if err := os.WriteFile(srcFile, []byte("nested content"), 0644); err != nil {
		tu.Failf(t, "Failed to create source file: %v", err)
	}

	// Create existing target directory in source (this is the key - it exists in source)
	existingDir := filepath.Join(srcDir, "existing_dir")
	if err := os.Mkdir(existingDir, 0755); err != nil {
		tu.Failf(t, "Failed to create existing directory in source: %v", err)
	}

	// Start filesystem with its own cache directory
	testMgr := tu.NewBinaryManager(t, testBinary)
	cmd, err := testMgr.RunBinary(t, mountPoint, srcDir, testMgr.CacheDir())
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer tu.GracefulShutdown(cmd, mountPoint, t)

	time.Sleep(100 * time.Millisecond)

	oldPath := filepath.Join(mountPoint, "level1", "level2", "level3", "file.txt")
	newPath := filepath.Join(mountPoint, "existing_dir", "renamed.txt")

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
	existingDirMount := filepath.Join(mountPoint, "existing_dir")
	if stat, err := os.Stat(existingDirMount); err != nil {
		tu.Failf(t, "Existing directory should exist in mount: %v", err)
	} else if !stat.IsDir() {
		tu.Failf(t, "Existing directory should be a directory")
	}

	// Step 4: Verify source directory structure is unchanged
	srcExistingDir := filepath.Join(srcDir, "existing_dir")
	if _, err := os.Stat(srcExistingDir); os.IsNotExist(err) {
		tu.Failf(t, "Source existing_dir should still exist")
	}
}

// ============================================================================
// Phase 3: Edge Cases Tests
// ============================================================================

// TestPhase3_TypeReplacement verifies type replacement (file <-> directory)
func TestPhase3_TypeReplacement(t *testing.T) {
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

	// Step 1: Verify file exists
	if stat, err := os.Stat(fooPath); err != nil {
		tu.Failf(t, "File should exist: %v", err)
	} else if stat.IsDir() {
		tu.Failf(t, "Should be a file initially")
	}

	// Step 2: Delete file
	if err := os.Remove(fooPath); err != nil {
		tu.Failf(t, "Failed to remove file: %v", err)
	}

	// Step 3: Create directory with same name (type replacement)
	if err := os.Mkdir(fooPath, 0755); err != nil {
		tu.Failf(t, "Failed to create directory: %v", err)
	}

	// Step 4: Verify directory exists
	if stat, err := os.Stat(fooPath); err != nil {
		tu.Failf(t, "Directory should exist: %v", err)
	} else if !stat.IsDir() {
		tu.Failf(t, "Should be a directory after type replacement")
	}

	// Step 4.5: Wait for kernel cache to expire (Strategy 1: entry timeouts)
	// Small delay to ensure filesystem operations complete
	time.Sleep(50 * time.Millisecond)

	// Step 5: Verify source file still exists (Principle 1: no source modification)
	if _, err := os.Stat(srcFile); os.IsNotExist(err) {
		tu.Failf(t, "Source file should still exist")
	}

	// Step 6: Verify we can use the new directory
	testFile := filepath.Join(fooPath, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		tu.Failf(t, "Failed to create file in directory: %v", err)
	}

	// Step 7: Verify xattr marks type replacement
	homeDir, _ := os.UserHomeDir()
	mountID := fmt.Sprintf("%x", sha256.Sum256([]byte(mountPoint+srcDir)))
	cacheRoot := filepath.Join(homeDir, ".shadowfs", mountID, ".root")
	cacheFooPath := filepath.Join(cacheRoot, "foo")

	attr := xattr.XAttr{}
	exists, errno := xattr.Get(cacheFooPath, &attr)
	if errno != 0 && errno != syscall.ENODATA {
		tu.Failf(t, "Failed to get xattr: %v", errno)
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

	// Create testFilesystem struct manually
	fs := &testFilesystem{
		mountPoint: mntDir,
		srcDir:     srcDir,
		cmd:        cmd,
		t:          t,
	}

	// Wait for filesystem to be ready
	time.Sleep(100 * time.Millisecond)

	level2Path := filepath.Join(fs.mountPoint, "level1", "level2")
	level3Path := filepath.Join(level2Path, "level3")
	filePath := filepath.Join(level3Path, "file.txt")

	// Step 1: Access nested directories to ensure they're discovered
	// First, verify parent directories exist
	tu.ShouldBeRegularDirectory(filepath.Join(fs.mountPoint, "level1"), t)
	tu.ShouldBeRegularDirectory(filepath.Join(fs.mountPoint, "level1", "level2"), t)
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
	expectedUnlinkPath := filepath.Join(fs.srcDir, "level1", "level2", "level3", "file.txt")
	if expectedUnlinkPath != testFile {
		tu.Failf(t, "Path mismatch: expected %s, got %s", expectedUnlinkPath, testFile)
	}
	if _, err := os.Stat(expectedUnlinkPath); err != nil {
		tu.Failf(t, "File should exist at Unlink path %s, but stat failed: %v", expectedUnlinkPath, err)
	}

	// Step 3: Delete file first (this creates a deletion marker)
	// Use the test helper which handles FUSE filesystem properly
	fs.removeFile("level1/level2/level3/file.txt")

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
