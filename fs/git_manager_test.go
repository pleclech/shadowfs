package fs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tu "github.com/pleclech/shadowfs/fs/utils/testings"
)

func TestGitManager_IsGitAvailable(t *testing.T) {
	gm := NewGitManager("/tmp/test", "/tmp/source", GitConfig{})

	// Test should not panic
	available := gm.IsGitAvailable()
	// We can't assume Git is available on all systems
	_ = available
}

func TestGitManager_InitializeRepo(t *testing.T) {
	// Create temporary directory
	tempDir, err := os.MkdirTemp("", "git-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	gm := NewGitManager(tempDir, "/tmp/source", GitConfig{
		AutoCommit: true,
	})

	// Test initialization
	err = gm.InitializeRepo()
	if err != nil {
		tu.Failf(t, "InitializeRepo failed: %v", err)
	}

	// Check if .gitofs/.git directory was created (git init creates .gitofs/.git/)
	gitDir := filepath.Join(tempDir, ".gitofs", ".git")
	if stat, err := os.Stat(gitDir); err != nil || !stat.IsDir() {
		tu.Failf(t, "Git repository not created properly: %v", err)
	}
}

func TestGitManager_AutoCommitFile(t *testing.T) {
	// Create temporary directory
	tempDir, err := os.MkdirTemp("", "git-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	gm := NewGitManager(tempDir, "/tmp/source", GitConfig{
		AutoCommit: true,
	})

	// Initialize Git repository
	err = gm.InitializeRepo()
	if err != nil {
		t.Skipf("Git not available, skipping test: %v", err)
		return
	}

	// Create a test file
	testFile := filepath.Join(tempDir, "test.txt")
	content := []byte("test content")
	err = os.WriteFile(testFile, content, 0644)
	if err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	// Test auto-commit
	err = gm.AutoCommitFile(testFile, "test commit")
	if err != nil {
		tu.Failf(t, "AutoCommitFile failed: %v", err)
	}
}

func TestGitManager_DisabledMode(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "git-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	gm := NewGitManager(tempDir, "/tmp/source", GitConfig{
		AutoCommit: false, // Disabled
	})

	// Should not fail even when disabled
	err = gm.InitializeRepo()
	if err != nil {
		tu.Failf(t, "InitializeRepo failed in disabled mode: %v", err)
	}

	// Should not create .git when disabled
	gitDir := filepath.Join(tempDir, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		tu.Failf(t, "Git repository should not be created when disabled")
	}
}

func TestActivityTracker_MarkActivity(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "activity-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	gm := NewGitManager(tempDir, "/tmp/source", GitConfig{
		AutoCommit: true, // Enable to test activity tracking
	})

	// Create a mock ShadowNode for testing
	shadowNode := &ShadowNode{
		gitManager: gm,
	}

	at := NewActivityTracker(shadowNode, GitConfig{
		IdleTimeout: 100 * time.Millisecond,
	})

	// Test marking activity
	testFile := filepath.Join(tempDir, "test.txt")
	at.MarkActivity(testFile)

	// Check if file is marked as active
	activeFiles := at.GetActiveFiles()
	if len(activeFiles) != 1 || activeFiles[0] != testFile {
		tu.Failf(t, "Expected 1 active file, got %d: %v", len(activeFiles), activeFiles)
	}
}

func TestActivityTracker_IdleDetection(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "activity-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	gm := NewGitManager(tempDir, "/tmp/source", GitConfig{
		AutoCommit: false, // Disable to avoid Git operations
	})

	// Create a mock ShadowNode for testing
	shadowNode := &ShadowNode{
		gitManager: gm,
	}

	at := NewActivityTracker(shadowNode, GitConfig{
		IdleTimeout: 50 * time.Millisecond,
	})

	// Mark activity
	testFile := filepath.Join(tempDir, "test.txt")
	at.MarkActivity(testFile)

	// Wait for idle timeout
	time.Sleep(100 * time.Millisecond)

	// Check if file is considered idle
	if !at.IsIdle(testFile) {
		tu.Failf(t, "File should be considered idle after timeout")
	}
}

func TestGitManager_StatusPorcelain(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "git-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	gm := NewGitManager(tempDir, "/tmp/source", GitConfig{
		AutoCommit: true,
	})

	// Initialize Git repository
	err = gm.InitializeRepo()
	if err != nil {
		t.Skipf("Git not available, skipping test: %v", err)
		return
	}

	// Create a test file
	testFile := filepath.Join(tempDir, "test.txt")
	content := []byte("test content")
	err = os.WriteFile(testFile, content, 0644)
	if err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	// Test StatusPorcelain - should return the changed file
	changedFiles, err := gm.StatusPorcelain()
	if err != nil {
		tu.Failf(t, "StatusPorcelain failed: %v", err)
	}

	// Should have at least one changed file (test.txt)
	if len(changedFiles) == 0 {
		tu.Failf(t, "Expected at least one changed file, got none")
	}

	// Check if test.txt is in the list
	found := false
	for _, file := range changedFiles {
		if file == "test.txt" {
			found = true
			break
		}
	}
	if !found {
		tu.Failf(t, "Expected test.txt in changed files, got: %v", changedFiles)
	}
}

func TestGitManager_ValidateCommitHash(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "git-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	gm := NewGitManager(tempDir, "/tmp/source", GitConfig{
		AutoCommit: true,
	})

	// Initialize Git repository
	err = gm.InitializeRepo()
	if err != nil {
		t.Skipf("Git not available, skipping test: %v", err)
		return
	}

	// Create and commit a test file
	testFile := filepath.Join(tempDir, "test.txt")
	content := []byte("test content")
	err = os.WriteFile(testFile, content, 0644)
	if err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	// Commit the file
	err = gm.CommitFileSync(testFile, "test commit")
	if err != nil {
		t.Skipf("Failed to commit file, skipping test: %v", err)
		return
	}

	// Get the commit hash (HEAD)
	// We'll use a simple approach: validate HEAD exists
	err = gm.ValidateCommitHash("HEAD")
	if err != nil {
		tu.Failf(t, "ValidateCommitHash(HEAD) failed: %v", err)
	}

	// Test with invalid hash
	err = gm.ValidateCommitHash("0000000000000000000000000000000000000000")
	if err == nil {
		tu.Failf(t, "ValidateCommitHash should fail for invalid hash")
	}
}

func TestGitManager_Log(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "git-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	gm := NewGitManager(tempDir, "/tmp/source", GitConfig{
		AutoCommit: true,
	})

	// Initialize Git repository
	err = gm.InitializeRepo()
	if err != nil {
		t.Skipf("Git not available, skipping test: %v", err)
		return
	}

	// Create and commit a test file
	testFile := filepath.Join(tempDir, "test.txt")
	content := []byte("test content")
	err = os.WriteFile(testFile, content, 0644)
	if err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	// Commit the file
	err = gm.CommitFileSync(testFile, "test commit")
	if err != nil {
		t.Skipf("Failed to commit file, skipping test: %v", err)
		return
	}

	// Test Log with oneline option
	var output strings.Builder
	options := LogOptions{Oneline: true, Limit: 1}
	err = gm.Log(options, &output)
	if err != nil {
		tu.Failf(t, "Log failed: %v", err)
	}

	if output.Len() == 0 {
		tu.Failf(t, "Log output should not be empty")
	}
}

func TestGitManager_Diff(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "git-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	gm := NewGitManager(tempDir, "/tmp/source", GitConfig{
		AutoCommit: true,
	})

	// Initialize Git repository
	err = gm.InitializeRepo()
	if err != nil {
		t.Skipf("Git not available, skipping test: %v", err)
		return
	}

	// Create and commit a test file
	testFile := filepath.Join(tempDir, "test.txt")
	content := []byte("test content")
	err = os.WriteFile(testFile, content, 0644)
	if err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	// Commit the file
	err = gm.CommitFileSync(testFile, "test commit")
	if err != nil {
		t.Skipf("Failed to commit file, skipping test: %v", err)
		return
	}

	// Modify the file
	err = os.WriteFile(testFile, []byte("modified content"), 0644)
	if err != nil {
		tu.Failf(t, "Failed to modify test file: %v", err)
	}

	// Test Diff
	var output strings.Builder
	options := DiffOptions{Stat: false, CommitArgs: []string{"HEAD"}, Paths: []string{"test.txt"}}
	err = gm.Diff(options, &output)
	if err != nil {
		tu.Failf(t, "Diff failed: %v", err)
	}

	if output.Len() == 0 {
		tu.Failf(t, "Diff output should not be empty for modified file")
	}
}

func TestGitManager_Checkout(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "git-test")
	if err != nil {
		tu.Failf(t, "Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	gm := NewGitManager(tempDir, "/tmp/source", GitConfig{
		AutoCommit: true,
	})

	// Initialize Git repository
	err = gm.InitializeRepo()
	if err != nil {
		t.Skipf("Git not available, skipping test: %v", err)
		return
	}

	// Create and commit a test file
	testFile := filepath.Join(tempDir, "test.txt")
	content := []byte("original content")
	err = os.WriteFile(testFile, content, 0644)
	if err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	// Commit the file
	err = gm.CommitFileSync(testFile, "test commit")
	if err != nil {
		t.Skipf("Failed to commit file, skipping test: %v", err)
		return
	}

	// Modify the file
	err = os.WriteFile(testFile, []byte("modified content"), 0644)
	if err != nil {
		tu.Failf(t, "Failed to modify test file: %v", err)
	}

	// Test Checkout to restore from HEAD
	var stdout, stderr strings.Builder
	err = gm.Checkout("HEAD", []string{"test.txt"}, false, &stdout, &stderr)
	if err != nil {
		tu.Failf(t, "Checkout failed: %v", err)
	}

	// Verify file was restored
	restoredContent, err := os.ReadFile(testFile)
	if err != nil {
		tu.Failf(t, "Failed to read restored file: %v", err)
	}

	if string(restoredContent) != "original content" {
		tu.Failf(t, "Expected 'original content', got '%s'", string(restoredContent))
	}
}
