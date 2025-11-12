package pathutil

import (
	"testing"
)

func TestRebaseToCache(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		cachePath string
		srcDir    string
		expected  string
	}{
		{
			name:      "simple file",
			path:      "/source/file.txt",
			cachePath: "/cache",
			srcDir:    "/source",
			expected:  "/cache/file.txt",
		},
		{
			name:      "already in cache",
			path:      "/cache/file.txt",
			cachePath: "/cache",
			srcDir:    "/source",
			expected:  "/cache/file.txt",
		},
		{
			name:      "nested path",
			path:      "/source/dir/subdir/file.txt",
			cachePath: "/cache",
			srcDir:    "/source",
			expected:  "/cache/dir/subdir/file.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RebaseToCache(tt.path, tt.cachePath, tt.srcDir)
			if result != tt.expected {
				t.Errorf("RebaseToCache(%s, %s, %s) = %s, expected %s", tt.path, tt.cachePath, tt.srcDir, result, tt.expected)
			}
		})
	}
}

func TestRebaseToMountPoint(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		mountPoint string
		cachePath  string
		expected   string
	}{
		{
			name:       "simple file",
			path:       "/cache/file.txt",
			mountPoint: "/mount",
			cachePath:  "/cache",
			expected:   "/mount/file.txt",
		},
		{
			name:       "already at mount point",
			path:       "/mount/file.txt",
			mountPoint: "/mount",
			cachePath:  "/cache",
			expected:   "/mount/file.txt",
		},
		{
			name:       "nested path",
			path:       "/cache/dir/subdir/file.txt",
			mountPoint: "/mount",
			cachePath:  "/cache",
			expected:   "/mount/dir/subdir/file.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RebaseToMountPoint(tt.path, tt.mountPoint, tt.cachePath)
			if result != tt.expected {
				t.Errorf("RebaseToMountPoint(%s, %s, %s) = %s, expected %s", tt.path, tt.mountPoint, tt.cachePath, result, tt.expected)
			}
		})
	}
}

func TestRebaseToSource(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		srcDir   string
		cachePath string
		expected string
	}{
		{
			name:      "simple file",
			path:      "/cache/file.txt",
			srcDir:    "/source",
			cachePath: "/cache",
			expected:  "/source/file.txt",
		},
		{
			name:      "already at source",
			path:      "/source/file.txt",
			srcDir:    "/source",
			cachePath: "/cache",
			expected:  "/source/file.txt",
		},
		{
			name:      "nested path",
			path:      "/cache/dir/subdir/file.txt",
			srcDir:    "/source",
			cachePath: "/cache",
			expected:  "/source/dir/subdir/file.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RebaseToSource(tt.path, tt.srcDir, tt.cachePath)
			if result != tt.expected {
				t.Errorf("RebaseToSource(%s, %s, %s) = %s, expected %s", tt.path, tt.srcDir, tt.cachePath, result, tt.expected)
			}
		})
	}
}

