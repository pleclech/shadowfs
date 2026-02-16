package rootinit

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	tu "github.com/pleclech/shadowfs/fs/utils/testings"
)

func TestGetMountPoint(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mountpoint-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	testFile := filepath.Join(tempDir, "testfile")
	os.WriteFile(testFile, []byte("test"), 0644)

	tests := []struct {
		name       string
		mountPoint string
		shouldErr  bool
	}{
		{
			name:       "absolute path",
			mountPoint: tempDir,
			shouldErr:  false,
		},
		{
			name:       "non-existent path",
			mountPoint: "/nonexistent/dir",
			shouldErr:  true,
		},
		{
			name:       "file instead of directory",
			mountPoint: testFile,
			shouldErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := GetMountPoint(tt.mountPoint)
			if tt.shouldErr {
				if err == nil {
					tu.Failf(
						t, "GetMountPoint(%s) expected error, got nil", tt.mountPoint)
				}
			} else {
				if err != nil {
					tu.Failf(
						t, "GetMountPoint(%s) unexpected error: %v", tt.mountPoint, err)
				}
				if result == "" {
					tu.Failf(
						t, "GetMountPoint() returned empty result")
				}
				if !filepath.IsAbs(result) {
					tu.Failf(
						t, "GetMountPoint() result should be absolute, got %s", result)
				}
			}
		})
	}
}

func TestGetMountPoint_Symlink(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mountpoint-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	realDir := filepath.Join(tempDir, "realdir")
	os.MkdirAll(realDir, 0755)

	symlinkDir := filepath.Join(tempDir, "linkdir")
	if err := os.Symlink(realDir, symlinkDir); err != nil {
		tu.Failf(t, "Failed to create symlink: %v", err)
	}

	result, err := GetMountPoint(symlinkDir)
	if err != nil {
		tu.Failf(t, "GetMountPoint() for symlink error: %v", err)
	}

	if result != realDir {
		tu.Failf(t, "Expected resolved path %q, got %q", realDir, result)
	}
}

func TestIsMountPointActive(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mountpoint-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	isActive, err := IsMountPointActive(tempDir)
	if err != nil {
		tu.Errorf(t, "IsMountPointActive() error (may be expected): %v", err)
	}
	if isActive {
		tu.Errorf(t, "IsMountPointActive() returned true for non-mounted directory (may be acceptable)")
	}
}

func TestCreateDir(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "createdir-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	newDir := filepath.Join(tempDir, "newdir")
	err = CreateDir(newDir, 0755)
	if err != nil {
		tu.Failf(t, "CreateDir() error: %v", err)
	}

	info, err := os.Stat(newDir)
	if err != nil {
		tu.Failf(t, "Created directory should exist: %v", err)
	}
	if !info.IsDir() {
		tu.Failf(t, "Created path should be a directory")
	}
}

func TestCreateDir_Existing(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "createdir-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	existingDir := filepath.Join(tempDir, "existing")
	os.MkdirAll(existingDir, 0755)

	err = CreateDir(existingDir, 0755)
	if err != nil {
		tu.Failf(t, "CreateDir() for existing dir should not error: %v", err)
	}
}

func TestCreateDir_FileExists(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "createdir-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	filePath := filepath.Join(tempDir, "afile")
	os.WriteFile(filePath, []byte("content"), 0644)

	err = CreateDir(filePath, 0755)
	if err == nil {
		tu.Failf(t, "CreateDir() for existing file should error")
	}
}

func TestCreateDir_Nested(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "createdir-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	nestedDir := filepath.Join(tempDir, "level1", "level2", "level3")
	err = CreateDir(nestedDir, 0755)
	if err != nil {
		tu.Failf(t, "CreateDir() for nested path error: %v", err)
	}

	info, err := os.Stat(nestedDir)
	if err != nil {
		tu.Failf(t, "Nested directory should exist: %v", err)
	}
	if !info.IsDir() {
		tu.Failf(t, "Nested path should be a directory")
	}
}

func TestWriteFileOnce(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "writefile-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	filePath := filepath.Join(tempDir, "newfile.txt")
	content := []byte("test content")

	err = WriteFileOnce(filePath, content, 0644)
	if err != nil {
		tu.Failf(t, "WriteFileOnce() error: %v", err)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		tu.Failf(t, "Failed to read created file: %v", err)
	}
	if string(data) != string(content) {
		tu.Failf(t, "Expected content %q, got %q", string(content), string(data))
	}
}

func TestWriteFileOnce_Existing(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "writefile-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	filePath := filepath.Join(tempDir, "existing.txt")
	originalContent := []byte("original")
	os.WriteFile(filePath, originalContent, 0644)

	newContent := []byte("new content")
	err = WriteFileOnce(filePath, newContent, 0644)
	if err != nil {
		tu.Failf(t, "WriteFileOnce() for existing file should not error: %v", err)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		tu.Failf(t, "Failed to read file: %v", err)
	}
	if string(data) != string(originalContent) {
		tu.Failf(t, "Existing file content should not change")
	}
}

func TestWriteFileOnce_DirectoryExists(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "writefile-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dirPath := filepath.Join(tempDir, "adir")
	os.MkdirAll(dirPath, 0755)

	err = WriteFileOnce(dirPath, []byte("content"), 0644)
	if err == nil {
		tu.Failf(t, "WriteFileOnce() for existing directory should error")
	}
}

func TestValidateCacheDirectory(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "validate-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	err = ValidateCacheDirectory(tempDir)
	if err != nil {
		tu.Failf(t, "ValidateCacheDirectory() for valid dir error: %v", err)
	}
}

func TestValidateCacheDirectory_NonExistent(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "validate-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	newDir := filepath.Join(tempDir, "newcache")
	err = ValidateCacheDirectory(newDir)
	if err != nil {
		tu.Failf(t, "ValidateCacheDirectory() should create non-existent dir: %v", err)
	}

	info, err := os.Stat(newDir)
	if err != nil {
		tu.Failf(t, "Created cache directory should exist: %v", err)
	}
	if !info.IsDir() {
		tu.Failf(t, "Created cache path should be a directory")
	}
}

func TestValidateCacheDirectory_FileExists(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "validate-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	filePath := filepath.Join(tempDir, "afile")
	os.WriteFile(filePath, []byte("content"), 0644)

	err = ValidateCacheDirectory(filePath)
	if err == nil {
		tu.Failf(t, "ValidateCacheDirectory() for file should error")
	}
}

func TestFindCacheDirectory_NotFound(t *testing.T) {
	originalEnv := os.Getenv("SHADOWFS_CACHE_DIR")
	defer os.Setenv("SHADOWFS_CACHE_DIR", originalEnv)

	tempDir, err := os.MkdirTemp("", "discovery-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	os.Setenv("SHADOWFS_CACHE_DIR", tempDir)

	_, err = FindCacheDirectory("/nonexistent/mount")
	if err == nil {
		tu.Failf(t, "FindCacheDirectory() for non-existent mount should error")
	}
}

func TestFindCacheDirectory_InvalidMountPoint(t *testing.T) {
	_, err := FindCacheDirectory("/nonexistent/path/mount")
	if err == nil {
		tu.Failf(t, "FindCacheDirectory() for invalid mount point should error")
	}
}

func TestFindSourceDirectory_WithValidCache(t *testing.T) {
	originalEnv := os.Getenv("SHADOWFS_CACHE_DIR")
	defer os.Setenv("SHADOWFS_CACHE_DIR", originalEnv)

	tempDir, err := os.MkdirTemp("", "discovery-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	os.Setenv("SHADOWFS_CACHE_DIR", tempDir)

	mountPoint := "/test/mount"
	srcDir := "/test/source"
	mountID := computeTestMountID(mountPoint, srcDir)

	sessionDir := filepath.Join(tempDir, mountID)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		tu.Failf(t, "Failed to create session dir: %v", err)
	}

	targetFile := filepath.Join(sessionDir, ".target")
	if err := os.WriteFile(targetFile, []byte(srcDir), 0644); err != nil {
		tu.Failf(t, "Failed to write target file: %v", err)
	}

	foundSrcDir, foundMountID, err := findSourceDirectory(mountPoint)
	if err != nil {
		tu.Failf(t, "findSourceDirectory() error: %v", err)
	}
	if foundSrcDir != srcDir {
		tu.Failf(t, "Expected srcDir %q, got %q", srcDir, foundSrcDir)
	}
	if foundMountID != mountID {
		tu.Failf(t, "Expected mountID %q, got %q", mountID, foundMountID)
	}
}

func TestFindSourceDirectory_NotFound(t *testing.T) {
	originalEnv := os.Getenv("SHADOWFS_CACHE_DIR")
	defer os.Setenv("SHADOWFS_CACHE_DIR", originalEnv)

	tempDir, err := os.MkdirTemp("", "discovery-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	os.Setenv("SHADOWFS_CACHE_DIR", tempDir)

	_, _, err = findSourceDirectory("/nonexistent/mount/point")
	if err == nil {
		tu.Failf(t, "findSourceDirectory() should return error for non-existent mount")
	}
}

func TestFindSourceDirectory_EmptyCacheDir(t *testing.T) {
	originalEnv := os.Getenv("SHADOWFS_CACHE_DIR")
	defer os.Setenv("SHADOWFS_CACHE_DIR", originalEnv)

	tempDir, err := os.MkdirTemp("", "discovery-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	os.Setenv("SHADOWFS_CACHE_DIR", tempDir)

	_, _, err = findSourceDirectory("/some/mount/point")
	if err == nil {
		tu.Failf(t, "findSourceDirectory() should error for empty cache directory")
	}
}

func TestFindSourceDirectory_NonMatchingSession(t *testing.T) {
	originalEnv := os.Getenv("SHADOWFS_CACHE_DIR")
	defer os.Setenv("SHADOWFS_CACHE_DIR", originalEnv)

	tempDir, err := os.MkdirTemp("", "discovery-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	os.Setenv("SHADOWFS_CACHE_DIR", tempDir)

	differentMountID := "somehashvalue"
	sessionDir := filepath.Join(tempDir, differentMountID)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		tu.Failf(t, "Failed to create session dir: %v", err)
	}

	targetFile := filepath.Join(sessionDir, ".target")
	if err := os.WriteFile(targetFile, []byte("/different/source"), 0644); err != nil {
		tu.Failf(t, "Failed to write target file: %v", err)
	}

	_, _, err = findSourceDirectory("/my/mount/point")
	if err == nil {
		tu.Failf(t, "findSourceDirectory() should error when no matching session found")
	}
}

func TestFindSourceDirectory_SkipsNonDirectories(t *testing.T) {
	originalEnv := os.Getenv("SHADOWFS_CACHE_DIR")
	defer os.Setenv("SHADOWFS_CACHE_DIR", originalEnv)

	tempDir, err := os.MkdirTemp("", "discovery-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	os.Setenv("SHADOWFS_CACHE_DIR", tempDir)

	fileInCacheDir := filepath.Join(tempDir, "somefile.txt")
	if err := os.WriteFile(fileInCacheDir, []byte("content"), 0644); err != nil {
		tu.Failf(t, "Failed to create file: %v", err)
	}

	_, _, err = findSourceDirectory("/test/mount")
	if err == nil {
		tu.Failf(t, "findSourceDirectory() should error when only files exist in cache dir")
	}
}

func computeTestMountID(mountPoint, srcDir string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(mountPoint+srcDir)))
}
