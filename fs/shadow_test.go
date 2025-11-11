//go:build linux
// +build linux

package fs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRebasePathUsingCache(t *testing.T) {
	ts := NewTestSetup(t)
	defer ts.Cleanup()

	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{
			name:     "source path",
			path:     filepath.Join(ts.SrcDir, "file.txt"),
			expected: filepath.Join(ts.CacheDir, "file.txt"),
		},
		{
			name:     "nested source path",
			path:     filepath.Join(ts.SrcDir, "dir1", "file.txt"),
			expected: filepath.Join(ts.CacheDir, "dir1", "file.txt"),
		},
		{
			name:     "cache path unchanged",
			path:     filepath.Join(ts.CacheDir, "file.txt"),
			expected: filepath.Join(ts.CacheDir, "file.txt"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ts.Root.RebasePathUsingCache(tt.path)
			if result != tt.expected {
				t.Errorf("RebasePathUsingCache() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestRebasePathUsingSrc(t *testing.T) {
	ts := NewTestSetup(t)
	defer ts.Cleanup()

	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{
			name:     "cache path",
			path:     filepath.Join(ts.CacheDir, "file.txt"),
			expected: filepath.Join(ts.SrcDir, "file.txt"),
		},
		{
			name:     "nested cache path",
			path:     filepath.Join(ts.CacheDir, "dir1", "file.txt"),
			expected: filepath.Join(ts.SrcDir, "dir1", "file.txt"),
		},
		{
			name:     "source path unchanged",
			path:     filepath.Join(ts.SrcDir, "file.txt"),
			expected: filepath.Join(ts.SrcDir, "file.txt"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ts.Root.RebasePathUsingSrc(tt.path)
			if result != tt.expected {
				t.Errorf("RebasePathUsingSrc() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestShadowXAttrOperations(t *testing.T) {
	ts := NewTestSetup(t)
	defer ts.Cleanup()

	// Create a test file in cache
	testFile := filepath.Join(ts.CacheDir, "testfile.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Test setting and getting xattr
	attr := ShadowXAttr{ShadowPathStatus: ShadowPathStatusDeleted}
	errno := SetShadowXAttr(testFile, &attr)
	if errno != 0 {
		t.Errorf("SetShadowXAttr() failed: %v", errno)
	}

	// Test getting xattr
	var retrievedAttr ShadowXAttr
	exists, errno := GetShadowXAttr(testFile, &retrievedAttr)
	if errno != 0 {
		t.Errorf("GetShadowXAttr() failed: %v", errno)
	}
	if !exists {
		t.Error("Expected xattr to exist")
	}
	if retrievedAttr.ShadowPathStatus != ShadowPathStatusDeleted {
		t.Errorf("Expected ShadowPathStatusDeleted, got %v", retrievedAttr.ShadowPathStatus)
	}

	// Test IsPathDeleted
	if !IsPathDeleted(retrievedAttr) {
		t.Error("Expected path to be marked as deleted")
	}
}

func TestShadowXAttrOperations_NonExistentFile(t *testing.T) {
	ts := NewTestSetup(t)
	defer ts.Cleanup()

	nonExistentFile := filepath.Join(ts.CacheDir, "nonexistent.txt")
	var attr ShadowXAttr
	exists, errno := GetShadowXAttr(nonExistentFile, &attr)

	if errno != 0 {
		t.Errorf("GetShadowXAttr() on non-existent file should not error: %v", errno)
	}
	if exists {
		t.Error("Expected xattr to not exist on non-existent file")
	}
}

func TestCreateMirroredDir(t *testing.T) {
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

func TestCreateMirroredFileOrDir(t *testing.T) {
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

func TestCreateMirroredFileOrDir_Directory(t *testing.T) {
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

func TestCreateDir(t *testing.T) {
	tempDir := t.TempDir()
	testDir := filepath.Join(tempDir, "testdir")

	// Test creating new directory
	err := createDir(testDir, 0755)
	if err != nil {
		t.Errorf("createDir() failed: %v", err)
	}

	// Verify directory exists
	if stat, err := os.Stat(testDir); err != nil {
		t.Errorf("Directory does not exist: %v", err)
	} else if !stat.IsDir() {
		t.Error("Path is not a directory")
	}

	// Test creating existing directory (should not error)
	err = createDir(testDir, 0755)
	if err != nil {
		t.Errorf("createDir() on existing directory failed: %v", err)
	}
}

func TestCreateDir_FileExists(t *testing.T) {
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "testfile")

	// Create a file
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Try to create directory with same name
	err := createDir(testFile, 0755)
	if err == nil {
		t.Error("Expected error when creating directory where file exists")
	}
}

func TestWriteFileOnce(t *testing.T) {
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "testfile")
	content := []byte("test content")

	// Test writing new file
	err := writeFileOnce(testFile, content, 0644)
	if err != nil {
		t.Errorf("writeFileOnce() failed: %v", err)
	}

	// Verify file content
	readContent, err := os.ReadFile(testFile)
	if err != nil {
		t.Errorf("Failed to read file: %v", err)
	}
	if string(readContent) != string(content) {
		t.Errorf("File content mismatch. Expected: %s, Got: %s", string(content), string(readContent))
	}

	// Test writing existing file (should not error)
	err = writeFileOnce(testFile, []byte("new content"), 0644)
	if err != nil {
		t.Errorf("writeFileOnce() on existing file failed: %v", err)
	}

	// Verify content unchanged
	readContent, err = os.ReadFile(testFile)
	if err != nil {
		t.Errorf("Failed to read file: %v", err)
	}
	if string(readContent) != string(content) {
		t.Errorf("File content should not change. Expected: %s, Got: %s", string(content), string(readContent))
	}
}

func TestWriteFileOnce_DirectoryExists(t *testing.T) {
	tempDir := t.TempDir()
	testDir := filepath.Join(tempDir, "testdir")

	// Create directory
	if err := os.Mkdir(testDir, 0755); err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}

	// Try to write file with same name
	err := writeFileOnce(testDir, []byte("test"), 0644)
	if err == nil {
		t.Error("Expected error when writing file where directory exists")
	}
}

func TestGetMountPoint(t *testing.T) {
	tempDir := t.TempDir()

	// Test absolute path
	result, err := getMountPoint(tempDir)
	if err != nil {
		t.Errorf("getMountPoint() failed: %v", err)
	}
	if result != tempDir {
		t.Errorf("getMountPoint() = %v, want %v", result, tempDir)
	}

	// Test non-existent path
	nonExistent := filepath.Join(tempDir, "nonexistent")
	_, err = getMountPoint(nonExistent)
	if err == nil {
		t.Error("Expected error for non-existent path")
	}

	// Test file instead of directory
	testFile := filepath.Join(tempDir, "testfile")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	_, err = getMountPoint(testFile)
	if err == nil {
		t.Error("Expected error for file path")
	}
}

func TestNewShadowRoot(t *testing.T) {
	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Test creating shadow root
	root, err := NewShadowRoot(mountPoint, srcDir, "")
	if err != nil {
		t.Errorf("NewShadowRoot() failed: %v", err)
	}

	shadowRoot := root.(*ShadowNode)
	if shadowRoot.mountPoint != mountPoint {
		t.Errorf("mountPoint = %v, want %v", shadowRoot.mountPoint, mountPoint)
	}
	if shadowRoot.srcDir != srcDir {
		t.Errorf("srcDir = %v, want %v", shadowRoot.srcDir, srcDir)
	}
	if shadowRoot.cachePath == "" {
		t.Error("cachePath should not be empty")
	}
	if shadowRoot.sessionPath == "" {
		t.Error("sessionPath should not be empty")
	}
	if shadowRoot.mountID == "" {
		t.Error("mountID should not be empty")
	}

	// Verify cache directory exists
	if _, err := os.Stat(shadowRoot.cachePath); os.IsNotExist(err) {
		t.Error("Cache directory does not exist")
	}

	// Verify target file exists
	targetFile := filepath.Join(shadowRoot.sessionPath, ".target")
	if _, err := os.Stat(targetFile); os.IsNotExist(err) {
		t.Error("Target file does not exist")
	}
}

func TestNewShadowRootWithCustomCacheDir(t *testing.T) {
	mountPoint := t.TempDir()
	srcDir := t.TempDir()
	customCacheDir := t.TempDir()

	// Test creating shadow root with custom cache directory
	root, err := NewShadowRoot(mountPoint, srcDir, customCacheDir)
	if err != nil {
		t.Errorf("NewShadowRoot() failed: %v", err)
	}

	shadowRoot := root.(*ShadowNode)
	if shadowRoot.mountPoint != mountPoint {
		t.Errorf("mountPoint = %v, want %v", shadowRoot.mountPoint, mountPoint)
	}
	if shadowRoot.srcDir != srcDir {
		t.Errorf("srcDir = %v, want %v", shadowRoot.srcDir, srcDir)
	}

	// Verify cache directory is in custom location
	if !strings.HasPrefix(shadowRoot.sessionPath, customCacheDir) {
		t.Errorf("sessionPath = %v, should be under %v", shadowRoot.sessionPath, customCacheDir)
	}

	// Verify cache directory exists
	if _, err := os.Stat(shadowRoot.cachePath); os.IsNotExist(err) {
		t.Error("Cache directory does not exist")
	}
}

func TestValidateCacheDirectory(t *testing.T) {
	tempDir := t.TempDir()

	// Test non-existent directory (should be created)
	nonExistent := filepath.Join(tempDir, "new-cache")
	err := validateCacheDirectory(nonExistent)
	if err != nil {
		t.Errorf("validateCacheDirectory() failed for non-existent dir: %v", err)
	}
	if _, err := os.Stat(nonExistent); os.IsNotExist(err) {
		t.Error("Directory should have been created")
	}

	// Test existing directory
	err = validateCacheDirectory(tempDir)
	if err != nil {
		t.Errorf("validateCacheDirectory() failed for existing dir: %v", err)
	}

	// Test file instead of directory
	testFile := filepath.Join(tempDir, "testfile")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	err = validateCacheDirectory(testFile)
	if err == nil {
		t.Error("validateCacheDirectory() should fail for file")
	}

	// Test relative path (should be normalized)
	relPath := "relative-cache"
	err = validateCacheDirectory(relPath)
	if err != nil {
		t.Errorf("validateCacheDirectory() failed for relative path: %v", err)
	}
	// Verify it was normalized to absolute
	absPath, _ := filepath.Abs(relPath)
	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		t.Error("Relative path should have been normalized and created")
	}
	os.RemoveAll(absPath) // Cleanup
}
