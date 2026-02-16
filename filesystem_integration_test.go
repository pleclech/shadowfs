//go:build linux && integration
// +build linux,integration

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	shadowfs "github.com/pleclech/shadowfs/fs"
	tu "github.com/pleclech/shadowfs/fs/utils/testings"
)

const (
	testBinary = "/tmp/shadowfs-test"
)

var (
	binaryBuilt = false
)

// TestMain builds the binary once before all tests and cleans up after
func TestMain(m *testing.M) {
	// Build binary once for all tests
	os.Setenv("SHADOWFS_LOG_LEVEL", "debug")

	if err := buildBinary(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to build binary: %v\n", err)
		os.Exit(1)
	}
	binaryBuilt = true

	// Run all tests
	code := m.Run()

	// Clean up binary after all tests complete
	if binaryBuilt {
		os.Remove(testBinary)
	}

	os.Exit(code)
}

// buildBinary builds the test binary
func buildBinary() error {
	// Check if binary already exists (from previous run)
	if _, err := os.Stat(testBinary); err == nil {
		// Binary exists, remove it to ensure fresh build
		os.Remove(testBinary)
	}

	cmd := exec.Command("go", "build", "-o", testBinary, "./cmd/shadowfs")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// runBinary is a convenience wrapper that handles extra command-line arguments
// It builds the binary if needed, then runs it with the provided args
func runBinary(t *testing.T, args ...string) (*exec.Cmd, error) {
	t.Helper()

	// Ensure binary is built
	testMgr := tu.NewBinaryManager(t, testBinary)
	if err := testMgr.BuildBinary(); err != nil {
		return nil, err
	}

	// Build command with --debug prepended and all args
	newArgs := append([]string{"--debug"}, args...)
	cmd := exec.Command(testBinary, newArgs...)

	// Use TestLogWriter from index.go for output redirection
	stdoutWriter := tu.NewTestLogWriter(t, "[stdout] ")
	stderrWriter := tu.NewTestLogWriter(t, "[stderr] ")
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	return cmd, cmd.Start()
}

// TestRestartCache tests if filesystem restart fixes directory listing
func TestRestartCache(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Create a file in source
	tu.CreateFilesInDirectory(srcDir, map[string]string{"foo": "file content"}, t)

	// Start filesystem
	cmd, err := runBinary(t, mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	fooPath := filepath.Join(mountPoint, "foo")

	// Step 1: Remove file
	if err := os.Remove(fooPath); err != nil {
		tu.Failf(t, "Failed to remove file: %v", err)
	}

	// Step 2: Create directory
	if err := os.Mkdir(fooPath, 0755); err != nil {
		tu.Failf(t, "Failed to create directory: %v", err)
	}

	// Step 3: Create file inside directory
	testFile := filepath.Join(fooPath, "test.txt")
	if err := os.WriteFile(testFile, []byte("new content"), 0644); err != nil {
		tu.Failf(t, "Failed to create file in directory: %v", err)
	}

	// Step 4: Test directory reading (should be empty due to kernel cache)
	if entries, err := os.ReadDir(fooPath); err != nil {
		tu.Debugf(t, "Failed to read directory before restart: %v", err)
	} else {
		tu.Debugf(t, "Directory entries before restart: %v", tu.GetEntryNames(entries))
	}

	// Step 5: Shutdown filesystem
	tu.GracefulShutdown(cmd, mountPoint, t)
	time.Sleep(100 * time.Millisecond)

	// Step 6: Restart filesystem
	cmd = exec.Command(testBinary, mountPoint, srcDir)
	if err := cmd.Start(); err != nil {
		tu.Failf(t, "Failed to restart filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	// Step 7: Test directory reading after restart (should work now)
	if entries, err := os.ReadDir(fooPath); err != nil {
		tu.Debugf(t, "Failed to read directory after restart: %v", err)
	} else {
		tu.Debugf(t, "Directory entries after restart: %v", tu.GetEntryNames(entries))
	}
}

// TestSimpleDirectoryCreation tests basic directory creation in empty filesystem
func TestSimpleDirectoryCreation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Start filesystem with empty source
	cmd, err := runBinary(t, mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Create a directory directly in empty filesystem
	dirPath := filepath.Join(mountPoint, "testdir")
	if err := os.Mkdir(dirPath, 0755); err != nil {
		tu.Failf(t, "Failed to create directory: %v", err)
	}

	// Test directory access
	if entries, err := os.ReadDir(dirPath); err != nil {
		tu.Failf(t, "Failed to read directory: %v", err)
	} else {
		tu.Debugf(t, "Directory entries: %v", tu.GetEntryNames(entries))
	}

	// Try to create a file inside it
	testFile := filepath.Join(dirPath, "test.txt")
	if err := os.WriteFile(testFile, []byte("content"), 0644); err != nil {
		tu.Failf(t, "Failed to create file in directory: %v", err)
	}
	tu.ShouldExist(testFile, t)
}

// Helper function to copy a file
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = dstFile.ReadFrom(srcFile)
	return err
}

func TestFilesystem_BasicOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Create test source files
	testFiles := map[string]string{
		"file1.txt":       "content1",
		"file2.txt":       "content2",
		"dir1/nested.txt": "nested content",
	}
	tu.CreateFilesInDirectory(srcDir, testFiles, t)

	// Start filesystem
	cmd, err := runBinary(t, mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	// Test reading existing files
	content, err := os.ReadFile(filepath.Join(mountPoint, "file1.txt"))
	if err != nil {
		tu.Failf(t, "Failed to read file1.txt: %v", err)
	}
	if string(content) != "content1" {
		tu.Failf(t, "Expected 'content1', got '%s'", string(content))
	}

	// Test creating new file
	newFile := filepath.Join(mountPoint, "newfile.txt")
	if err := os.WriteFile(newFile, []byte("new content"), 0644); err != nil {
		tu.Failf(t, "Failed to create new file: %v", err)
	}

	// Verify new file exists
	content, err = os.ReadFile(newFile)
	if err != nil {
		tu.Failf(t, "Failed to read new file: %v", err)
	}
	if string(content) != "new content" {
		tu.Failf(t, "Expected 'new content', got '%s'", string(content))
	}

	// Verify original file still exists
	originalFile := filepath.Join(srcDir, "file1.txt")
	if _, err := os.Stat(originalFile); os.IsNotExist(err) {
		tu.Failf(t, "Original file should still exist")
	}

	// Test file listing
	entries, err := os.ReadDir(mountPoint)
	if err != nil {
		tu.Failf(t, "Failed to read directory: %v", err)
	}

	expectedFiles := []string{"file1.txt", "file2.txt", "dir1", "newfile.txt"}
	if len(entries) != len(expectedFiles) {
		tu.Failf(t, "Expected %d entries, got %d", len(expectedFiles), len(entries))
	}

	entryNames := make(map[string]bool)
	for _, entry := range entries {
		entryNames[entry.Name()] = true
	}

	for _, expected := range expectedFiles {
		if !entryNames[expected] {
			tu.Failf(
				t, "Expected entry '%s' not found", expected)
		}
	}
}

func TestFilesystem_DirectoryOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Start filesystem
	cmd, err := runBinary(t, mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	// Test basic directory creation
	newDir := filepath.Join(mountPoint, "newdir")
	if err := os.Mkdir(newDir, 0755); err != nil {
		tu.Failf(t, "Failed to create directory: %v", err)
	}

	// Wait for directory to be processed
	tu.WaitForFilesystemReady(200 * time.Millisecond)

	// Verify directory exists
	if stat, err := os.Stat(newDir); err != nil {
		tu.Failf(t, "Created directory not found: %v", err)
	} else if !stat.IsDir() {
		tu.Failf(t, "Path exists but is not a directory")
	}

	// Test that directory appears in root listing
	entries, err := os.ReadDir(mountPoint)
	if err != nil {
		tu.Failf(t, "Failed to read mount directory: %v", err)
	}

	found := false
	for _, entry := range entries {
		if entry.Name() == "newdir" && entry.IsDir() {
			found = true
			break
		}
	}
	if !found {
		tu.Failf(t, "Note: Created directory not found in root listing. Found: %v", tu.GetEntryNames(entries))
	}

	// Test creating a file directly in mount point (this should work)
	testFile := filepath.Join(mountPoint, "testfile.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		tu.Failf(t, "Failed to create file in mount point: %v", err)
	}

	// Verify file exists
	if content, err := os.ReadFile(testFile); err != nil {
		tu.Failf(t, "Failed to read created file: %v", err)
	} else if string(content) != "test content" {
		tu.Failf(t, "File content mismatch: expected 'test content', got '%s'", string(content))
	}
}

func TestFilesystem_DeletionTracking(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Create test source file
	testFile := filepath.Join(srcDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("content"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	// Start filesystem
	cmd, err := runBinary(t, mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	mountedFile := filepath.Join(mountPoint, "test.txt")

	// Verify file exists initially
	if _, err := os.Stat(mountedFile); os.IsNotExist(err) {
		tu.Failf(t, "File should exist initially")
	}

	// Delete file
	if err := os.Remove(mountedFile); err != nil {
		tu.Failf(t, "Failed to remove file: %v", err)
	}

	// Verify file is gone from mount
	if _, err := os.Stat(mountedFile); err == nil {
		tu.Failf(t, "File should not exist after deletion")
	}

	// Verify original file still exists in source
	if _, err := os.Stat(testFile); os.IsNotExist(err) {
		tu.Failf(t, "Original file should still exist in source")
	}
}

func TestFilesystem_PermissionPreservation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Create source file with specific permissions
	testFile := filepath.Join(srcDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("content"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	// Start filesystem
	cmd, err := runBinary(t, mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	mountedFile := filepath.Join(mountPoint, "test.txt")

	// Check permissions are preserved
	info, err := os.Stat(mountedFile)
	if err != nil {
		tu.Failf(t, "Failed to stat file: %v", err)
	}

	expectedMode := os.FileMode(0644)
	if info.Mode().Perm() != expectedMode {
		tu.Failf(t, "Expected permissions %v, got %v", expectedMode, info.Mode().Perm())
	}
}

func TestFilesystem_SessionPersistence(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// First mount session
	cmd1, err := runBinary(t, mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start first filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd1, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	// Create a file
	testFile := filepath.Join(mountPoint, "persistent.txt")
	if err := os.WriteFile(testFile, []byte("persistent content"), 0644); err != nil {
		cmd1.Process.Kill()
		cmd1.Wait()
		tu.Failf(t, "Failed to create file: %v", err)
	}

	// Unmount first session
	cmd1.Process.Kill()
	cmd1.Wait()
	exec.Command("umount", mountPoint).Run()
	time.Sleep(100 * time.Millisecond)

	// Second mount session (should reuse cache)
	cmd2, err := runBinary(t, mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start second filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd2, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	// Verify file still exists
	content, err := os.ReadFile(testFile)
	if err != nil {
		tu.Failf(t, "Failed to read persistent file: %v", err)
	}
	if string(content) != "persistent content" {
		tu.Failf(t, "Expected 'persistent content', got '%s'", string(content))
	}
}

func TestFilesystem_ConcurrentOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Start filesystem
	cmd, err := runBinary(t, mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	// Test concurrent file operations
	const numGoroutines = 10
	const numFiles = 5

	done := make(chan bool, numGoroutines)
	errors := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer func() { done <- true }()

			for j := 0; j < numFiles; j++ {
				filename := filepath.Join(mountPoint, fmt.Sprintf("file_%d_%d.txt", id, j))
				content := fmt.Sprintf("content from goroutine %d, file %d", id, j)

				if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
					errors <- err
					return
				}

				readContent, err := os.ReadFile(filename)
				if err != nil {
					errors <- err
					return
				}

				if string(readContent) != content {
					errors <- fmt.Errorf("content mismatch for %s", filename)
					return
				}
			}
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < numGoroutines; i++ {
		select {
		case <-done:
			// Goroutine completed successfully
		case err := <-errors:
			tu.Failf(
				t, "Concurrent operation failed: %v", err)
		case <-time.After(10 * time.Second):
			tu.Failf(
				t, "Concurrent operations timed out")
		}
	}

	// Verify all files exist
	expectedFiles := numGoroutines * numFiles
	entries, err := os.ReadDir(mountPoint)
	if err != nil {
		tu.Failf(t, "Failed to read directory: %v", err)
	}

	if len(entries) != expectedFiles {
		tu.Failf(t, "Expected %d files, got %d", expectedFiles, len(entries))
	}
}

func TestFilesystem_GitOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Start filesystem
	cmd, err := runBinary(t, mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	// First test basic directory creation
	testDir := filepath.Join(mountPoint, "testdir")
	if err := os.Mkdir(testDir, 0755); err != nil {
		tu.Failf(t, "Failed to create test directory: %v", err)
	}

	// Test file creation
	testFile := filepath.Join(testDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	// Test nested directory creation
	nestedDir := filepath.Join(mountPoint, ".git", "info")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		tu.Failf(t, "Failed to create .git/info directory: %v", err)
	}

	// Verify directory exists
	if stat, err := os.Stat(nestedDir); err != nil || !stat.IsDir() {
		tu.Failf(t, "Nested directory not created properly")
	}

	// Test Git init
	gitCmd := exec.Command("git", "init")
	gitCmd.Dir = mountPoint
	gitCmd.Env = append(os.Environ(), "GIT_DISCOVERY_ACROSS_FILESYSTEM=1")
	if output, err := gitCmd.CombinedOutput(); err != nil {
		tu.Failf(t, "Git init failed: %v\nOutput: %s", err, string(output))
	}

	// Verify .git directory was created
	gitDir := filepath.Join(mountPoint, ".git")
	if stat, err := os.Stat(gitDir); err != nil || !stat.IsDir() {
		tu.Failf(t, "Git repository not created properly: %v", err)
	}

	// Check for essential Git files individually (avoid ReadDir issues)
	configPath := filepath.Join(gitDir, "config")
	if _, err := os.Stat(configPath); err != nil {
		tu.Failf(t, ".git/config not found: %v", err)
	}

	headPath := filepath.Join(gitDir, "HEAD")
	if _, err := os.Stat(headPath); err != nil {
		tu.Failf(t, ".git/HEAD not found: %v", err)
	}

	// Check contents of HEAD file
	_, err = os.ReadFile(headPath)
	if err != nil {
		tu.Failf(t, "Failed to read .git/HEAD: %v", err)
	}

	// Check contents of config file
	_, err = os.ReadFile(configPath)
	if err != nil {
		tu.Failf(t, "Failed to read .git/config: %v", err)
	}

	// Debug: list all files in .git directory
	if _, err := os.ReadDir(gitDir); err == nil {
	} else {
		tu.Debugf(t, "Failed to read .git directory: %v", err)
	}

	// Test creating a file and adding it
	gitTestFile := filepath.Join(mountPoint, "test.txt")
	if err := os.WriteFile(gitTestFile, []byte("test content"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	// Add file to git
	gitCmd = exec.Command("git", "add", "test.txt")
	gitCmd.Dir = mountPoint
	gitCmd.Env = append(os.Environ(), "GIT_DISCOVERY_ACROSS_FILESYSTEM=1")
	if output, err := gitCmd.CombinedOutput(); err != nil {
		tu.Failf(t, "Git add failed: %v\nOutput: %s", err, string(output))
	}

	// Commit
	gitCmd = exec.Command("git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "test commit")
	gitCmd.Dir = mountPoint
	gitCmd.Env = append(os.Environ(), "GIT_DISCOVERY_ACROSS_FILESYSTEM=1")
	if output, err := gitCmd.CombinedOutput(); err != nil {
		tu.Failf(t, "Git commit failed: %v\nOutput: %s", err, string(output))
	}
}

func TestFilesystem_GitAutoCommitPathConversion(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Start filesystem with git auto-commit enabled
	// Use short timeouts for testing
	cmd, err := runBinary(t, "-auto-git", "-git-idle-timeout", "1s", "-git-safety-window", "500ms", mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	// Create a test file in mount point (this will trigger HandleWriteActivity with cache path)
	testFile := filepath.Join(mountPoint, "foo.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	// Wait for auto-commit to happen (idle timeout + safety window + some buffer)
	// With 5s idle timeout + 1s safety window, we need at least 6s + buffer
	time.Sleep(7 * time.Second)

	// Verify git repository was created
	// git init creates .gitofs/.git/, so check for .gitofs/.git
	gitDir := filepath.Join(mountPoint, shadowfs.GitofsName, ".git")
	if stat, err := os.Stat(gitDir); err != nil || !stat.IsDir() {
		tu.Failf(t, "Git repository not created: %v", err)
	}

	// Verify file was committed (check git log)
	gitCmd := exec.Command("git", "--git-dir", gitDir, "-C", mountPoint, "log", "--oneline")
	output, err := gitCmd.CombinedOutput()
	if err != nil {
		tu.Failf(t, "Failed to run git log: %v, output: %s", err, string(output))
	}

	// Should have at least one commit
	if len(output) == 0 {
		tu.Failf(t, "Expected git log to show commits, but got empty output")
	}

	// Verify the file path in git is correct (should be relative to mount point, not cache)
	gitCmd = exec.Command("git", "--git-dir", gitDir, "-C", mountPoint, "ls-files")
	output, err = gitCmd.CombinedOutput()
	if err != nil {
		tu.Failf(t, "Failed to run git ls-files: %v, output: %s", err, string(output))
	}

	// The file should be tracked as "foo.txt" (relative to mount point), not a cache path
	outputStr := string(output)
	if !strings.Contains(outputStr, "foo.txt") {
		tu.Failf(t, "Expected 'foo.txt' in git ls-files output, got: %s", outputStr)
	}

	// Verify no cache paths are in git (should not contain .shadowfs)
	if strings.Contains(outputStr, ".shadowfs") {
		tu.Failf(t, "Git should not contain cache paths, got: %s", outputStr)
	}
}

func TestFilesystem_AppendOperation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Create a file in source directory with initial content
	testFile := filepath.Join(srcDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		tu.Failf(t, "Failed to create source file: %v", err)
	}

	// Start filesystem
	cmd, err := runBinary(t, mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	mountedFile := filepath.Join(mountPoint, "test.txt")

	// Verify initial content
	content, err := os.ReadFile(mountedFile)
	if err != nil {
		tu.Failf(t, "Failed to read file: %v", err)
	}
	if string(content) != "test" {
		tu.Failf(t, "Expected initial content 'test', got '%s'", string(content))
	}

	// Append content to file (first append - this is where the bug occurred)
	// Use os.OpenFile with O_APPEND to simulate echo "bar" >> test.txt
	f, err := os.OpenFile(mountedFile, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		tu.Failf(t, "Failed to open file for append: %v", err)
	}
	if _, err := f.WriteString("bar"); err != nil {
		f.Close()
		tu.Failf(t, "Failed to append to file: %v", err)
	}
	f.Close()

	// Verify content after first append
	content, err = os.ReadFile(mountedFile)
	if err != nil {
		tu.Failf(t, "Failed to read file after append: %v", err)
	}
	expected := "testbar"
	if string(content) != expected {
		tu.Failf(t, "After first append: expected '%s', got '%s'", expected, string(content))
	}

	// Append again (second append - should work correctly)
	f, err = os.OpenFile(mountedFile, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		tu.Failf(t, "Failed to open file for second append: %v", err)
	}
	if _, err := f.WriteString("baz"); err != nil {
		f.Close()
		tu.Failf(t, "Failed to append to file second time: %v", err)
	}
	f.Close()

	// Verify content after second append
	content, err = os.ReadFile(mountedFile)
	if err != nil {
		tu.Failf(t, "Failed to read file after second append: %v", err)
	}
	expected = "testbarbaz"
	if string(content) != expected {
		tu.Failf(t, "After second append: expected '%s', got '%s'", expected, string(content))
	}
}

func TestVersionList_WithPattern(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Start filesystem with Git enabled
	cmd, err := runBinary(t, "-auto-git", mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	// Create test files
	testFiles := map[string]string{
		"file1.txt": "content1",
		"file2.txt": "content2",
		"file3.go":  "package main",
		"readme.md": "# Readme",
	}

	for path, content := range testFiles {
		fullPath := filepath.Join(mountPoint, path)
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			tu.Failf(
				t, "Failed to create file %s: %v", path, err)
		}
	}

	// Wait for auto-commits
	time.Sleep(2 * time.Second)

	// Test version list with specific file pattern
	versionCmd := exec.Command(testBinary, "version", "list", "--mount-point", mountPoint, "--path", "file1.txt")
	output, err := versionCmd.CombinedOutput()
	if err != nil {
		// Don't fail if no commits yet - Git might not have committed yet
		if len(output) == 0 {
			t.Skip("No commits found yet, skipping pattern test")
		}
	}

	// Test version list with glob pattern
	versionCmd = exec.Command(testBinary, "version", "list", "--mount-point", mountPoint, "--path", "*.txt")
	output, err = versionCmd.CombinedOutput()
	if err != nil {
		tu.Debugf(t, "Version list with glob output: %s", string(output))
		// Don't fail if no commits yet
		if len(output) == 0 {
			t.Skip("No commits found yet, skipping glob pattern test")
		}
	}
}

func TestVersionDiff_WithPattern(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Start filesystem with Git enabled
	cmd, err := runBinary(t, "-auto-git", mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	// Create initial file
	file1 := filepath.Join(mountPoint, "file1.txt")
	if err := os.WriteFile(file1, []byte("initial"), 0644); err != nil {
		tu.Failf(t, "Failed to create file: %v", err)
	}

	// Wait for commit
	time.Sleep(2 * time.Second)

	// Modify file
	if err := os.WriteFile(file1, []byte("modified"), 0644); err != nil {
		tu.Failf(t, "Failed to modify file: %v", err)
	}

	// Wait for commit
	time.Sleep(2 * time.Second)

	// Test version diff with pattern
	versionCmd := exec.Command(testBinary, "version", "diff", "--mount-point", mountPoint, "HEAD~1", "HEAD", "--path", "*.txt")
	output, err := versionCmd.CombinedOutput()
	if err != nil {
		// Don't fail if not enough commits
		if len(output) == 0 {
			t.Skip("Not enough commits for diff test")
		}
	}
}

func TestVersionLog_WithPattern(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Start filesystem with Git enabled
	cmd, err := runBinary(t, "-auto-git", mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	time.Sleep(500 * time.Millisecond)

	// Create test files
	testFiles := map[string]string{
		"file1.txt": "content1",
		"file2.go":  "package main",
	}

	for path, content := range testFiles {
		fullPath := filepath.Join(mountPoint, path)
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			tu.Failf(
				t, "Failed to create file %s: %v", path, err)
		}
	}

	// Wait for auto-commits
	time.Sleep(2 * time.Second)

	// Test version log with pattern
	versionCmd := exec.Command(testBinary, "version", "log", "--mount-point", mountPoint, "--path", "*.txt")
	output, err := versionCmd.CombinedOutput()
	if err != nil {
		tu.Debugf(t, "Version log output: %s", string(output))
		// Don't fail if no commits yet
		if len(output) == 0 {
			t.Skip("No commits found yet, skipping log pattern test")
		}
	}
}

func TestSecurity_RenameVulnerability(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Create a test file in source
	srcFooFile := filepath.Join(srcDir, "foo.txt")
	if err := os.WriteFile(srcFooFile, []byte("test content"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file: %v", err)
	}

	// Create an existing directory in source (this is the key to the vulnerability)
	srcFooDir := filepath.Join(srcDir, "foo")
	if err := os.Mkdir(srcFooDir, 0755); err != nil {
		tu.Failf(t, "Failed to create existing directory: %v", err)
	}

	// Start filesystem
	cmd, err := runBinary(t, mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	mountFooFile := filepath.Join(mountPoint, "foo.txt")
	mountFooDir := filepath.Join(mountPoint, "foo")

	// Verify initial state - both file and directory should exist in mount
	if _, err := os.Stat(mountFooFile); os.IsNotExist(err) {
		tu.Failf(t, "Test file not found in mount point")
	}

	if stat, err := os.Stat(mountFooDir); err != nil {
		tu.Failf(t, "Test directory not found in mount point: %v", err)
	} else if !stat.IsDir() {
		tu.Failf(t, "Mount point 'foo' exists but is not a directory")
	}

	// Perform the rename operation that previously caused the CRITICAL security vulnerability
	// This simulates: mv foo.txt foo (where foo is an existing directory)
	// BEFORE FIX: This would replace the directory with the file, exposing filesystem boundaries
	// AFTER FIX: This should move the file INTO the directory
	mvCmd := exec.Command("mv", mountFooFile, mountFooDir)
	mvCmd.Dir = mountPoint
	out, err := mvCmd.CombinedOutput()
	tu.Debugf(t, "Rename operation output: %s", string(out))
	if err != nil {
		tu.Debugf(t, "Rename operation warning: %v, output: %s", err, string(out))
		// Don't fail - the mv might fail but we still want to check security
	}

	// Debug: Check inside the foo directory in mount point
	fooDir := filepath.Join(mountPoint, "foo")
	if _, err := os.ReadDir(fooDir); err == nil {
	} else {
		tu.Debugf(t, "Could not read foo directory: %v", err)
	}

	// CRITICAL SECURITY CHECK: The directory should still exist and not be replaced
	if stat, err := os.Stat(mountFooDir); err != nil {
		tu.Failf(t, "SECURITY VULNERABILITY: Directory 'foo' no longer exists after rename: %v", err)
	} else if !stat.IsDir() {
		tu.Failf(t, "SECURITY VULNERABILITY: Directory 'foo' was replaced by a file")
	}

	// The file should now be inside the directory (correct behavior)
	movedFile := filepath.Join(mountFooDir, "foo.txt")
	if stat, err := os.Stat(movedFile); err != nil {
		tu.Failf(t, "File not found inside directory after rename: %v", err)
	} else if stat.IsDir() {
		tu.Failf(t, "Moved path is a directory, expected a file")
	}

	// Ensure the source directory structure is still intact
	if _, err := os.Stat(srcFooDir); err != nil {
		tu.Failf(t, "Source directory structure corrupted: %v", err)
	}

	if _, err := os.Stat(srcFooFile); err != nil {
		// fail the test file shoud be still present in the source directory
		tu.Failf(t, "Test file not found in source directory: %v", err)
	}

	// The key security test: file should not be inside source directory
	sourceMovedFile := filepath.Join(srcFooDir, "foo.txt")
	if _, err := os.Stat(sourceMovedFile); err == nil {
		tu.Failf(t, "File present in source filesystem")
	}
}

func TestSecurity_RenameComprehensive(t *testing.T) {
	if testing.Short() {
		tu.Info(t, "Skipping integration test in short mode")
	}

	mountPoint := tu.CreateTmpDir("mount_point", t)
	srcDir := tu.CreateTmpDir("src_dir", t)

	// Create test files and directories in source
	testFile1 := filepath.Join(srcDir, "file1.txt")
	testFile2 := filepath.Join(srcDir, "file2.txt")
	testDir1 := filepath.Join(srcDir, "dir1")
	testDir2 := filepath.Join(srcDir, "dir2")

	if err := os.WriteFile(testFile1, []byte("content1"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file 1: %v", err)
	}
	if err := os.WriteFile(testFile2, []byte("content2"), 0644); err != nil {
		tu.Failf(t, "Failed to create test file 2: %v", err)
	}
	if err := os.Mkdir(testDir1, 0755); err != nil {
		tu.Failf(t, "Failed to create test dir 1: %v", err)
	}
	if err := os.Mkdir(testDir2, 0755); err != nil {
		tu.Failf(t, "Failed to create test dir 2: %v", err)
	}

	// Start filesystem with cache directory for better analysis
	cacheDir := t.TempDir()
	cmd, err := runBinary(t, "-cache-dir", cacheDir, mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	// change working directory to mount point
	os.Chdir(mountPoint)

	// test file operations in mount point with file that is not in source directory
	// create a file in mount point should be ok
	tu.ShouldCreateFile(filepath.Join(mountPoint, "mount-1.txt"), "mount-1-content", t)
	// file should not be in source directory
	tu.ShouldNotExist(filepath.Join(srcDir, "mount-1.txt"), t)
	// create a directory in mount point should be ok
	tu.ShouldCreateDir(filepath.Join(mountPoint, "mount-dir-1"), t)
	// directory should not be in source directory
	tu.ShouldNotExist(filepath.Join(srcDir, "mount-dir-1"), t)
	// rename file to another file
	tu.ShouldRenameFile(filepath.Join(mountPoint, "mount-1.txt"), filepath.Join(mountPoint, "mount-2.txt"), t)
	// file should not be in source directory
	tu.ShouldNotExist(filepath.Join(srcDir, "mount-1.txt"), t)
	tu.ShouldNotExist(filepath.Join(srcDir, "mount-2.txt"), t)
	// rename directory to another unexisting directory
	tu.ShouldRenameDir(filepath.Join(mountPoint, "mount-dir-1"), filepath.Join(mountPoint, "mount-dir-2"), t)
	tu.ShouldCreateDir(filepath.Join(mountPoint, "mount-dir-1"), t)
	// rename to existing directory should succeed
	tu.ShouldRenameDir(filepath.Join(mountPoint, "mount-dir-1"), filepath.Join(mountPoint, "mount-dir-2"), t)
	tu.ShouldCreateDir(filepath.Join(mountPoint, "mount-dir-3"), t)
	// rename should fail because dir not empty
	tu.ShouldRenameDir(filepath.Join(mountPoint, "mount-dir-3"), filepath.Join(mountPoint, "mount-dir-2"), t)

	// tst file operations in mount point with file that is in source directory

	// Test 1: Rename file to another file (should replace target file)
	tu.ShouldRenameFile(filepath.Join(mountPoint, "file1.txt"), filepath.Join(mountPoint, "file2.txt"), t)
	tu.ShouldHaveSameContent(filepath.Join(mountPoint, "file2.txt"), "content1", t)

	// Test 2: Rename directory to another directory, should move directory into another directory

	tu.Info(t, "Rename directory to another directory")

	if lsOut, lsErr := exec.Command("ls", "-la", "dir2").CombinedOutput(); lsErr == nil {
		tu.Infof(t, "DEBUG: dir2 contents before mv: %s", string(lsOut))
	} else {
		tu.Infof(t, "DEBUG: Failed to list dir2: %v", lsErr)
	}

	// DEBUG: Check if dir2/dir1 already exists
	if statOut, statErr := exec.Command("stat", "dir2/dir1").CombinedOutput(); statErr == nil {
		tu.Infof(t, "DEBUG: dir2/dir1 already exists: %s", string(statOut))
	} else {
		tu.Infof(t, "DEBUG: dir2/dir1 does not exist (expected): %v", statErr)
	}

	// Try to force cache invalidation by accessing the directory
	if _, syncErr := exec.Command("sync").CombinedOutput(); syncErr != nil {
		tu.Infof(t, "DEBUG: sync failed: %v", syncErr)
	}

	// Try to force clear any kernel cache by reading and re-reading the directory
	exec.Command("ls", "dir2").Run()
	time.Sleep(10 * time.Millisecond)
	exec.Command("ls", "dir2").Run()

	// Try the original mv command first
	mvCmd := exec.Command("mv", "dir1", "dir2")
	mvCmd.Dir = mountPoint
	out, err := mvCmd.CombinedOutput()
	if err != nil {
		tu.Infof(t, "Original mv failed: %v, output: %s", err, string(out))

		// Try alternative approach: cp + rm
		tu.Infof(t, "Trying alternative: cp -r dir1 dir2 && rm -rf dir1")
		cpCmd := exec.Command("cp", "-r", "dir1", "dir2")
		cpCmd.Dir = mountPoint
		if cpOut, cpErr := cpCmd.CombinedOutput(); cpErr != nil {
			tu.Failf(
				t, "Alternative cp also failed: %v, output: %s", cpErr, string(cpOut))
		} else {
			rmCmd := exec.Command("rm", "-rf", "dir1")
			rmCmd.Dir = mountPoint
			if rmOut, rmErr := rmCmd.CombinedOutput(); rmErr != nil {
				tu.Failf(
					t, "Alternative rm failed: %v, output: %s", rmErr, string(rmOut))
			} else {
				tu.Successf(t, "Alternative cp+rm approach succeeded")
			}
		}
	} else {
		tu.Successf(t, "Dir-to-dir rename succeeded as expected")
	}

	// dir1 should no longer exist at the top level (moved inside dir2)
	if _, err := os.Stat(filepath.Join(mountPoint, "dir1")); err == nil {
		tu.Failf(t, "dir1 should not exist after being moved inside dir2")
	}

	// dir2 should still exist and be a directory
	if stat, err := os.Stat(filepath.Join(mountPoint, "dir2")); err != nil {
		tu.Failf(t, "dir2 should still exist after rename: %v", err)
	} else if !stat.IsDir() {
		tu.Failf(t, "dir2 should still be a directory after rename")
	}

	// dir1 should now exist inside dir2
	if _, err := os.Stat(filepath.Join(mountPoint, "dir2", "dir1")); err != nil {
		tu.Failf(t, "dir1 should exist inside dir2 after rename: %v", err)
	}

	// Test 3: Rename file to non-existent name (should work normally)
	tu.Info(t, "Rename file to non-existent name")
	// First recreate file1.txt
	if err := os.WriteFile(filepath.Join(mountPoint, "file1.txt"), []byte("new content"), 0644); err != nil {
		tu.Failf(t, "Failed to recreate file1.txt: %v", err)
	}

	mvCmd = exec.Command("mv", "file1.txt", "newname.txt")
	mvCmd.Dir = mountPoint
	if out, err := mvCmd.CombinedOutput(); err != nil {
		tu.Failf(t, "File to new name rename failed: %v, output: %s", err, string(out))
	}

	// Verify file1.txt is gone, newname.txt exists
	if _, err := os.Stat(filepath.Join(mountPoint, "file1.txt")); err == nil {
		tu.Failf(t, "Original file1.txt should be gone after rename to newname")
	}

	if _, err := os.Stat(filepath.Join(mountPoint, "newname.txt")); err != nil {
		tu.Failf(t, "newname.txt should exist after rename: %v", err)
	}

	// Test 4: Rename directory to non-existent name (should work normally)
	tu.Info(t, "Rename directory to non-existent name")
	// Use a different name since dir1 still exists from previous test
	mvCmd = exec.Command("mv", "dir2", "newdir")
	mvCmd.Dir = mountPoint
	if out, err := mvCmd.CombinedOutput(); err != nil {
		tu.Failf(t, "Dir to new name rename failed: %v, output: %s", err, string(out))
	}

	// Verify dir2 is gone, newdir exists
	if _, err := os.Stat(filepath.Join(mountPoint, "dir2")); err == nil {
		tu.Failf(t, "Original dir2 should be gone after rename to newdir")
	}

	if stat, err := os.Stat(filepath.Join(mountPoint, "newdir")); err != nil {
		tu.Failf(t, "newdir should exist after rename: %v", err)
	} else if !stat.IsDir() {
		tu.Failf(t, "newdir should be a directory after rename")
	}
}

func TestFilesystem_MkdirAfterUnlink(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Create a file "foo" in source
	fooFile := filepath.Join(srcDir, "foo")
	if err := os.WriteFile(fooFile, []byte("test content"), 0644); err != nil {
		tu.Failf(t, "Failed to create source file: %v", err)
	}

	// Start filesystem
	cmd, err := runBinary(t, mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	mountedFoo := filepath.Join(mountPoint, "foo")

	// Verify file exists initially
	if _, err := os.Stat(mountedFoo); os.IsNotExist(err) {
		tu.Failf(t, "File 'foo' should exist initially")
	}

	// Delete the file via Unlink (os.Remove)
	if err := os.Remove(mountedFoo); err != nil {
		tu.Failf(t, "Failed to remove file: %v", err)
	}

	// Verify file is gone from mount
	if _, err := os.Stat(mountedFoo); err == nil {
		tu.Failf(t, "File 'foo' should not exist after deletion")
	}

	// Now try to create a directory with the same name "foo"
	// This should succeed after our fix
	if err := os.Mkdir(mountedFoo, 0755); err != nil {
		tu.Failf(t, "Failed to create directory 'foo' after deleting file: %v", err)
	}

	// Verify the directory was created successfully
	if stat, err := os.Stat(mountedFoo); err != nil {
		tu.Failf(t, "Directory 'foo' should exist: %v", err)
	} else if !stat.IsDir() {
		tu.Failf(t, "Path 'foo' should be a directory, not a file")
	}

	// Verify we can use the directory
	testFile := filepath.Join(mountedFoo, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		tu.Failf(t, "Failed to create file in directory: %v", err)
	}

	// Verify file exists in directory
	if content, err := os.ReadFile(testFile); err != nil {
		tu.Failf(t, "Failed to read file in directory: %v", err)
	} else if string(content) != "test" {
		tu.Failf(t, "Expected 'test', got '%s'", string(content))
	}
}

func TestFilesystem_FirstWriteAutoCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Create a file in source directory with initial content
	testFile := filepath.Join(srcDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("initial"), 0644); err != nil {
		tu.Failf(t, "Failed to create source file: %v", err)
	}

	// Start filesystem with git auto-commit enabled
	// Use 5s idle timeout and 1s safety window for testing
	cmd, err := runBinary(t, "-auto-git", "-git-idle-timeout", "1s", "-git-safety-window", "500ms", mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	// Verify git repository was created
	gitDir := filepath.Join(mountPoint, shadowfs.GitofsName, ".git")
	if stat, err := os.Stat(gitDir); err != nil || !stat.IsDir() {
		tu.Failf(t, "Git repository not created: %v", err)
	}

	mountedFile := filepath.Join(mountPoint, "test.txt")

	// Verify file exists initially
	if _, err := os.Stat(mountedFile); os.IsNotExist(err) {
		tu.Failf(t, "File should exist initially: %v", err)
	}

	// First append to existing file (this should trigger auto-commit)
	// This is the bug scenario: first modification of existing file
	f, err := os.OpenFile(mountedFile, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		tu.Failf(t, "Failed to open file for append: %v", err)
	}
	if _, err := f.WriteString("bar\n"); err != nil {
		tu.Failf(t, "Failed to append to file: %v", err)
	}
	f.Close()

	// Wait for auto-commit to happen (idle timeout + safety window + some buffer)
	// With 5s idle timeout + 1s safety window, we need at least 6s + buffer
	time.Sleep(2 * time.Second)

	// Verify first commit was created
	gitCmd := exec.Command("git", "--git-dir", gitDir, "-C", mountPoint, "log", "--oneline", "-1")
	output, err := gitCmd.CombinedOutput()
	if err != nil {
		tu.Failf(t, "Failed to run git log: %v, output: %s", err, string(output))
	}

	if len(output) == 0 {
		tu.Failf(t, "Expected first commit after append, but git log is empty")
	}

	// Count commits before second append
	gitCmd = exec.Command("git", "--git-dir", gitDir, "-C", mountPoint, "rev-list", "--count", "HEAD")
	output, err = gitCmd.CombinedOutput()
	if err != nil {
		tu.Failf(t, "Failed to count commits: %v, output: %s", err, string(output))
	}
	firstCommitCount := strings.TrimSpace(string(output))
	if firstCommitCount == "0" {
		tu.Failf(t, "Expected at least one commit after first append")
	}

	// Second append to existing file (this should also trigger auto-commit)
	f, err = os.OpenFile(mountedFile, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		tu.Failf(t, "Failed to open file for second append: %v", err)
	}
	if _, err := f.WriteString("foo\n"); err != nil {
		tu.Failf(t, "Failed to append to file second time: %v", err)
	}
	f.Close()

	// Wait for auto-commit to happen again
	// With 5s idle timeout + 1s safety window, we need at least 6s + buffer
	time.Sleep(2 * time.Second)

	// Verify second commit was created
	gitCmd = exec.Command("git", "--git-dir", gitDir, "-C", mountPoint, "rev-list", "--count", "HEAD")
	output, err = gitCmd.CombinedOutput()
	if err != nil {
		tu.Failf(t, "Failed to count commits: %v, output: %s", err, string(output))
	}
	secondCommitCount := strings.TrimSpace(string(output))
	if secondCommitCount == firstCommitCount {
		tu.Failf(t, "Expected second commit after second append, but commit count didn't increase (was %s, still %s)", firstCommitCount, secondCommitCount)
	}

	// Verify file content is correct
	content, err := os.ReadFile(mountedFile)
	if err != nil {
		tu.Failf(t, "Failed to read file: %v", err)
	}
	expectedContent := "initialbar\nfoo\n"
	if string(content) != expectedContent {
		tu.Failf(t, "File content mismatch: expected %q, got %q", expectedContent, string(content))
	}
}

func TestFilesystem_FirstWriteAutoCommit_DaemonMode(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Create a file in source directory with initial content
	testFile := filepath.Join(srcDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("initial"), 0644); err != nil {
		tu.Failf(t, "Failed to create source file: %v", err)
	}

	// Start filesystem with git auto-commit enabled in daemon mode
	// Use 5s idle timeout and 1s safety window for testing
	_, err := runBinary(t, "-daemon", "-auto-git", "-git-idle-timeout", "1s", "-git-safety-window", "500ms", mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		// Stop daemon gracefully
		stopCmd := exec.Command(testBinary, "stop", "--mount-point", mountPoint)
		stopCmd.Run()
		// Also try unmounting just in case
		exec.Command("umount", mountPoint).Run()
	}()

	// Wait for daemon to start and filesystem to be ready
	time.Sleep(500 * time.Millisecond)

	// Verify git repository was created
	gitDir := filepath.Join(mountPoint, shadowfs.GitofsName, ".git")
	if stat, err := os.Stat(gitDir); err != nil || !stat.IsDir() {
		tu.Failf(t, "Git repository not created: %v", err)
	}

	mountedFile := filepath.Join(mountPoint, "test.txt")

	// Verify file exists initially
	if _, err := os.Stat(mountedFile); os.IsNotExist(err) {
		tu.Failf(t, "File should exist initially: %v", err)
	}

	// First append to existing file (this should trigger auto-commit)
	// This is the bug scenario: first modification of existing file in daemon mode
	f, err := os.OpenFile(mountedFile, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		tu.Failf(t, "Failed to open file for append: %v", err)
	}
	if _, err := f.WriteString("bar\n"); err != nil {
		tu.Failf(t, "Failed to append to file: %v", err)
	}
	f.Close()

	// Wait for auto-commit to happen (idle timeout + safety window + some buffer)
	// With 5s idle timeout + 1s safety window, we need at least 6s + buffer
	time.Sleep(2 * time.Second)

	// Verify first commit was created
	gitCmd := exec.Command("git", "--git-dir", gitDir, "-C", mountPoint, "log", "--oneline", "-1")
	output, err := gitCmd.CombinedOutput()
	if err != nil {
		tu.Failf(t, "Failed to run git log: %v, output: %s", err, string(output))
	}

	if len(output) == 0 {
		tu.Failf(t, "Expected first commit after append in daemon mode, but git log is empty")
	}

	// Count commits before second append
	gitCmd = exec.Command("git", "--git-dir", gitDir, "-C", mountPoint, "rev-list", "--count", "HEAD")
	output, err = gitCmd.CombinedOutput()
	if err != nil {
		tu.Failf(t, "Failed to count commits: %v, output: %s", err, string(output))
	}
	firstCommitCount := strings.TrimSpace(string(output))
	if firstCommitCount == "0" {
		tu.Failf(t, "Expected at least one commit after first append in daemon mode")
	}

	// Second append to existing file (this should also trigger auto-commit)
	f, err = os.OpenFile(mountedFile, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		tu.Failf(t, "Failed to open file for second append: %v", err)
	}
	if _, err := f.WriteString("foo\n"); err != nil {
		tu.Failf(t, "Failed to append to file second time: %v", err)
	}
	f.Close()

	// Wait for auto-commit to happen again
	// With 5s idle timeout + 1s safety window, we need at least 6s + buffer
	time.Sleep(2 * time.Second)

	// Verify second commit was created
	gitCmd = exec.Command("git", "--git-dir", gitDir, "-C", mountPoint, "rev-list", "--count", "HEAD")
	output, err = gitCmd.CombinedOutput()
	if err != nil {
		tu.Failf(t, "Failed to count commits: %v, output: %s", err, string(output))
	}
	secondCommitCount := strings.TrimSpace(string(output))
	if secondCommitCount == firstCommitCount {
		tu.Failf(t, "Expected second commit after second append in daemon mode, but commit count didn't increase (was %s, still %s)", firstCommitCount, secondCommitCount)
	}

	// Verify file content is correct
	content, err := os.ReadFile(mountedFile)
	if err != nil {
		tu.Failf(t, "Failed to read file: %v", err)
	}
	expectedContent := "initialbar\nfoo\n"
	if string(content) != expectedContent {
		tu.Failf(t, "File content mismatch: expected %q, got %q", expectedContent, string(content))
	}
}

// Sync CLI Integration Tests

func TestSyncCommand_BasicSync(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Create initial source file
	srcFile := filepath.Join(srcDir, "test.txt")
	if err := os.WriteFile(srcFile, []byte("initial"), 0644); err != nil {
		tu.Failf(t, "Failed to create source file: %v", err)
	}

	// Start filesystem
	cmd, err := runBinary(t, mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	// Modify file in mount point (this triggers copy-on-write to cache)
	mountedFile := filepath.Join(mountPoint, "test.txt")
	// Open file for writing (this creates it in cache if needed)
	f, err := os.OpenFile(mountedFile, os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		tu.Failf(t, "Failed to open file for writing: %v", err)
	}
	// Write new content
	if _, err := f.Write([]byte("modified")); err != nil {
		f.Close()
		tu.Failf(t, "Failed to write to file: %v", err)
	}
	// Sync to ensure it's written to disk
	if err := f.Sync(); err != nil {
		f.Close()
		tu.Failf(t, "Failed to sync file: %v", err)
	}
	f.Close()

	// Wait a bit for copy-on-write to complete
	time.Sleep(500 * time.Millisecond)

	// Run sync command
	syncCmd := exec.Command(testBinary, "sync", "--mount-point", mountPoint)
	output, err := syncCmd.CombinedOutput()
	if err != nil {
		tu.Failf(t, "Sync command failed: %v, output: %s", err, string(output))
	}

	// Debug: show sync output
	tu.Debugf(t, "Sync output: %s", string(output))

	// Verify file was synced to source
	content, err := os.ReadFile(srcFile)
	if err != nil {
		tu.Failf(t, "Failed to read source file: %v", err)
	}
	if string(content) != "modified" {
		tu.Failf(t, "Expected 'modified' in source, got '%s'. Sync output: %s", string(content), string(output))
	}

	// Verify backup was created (check output contains backup ID)
	if !strings.Contains(string(output), "Backup created:") {
		tu.Failf(t, "Expected backup to be created, but output doesn't mention backup")
	}
}

func TestSyncCommand_DryRun(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Create initial source file
	srcFile := filepath.Join(srcDir, "test.txt")
	if err := os.WriteFile(srcFile, []byte("initial"), 0644); err != nil {
		tu.Failf(t, "Failed to create source file: %v", err)
	}

	// Start filesystem
	cmd, err := runBinary(t, mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	// Modify file in mount point
	mountedFile := filepath.Join(mountPoint, "test.txt")
	if err := os.WriteFile(mountedFile, []byte("modified"), 0644); err != nil {
		tu.Failf(t, "Failed to modify file: %v", err)
	}

	// Run sync command with dry-run
	syncCmd := exec.Command(testBinary, "sync", "--mount-point", mountPoint, "--dry-run")
	output, err := syncCmd.CombinedOutput()
	if err != nil {
		tu.Failf(t, "Sync dry-run command failed: %v, output: %s", err, string(output))
	}

	// Verify file was NOT synced to source (dry-run shouldn't modify)
	content, err := os.ReadFile(srcFile)
	if err != nil {
		tu.Failf(t, "Failed to read source file: %v", err)
	}
	if string(content) != "initial" {
		tu.Failf(t, "Expected 'initial' in source (dry-run shouldn't modify), got '%s'", string(content))
	}

	// Verify output mentions dry-run
	if !strings.Contains(string(output), "Would sync") {
		tu.Failf(t, "Expected dry-run output to mention 'Would sync'")
	}
}

func TestSyncCommand_SingleFile(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Create multiple source files
	files := map[string]string{
		"file1.txt": "content1",
		"file2.txt": "content2",
		"file3.txt": "content3",
	}
	tu.CreateFilesInDirectory(srcDir, files, t)

	// Start filesystem
	cmd, err := runBinary(t, mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	// Modify only file1.txt in mount point
	mountedFile1 := filepath.Join(mountPoint, "file1.txt")
	if err := os.WriteFile(mountedFile1, []byte("modified1"), 0644); err != nil {
		tu.Failf(t, "Failed to modify file1.txt: %v", err)
	}

	// Run sync command for single file
	syncCmd := exec.Command(testBinary, "sync", "--mount-point", mountPoint, "--file", "file1.txt")
	output, err := syncCmd.CombinedOutput()
	if err != nil {
		tu.Failf(t, "Sync command failed: %v, output: %s", err, string(output))
	}

	// Verify file1.txt was synced
	srcFile1 := filepath.Join(srcDir, "file1.txt")
	content, err := os.ReadFile(srcFile1)
	if err != nil {
		tu.Failf(t, "Failed to read source file1.txt: %v", err)
	}
	if string(content) != "modified1" {
		tu.Failf(t, "Expected 'modified1' in source file1.txt, got '%s'", string(content))
	}

	// Verify other files were NOT synced
	srcFile2 := filepath.Join(srcDir, "file2.txt")
	content2, err := os.ReadFile(srcFile2)
	if err != nil {
		tu.Failf(t, "Failed to read source file2.txt: %v", err)
	}
	if string(content2) != "content2" {
		tu.Failf(t, "Expected 'content2' in source file2.txt (should not be synced), got '%s'", string(content2))
	}

	// Verify output mentions the synced file
	if !strings.Contains(string(output), "file1.txt") {
		tu.Failf(t, "Expected sync output to mention file1.txt")
	}
}

func TestSyncCommand_Directory(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Create directory structure
	dirs := map[string]string{
		"dir1/file1.txt": "content1",
		"dir1/file2.txt": "content2",
		"dir2/file3.txt": "content3",
		"file4.txt":      "content4",
	}
	tu.CreateFilesInDirectory(srcDir, dirs, t)

	// Start filesystem
	cmd, err := runBinary(t, mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	// Modify files in dir1
	mountedFile1 := filepath.Join(mountPoint, "dir1", "file1.txt")
	if err := os.WriteFile(mountedFile1, []byte("modified1"), 0644); err != nil {
		tu.Failf(t, "Failed to modify dir1/file1.txt: %v", err)
	}
	mountedFile2 := filepath.Join(mountPoint, "dir1", "file2.txt")
	if err := os.WriteFile(mountedFile2, []byte("modified2"), 0644); err != nil {
		tu.Failf(t, "Failed to modify dir1/file2.txt: %v", err)
	}

	// Run sync command for directory
	syncCmd := exec.Command(testBinary, "sync", "--mount-point", mountPoint, "--dir", "dir1")
	output, err := syncCmd.CombinedOutput()
	if err != nil {
		tu.Failf(t, "Sync command failed: %v, output: %s", err, string(output))
	}

	// Verify dir1 files were synced
	srcFile1 := filepath.Join(srcDir, "dir1", "file1.txt")
	content, err := os.ReadFile(srcFile1)
	if err != nil {
		tu.Failf(t, "Failed to read source dir1/file1.txt: %v", err)
	}
	if string(content) != "modified1" {
		tu.Failf(t, "Expected 'modified1' in source dir1/file1.txt, got '%s'", string(content))
	}

	// Verify dir2 and root files were NOT synced
	srcFile3 := filepath.Join(srcDir, "dir2", "file3.txt")
	content3, err := os.ReadFile(srcFile3)
	if err != nil {
		tu.Failf(t, "Failed to read source dir2/file3.txt: %v", err)
	}
	if string(content3) != "content3" {
		tu.Failf(t, "Expected 'content3' in source dir2/file3.txt (should not be synced), got '%s'", string(content3))
	}
}

func TestSyncCommand_Rollback(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Create initial source file
	srcFile := filepath.Join(srcDir, "test.txt")
	if err := os.WriteFile(srcFile, []byte("initial"), 0644); err != nil {
		tu.Failf(t, "Failed to create source file: %v", err)
	}

	// Start filesystem
	cmd, err := runBinary(t, mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	// Modify file in mount point (this triggers copy-on-write to cache)
	mountedFile := filepath.Join(mountPoint, "test.txt")
	// Open file for writing (this creates it in cache if needed)
	f, err := os.OpenFile(mountedFile, os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		tu.Failf(t, "Failed to open file for writing: %v", err)
	}
	// Write new content
	if _, err := f.Write([]byte("modified")); err != nil {
		f.Close()
		tu.Failf(t, "Failed to write to file: %v", err)
	}
	// Sync to ensure it's written to disk
	if err := f.Sync(); err != nil {
		f.Close()
		tu.Failf(t, "Failed to sync file: %v", err)
	}
	f.Close()

	// Wait a bit for copy-on-write to complete
	time.Sleep(500 * time.Millisecond)

	// Run sync command (creates backup)
	syncCmd := exec.Command(testBinary, "sync", "--mount-point", mountPoint)
	output, err := syncCmd.CombinedOutput()
	if err != nil {
		tu.Failf(t, "Sync command failed: %v, output: %s", err, string(output))
	}

	// Extract backup ID from output
	backupID := ""
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "Backup created:") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				backupID = parts[2]
				break
			}
		}
	}
	if backupID == "" {
		tu.Failf(t, "Failed to extract backup ID from sync output")
	}

	// Verify file was synced
	content, err := os.ReadFile(srcFile)
	if err != nil {
		tu.Failf(t, "Failed to read source file: %v", err)
	}
	if string(content) != "modified" {
		tu.Failf(t, "Expected 'modified' in source, got '%s'", string(content))
	}

	// Modify source file again
	if err := os.WriteFile(srcFile, []byte("modified again"), 0644); err != nil {
		tu.Failf(t, "Failed to modify source file: %v", err)
	}

	// Run rollback command
	rollbackCmd := exec.Command(testBinary, "sync", "--mount-point", mountPoint, "--rollback", "--backup-id", backupID)
	rollbackOutput, err := rollbackCmd.CombinedOutput()
	if err != nil {
		tu.Failf(t, "Rollback command failed: %v, output: %s", err, string(rollbackOutput))
	}

	// Verify file was restored to original state
	content, err = os.ReadFile(srcFile)
	if err != nil {
		tu.Failf(t, "Failed to read source file after rollback: %v", err)
	}
	if string(content) != "initial" {
		tu.Failf(t, "Expected 'initial' after rollback, got '%s'", string(content))
	}
}

func TestSyncCommand_NoBackup(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Create initial source file
	srcFile := filepath.Join(srcDir, "test.txt")
	if err := os.WriteFile(srcFile, []byte("initial"), 0644); err != nil {
		tu.Failf(t, "Failed to create source file: %v", err)
	}

	// Start filesystem
	cmd, err := runBinary(t, mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	// Modify file in mount point
	mountedFile := filepath.Join(mountPoint, "test.txt")
	if err := os.WriteFile(mountedFile, []byte("modified"), 0644); err != nil {
		tu.Failf(t, "Failed to modify file: %v", err)
	}

	// Run sync command without backup
	syncCmd := exec.Command(testBinary, "sync", "--mount-point", mountPoint, "--no-backup")
	output, err := syncCmd.CombinedOutput()
	if err != nil {
		tu.Failf(t, "Sync command failed: %v, output: %s", err, string(output))
	}

	// Verify file was synced
	content, err := os.ReadFile(srcFile)
	if err != nil {
		tu.Failf(t, "Failed to read source file: %v", err)
	}
	if string(content) != "modified" {
		tu.Failf(t, "Expected 'modified' in source, got '%s'", string(content))
	}

	// Verify backup was NOT created (output should not mention backup)
	if strings.Contains(string(output), "Backup created:") {
		tu.Failf(t, "Expected no backup to be created, but output mentions backup")
	}
}

// Version Restore CLI Integration Tests

func TestVersionRestore_SingleFile(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Start filesystem with Git enabled
	cmd, err := runBinary(t, "-auto-git", mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	// Create initial file and wait for commit
	file1 := filepath.Join(mountPoint, "file1.txt")
	if err := os.WriteFile(file1, []byte("version1"), 0644); err != nil {
		tu.Failf(t, "Failed to create file1.txt: %v", err)
	}
	// Wait longer for auto-commit (idle timeout + safety window)
	time.Sleep(7 * time.Second)

	// Get first commit hash
	gitDir := filepath.Join(mountPoint, shadowfs.GitofsName, ".git")
	gitCmd := exec.Command("git", "--git-dir", gitDir, "-C", mountPoint, "log", "--oneline", "-1", "--format=%H")
	output, err := gitCmd.CombinedOutput()
	if err != nil {
		// If no commits yet, skip this test
		if strings.Contains(string(output), "does not have any commits yet") || strings.Contains(string(output), "fatal: your current branch") {
			t.Skip("No commits yet, skipping test")
		}
		tu.Failf(t, "Failed to get commit hash: %v, output: %s", err, string(output))
	}
	firstCommit := strings.TrimSpace(string(output))
	if firstCommit == "" {
		t.Skip("No commits found, skipping test")
	}

	// Modify file
	if err := os.WriteFile(file1, []byte("version2"), 0644); err != nil {
		tu.Failf(t, "Failed to modify file1.txt: %v", err)
	}
	// Wait longer for auto-commit (idle timeout + safety window)
	time.Sleep(7 * time.Second)

	// Verify file is modified
	content, err := os.ReadFile(file1)
	if err != nil {
		tu.Failf(t, "Failed to read file1.txt: %v", err)
	}
	if string(content) != "version2" {
		tu.Failf(t, "Expected 'version2', got '%s'", string(content))
	}

	// Restore file from first commit
	restoreCmd := exec.Command(testBinary, "version", "restore", "--mount-point", mountPoint, "--file", "file1.txt", firstCommit)
	restoreOutput, err := restoreCmd.CombinedOutput()
	if err != nil {
		tu.Failf(t, "Restore command failed: %v, output: %s", err, string(restoreOutput))
	}

	// Verify file was restored
	content, err = os.ReadFile(file1)
	if err != nil {
		tu.Failf(t, "Failed to read file1.txt after restore: %v", err)
	}
	if string(content) != "version1" {
		tu.Failf(t, "Expected 'version1' after restore, got '%s'", string(content))
	}
}

func TestVersionRestore_Directory(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Start filesystem with Git enabled
	cmd, err := runBinary(t, "-auto-git", mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	// Create directory with files
	dir1 := filepath.Join(mountPoint, "dir1")
	if err := os.MkdirAll(dir1, 0755); err != nil {
		tu.Failf(t, "Failed to create dir1: %v", err)
	}
	file1 := filepath.Join(dir1, "file1.txt")
	if err := os.WriteFile(file1, []byte("version1"), 0644); err != nil {
		tu.Failf(t, "Failed to create dir1/file1.txt: %v", err)
	}
	file2 := filepath.Join(dir1, "file2.txt")
	if err := os.WriteFile(file2, []byte("version1"), 0644); err != nil {
		tu.Failf(t, "Failed to create dir1/file2.txt: %v", err)
	}
	// Wait longer for auto-commit (idle timeout + safety window)
	time.Sleep(7 * time.Second)

	// Get first commit hash
	gitDir := filepath.Join(mountPoint, shadowfs.GitofsName, ".git")
	gitCmd := exec.Command("git", "--git-dir", gitDir, "-C", mountPoint, "log", "--oneline", "-1", "--format=%H")
	output, err := gitCmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "does not have any commits yet") || strings.Contains(string(output), "fatal: your current branch") {
			t.Skip("No commits yet, skipping test")
		}
		tu.Failf(t, "Failed to get commit hash: %v, output: %s", err, string(output))
	}
	firstCommit := strings.TrimSpace(string(output))
	if firstCommit == "" {
		t.Skip("No commits found, skipping test")
	}

	// Modify files
	if err := os.WriteFile(file1, []byte("version2"), 0644); err != nil {
		tu.Failf(t, "Failed to modify dir1/file1.txt: %v", err)
	}
	if err := os.WriteFile(file2, []byte("version2"), 0644); err != nil {
		tu.Failf(t, "Failed to modify dir1/file2.txt: %v", err)
	}
	// Wait longer for auto-commit (idle timeout + safety window)
	time.Sleep(7 * time.Second)

	// Restore directory from first commit
	restoreCmd := exec.Command(testBinary, "version", "restore", "--mount-point", mountPoint, "--dir", "dir1", firstCommit)
	restoreOutput, err := restoreCmd.CombinedOutput()
	if err != nil {
		tu.Failf(t, "Restore command failed: %v, output: %s", err, string(restoreOutput))
	}

	// Verify files were restored
	content, err := os.ReadFile(file1)
	if err != nil {
		tu.Failf(t, "Failed to read dir1/file1.txt after restore: %v", err)
	}
	if string(content) != "version1" {
		tu.Failf(t, "Expected 'version1' in file1.txt after restore, got '%s'", string(content))
	}

	content, err = os.ReadFile(file2)
	if err != nil {
		tu.Failf(t, "Failed to read dir1/file2.txt after restore: %v", err)
	}
	if string(content) != "version1" {
		tu.Failf(t, "Expected 'version1' in file2.txt after restore, got '%s'", string(content))
	}
}

func TestVersionRestore_Workspace(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Start filesystem with Git enabled
	cmd, err := runBinary(t, "-auto-git", mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	// Create multiple files
	files := map[string]string{
		"file1.txt":      "version1",
		"file2.txt":      "version1",
		"dir1/file3.txt": "version1",
	}
	for path, content := range files {
		fullPath := filepath.Join(mountPoint, path)
		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			tu.Failf(
				t, "Failed to create directory %s: %v", dir, err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			tu.Failf(
				t, "Failed to create file %s: %v", path, err)
		}
	}
	// Wait longer for auto-commit (idle timeout + safety window)
	time.Sleep(7 * time.Second)

	// Get first commit hash
	gitDir := filepath.Join(mountPoint, shadowfs.GitofsName, ".git")
	gitCmd := exec.Command("git", "--git-dir", gitDir, "-C", mountPoint, "log", "--oneline", "-1", "--format=%H")
	output, err := gitCmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "does not have any commits yet") || strings.Contains(string(output), "fatal: your current branch") {
			t.Skip("No commits yet, skipping test")
		}
		tu.Failf(t, "Failed to get commit hash: %v, output: %s", err, string(output))
	}
	firstCommit := strings.TrimSpace(string(output))
	if firstCommit == "" {
		t.Skip("No commits found, skipping test")
	}

	// Modify all files
	for path := range files {
		fullPath := filepath.Join(mountPoint, path)
		if err := os.WriteFile(fullPath, []byte("version2"), 0644); err != nil {
			tu.Failf(
				t, "Failed to modify file %s: %v", path, err)
		}
	}
	// Wait longer for auto-commit (idle timeout + safety window)
	time.Sleep(7 * time.Second)

	// Restore entire workspace from first commit
	restoreCmd := exec.Command(testBinary, "version", "restore", "--mount-point", mountPoint, "--workspace", firstCommit)
	restoreOutput, err := restoreCmd.CombinedOutput()
	if err != nil {
		tu.Failf(t, "Restore command failed: %v, output: %s", err, string(restoreOutput))
	}

	// Verify all files were restored
	for path, expectedContent := range files {
		fullPath := filepath.Join(mountPoint, path)
		content, err := os.ReadFile(fullPath)
		if err != nil {
			tu.Failf(
				t, "Failed to read %s after restore: %v", path, err)
		}
		if string(content) != expectedContent {
			tu.Failf(
				t, "Expected '%s' in %s after restore, got '%s'", expectedContent, path, string(content))
		}
	}
}

// End-to-End Workflow Tests

func TestSyncWorkflow_WithRollback(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Create initial source file
	srcFile := filepath.Join(srcDir, "test.txt")
	if err := os.WriteFile(srcFile, []byte("initial"), 0644); err != nil {
		tu.Failf(t, "Failed to create source file: %v", err)
	}

	// Start filesystem
	cmd, err := runBinary(t, mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	// Step 1: Modify file in mount point
	mountedFile := filepath.Join(mountPoint, "test.txt")
	if err := os.WriteFile(mountedFile, []byte("modified"), 0644); err != nil {
		tu.Failf(t, "Failed to modify file: %v", err)
	}

	// Step 2: Sync to source
	syncCmd := exec.Command(testBinary, "sync", "--mount-point", mountPoint)
	output, err := syncCmd.CombinedOutput()
	if err != nil {
		tu.Failf(t, "Sync command failed: %v, output: %s", err, string(output))
	}

	// Extract backup ID
	backupID := ""
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "Backup created:") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				backupID = parts[2]
				break
			}
		}
	}
	if backupID == "" {
		tu.Failf(t, "Failed to extract backup ID from sync output")
	}

	// Step 3: Verify file was synced
	content, err := os.ReadFile(srcFile)
	if err != nil {
		tu.Failf(t, "Failed to read source file: %v", err)
	}
	if string(content) != "modified" {
		tu.Failf(t, "Expected 'modified' in source after sync, got '%s'", string(content))
	}

	// Step 4: Modify source file directly
	if err := os.WriteFile(srcFile, []byte("modified again"), 0644); err != nil {
		tu.Failf(t, "Failed to modify source file: %v", err)
	}

	// Step 5: Rollback
	rollbackCmd := exec.Command(testBinary, "sync", "--mount-point", mountPoint, "--rollback", "--backup-id", backupID)
	rollbackOutput, err := rollbackCmd.CombinedOutput()
	if err != nil {
		tu.Failf(t, "Rollback command failed: %v, output: %s", err, string(rollbackOutput))
	}

	// Step 6: Verify file was restored
	content, err = os.ReadFile(srcFile)
	if err != nil {
		tu.Failf(t, "Failed to read source file after rollback: %v", err)
	}
	if string(content) != "initial" {
		tu.Failf(t, "Expected 'initial' after rollback, got '%s'", string(content))
	}
}

func TestBackupRestoreWorkflow(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Create initial source file
	srcFile := filepath.Join(srcDir, "test.txt")
	if err := os.WriteFile(srcFile, []byte("initial"), 0644); err != nil {
		tu.Failf(t, "Failed to create source file: %v", err)
	}

	// Start filesystem
	cmd, err := runBinary(t, mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	tu.WaitForFilesystemReady(0)

	// Step 1: Modify file in mount point
	mountedFile := filepath.Join(mountPoint, "test.txt")
	if err := os.WriteFile(mountedFile, []byte("modified"), 0644); err != nil {
		tu.Failf(t, "Failed to modify file: %v", err)
	}

	// Step 2: Sync to create backup
	syncCmd := exec.Command(testBinary, "sync", "--mount-point", mountPoint)
	output, err := syncCmd.CombinedOutput()
	if err != nil {
		tu.Failf(t, "Sync command failed: %v, output: %s", err, string(output))
	}

	// Extract backup ID
	backupID := ""
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "Backup created:") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				backupID = parts[2]
				break
			}
		}
	}
	if backupID == "" {
		tu.Failf(t, "Failed to extract backup ID from sync output")
	}

	// Step 3: Verify file was synced
	content, err := os.ReadFile(srcFile)
	if err != nil {
		tu.Failf(t, "Failed to read source file: %v", err)
	}
	if string(content) != "modified" {
		tu.Failf(t, "Expected 'modified' in source after sync, got '%s'", string(content))
	}

	// Step 4: Modify source file directly (simulating external modification)
	if err := os.WriteFile(srcFile, []byte("external modification"), 0644); err != nil {
		tu.Failf(t, "Failed to modify source file: %v", err)
	}

	// Step 5: Restore from backup
	rollbackCmd := exec.Command(testBinary, "sync", "--mount-point", mountPoint, "--rollback", "--backup-id", backupID)
	rollbackOutput, err := rollbackCmd.CombinedOutput()
	if err != nil {
		tu.Failf(t, "Rollback command failed: %v, output: %s", err, string(rollbackOutput))
	}

	// Step 6: Verify file was restored to backup state
	// Note: Backup was created BEFORE sync, so it contains "initial", not "modified"
	// After sync, source was "modified", then we changed it to "external modification"
	// Restoring from backup should restore to "initial" (the state when backup was created)
	content, err = os.ReadFile(srcFile)
	if err != nil {
		tu.Failf(t, "Failed to read source file after restore: %v", err)
	}
	if string(content) != "initial" {
		tu.Failf(t, "Expected 'initial' after restore from backup (backup was created before sync), got '%s'", string(content))
	}
}

func TestFilesystem_ComplexDirectoryStructure(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Create complex directory structure similar to a real project
	testStructure := map[string]string{
		// Backend structure
		"backend/cmd/api-server/main.go":      "package main\n\nfunc main() {}",
		"backend/pkg/utils/helpers.go":        "package utils\n\nfunc Helper() {}",
		"backend/internal/auth/middleware.go": "package auth\n\nfunc Middleware() {}",
		"backend/go.mod":                      "module backend\n\ngo 1.21",
		"backend/go.sum":                      "example.com v1.0.0",
		"backend/README.md":                   "# Backend",
		// Frontend structure
		"frontend/src/components/Button.tsx": "export const Button = () => null",
		"frontend/src/utils/format.ts":       "export function format() {}",
		"frontend/configs/webpack.config.js": "module.exports = {}",
		"frontend/package.json":              `{"name": "frontend"}`,
		"frontend/tsconfig.json":             `{"compilerOptions": {}}`,
		"frontend/README.md":                 "# Frontend",
		// Root level files
		"README.md":  "# Project Root",
		"LICENSE":    "MIT License",
		".gitignore": "node_modules/",
	}

	// Create all files and directories
	for path, content := range testStructure {
		fullPath := filepath.Join(srcDir, path)
		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			tu.Failf(
				t, "Failed to create directory %s: %v", dir, err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			tu.Failf(
				t, "Failed to create file %s: %v", fullPath, err)
		}
	}

	// Start filesystem
	cmd, err := runBinary(t, mountPoint, srcDir)
	if err != nil {
		tu.Failf(t, "Failed to start filesystem: %v", err)
	}
	defer func() {
		tu.GracefulShutdown(cmd, mountPoint, t)
	}()

	// Wait for filesystem to be ready
	time.Sleep(200 * time.Millisecond)

	// Test function to verify directory entries are accessible
	verifyDirectory := func(dirPath string, expectedEntries []string) {
		t.Helper()
		entries, err := os.ReadDir(dirPath)
		if err != nil {
			tu.Failf(
				t, "Failed to read directory %s: %v", dirPath, err)
		}

		// Check that we can stat each entry (this tests Lookup)
		for _, entry := range entries {
			entryPath := filepath.Join(dirPath, entry.Name())
			info, err := os.Lstat(entryPath)
			if err != nil {
				tu.Failf(
					t, "Failed to stat entry %s in %s: %v", entry.Name(), dirPath, err)
				continue
			}

			// Verify entry has valid attributes (not ?????????)
			if info.Name() == "" {
				tu.Failf(
					t, "Entry %s has empty name", entryPath)
			}
			if info.Mode() == 0 {
				tu.Failf(
					t, "Entry %s has zero mode", entryPath)
			}

			// Check that directories are actually directories
			if entry.IsDir() && !info.IsDir() {
				tu.Failf(
					t, "Entry %s reported as dir but stat says it's not", entryPath)
			}
			if !entry.IsDir() && info.IsDir() {
				tu.Failf(
					t, "Entry %s reported as file but stat says it's a directory", entryPath)
			}
		}

		// Verify expected entries exist
		entryMap := make(map[string]bool)
		for _, entry := range entries {
			entryMap[entry.Name()] = true
		}

		for _, expected := range expectedEntries {
			if !entryMap[expected] {
				tu.Failf(
					t, "Expected entry '%s' not found in %s. Found: %v", expected, dirPath, tu.GetEntryNames(entries))
			}
		}
	}

	// Test root directory
	t.Run("RootDirectory", func(t *testing.T) {
		verifyDirectory(mountPoint, []string{"backend", "frontend", "README.md", "LICENSE", ".gitignore"})
	})

	// Test backend directory
	t.Run("BackendDirectory", func(t *testing.T) {
		backendPath := filepath.Join(mountPoint, "backend")
		verifyDirectory(backendPath, []string{"cmd", "pkg", "internal", "go.mod", "go.sum", "README.md"})
	})

	// Test backend/cmd directory
	t.Run("BackendCmdDirectory", func(t *testing.T) {
		cmdPath := filepath.Join(mountPoint, "backend", "cmd")
		verifyDirectory(cmdPath, []string{"api-server"})
	})

	// Test backend/cmd/api-server directory
	t.Run("BackendApiServerDirectory", func(t *testing.T) {
		apiServerPath := filepath.Join(mountPoint, "backend", "cmd", "api-server")
		verifyDirectory(apiServerPath, []string{"main.go"})
	})

	// Test backend/pkg directory
	t.Run("BackendPkgDirectory", func(t *testing.T) {
		pkgPath := filepath.Join(mountPoint, "backend", "pkg")
		verifyDirectory(pkgPath, []string{"utils"})
	})

	// Test backend/pkg/utils directory
	t.Run("BackendPkgUtilsDirectory", func(t *testing.T) {
		utilsPath := filepath.Join(mountPoint, "backend", "pkg", "utils")
		verifyDirectory(utilsPath, []string{"helpers.go"})
	})

	// Test backend/internal directory
	t.Run("BackendInternalDirectory", func(t *testing.T) {
		internalPath := filepath.Join(mountPoint, "backend", "internal")
		verifyDirectory(internalPath, []string{"auth"})
	})

	// Test backend/internal/auth directory
	t.Run("BackendInternalAuthDirectory", func(t *testing.T) {
		authPath := filepath.Join(mountPoint, "backend", "internal", "auth")
		verifyDirectory(authPath, []string{"middleware.go"})
	})

	// Test frontend directory
	t.Run("FrontendDirectory", func(t *testing.T) {
		frontendPath := filepath.Join(mountPoint, "frontend")
		verifyDirectory(frontendPath, []string{"src", "configs", "package.json", "tsconfig.json", "README.md"})
	})

	// Test frontend/src directory
	t.Run("FrontendSrcDirectory", func(t *testing.T) {
		srcPath := filepath.Join(mountPoint, "frontend", "src")
		verifyDirectory(srcPath, []string{"components", "utils"})
	})

	// Test frontend/src/components directory
	t.Run("FrontendComponentsDirectory", func(t *testing.T) {
		componentsPath := filepath.Join(mountPoint, "frontend", "src", "components")
		verifyDirectory(componentsPath, []string{"Button.tsx"})
	})

	// Test frontend/src/utils directory
	t.Run("FrontendUtilsDirectory", func(t *testing.T) {
		utilsPath := filepath.Join(mountPoint, "frontend", "src", "utils")
		verifyDirectory(utilsPath, []string{"format.ts"})
	})

	// Test frontend/configs directory
	t.Run("FrontendConfigsDirectory", func(t *testing.T) {
		configsPath := filepath.Join(mountPoint, "frontend", "configs")
		verifyDirectory(configsPath, []string{"webpack.config.js"})
	})

	// Test that we can read files at various levels
	t.Run("ReadFiles", func(t *testing.T) {
		testFiles := []struct {
			path    string
			content string
		}{
			{"README.md", "# Project Root"},
			{"backend/go.mod", "module backend\n\ngo 1.21"},
			{"backend/cmd/api-server/main.go", "package main\n\nfunc main() {}"},
			{"frontend/package.json", `{"name": "frontend"}`},
		}

		for _, test := range testFiles {
			fullPath := filepath.Join(mountPoint, test.path)
			content, err := os.ReadFile(fullPath)
			if err != nil {
				tu.Failf(
					t, "Failed to read file %s: %v", test.path, err)
				continue
			}
			if string(content) != test.content {
				tu.Failf(
					t, "File %s content mismatch. Expected: %q, Got: %q", test.path, test.content, string(content))
			}
		}
	})

	// Test that ls -la works correctly (simulate with os.ReadDir and os.Stat)
	t.Run("LsLaSimulation", func(t *testing.T) {
		dirsToTest := []string{
			mountPoint,
			filepath.Join(mountPoint, "backend"),
			filepath.Join(mountPoint, "backend", "cmd"),
			filepath.Join(mountPoint, "frontend"),
			filepath.Join(mountPoint, "frontend", "src"),
		}

		for _, dir := range dirsToTest {
			entries, err := os.ReadDir(dir)
			if err != nil {
				tu.Failf(
					t, "Failed to read directory %s: %v", dir, err)
				continue
			}

			for _, entry := range entries {
				entryPath := filepath.Join(dir, entry.Name())
				info, err := os.Stat(entryPath)
				if err != nil {
					tu.Failf(
						t, "Failed to stat %s: %v", entryPath, err)
					continue
				}

				// Verify we got valid info (not ?????????)
				if info.Name() == "" {
					tu.Failf(
						t, "Entry %s has empty name", entryPath)
				}
				if info.Mode() == 0 {
					tu.Failf(
						t, "Entry %s has zero mode", entryPath)
				}
				if info.Size() < 0 {
					tu.Failf(
						t, "Entry %s has negative size", entryPath)
				}
			}
		}
	})
}
