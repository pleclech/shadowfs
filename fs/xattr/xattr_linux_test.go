//go:build linux
// +build linux

package xattr

import (
	"os"
	"path/filepath"
	"testing"

	tu "github.com/pleclech/shadowfs/fs/utils/testings"
)

func TestGet_Set_Remove_Linux(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "xattr-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	testFile := filepath.Join(tempDir, "testfile")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	attr := XAttr{
		PathStatus: PathStatusDeleted,
	}
	errno := Set(testFile, &attr)
	if errno != 0 {
		tu.Failf(t, "Set() error = %v", errno)
	}

	var readAttr XAttr
	exists, errno := Get(testFile, &readAttr)
	if errno != 0 {
		tu.Failf(t, "Get() error = %v", errno)
	}
	if !exists {
		tu.Failf(t, "Expected xattr to exist after Set")
	}
	if readAttr.PathStatus != PathStatusDeleted {
		tu.Failf(t, "Expected PathStatus %d, got %d", PathStatusDeleted, readAttr.PathStatus)
	}

	errno = Remove(testFile)
	if errno != 0 {
		tu.Failf(t, "Remove() error = %v", errno)
	}

	exists, errno = Get(testFile, &readAttr)
	if errno != 0 && errno != 61 {
		tu.Failf(t, "Get() after Remove unexpected error = %v", errno)
	}
}

func TestGet_NonExistentFile(t *testing.T) {
	var attr XAttr
	exists, _ := Get("/nonexistent/file", &attr)
	if exists {
		tu.Failf(t, "Expected xattr to not exist for non-existent file")
	}
}

func TestManager_SetDeleted(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "xattr-mgr-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	testFile := filepath.Join(tempDir, "testfile")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	mgr := NewManager()
	errno := mgr.SetDeleted(testFile, 0o100644)
	if errno != 0 {
		tu.Failf(t, "SetDeleted() error = %v", errno)
	}

	isDeleted, errno := mgr.IsDeleted(testFile)
	if errno != 0 {
		tu.Failf(t, "IsDeleted() error = %v", errno)
	}
	if !isDeleted {
		tu.Failf(t, "Expected file to be marked as deleted")
	}

	attr, exists, errno := mgr.GetStatus(testFile)
	if errno != 0 {
		tu.Failf(t, "GetStatus() error = %v", errno)
	}
	if !exists {
		tu.Failf(t, "Expected xattr to exist")
	}
	if attr.OriginalType != 0o100644 {
		tu.Failf(t, "Expected OriginalType 0o100644, got %o", attr.OriginalType)
	}
	if !attr.CacheIndependent {
		tu.Failf(t, "Expected CacheIndependent to be true for deleted files")
	}
}

func TestManager_SetTypeReplaced(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "xattr-mgr-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	testFile := filepath.Join(tempDir, "testfile")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	mgr := NewManager()
	errno := mgr.SetTypeReplaced(testFile, 0o100644, 0o040755)
	if errno != 0 {
		tu.Failf(t, "SetTypeReplaced() error = %v", errno)
	}

	attr, exists, errno := mgr.GetStatus(testFile)
	if errno != 0 {
		tu.Failf(t, "GetStatus() error = %v", errno)
	}
	if !exists {
		tu.Failf(t, "Expected xattr to exist")
	}
	if attr.OriginalType != 0o100644 {
		tu.Failf(t, "Expected OriginalType 0o100644, got %o", attr.OriginalType)
	}
	if attr.CurrentType != 0o040755 {
		tu.Failf(t, "Expected CurrentType 0o040755, got %o", attr.CurrentType)
	}
	if !attr.TypeReplaced {
		tu.Failf(t, "Expected TypeReplaced to be true")
	}
	if !attr.CacheIndependent {
		tu.Failf(t, "Expected CacheIndependent to be true")
	}
	if !attr.DeletedFromSource {
		tu.Failf(t, "Expected DeletedFromSource to be true")
	}
}

func TestManager_SetCacheIndependent(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "xattr-mgr-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	testFile := filepath.Join(tempDir, "testfile")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	mgr := NewManager()

	errno := mgr.SetCacheIndependent(testFile)
	if errno != 0 {
		tu.Failf(t, "SetCacheIndependent() error = %v", errno)
	}

	attr, exists, errno := mgr.GetStatus(testFile)
	if errno != 0 {
		tu.Failf(t, "GetStatus() error = %v", errno)
	}
	if !exists {
		tu.Failf(t, "Expected xattr to exist")
	}
	if !attr.CacheIndependent {
		tu.Failf(t, "Expected CacheIndependent to be true")
	}
}

func TestManager_SetRenameMapping(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "xattr-mgr-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	testFile := filepath.Join(tempDir, "testfile")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	mgr := NewManager()

	errno := mgr.SetRenameMapping(testFile, "/original/source", "/renamed/from", 2)
	if errno != 0 {
		tu.Failf(t, "SetRenameMapping() error = %v", errno)
	}

	attr, exists, errno := mgr.GetStatus(testFile)
	if errno != 0 {
		tu.Failf(t, "GetStatus() error = %v", errno)
	}
	if !exists {
		tu.Failf(t, "Expected xattr to exist")
	}
	if attr.GetOriginalSourcePath() != "/original/source" {
		tu.Failf(t, "Expected OriginalSourcePath '/original/source', got %q", attr.GetOriginalSourcePath())
	}
	if attr.GetRenamedFromPath() != "/renamed/from" {
		tu.Failf(t, "Expected RenamedFromPath '/renamed/from', got %q", attr.GetRenamedFromPath())
	}
	if attr.RenameDepth != 2 {
		tu.Failf(t, "Expected RenameDepth 2, got %d", attr.RenameDepth)
	}
	if !attr.DeletedFromSource {
		tu.Failf(t, "Expected DeletedFromSource to be true for renamed items")
	}
}

func TestManager_ClearRenameMapping(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "xattr-mgr-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	testFile := filepath.Join(tempDir, "testfile")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	mgr := NewManager()

	errno := mgr.SetRenameMapping(testFile, "/original/source", "/renamed/from", 2)
	if errno != 0 {
		tu.Failf(t, "SetRenameMapping() error = %v", errno)
	}

	errno = mgr.ClearRenameMapping(testFile)
	if errno != 0 {
		tu.Failf(t, "ClearRenameMapping() error = %v", errno)
	}

	attr, _, errno := mgr.GetStatus(testFile)
	if errno != 0 {
		tu.Failf(t, "GetStatus() error = %v", errno)
	}
	if attr.GetOriginalSourcePath() != "" {
		tu.Failf(t, "Expected OriginalSourcePath to be empty after clear, got %q", attr.GetOriginalSourcePath())
	}
	if attr.GetRenamedFromPath() != "" {
		tu.Failf(t, "Expected RenamedFromPath to be empty after clear, got %q", attr.GetRenamedFromPath())
	}
	if attr.RenameDepth != 0 {
		tu.Failf(t, "Expected RenameDepth 0 after clear, got %d", attr.RenameDepth)
	}
}

func TestManager_ClearRenameMapping_NonExistent(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "xattr-mgr-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	testFile := filepath.Join(tempDir, "testfile")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	mgr := NewManager()

	errno := mgr.ClearRenameMapping(testFile)
	if errno != 0 {
		tu.Failf(t, "ClearRenameMapping() on file without xattr should return 0, got %v", errno)
	}
}

func TestManager_Clear(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "xattr-mgr-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	testFile := filepath.Join(tempDir, "testfile")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	mgr := NewManager()

	errno := mgr.SetDeleted(testFile, 0o100644)
	if errno != 0 {
		tu.Failf(t, "SetDeleted() error = %v", errno)
	}

	errno = mgr.Clear(testFile)
	if errno != 0 {
		tu.Failf(t, "Clear() error = %v", errno)
	}

	_, exists, _ := mgr.GetStatus(testFile)
	if exists {
		tu.Failf(t, "Expected xattr to not exist after Clear")
	}
}

func TestManager_IsDeleted_NonExistent(t *testing.T) {
	mgr := NewManager()
	isDeleted, errno := mgr.IsDeleted("/nonexistent/path")
	if errno != 0 {
		tu.Failf(t, "IsDeleted() for non-existent file should return errno 0, got %v", errno)
	}
	if isDeleted {
		tu.Failf(t, "Expected isDeleted=false for non-existent file")
	}
}

func TestManager_GetStatus_NoXattr(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "xattr-mgr-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	testFile := filepath.Join(tempDir, "testfile")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	mgr := NewManager()
	_, exists, errno := mgr.GetStatus(testFile)
	if errno != 0 && errno != 61 {
		tu.Failf(t, "GetStatus() for file without xattr unexpected error = %v", errno)
	}
	if exists {
		tu.Failf(t, "Expected exists=false for file without xattr")
	}
}

func TestList_NoXattrs(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "xattr-list-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	testFile := filepath.Join(tempDir, "testfile")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	attrs, errno := List(testFile)
	if errno != 0 {
		tu.Failf(t, "List() error = %v", errno)
	}
	if len(attrs) != 0 {
		tu.Failf(t, "Expected empty list for file without xattrs, got %v", attrs)
	}
}

func TestList_WithXattr(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "xattr-list-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	testFile := filepath.Join(tempDir, "testfile")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	attr := XAttr{PathStatus: PathStatusDeleted}
	errno := Set(testFile, &attr)
	if errno != 0 {
		tu.Failf(t, "Set() error = %v", errno)
	}

	attrs, errno := List(testFile)
	if errno != 0 {
		tu.Failf(t, "List() error = %v", errno)
	}

	found := false
	for _, name := range attrs {
		if name == Name {
			found = true
			break
		}
	}
	if !found {
		tu.Failf(t, "Expected to find xattr %q in list %v", Name, attrs)
	}
}

func TestList_NonExistentFile(t *testing.T) {
	attrs, errno := List("/nonexistent/file/path")
	if errno != 0 {
		t.Logf("List() on non-existent file returned errno = %v (acceptable)", errno)
	}
	if attrs != nil && len(attrs) > 0 {
		tu.Failf(t, "Expected nil or empty attrs for non-existent file, got %v", attrs)
	}
}

func TestList_Directory(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "xattr-list-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	attrs, errno := List(tempDir)
	if errno != 0 {
		tu.Failf(t, "List() on directory error = %v", errno)
	}
	if attrs == nil {
		tu.Failf(t, "Expected empty slice, not nil")
	}
}

func TestGet_FullRoundTrip(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "xattr-get-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	testFile := filepath.Join(tempDir, "testfile")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	attr := XAttr{
		PathStatus:       PathStatusDeleted,
		CacheIndependent: true,
		OriginalType:     0o100644,
	}
	errno := Set(testFile, &attr)
	if errno != 0 {
		tu.Failf(t, "Set() error = %v", errno)
	}

	var readAttr XAttr
	exists, errno := Get(testFile, &readAttr)
	if errno != 0 {
		tu.Failf(t, "Get() error = %v", errno)
	}
	if !exists {
		tu.Failf(t, "Expected xattr to exist")
	}
	if readAttr.PathStatus != PathStatusDeleted {
		tu.Failf(t, "Expected PathStatus %d, got %d", PathStatusDeleted, readAttr.PathStatus)
	}
	if !readAttr.CacheIndependent {
		tu.Failf(t, "Expected CacheIndependent to be true")
	}
	if readAttr.OriginalType != 0o100644 {
		tu.Failf(t, "Expected OriginalType 0o100644, got %o", readAttr.OriginalType)
	}
}

func TestGet_AfterRemove(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "xattr-get-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	testFile := filepath.Join(tempDir, "testfile")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	attr := XAttr{PathStatus: PathStatusDeleted}
	errno := Set(testFile, &attr)
	if errno != 0 {
		tu.Failf(t, "Set() error = %v", errno)
	}

	errno = Remove(testFile)
	if errno != 0 {
		tu.Failf(t, "Remove() error = %v", errno)
	}

	var readAttr XAttr
	exists, _ := Get(testFile, &readAttr)
	if exists {
		tu.Failf(t, "Expected xattr to not exist after Remove")
	}
}

func TestGet_OnDirectory(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "xattr-get-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	attr := XAttr{PathStatus: PathStatusDeleted}
	errno := Set(tempDir, &attr)
	if errno != 0 {
		tu.Failf(t, "Set() on directory error = %v", errno)
	}

	var readAttr XAttr
	exists, errno := Get(tempDir, &readAttr)
	if errno != 0 {
		tu.Failf(t, "Get() on directory error = %v", errno)
	}
	if !exists {
		tu.Failf(t, "Expected xattr to exist on directory")
	}
	if readAttr.PathStatus != PathStatusDeleted {
		tu.Failf(t, "Expected PathStatus %d, got %d", PathStatusDeleted, readAttr.PathStatus)
	}
}
