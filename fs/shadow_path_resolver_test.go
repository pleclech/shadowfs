package fs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/pleclech/shadowfs/fs/cache"
	"github.com/pleclech/shadowfs/fs/operations"
	"github.com/pleclech/shadowfs/fs/xattr"
)

func TestPathResolver_ResolveSimplePath(t *testing.T) {
	// Setup
	srcDir := t.TempDir()
	cacheDir := t.TempDir()
	mountPoint := t.TempDir()

	cacheMgr := cache.NewManager(cacheDir, srcDir)
	xattrMgr := xattr.NewManager()
	renameTracker := operations.NewRenameTracker()
	resolver := NewPathResolver(srcDir, cacheDir, mountPoint, renameTracker, cacheMgr, xattrMgr)

	// Create test file in source
	testFile := filepath.Join(srcDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Test resolution
	mountPointPath := filepath.Join(mountPoint, "test.txt")
	cachePath, sourcePath, isIndependent, errno := resolver.ResolveForRead(mountPointPath)

	if errno != 0 {
		t.Errorf("ResolveForRead() errno = %v, want 0", errno)
	}
	if sourcePath != testFile {
		t.Errorf("ResolveForRead() sourcePath = %v, want %v", sourcePath, testFile)
	}
	expectedCachePath := filepath.Join(cacheDir, "test.txt")
	if cachePath != expectedCachePath {
		t.Errorf("ResolveForRead() cachePath = %v, want %v", cachePath, expectedCachePath)
	}
	if isIndependent {
		t.Error("ResolveForRead() isIndependent = true, want false")
	}
}

func TestPathResolver_ResolveRenamedPath(t *testing.T) {
	// Setup
	srcDir := t.TempDir()
	cacheDir := t.TempDir()
	mountPoint := t.TempDir()

	cacheMgr := cache.NewManager(cacheDir, srcDir)
	xattrMgr := xattr.NewManager()
	renameTracker := operations.NewRenameTracker()
	resolver := NewPathResolver(srcDir, cacheDir, mountPoint, renameTracker, cacheMgr, xattrMgr)

	// Create test file in source at original path
	originalPath := filepath.Join(srcDir, "foo", "bar", "test.txt")
	if err := os.MkdirAll(filepath.Dir(originalPath), 0755); err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}
	if err := os.WriteFile(originalPath, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Store rename mapping: foo/baz -> foo/bar
	renameTracker.StoreRenameMapping("foo/bar", "foo/baz", false)

	// Test resolution of renamed path
	mountPointPath := filepath.Join(mountPoint, "foo", "baz", "test.txt")
	_, sourcePath, isIndependent, errno := resolver.ResolveForRead(mountPointPath)

	if errno != 0 {
		t.Errorf("ResolveForRead() errno = %v, want 0", errno)
	}
	// Should resolve to original source path
	if sourcePath != originalPath {
		t.Errorf("ResolveForRead() sourcePath = %v, want %v", sourcePath, originalPath)
	}
	if isIndependent {
		t.Error("ResolveForRead() isIndependent = true, want false")
	}
}

func TestPathResolver_ResolveNestedRenamedPath(t *testing.T) {
	// Setup
	srcDir := t.TempDir()
	cacheDir := t.TempDir()
	mountPoint := t.TempDir()

	cacheMgr := cache.NewManager(cacheDir, srcDir)
	xattrMgr := xattr.NewManager()
	renameTracker := operations.NewRenameTracker()
	resolver := NewPathResolver(srcDir, cacheDir, mountPoint, renameTracker, cacheMgr, xattrMgr)

	// Create nested structure in source at original path
	originalPath := filepath.Join(srcDir, "foo", "bar", "hello", "world", "test.txt")
	if err := os.MkdirAll(filepath.Dir(originalPath), 0755); err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}
	if err := os.WriteFile(originalPath, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Store rename mapping: foo/baz -> foo/bar
	renameTracker.StoreRenameMapping("foo/bar", "foo/baz", false)

	// Test resolution of nested renamed path
	mountPointPath := filepath.Join(mountPoint, "foo", "baz", "hello", "world", "test.txt")
	_, sourcePath, isIndependent, errno := resolver.ResolveForRead(mountPointPath)

	if errno != 0 {
		t.Errorf("ResolveForRead() errno = %v, want 0", errno)
	}
	// Should resolve to original source path
	if sourcePath != originalPath {
		t.Errorf("ResolveForRead() sourcePath = %v, want %v", sourcePath, originalPath)
	}
	if isIndependent {
		t.Error("ResolveForRead() isIndependent = true, want false")
	}
}

func TestPathResolver_ResolveIndependentPath(t *testing.T) {
	// Setup
	srcDir := t.TempDir()
	cacheDir := t.TempDir()
	mountPoint := t.TempDir()

	cacheMgr := cache.NewManager(cacheDir, srcDir)
	xattrMgr := xattr.NewManager()
	renameTracker := operations.NewRenameTracker()
	resolver := NewPathResolver(srcDir, cacheDir, mountPoint, renameTracker, cacheMgr, xattrMgr)

	// Store rename mapping as independent: foo/baz -> foo/bar (independent)
	renameTracker.StoreRenameMapping("foo/bar", "foo/baz", true)

	// Test resolution of independent path
	mountPointPath := filepath.Join(mountPoint, "foo", "baz", "test.txt")
	cachePath, sourcePath, isIndependent, errno := resolver.ResolveForRead(mountPointPath)

	if errno != 0 {
		t.Errorf("ResolveForRead() errno = %v, want 0", errno)
	}
	if !isIndependent {
		t.Error("ResolveForRead() isIndependent = false, want true")
	}
	// Independent paths should use mount point path (not resolved)
	expectedSourcePath := filepath.Join(srcDir, "foo", "baz", "test.txt")
	if sourcePath != expectedSourcePath {
		t.Errorf("ResolveForRead() sourcePath = %v, want %v", sourcePath, expectedSourcePath)
	}
	expectedCachePath := filepath.Join(cacheDir, "foo", "baz", "test.txt")
	if cachePath != expectedCachePath {
		t.Errorf("ResolveForRead() cachePath = %v, want %v", cachePath, expectedCachePath)
	}
}

func TestPathResolver_ResolveDeletedPath(t *testing.T) {
	// Setup
	srcDir := t.TempDir()
	cacheDir := t.TempDir()
	mountPoint := t.TempDir()

	cacheMgr := cache.NewManager(cacheDir, srcDir)
	xattrMgr := xattr.NewManager()
	renameTracker := operations.NewRenameTracker()
	resolver := NewPathResolver(srcDir, cacheDir, mountPoint, renameTracker, cacheMgr, xattrMgr)

	// Create deletion marker in cache
	deletedPath := filepath.Join(cacheDir, "deleted.txt")
	if err := os.MkdirAll(filepath.Dir(deletedPath), 0755); err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}
	if err := os.WriteFile(deletedPath, []byte(""), 0644); err != nil {
		t.Fatalf("Failed to create deletion marker: %v", err)
	}
	// Mark as deleted
	attr := xattr.XAttr{
		PathStatus:       xattr.PathStatusDeleted,
		CacheIndependent: true,
	}
	if errno := xattr.Set(deletedPath, &attr); errno != 0 {
		t.Fatalf("Failed to set deletion xattr: %v", errno)
	}

	// Test resolution of deleted path
	mountPointPath := filepath.Join(mountPoint, "deleted.txt")
	_, _, _, errno := resolver.ResolveForRead(mountPointPath)

	if errno != syscall.ENOENT {
		t.Errorf("ResolveForRead() errno = %v, want ENOENT", errno)
	}
}

func TestPathResolver_DeletionClearsRenameMapping(t *testing.T) {
	// Setup
	renameTracker := operations.NewRenameTracker()

	// Store rename mapping: foo/baz -> foo/bar
	renameTracker.StoreRenameMapping("foo/bar", "foo/baz", false)

	// Verify mapping exists
	_, _, found := renameTracker.ResolveRenamedPath("foo/baz")
	if !found {
		t.Fatal("Rename mapping should exist before deletion")
	}

	// Remove rename mapping (simulating deletion)
	renameTracker.RemoveRenameMapping("foo/baz")

	// Verify mapping is removed
	_, _, found = renameTracker.ResolveRenamedPath("foo/baz")
	if found {
		t.Error("Rename mapping should be removed after deletion")
	}
}

func TestPathResolver_RecreationAfterDeletionIsIndependent(t *testing.T) {
	// Setup
	srcDir := t.TempDir()
	cacheDir := t.TempDir()
	mountPoint := t.TempDir()

	cacheMgr := cache.NewManager(cacheDir, srcDir)
	xattrMgr := xattr.NewManager()
	renameTracker := operations.NewRenameTracker()
	resolver := NewPathResolver(srcDir, cacheDir, mountPoint, renameTracker, cacheMgr, xattrMgr)

	// Create original file in source
	originalPath := filepath.Join(srcDir, "foo", "bar", "test.txt")
	if err := os.MkdirAll(filepath.Dir(originalPath), 0755); err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}
	if err := os.WriteFile(originalPath, []byte("original"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Store rename mapping: foo/baz -> foo/bar
	renameTracker.StoreRenameMapping("foo/bar", "foo/baz", false)

	// Create deletion marker in cache (simulating deletion)
	deletedCachePath := filepath.Join(cacheDir, "foo", "baz", "test.txt")
	if err := os.MkdirAll(filepath.Dir(deletedCachePath), 0755); err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}
	if err := os.WriteFile(deletedCachePath, []byte(""), 0644); err != nil {
		t.Fatalf("Failed to create deletion marker: %v", err)
	}
	attr := xattr.XAttr{
		PathStatus:       xattr.PathStatusDeleted,
		CacheIndependent: true,
	}
	if errno := xattr.Set(deletedCachePath, &attr); errno != 0 {
		t.Fatalf("Failed to set deletion xattr: %v", errno)
	}

	// Remove rename mapping (simulating recreation clearing the mapping)
	renameTracker.RemoveRenameMapping("foo/baz")

	// Create new file in cache (simulating recreation)
	newContent := []byte("new content")
	if err := os.WriteFile(deletedCachePath, newContent, 0644); err != nil {
		t.Fatalf("Failed to create new file: %v", err)
	}
	// Clear deletion marker and mark as independent (simulating recreation)
	// First clear the deletion xattr
	if errno := xattr.Remove(deletedCachePath); errno != 0 && errno != syscall.ENODATA {
		t.Fatalf("Failed to remove deletion xattr: %v", errno)
	}
	// Then mark as independent
	xattrMgr.SetCacheIndependent(deletedCachePath)

	// Test resolution - should be independent now
	mountPointPath := filepath.Join(mountPoint, "foo", "baz", "test.txt")
	_, sourcePath, isIndependent, errno := resolver.ResolveForRead(mountPointPath)

	if errno != 0 {
		t.Errorf("ResolveForRead() errno = %v, want 0", errno)
	}
	// Should be independent (not linked to source)
	if !isIndependent {
		t.Error("ResolveForRead() isIndependent = false, want true (recreated path should be independent)")
	}
	// Should use mount point path, not resolved source path
	expectedSourcePath := filepath.Join(srcDir, "foo", "baz", "test.txt")
	if sourcePath != expectedSourcePath {
		t.Errorf("ResolveForRead() sourcePath = %v, want %v (independent path should not resolve)", sourcePath, expectedSourcePath)
	}
}

func TestPathResolver_ResolveRootPath(t *testing.T) {
	// Setup
	srcDir := t.TempDir()
	cacheDir := t.TempDir()
	mountPoint := t.TempDir()

	cacheMgr := cache.NewManager(cacheDir, srcDir)
	xattrMgr := xattr.NewManager()
	renameTracker := operations.NewRenameTracker()
	resolver := NewPathResolver(srcDir, cacheDir, mountPoint, renameTracker, cacheMgr, xattrMgr)

	// Test resolution of root path
	cachePath, sourcePath, isIndependent, errno := resolver.ResolveForRead(mountPoint)

	if errno != 0 {
		t.Errorf("ResolveForRead() errno = %v, want 0", errno)
	}
	if cachePath != cacheDir {
		t.Errorf("ResolveForRead() cachePath = %v, want %v", cachePath, cacheDir)
	}
	if sourcePath != srcDir {
		t.Errorf("ResolveForRead() sourcePath = %v, want %v", sourcePath, srcDir)
	}
	if isIndependent {
		t.Error("ResolveForRead() isIndependent = true, want false")
	}
}

func TestPathResolver_ResolveForWrite(t *testing.T) {
	// Setup
	srcDir := t.TempDir()
	cacheDir := t.TempDir()
	mountPoint := t.TempDir()

	cacheMgr := cache.NewManager(cacheDir, srcDir)
	xattrMgr := xattr.NewManager()
	renameTracker := operations.NewRenameTracker()
	resolver := NewPathResolver(srcDir, cacheDir, mountPoint, renameTracker, cacheMgr, xattrMgr)

	// Store rename mapping: foo/baz -> foo/bar
	renameTracker.StoreRenameMapping("foo/bar", "foo/baz", false)

	// Test resolution for write
	mountPointPath := filepath.Join(mountPoint, "foo", "baz", "test.txt")
	_, sourcePath, errno := resolver.ResolveForWrite(mountPointPath)

	if errno != 0 {
		t.Errorf("ResolveForWrite() errno = %v, want 0", errno)
	}
	// Source path should be resolved (for reference)
	expectedSourcePath := filepath.Join(srcDir, "foo", "bar", "test.txt")
	if sourcePath != expectedSourcePath {
		t.Errorf("ResolveForWrite() sourcePath = %v, want %v", sourcePath, expectedSourcePath)
	}
}

