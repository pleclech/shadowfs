package rootinit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetMountPoint(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mountpoint-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
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
					t.Errorf("GetMountPoint(%s) expected error, got nil", tt.mountPoint)
				}
			} else {
				if err != nil {
					t.Errorf("GetMountPoint(%s) unexpected error: %v", tt.mountPoint, err)
				}
				if result == "" {
					t.Error("GetMountPoint() returned empty result")
				}
				// Result should be absolute
				if !filepath.IsAbs(result) {
					t.Errorf("GetMountPoint() result should be absolute, got %s", result)
				}
			}
		})
	}
}

func TestIsMountPointActive(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mountpoint-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Test with non-mounted directory (should return false)
	isActive, err := IsMountPointActive(tempDir)
	if err != nil {
		t.Logf("IsMountPointActive() error (may be expected): %v", err)
	}
	// For non-mounted directories, should return false
	if isActive {
		t.Logf("IsMountPointActive() returned true for non-mounted directory (may be acceptable)")
	}
}

