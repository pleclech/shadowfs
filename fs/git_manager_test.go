package fs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	gm := NewGitManager(tempDir, "/tmp/source", GitConfig{
		AutoCommit: true,
	})

	// Test initialization
	err = gm.InitializeRepo()
	if err != nil {
		t.Errorf("InitializeRepo failed: %v", err)
	}

	// Check if .gitofs/.git directory was created (git init creates .gitofs/.git/)
	gitDir := filepath.Join(tempDir, ".gitofs", ".git")
	if stat, err := os.Stat(gitDir); err != nil || !stat.IsDir() {
		t.Errorf("Git repository not created properly: %v", err)
	}

	// remove we don't want to overwrite an existing .gitignore in source dir
	// Check if .gitignore was created
	// gitIgnorePath := filepath.Join(tempDir, ".gitignore")
	// if _, err := os.Stat(gitIgnorePath); err != nil {
	// 	t.Errorf(".gitignore not created")
	// }
}

func TestGitManager_AutoCommitFile(t *testing.T) {
	// Create temporary directory
	tempDir, err := os.MkdirTemp("", "git-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
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
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Test auto-commit
	err = gm.AutoCommitFile(testFile, "test commit")
	if err != nil {
		t.Errorf("AutoCommitFile failed: %v", err)
	}
}

func TestGitManager_DisabledMode(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "git-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	gm := NewGitManager(tempDir, "/tmp/source", GitConfig{
		AutoCommit: false, // Disabled
	})

	// Should not fail even when disabled
	err = gm.InitializeRepo()
	if err != nil {
		t.Errorf("InitializeRepo failed in disabled mode: %v", err)
	}

	// Should not create .git when disabled
	gitDir := filepath.Join(tempDir, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		t.Errorf("Git repository should not be created when disabled")
	}
}

func TestActivityTracker_MarkActivity(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "activity-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
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
		t.Errorf("Expected 1 active file, got %d: %v", len(activeFiles), activeFiles)
	}
}

func TestActivityTracker_IdleDetection(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "activity-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
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
		t.Errorf("File should be considered idle after timeout")
	}
}
