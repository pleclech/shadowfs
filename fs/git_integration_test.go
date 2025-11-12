//go:build linux
// +build linux

package fs

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestGitOperationsInMountPoint(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create test directories
	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Create a test file in source
	testFile := filepath.Join(srcDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create shadowfs root
	_, err := NewShadowRoot(mountPoint, srcDir, "")
	if err != nil {
		t.Fatalf("Failed to create shadow root: %v", err)
	}

	// Give filesystem time to start
	time.Sleep(100 * time.Millisecond)

	// Test Git operations in mount point
	gitTestDir := filepath.Join(mountPoint, "git-test")
	if err := os.Mkdir(gitTestDir, 0755); err != nil {
		t.Fatalf("Failed to create git test directory: %v", err)
	}

	// Initialize Git repository
	cmd := exec.Command("git", "init")
	cmd.Dir = gitTestDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to init git repo: %v, output: %s", err, string(out))
	}

	// Configure Git
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = gitTestDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to configure git email: %v, output: %s", err, string(output))
	}

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = gitTestDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to configure git name: %v, output: %s", err, string(output))
	}

	// Create a test file in mount point
	testFileInMount := filepath.Join(gitTestDir, "test.txt")
	if err := os.WriteFile(testFileInMount, []byte("hello world"), 0644); err != nil {
		t.Fatalf("Failed to create test file in mount: %v", err)
	}

	// Add file to Git
	cmd = exec.Command("git", "add", "test.txt")
	cmd.Dir = gitTestDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to git add: %v, output: %s", err, string(output))
	}

	// Commit file
	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = gitTestDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to git commit: %v, output: %s", err, string(output))
	}

	// Verify commit exists
	cmd = exec.Command("git", "log", "--oneline")
	cmd.Dir = gitTestDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to git log: %v, output: %s", err, string(out))
	}

	if len(out) == 0 {
		t.Error("Expected git log to show commits, but got empty output")
	}

	// Test creating another file and committing
	testFile2 := filepath.Join(gitTestDir, "test2.txt")
	if err := os.WriteFile(testFile2, []byte("second file"), 0644); err != nil {
		t.Fatalf("Failed to create second test file: %v", err)
	}

	cmd = exec.Command("git", "add", "test2.txt")
	cmd.Dir = gitTestDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to git add second file: %v, output: %s", err, string(output))
	}

	cmd = exec.Command("git", "commit", "-m", "Second commit")
	cmd.Dir = gitTestDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to git commit second file: %v, output: %s", err, string(output))
	}

	// Verify we have 2 commits
	cmd = exec.Command("git", "rev-list", "--count", "HEAD")
	cmd.Dir = gitTestDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to count commits: %v, output: %s", err, string(output))
	}

	if string(output) != "2\n" {
		t.Errorf("Expected 2 commits, got: %s", string(output))
	}

	// Test Git status shows clean working directory
	cmd = exec.Command("git", "status", "--porcelain")
	cmd.Dir = gitTestDir
	output, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to git status: %v, output: %s", err, string(output))
	}

	if len(output) != 0 {
		t.Errorf("Expected clean working directory, got: %s", string(output))
	}

	t.Logf("Git operations completed successfully in mount point: %s", gitTestDir)
}
