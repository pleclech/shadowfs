//go:build linux && integration
// +build linux,integration

package integrations

import (
	"testing"
	"time"

	tu "github.com/pleclech/shadowfs/fs/utils/testings"
)

// ============================================================================
// Test Helpers for Restoration Tests
// ============================================================================

var (
	// Test Git timing configuration (faster for tests)
	testGitIdleTimeout  = 500 * time.Millisecond // Much faster for tests (default: 30s)
	testGitSafetyWindow = 250 * time.Millisecond // Much faster for tests (default: 5s)
)

// setupTestFilesystemWithGit creates a new test filesystem with Git enabled
// Uses faster Git timing for tests: 2s idle timeout + 1s safety window
// This makes tests run faster while still testing auto-commit behavior
func setupTestFilesystemWithGit(t *testing.T, initialContent map[string]string) *tu.TestFilesystem {
	t.Helper()
	config := tu.GitTestConfig{
		IdleTimeout:  testGitIdleTimeout,
		SafetyWindow: testGitSafetyWindow,
		Daemon:       false, // Foreground mode for proper test cleanup
	}
	return tu.NewTestFilesystemWithGitConfig(t, testBinary, config, initialContent)
}

// waitForTestAutoCommit waits for auto-commit using test timing configuration
// and verifies that at least one commit exists after waiting
func waitForTestAutoCommit(t *testing.T, fs *tu.TestFilesystem) {
	t.Helper()
	tu.WaitForAutoCommitWithTiming(testGitIdleTimeout, testGitSafetyWindow)

	// Small delay to ensure Git operations complete before accessing through FUSE
	// This prevents potential deadlocks when Git is still writing files
	time.Sleep(500 * time.Millisecond)

	// Verify that commits exist - if auto-git is enabled, commits MUST happen
	commits := fs.GetAllCommitsFS()
	if len(commits) == 0 {
		tu.Failf(t, "Auto-git commit failed: expected at least one commit after waiting %v + %v, but found none. This indicates a bug in auto-commit functionality.", testGitIdleTimeout, testGitSafetyWindow)
	}
}

// waitForTestAutoCommitWithCount waits for auto-commit and verifies minimum commit count
func waitForTestAutoCommitWithCount(t *testing.T, fs *tu.TestFilesystem, minCount int, maxCount int) {
	t.Helper()
	tu.WaitForAutoCommitWithTiming(testGitIdleTimeout, testGitSafetyWindow)

	// Small delay to ensure Git operations complete before accessing through FUSE
	// This prevents potential deadlocks when Git is still writing files
	time.Sleep(500 * time.Millisecond)

	commits := fs.GetAllCommitsFS()

	commitLen := len(commits)
	if minCount >= 0 && commitLen < minCount {
		tu.Failf(t, "Auto-git commit failed: expected at least %d commits after waiting %v + %v, but found %d. This indicates a bug in auto-commit functionality.", minCount, testGitIdleTimeout, testGitSafetyWindow, commitLen)
	}
	if maxCount >= 0 && commitLen > maxCount {
		tu.Failf(t, "Auto-git commit failed: expected at most %d commits after waiting %v + %v, but found %d. This indicates a bug in auto-commit functionality.", maxCount, testGitIdleTimeout, testGitSafetyWindow, commitLen)
	}
}

// ============================================================================
// Basic Restoration Tests
// ============================================================================

// TestRestore_SingleFile verifies basic single file restoration
func TestRestore_SingleFile(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystemWithGit(t, map[string]string{
		"file1.txt": "version1",
	})
	defer fs.Cleanup()

	// Get first commit (must exist after waitForTestAutoCommit)
	commits := fs.GetAllCommitsFS()
	if len(commits) != 0 {
		t.Fatalf("Expected no commits after initial setup, but found %d", len(commits))
	}

	// Modify file
	fs.WriteFile("file1.txt", []byte("v2"))
	fs.AssertFileContent("file1.txt", "v2")

	waitForTestAutoCommitWithCount(t, fs, 2, 2) // Should have at least 2 commits now

	allCommits := fs.GetAllCommitsFS()
	initialCommit := allCommits[0]

	// Restore to first commit
	fs.RestorePathFS("file1.txt", initialCommit)

	// Verify restored
	fs.AssertFileContent("file1.txt", "version1")
}

// TestRestore_Directory verifies directory restoration
func TestRestore_Directory(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystemWithGit(t, map[string]string{
		"dir1/file1.txt": "version1",
		"dir1/file2.txt": "version1",
	})
	defer fs.Cleanup()

	// Modify files to trigger initial commits (auto-commit only triggers on modifications, not file creation)
	fs.WriteFileInMount("dir1/file1.txt", []byte("version1"))
	fs.WriteFileInMount("dir1/file2.txt", []byte("version1"))
	waitForTestAutoCommitWithCount(t, fs, 2, -1) // Should have at least 2 commits now

	versions := fs.ListVersionsFS()

	// Use the most recent commit (not commits[0] which is oldest)
	allCommits := fs.GetAllCommitsFS()
	tu.Debugf(t, "allCommits: %v", allCommits)
	lastCommit := allCommits[len(allCommits)-1]

	tu.Debugf(t, "\nversions: %s\nlastCommit: %s\n", versions, lastCommit)

	// Modify files again to create more commits
	fs.WriteFileInMount("dir1/file1.txt", []byte("version2"))
	fs.WriteFileInMount("dir1/file2.txt", []byte("version2"))
	waitForTestAutoCommitWithCount(t, fs, 4, -1) // Should have at least 4 commits now

	// Restore directory to the first commit (which has version1 content)
	fs.RestorePathFS("dir1/", lastCommit)

	// Verify restored
	fs.AssertFileContent("dir1/file1.txt", "version1")
	fs.AssertFileContent("dir1/file2.txt", "version1")
}

// TestRestore_Workspace verifies full workspace restoration
func TestRestore_Workspace(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystemWithGit(t, map[string]string{
		"file1.txt":      "version1",
		"file2.txt":      "version1",
		"dir1/file3.txt": "version1",
	})
	defer fs.Cleanup()

	// Modify files to trigger initial commits (auto-commit only triggers on modifications, not file creation)
	fs.WriteFileInMount("file1.txt", []byte("version1"))
	fs.WriteFileInMount("file2.txt", []byte("version1"))
	fs.WriteFileInMount("dir1/file3.txt", []byte("version1"))
	waitForTestAutoCommitWithCount(t, fs, 3, -1) // Should have at least 3 commits now

	// Use the most recent commit (not commits[0] which is oldest)
	allCommits := fs.GetAllCommitsFS()
	initialCommit := allCommits[len(allCommits)-1]

	// Modify all files again to create more commits
	fs.WriteFileInMount("file1.txt", []byte("version2"))
	fs.WriteFileInMount("file2.txt", []byte("version2"))
	fs.WriteFileInMount("dir1/file3.txt", []byte("version2"))
	waitForTestAutoCommitWithCount(t, fs, 6, -1) // Should have at least 6 commits now

	// Restore workspace to initial commit
	fs.RestoreWorkspaceFS(initialCommit)

	// Verify restored
	fs.AssertFileContent("file1.txt", "version1")
	fs.AssertFileContent("file2.txt", "version1")
	fs.AssertFileContent("dir1/file3.txt", "version1")
}

// ============================================================================
// Rename and Restoration Tests
// ============================================================================

// TestRestore_RenamedFile verifies restoration of renamed file
func TestRestore_RenamedFile(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystemWithGit(t, map[string]string{
		"file1.txt": "content1",
	})
	defer fs.Cleanup()

	// Modify file to trigger initial commit
	fs.WriteFileInMount("file1.txt", []byte("content1"))
	waitForTestAutoCommitWithCount(t, fs, 1, -1)

	fs.Rename("file1.txt", "renamed.txt")
	fs.AssertFileContent("renamed.txt", "content1")
	fs.AssertFileNotExists("file1.txt")

	// Modify renamed file to trigger commit after rename
	fs.WriteFileInMount("renamed.txt", []byte("content2"))
	fs.AssertFileContent("renamed.txt", "content2")

	waitForTestAutoCommitWithCount(t, fs, 3, -1) // At least 3 commits

	tu.Debugf(t, "versions: ==>\n%s\n<==", fs.ListVersionsFS())

	commits := fs.GetAllCommitsFS()
	tu.Debugf(t, "commits: %v", commits)
	if len(commits) == 0 {
		t.Fatalf("Expected at least 1 commit (initial), but found none")
	}

	// Use the latest commit for restore
	latestCommit := commits[len(commits)-1]

	// Restore renamed file to latest commit
	fs.RestorePathFSWithForce("renamed.txt", latestCommit)
	fs.AssertFileNotExists("file1.txt")
	fs.AssertFileContent("renamed.txt", "content2") // Should restore to modified content
}

// TestRestore_OriginalPathAfterRename verifies restoring original path after rename
func TestRestore_OriginalPathAfterRename(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystemWithGit(t, map[string]string{
		"file1.txt": "content1",
	})
	defer fs.Cleanup()

	// Modify file to trigger initial commit
	fs.WriteFileInMount("file1.txt", []byte("content1"))
	waitForTestAutoCommitWithCount(t, fs, 1, -1)

	// Get initial commit
	commits := fs.GetAllCommitsFS()
	if len(commits) == 0 {
		t.Fatalf("Expected at least one commit after file modification, but found none")
	}
	initialCommit := commits[len(commits)-1] // Use most recent

	// Rename file
	fs.Rename("file1.txt", "renamed.txt")
	waitForTestAutoCommitWithCount(t, fs, 2, -1)
	fs.AssertFileNotExists("file1.txt")
	fs.AssertFileExists("renamed.txt")

	// Restore under current name (file1.txt was renamed to renamed.txt)
	// The restore operation will restore content from initialCommit to the current name
	fs.RestorePathFS("renamed.txt", initialCommit)
	fs.AssertFileExists("renamed.txt")
	fs.AssertFileContent("renamed.txt", "content1")
}

// ============================================================================
// Deletion and Restoration Tests
// ============================================================================

// TestRestore_DeletedFile verifies restoration of deleted file
func TestRestore_DeletedFile(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystemWithGit(t, map[string]string{
		"file1.txt": "content1",
	})
	defer fs.Cleanup()

	// Modify file to trigger initial commit (auto-commit only triggers on modifications)
	fs.WriteFileInMount("file1.txt", []byte("content1-modified"))
	waitForTestAutoCommitWithCount(t, fs, 1, -1) // At least 1 commit

	// Get initial commit
	commits := fs.GetAllCommitsFS()
	if len(commits) == 0 {
		t.Fatalf("Expected at least one commit after file modification, but found none")
	}
	initialCommit := commits[len(commits)-1] // Use most recent

	// Delete file
	fs.RemoveFile("file1.txt")
	waitForTestAutoCommitWithCount(t, fs, 2, -1) // Should have at least 2 commits now
	fs.AssertFileNotExists("file1.txt")

	// Restore deleted file (use force since there are uncommitted changes)
	fs.RestorePathFSWithForce("file1.txt", initialCommit)
	fs.AssertFileExists("file1.txt")
	fs.AssertFileContent("file1.txt", "content1-modified")
}

// TestRestore_DeletedDirectory verifies restoration of deleted directory
func TestRestore_DeletedDirectory(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystemWithGit(t, map[string]string{
		"dir1/file1.txt": "content1",
		"dir1/file2.txt": "content2",
	})
	defer fs.Cleanup()

	// Modify files to trigger initial commits
	fs.WriteFileInMount("dir1/file1.txt", []byte("content1"))
	fs.WriteFileInMount("dir1/file2.txt", []byte("content2"))
	waitForTestAutoCommitWithCount(t, fs, 2, -1)

	// Get initial commit
	commits := fs.GetAllCommitsFS()
	if len(commits) == 0 {
		t.Fatalf("Expected at least one commit after file modification, but found none")
	}
	initialCommit := commits[len(commits)-1] // Use most recent

	// Delete directory (remove all files)
	fs.RemoveFile("dir1/file1.txt")
	fs.RemoveFile("dir1/file2.txt")
	waitForTestAutoCommitWithCount(t, fs, 4, -1)
	fs.AssertFileNotExists("dir1/file1.txt")
	fs.AssertFileNotExists("dir1/file2.txt")

	// Restore directory (use force since there are uncommitted changes)
	fs.RestorePathFSWithForce("dir1/", initialCommit)
	fs.AssertFileExists("dir1/file1.txt")
	fs.AssertFileExists("dir1/file2.txt")
	fs.AssertFileContent("dir1/file1.txt", "content1")
	fs.AssertFileContent("dir1/file2.txt", "content2")
}

// ============================================================================
// Forward/Backward Restoration Tests
// ============================================================================

// TestRestore_BackwardRestoration verifies backward restoration (to earlier commit)
func TestRestore_BackwardRestoration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystemWithGit(t, map[string]string{
		"file1.txt": "version1",
	})
	defer fs.Cleanup()

	// Modify file to trigger initial commit
	fs.WriteFileInMount("file1.txt", []byte("version1"))
	waitForTestAutoCommitWithCount(t, fs, 1, -1)

	commits := fs.GetAllCommitsFS()
	if len(commits) == 0 {
		t.Fatalf("Expected at least one commit after file modification, but found none")
	}
	commit1 := commits[len(commits)-1] // Use most recent

	// Modify to version2
	fs.WriteFileInMount("file1.txt", []byte("version2"))
	waitForTestAutoCommitWithCount(t, fs, 2, -1)
	commits = fs.GetAllCommitsFS()
	if len(commits) < 2 {
		t.Fatalf("Expected at least 2 commits (create + modify), but found %d", len(commits))
	}

	// Modify to version3
	fs.WriteFileInMount("file1.txt", []byte("version3"))
	waitForTestAutoCommitWithCount(t, fs, 3, -1)
	fs.AssertFileContent("file1.txt", "version3")

	// Restore backward to commit1
	fs.RestorePathFS("file1.txt", commit1)
	fs.AssertFileContent("file1.txt", "version1")
}

// TestRestore_ForwardRestoration verifies forward restoration (to later commit)
func TestRestore_ForwardRestoration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystemWithGit(t, map[string]string{
		"file1.txt": "version1",
		"file2.txt": "version1",
	})
	defer fs.Cleanup()

	// Create file with version1
	fs.CreateSourceFile("file1.txt", []byte("version1"))
	fs.WriteFileInMount("file1.txt", []byte("version1"))
	waitForTestAutoCommit(t, fs)

	commits := fs.GetAllCommitsFS()
	if len(commits) == 0 {
		t.Fatalf("Expected at least one commit after file creation, but found none")
	}
	commit1 := commits[0]

	// Modify to version2
	fs.WriteFileInMount("file1.txt", []byte("version2"))
	waitForTestAutoCommitWithCount(t, fs, 2, 2) // Should have at least 2 commits now
	commits = fs.GetAllCommitsFS()
	if len(commits) < 2 {
		t.Fatalf("Expected at least 2 commits (create + modify), but found %d", len(commits))
	}
	commit2 := commits[len(commits)-1]

	// Restore backward to commit1 first
	fs.RestorePathFS("file1.txt", commit1)
	fs.AssertFileContent("file1.txt", "version1")

	// Restore forward to commit2
	fs.RestorePathFS("file1.txt", commit2)
	fs.AssertFileContent("file1.txt", "version2")
}

// ============================================================================
// Partial Restoration Tests
// ============================================================================

// TestRestore_PartialRestoration verifies partial restoration (some files only)
func TestRestore_PartialRestoration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystemWithGit(t, map[string]string{
		"file1.txt": "version1",
		"file2.txt": "version1",
	})
	defer fs.Cleanup()

	// Modify files to trigger initial commits (auto-commit only triggers on modifications, not file creation)
	fs.WriteFileInMount("file1.txt", []byte("version1-modified"))
	fs.WriteFileInMount("file2.txt", []byte("version1-modified"))
	waitForTestAutoCommitWithCount(t, fs, 2, -1) // Should have at least 2 commits now (may have more from setup)

	// Get initial commit - use the most recent commit after modifications (not commits[0] which is oldest)
	commits := fs.GetAllCommitsFS()
	if len(commits) == 0 {
		t.Fatalf("Expected at least one commit after file modification, but found none")
	}
	initialCommit := commits[len(commits)-1] // Use most recent commit

	// Modify both files again
	fs.WriteFileInMount("file1.txt", []byte("version2"))
	fs.WriteFileInMount("file2.txt", []byte("version2"))
	waitForTestAutoCommitWithCount(t, fs, 4, -1) // Should have at least 4 commits now
	fs.AssertFileContent("file1.txt", "version2")
	fs.AssertFileContent("file2.txt", "version2")

	// Restore only file1
	fs.RestorePathFS("file1.txt", initialCommit)

	// Verify file1 restored, file2 unchanged
	fs.AssertFileContent("file1.txt", "version1-modified")
	fs.AssertFileContent("file2.txt", "version2")
}

// ============================================================================
// Complex Scenarios Tests
// ============================================================================

// TestRestore_RenameThenDelete verifies restoration after rename then delete
func TestRestore_RenameThenDelete(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystemWithGit(t, map[string]string{
		"file1.txt": "content1",
	})
	defer fs.Cleanup()

	// Modify file to trigger initial commit
	fs.WriteFileInMount("file1.txt", []byte("content1"))
	waitForTestAutoCommitWithCount(t, fs, 1, -1)

	// Get the commit where file existed as file1.txt
	commitsBeforeRename := fs.GetAllCommitsFS()
	if len(commitsBeforeRename) == 0 {
		t.Fatalf("Expected at least one commit after file modification, but found none")
	}
	commitBeforeRename := commitsBeforeRename[len(commitsBeforeRename)-1]

	// Rename file
	fs.Rename("file1.txt", "renamed.txt")
	waitForTestAutoCommitWithCount(t, fs, 2, -1)

	// Delete renamed file
	fs.RemoveFile("renamed.txt")
	fs.AssertFileNotExists("renamed.txt")

	waitForTestAutoCommitWithCount(t, fs, 3, -1)

	// Debug: Check final commit count
	commitsFinal := fs.GetAllCommitsFS()
	tu.Debugf(t, "Final commit count: %d (expected 3-4)", len(commitsFinal))
	for i, commit := range commitsFinal {
		tu.Debugf(t, "Final Commit %d: %s", i, commit)
	}

	tu.Debugf(t, "ListVersionsFS: ==>\n%s\n<==", fs.ListVersionsFS())

	// Restore to the commit before rename (file existed as file1.txt)
	// This will restore the file under its new name since the old name doesn't exist
	fs.RestorePathFS("renamed.txt", commitBeforeRename)
	fs.AssertFileExists("renamed.txt")
	fs.AssertFileContent("renamed.txt", "content1")
}

// TestRestore_MultipleOperations verifies restoration after multiple operations
func TestRestore_MultipleOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fs := setupTestFilesystemWithGit(t, map[string]string{
		"file1.txt": "version1",
	})
	defer fs.Cleanup()

	// Modify file to trigger initial commit
	fs.WriteFileInMount("file1.txt", []byte("version1"))
	waitForTestAutoCommitWithCount(t, fs, 1, -1)

	// Get commit after initial modification
	commits := fs.GetAllCommitsFS()
	if len(commits) < 1 {
		t.Fatalf("Expected at least 1 commit, but found %d", len(commits))
	}

	// Modify to version2
	fs.WriteFileInMount("file1.txt", []byte("version2"))
	waitForTestAutoCommitWithCount(t, fs, 2, -1)
	commits = fs.GetAllCommitsFS()
	if len(commits) < 2 {
		t.Fatalf("Expected at least 2 commits (create + modify), but found %d", len(commits))
	}
	modifyCommit := commits[len(commits)-1]

	// Rename
	fs.Rename("file1.txt", "renamed.txt")
	waitForTestAutoCommitWithCount(t, fs, 3, -1)
	commits = fs.GetAllCommitsFS()
	if len(commits) < 3 {
		t.Fatalf("Expected at least 3 commits (create + modify + rename), but found %d", len(commits))
	}

	// Restore to modify commit using the new name (file1.txt no longer exists)
	// This should restore the file content from modifyCommit under the current name
	fs.RestorePathFS("renamed.txt", modifyCommit)
	fs.AssertFileExists("renamed.txt")
	fs.AssertFileContent("renamed.txt", "version2")
}
