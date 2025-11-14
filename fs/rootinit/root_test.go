package rootinit

import (
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

	// Test file instead of directory
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
				// Result should be absolute
				if !filepath.IsAbs(result) {
					tu.Failf(
						t, "GetMountPoint() result should be absolute, got %s", result)
				}
			}
		})
	}
}

func TestIsMountPointActive(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mountpoint-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Test with non-mounted directory (should return false)
	isActive, err := IsMountPointActive(tempDir)
	if err != nil {
		tu.Errorf(t, "IsMountPointActive() error (may be expected): %v", err)
	}
	// For non-mounted directories, should return false
	if isActive {
		tu.Errorf(t, "IsMountPointActive() returned true for non-mounted directory (may be acceptable)")
	}
}
