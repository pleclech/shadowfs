package utils

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestValidateName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"valid name", "file.txt", true},
		{"valid name with dash", "my-file.txt", true},
		{"valid name with underscore", "my_file.txt", true},
		{"contains ..", "../file.txt", false},
		{"contains .. in middle", "file../test.txt", false},
		{"absolute path", "/file.txt", false},
		{"empty name", "", false},
		{"just dots", "..", false},
		{"single dot", ".", true},
		{"hidden file", ".hidden", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateName(tt.input)
			if result != tt.expected {
				t.Errorf("ValidateName(%q) = %v, expected %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestValidatePathWithinRoot(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "validation-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create subdirectories
	subDir := filepath.Join(tempDir, "subdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatalf("Failed to create subdirectory: %v", err)
	}

	tests := []struct {
		name      string
		path      string
		root      string
		shouldErr bool
	}{
		{
			name:      "path within root",
			path:      filepath.Join(tempDir, "file.txt"),
			root:      tempDir,
			shouldErr: false,
		},
		{
			name:      "subdirectory path",
			path:      filepath.Join(subDir, "file.txt"),
			root:      tempDir,
			shouldErr: false,
		},
		{
			name:      "root itself",
			path:      tempDir,
			root:      tempDir,
			shouldErr: false,
		},
		{
			name:      "path outside root",
			path:      "/tmp/outside",
			root:      tempDir,
			shouldErr: true,
		},
		{
			name:      "path traversal attempt",
			path:      filepath.Join(tempDir, "..", "outside"),
			root:      tempDir,
			shouldErr: true,
		},
		{
			name:      "relative path within root",
			path:      filepath.Join(tempDir, "file.txt"),
			root:      tempDir,
			shouldErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleaned, err := ValidatePathWithinRoot(tt.path, tt.root)
			if tt.shouldErr {
				if err == nil {
					t.Errorf("ValidatePathWithinRoot(%q, %q) expected error, got nil", tt.path, tt.root)
				}
				if err != syscall.EPERM {
					t.Errorf("ValidatePathWithinRoot(%q, %q) expected EPERM, got %v", tt.path, tt.root, err)
				}
			} else {
				if err != nil {
					t.Errorf("ValidatePathWithinRoot(%q, %q) unexpected error: %v", tt.path, tt.root, err)
				}
				if cleaned == "" {
					t.Error("ValidatePathWithinRoot() returned empty cleaned path")
				}
			}
		})
	}
}

func TestValidatePathWithinRoot_InvalidPath(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "validation-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Test with invalid characters (if any)
	_, err = ValidatePathWithinRoot("\x00invalid", tempDir)
	if err == nil {
		t.Error("ValidatePathWithinRoot() with invalid path should return error")
	}
}

