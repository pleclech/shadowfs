package testings

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Constants duplicated from fs package to avoid import cycle
// These match fs/constants.go
const rootName = ".root"

func IndentMessage(level int, message string) string {
	indent := strings.Repeat("  ", level) // 2 spaces per level
	lines := strings.Split(message, "\n")
	indented := make([]string, len(lines))
	for i, line := range lines {
		indented[i] = indent + line
	}
	return strings.Join(indented, "\n")
}

func DirListUsingLs(dir string, t *testing.T, recursive ...bool) {
	t.Helper()
	Debug(t, fmt.Sprintf("Debug list the directory: %s", dir))
	flags := "-la"
	if len(recursive) > 0 && recursive[0] {
		flags += "R"
	}
	lsCmd := exec.Command("ls", flags)
	lsCmd.Dir = dir
	out, err := lsCmd.CombinedOutput()
	if err != nil {
		Failf(t, "Failed to list directory %s: %v, output: %s", dir, err, string(out))
	}
	Info(t, fmt.Sprintf("Directory list of %s: %s", dir, string(out)))
}

func ShouldCreateDir(path string, t *testing.T, levels ...int) {
	t.Helper()
	level := getLevel(levels...)
	InfoFIndent(t, level, "directory %s should be created", path)
	// Check if directory already exists (idempotent mkdir)
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		// Directory already exists - idempotent success
		DebugFIndent(t, level+1, "directory %s already exists (idempotent)", path)
	} else if err := os.Mkdir(path, 0755); err != nil {
		FailFIndent(t, level, "failed to create directory: %v", err)
	}
	ShouldExist(path, t, level+1)
	ShouldBeRegularDirectory(path, t, level+1)
	SuccessFIndent(t, level, "")
}

func ShouldCreateFile(path string, content string, t *testing.T, levels ...int) {
	t.Helper()
	level := getLevel(levels...)
	InfoFIndent(t, level, "file %s should be created", path)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		FailFIndent(t, level, "failed to create file: %v", err)
	}
	ShouldExist(path, t, level+1)
	ShouldBeRegularFile(path, t, level+1)
	ShouldHaveSameContent(path, content, t, level+1)
	SuccessFIndent(t, level, "")
}

func ShouldRenameFile(oldPath string, newPath string, t *testing.T, levels ...int) {
	t.Helper()
	level := getLevel(levels...)
	InfoFIndent(t, level, "file %s should be renamed to %s", oldPath, newPath)
	if err := os.Rename(oldPath, newPath); err != nil {
		FailFIndent(t, level, "failed to rename file: %v", err)
	}

	ShouldNotExist(oldPath, t, level+1)
	ShouldExist(newPath, t, level+1)
	ShouldBeRegularFile(newPath, t, level+1)
	SuccessFIndent(t, level, "")
}

func ShouldRenameDir(srcPath string, dstPath string, t *testing.T, levels ...int) {
	t.Helper()
	level := getLevel(levels...)
	InfoFIndent(t, level, "directory %s should be renamed to %s", srcPath, dstPath)

	// Check if dest dir exists - rename should always fail (rename() syscall behavior)
	if info, err := os.Stat(dstPath); err == nil {
		if info.IsDir() {
			// Dest dir exists - rename should fail regardless of whether it's empty or not
			InfoFIndent(t, level+1, "dest directory %s exists, attempting rename (should fail)", dstPath)
			if err := os.Rename(srcPath, dstPath); err == nil {
				FailFIndent(t, level, "rename should have failed because dest directory %s exists, but succeeded", dstPath)
				return
			}
			SuccessFIndent(t, level, "rename correctly failed because dest directory exists")
			return
		}
	}

	// Dest dir does not exist - src should be renamed to dst
	if err := os.Rename(srcPath, dstPath); err != nil {
		FailFIndent(t, level, "failed to rename directory: %v", err)
	}
	ShouldNotExist(srcPath, t, level+1)
	ShouldExist(dstPath, t, level+1)
	ShouldBeRegularDirectory(dstPath, t, level+1)
	SuccessFIndent(t, level, "")
}

func ShouldHaveSameContent(path string, base string, t *testing.T, levels ...int) {
	t.Helper()
	level := getLevel(levels...)
	InfoFIndent(t, level, "file %s should have same content", path)
	if content, err := os.ReadFile(path); err != nil {
		FailFIndent(t, level, "failed to read file: %v", err)
	} else if string(content) != base {
		FailFIndent(t, level, "file content mismatch (path: %s): expected '%s', got '%s'", path, base, string(content))
	}
	SuccessFIndent(t, level, "")
}

func CreateTmpDir(name string, t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(dir, 0755); err != nil {
		Failf(t, "Failed to create temporary directory %s: %v", name, err)
	}

	return dir
}

func Info(t *testing.T, message string) {
	t.Helper()
	t.Logf("\033[33mℹ\033[0m info: %s", message)
}

func Infof(t *testing.T, format string, args ...interface{}) {
	t.Helper()
	Info(t, fmt.Sprintf(format, args...))
}

func InfoIndent(t *testing.T, level int, message string) {
	t.Helper()
	msg := fmt.Sprintf("\033[33mℹ\033[0m info: %s", message)
	t.Log(IndentMessage(level, msg))
}

func InfoFIndent(t *testing.T, level int, format string, args ...interface{}) {
	t.Helper()
	msg := fmt.Sprintf(format, args...)
	InfoIndent(t, level, msg)
}

func Debug(t *testing.T, message string) {
	t.Helper()
	t.Logf("\033[34m🐛\033[0m debug: %s", message)
}

func Debugf(t *testing.T, format string, args ...interface{}) {
	t.Helper()
	Debug(t, fmt.Sprintf(format, args...))
}

func DebugIndent(t *testing.T, level int, message string) {
	t.Helper()
	msg := fmt.Sprintf("\033[34m🐛\033[0m debug: %s", message)
	t.Log(IndentMessage(level, msg))
}

func DebugFIndent(t *testing.T, level int, format string, args ...interface{}) {
	t.Helper()
	msg := fmt.Sprintf(format, args...)
	DebugIndent(t, level, msg)
}
func Fail(t *testing.T, message string) {
	t.Helper()
	// fail adding red cross to the test output
	t.Logf("\033[31m✗\033[0m error: %s", message)
	t.Fatal()
}

func Failf(t *testing.T, format string, args ...interface{}) {
	t.Helper()
	Fail(t, fmt.Sprintf(format, args...))
}

func FailIndent(t *testing.T, level int, message string) {
	t.Helper()
	msg := fmt.Sprintf("\033[31m✗\033[0m error: %s", message)
	t.Log(IndentMessage(level, msg))
	t.Fatal()
}

func FailFIndent(t *testing.T, level int, format string, args ...interface{}) {
	t.Helper()
	msg := fmt.Sprintf(format, args...)
	FailIndent(t, level, msg)
}

// Error logs an error but does not stop test execution
// Use this when you want to report an error but continue the test
func Error(t *testing.T, message string) {
	t.Helper()
	t.Logf("\033[31m✗\033[0m error: %s", message)
	t.Error(message)
}

// Errorf logs a formatted error but does not stop test execution
// Use this when you want to report an error but continue the test
func Errorf(t *testing.T, format string, args ...interface{}) {
	t.Helper()
	Error(t, fmt.Sprintf(format, args...))
}

func Success(t *testing.T, message string) {
	t.Helper()
	status := "success"
	if message == "" {
		status += "."
	} else {
		status += ": "
	}
	t.Logf("\033[32m✓\033[0m %s%s", status, message)
}

func Successf(t *testing.T, format string, args ...interface{}) {
	t.Helper()
	Success(t, fmt.Sprintf(format, args...))
}

func SuccessIndent(t *testing.T, level int, message string) {
	t.Helper()
	status := "success"
	if message == "" {
		status += "."
	} else {
		status += ": "
	}
	msg := fmt.Sprintf("\033[32m✓\033[0m %s%s", status, message)
	t.Log(IndentMessage(level, msg))
}

func SuccessFIndent(t *testing.T, level int, format string, args ...interface{}) {
	t.Helper()
	msg := fmt.Sprintf(format, args...)
	SuccessIndent(t, level, msg)
}

func ListDir(dir string, t *testing.T) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		Failf(t, "failed to read directory: %v", err)
	}
	return entries
}

func getLevel(levels ...int) int {
	if len(levels) > 0 {
		return levels[0]
	}
	return 0
}

func DirShouldBeEmpty(dir string, t *testing.T, levels ...int) {
	t.Helper()
	level := getLevel(levels...)
	InfoFIndent(t, level, "directory %s should be empty", dir)
	entries := ListDir(dir, t)
	if len(entries) != 0 {
		FailFIndent(t, level, "directory %s should be empty, but has %d entries", dir, len(entries))
	}
	SuccessFIndent(t, level, "")
}

func DirShouldNotBeEmpty(dir string, t *testing.T, levels ...int) {
	t.Helper()
	level := getLevel(levels...)
	InfoFIndent(t, level, "directory %s should not be empty", dir)
	entries := ListDir(dir, t)
	if len(entries) == 0 {
		FailFIndent(t, level, "directory %s should not be empty", dir)
	}
	SuccessFIndent(t, level, "")
}

func DirShouldHaveEntries(dir string, expectedEntries []string, t *testing.T, levels ...int) {
	t.Helper()
	level := getLevel(levels...)
	InfoFIndent(t, level, "directory %s should have %d entries", dir, len(expectedEntries))
	entries := ListDir(dir, t)
	if len(entries) != len(expectedEntries) {
		FailFIndent(t, level, "directory %s should have %d entries, but has %d. Entries: %v", dir, len(expectedEntries), len(entries), entries)
	}
	for _, entry := range entries {
		if !slices.Contains(expectedEntries, entry.Name()) {
			FailFIndent(t, level, "directory %s should have entry %s, but does not", dir, entry.Name())
		}
	}
	SuccessFIndent(t, level, "")
}

func DirShouldContainEntries(dir string, expectedEntries []string, t *testing.T, levels ...int) {
	t.Helper()
	level := getLevel(levels...)
	InfoFIndent(t, level, "directory %s should contain entries %v", dir, expectedEntries)
	entries := ListDir(dir, t)
	found := false
	for _, entry := range entries {
		if slices.Contains(expectedEntries, entry.Name()) {
			found = true
			break
		}
	}
	if !found {
		FailFIndent(t, level, "directory %s should contain entries %v, but does not", dir, expectedEntries)
	}
	SuccessFIndent(t, level, "")
}

func ShouldNotExist(path string, t *testing.T, levels ...int) {
	t.Helper()
	level := getLevel(levels...)
	InfoFIndent(t, level, "file or directory %s should not exist", path)
	if _, err := os.Stat(path); err == nil {
		FailFIndent(t, level, "file or directory %s should not exist, but does", path)
	}
	SuccessFIndent(t, level, "")
}

func ShouldExist(path string, t *testing.T, levels ...int) {
	t.Helper()
	level := getLevel(levels...)
	InfoFIndent(t, level, "file or directory %s should exist", path)
	if _, err := os.Stat(path); err != nil {
		FailFIndent(t, level, "file or directory %s should exist, but does not", path)
	}
	SuccessFIndent(t, level, "")
}

func ShouldBeFile(path string, t *testing.T, levels ...int) {
	t.Helper()
	level := getLevel(levels...)
	InfoFIndent(t, level, "file %s should be a file", path)
	if info, err := os.Stat(path); err != nil {
		FailFIndent(t, level, "failed to stat file: %v", err)
	} else if info.IsDir() {
		FailFIndent(t, level, "file %s should be a file, but is a directory: %v", path, info.Mode())
	}
	SuccessFIndent(t, level, "")
}

func ShouldBeSymlink(path string, t *testing.T, levels ...int) {
	t.Helper()
	level := getLevel(levels...)
	InfoFIndent(t, level, "symlink %s should be a symlink", path)
	if info, err := os.Lstat(path); err != nil {
		FailFIndent(t, level, "failed to stat symlink: %v", err)
	} else if info.Mode()&os.ModeSymlink == 0 {
		FailFIndent(t, level, "symlink %s should be a symlink, but is a file", path)
	}
	SuccessFIndent(t, level, "")
}

func ShouldBeRegularFile(path string, t *testing.T, levels ...int) {
	t.Helper()
	level := getLevel(levels...)
	InfoFIndent(t, level, "file %s should be a regular file", path)
	if info, err := os.Stat(path); err != nil {
		FailFIndent(t, level, "failed to stat file: %v", err)
	} else if info.Mode()&os.ModeSymlink != 0 {
		FailFIndent(t, level, "file %s should be a regular file, but is a symlink", path)
	}
	SuccessFIndent(t, level, "")
}

func ShouldBeRegularDirectory(path string, t *testing.T, levels ...int) {
	t.Helper()
	level := getLevel(levels...)
	InfoFIndent(t, level, "directory %s should be a regular directory", path)
	if info, err := os.Stat(path); err != nil {
		FailFIndent(t, level, "failed to stat directory: %v", err)
	} else if info.Mode()&os.ModeSymlink != 0 {
		FailFIndent(t, level, "directory %s should be a regular directory, but is a symlink", path)
	}
	SuccessFIndent(t, level, "")
}

func ShouldRemoveFile(path string, t *testing.T, levels ...int) {
	t.Helper()
	level := getLevel(levels...)
	InfoFIndent(t, level, "file %s should be removed", path)

	// Check if path exists before trying to remove
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// File doesn't exist - that's fine, consider it already removed
		SuccessFIndent(t, level, "file already removed")
		return
	}

	if err := os.Remove(path); err != nil {
		// If we get "transport endpoint is not connected", the filesystem was unmounted
		// This is acceptable in test cleanup scenarios
		if strings.Contains(err.Error(), "transport endpoint is not connected") {
			InfoFIndent(t, level+1, "filesystem already unmounted, skipping removal")
			SuccessFIndent(t, level, "")
			return
		}
		FailFIndent(t, level, "failed to remove file: %v", err)
	}
	ShouldNotExist(path, t, level+1)
	SuccessFIndent(t, level, "")
}

func ReadFileContent(path string, t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		Failf(t, "failed to read file %s: %v", path, err)
	}
	return string(content)
}

// GetEntryNames extracts entry names from a slice of directory entries
// Useful for debugging and logging directory contents
func GetEntryNames(entries []os.DirEntry) []string {
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names
}

// WaitForFilesystemReady waits for the filesystem to be ready after starting
// If duration is 0, defaults to 100ms
func WaitForFilesystemReady(duration time.Duration) {
	if duration == 0 {
		duration = 100 * time.Millisecond
	}
	time.Sleep(duration)
}

// GetCachePath computes the cache path for a given mount point, source directory, and relative path
// Uses SHA256 hash pattern: ~/.shadowfs/{hash}/.root/{relPath}
func GetCachePath(mountPoint, srcDir, relPath string) string {
	homeDir, _ := os.UserHomeDir()
	mountID := fmt.Sprintf("%x", sha256.Sum256([]byte(mountPoint+srcDir)))
	cacheRoot := filepath.Join(homeDir, ".shadowfs", mountID, ".root")
	return filepath.Join(cacheRoot, relPath)
}

// CreateFilesInDirectory creates multiple files in the source directory
// Automatically creates parent directories as needed
func CreateFilesInDirectory(srcDir string, files map[string]string, t *testing.T, levels ...int) {
	t.Helper()
	level := getLevel(levels...)
	InfoFIndent(t, level, "creating %d files in %s", len(files), srcDir)
	for path, content := range files {
		fullPath := filepath.Join(srcDir, path)
		dirOnly := strings.HasSuffix(path, "/")

		var dir string
		if dirOnly {
			dir = fullPath
		} else {
			dir = filepath.Dir(fullPath)
		}

		if err := os.MkdirAll(dir, 0755); err != nil {
			FailFIndent(t, level, "failed to create directory %s: %v", dir, err)
		}
		ShouldBeRegularDirectory(dir, t, level+1)

		if !dirOnly {
			ShouldCreateFile(fullPath, content, t, level+1)
		}
	}
	SuccessFIndent(t, level, "")
}

// AssertDirectoryContains checks that a directory contains at least the expected entries
// Does not require exact match - directory can have more entries
func AssertDirectoryContains(dir string, expected []string, t *testing.T, levels ...int) {
	t.Helper()
	level := getLevel(levels...)
	InfoFIndent(t, level, "directory %s should contain entries %v", dir, expected)
	entries := ListDir(dir, t)
	entryMap := make(map[string]bool)
	for _, entry := range entries {
		entryMap[entry.Name()] = true
	}
	for _, expectedName := range expected {
		if !entryMap[expectedName] {
			FailFIndent(t, level, "directory %s should contain entry %s, but does not", dir, expectedName)
		}
	}
	SuccessFIndent(t, level, "")
}

// AssertDirectoryHasExactEntries checks that a directory has exactly the expected entries
// Directory must have exactly the same entries, no more, no less
func AssertDirectoryHasExactEntries(dir string, expected []string, t *testing.T, levels ...int) {
	t.Helper()
	level := getLevel(levels...)
	InfoFIndent(t, level, "directory %s should have exactly %d entries", dir, len(expected))
	entries := ListDir(dir, t)
	if len(entries) != len(expected) {
		entryNames := GetEntryNames(entries)
		FailFIndent(t, level, "directory %s should have %d entries, but has %d. Expected: %v, Got: %v", dir, len(expected), len(entries), expected, entryNames)
	}
	entryMap := make(map[string]bool)
	for _, entry := range entries {
		entryMap[entry.Name()] = true
	}
	for _, expectedName := range expected {
		if !entryMap[expectedName] {
			FailFIndent(t, level, "directory %s should have entry %s, but does not", dir, expectedName)
		}
	}
	SuccessFIndent(t, level, "")
}

// ============================================================================
// Filesystem Binary Management Utilities
// ============================================================================

// BinaryManager manages test binary lifecycle
type BinaryManager struct {
	binaryPath string
	built      bool
	mu         sync.Mutex
	srcDir     string
	mntDir     string
	cacheDir   string
}

// NewBinaryManager creates a new binary manager
func NewBinaryManager(t *testing.T, binaryPath string) *BinaryManager {
	srcDir := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatalf("Failed to create source directory %s: %v", srcDir, err)
	}
	mntDir := filepath.Join(t.TempDir(), "mnt")
	if err := os.MkdirAll(mntDir, 0755); err != nil {
		t.Fatalf("Failed to create mount directory %s: %v", mntDir, err)
	}
	cacheDir := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatalf("Failed to create cache directory %s: %v", cacheDir, err)
	}
	return &BinaryManager{
		binaryPath: binaryPath,
		srcDir:     srcDir,
		mntDir:     mntDir,
		cacheDir:   cacheDir,
	}
}

// SrcDir returns the source directory path
func (bm *BinaryManager) SrcDir() string {
	return bm.srcDir
}

// MntDir returns the mount directory path
func (bm *BinaryManager) MntDir() string {
	return bm.mntDir
}

// CacheDir returns the cache directory path
func (bm *BinaryManager) CacheDir() string {
	return bm.cacheDir
}

// BuildBinary builds the test binary
func (bm *BinaryManager) BuildBinary() error {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if bm.built {
		return nil
	}

	// Remove existing binary if it exists
	if _, err := os.Stat(bm.binaryPath); err == nil {
		os.Remove(bm.binaryPath)
	}

	// Find project root by looking for go.mod
	projectRoot := findProjectRoot()
	if projectRoot == "" {
		return fmt.Errorf("failed to find project root (looking for go.mod)")
	}

	cmdPath := filepath.Join(projectRoot, "cmd", "shadowfs")
	cmd := exec.Command("go", "build", "-o", bm.binaryPath, cmdPath)
	cmd.Dir = projectRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to build binary: %w", err)
	}

	bm.built = true
	return nil
}

// findProjectRoot finds the project root by looking for go.mod
func findProjectRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}

	for {
		goModPath := filepath.Join(dir, "go.mod")
		if _, err := os.Stat(goModPath); err == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root
			break
		}
		dir = parent
	}

	return ""
}

// Cleanup removes the test binary
func (bm *BinaryManager) Cleanup() {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if bm.built {
		os.Remove(bm.binaryPath)
		bm.built = false
	}
}

// TestLogWriter wraps t.Logf to implement io.Writer
// Exported for use in test files that need custom command execution
type TestLogWriter struct {
	t      *testing.T
	prefix string
	buf    []byte
}

// NewTestLogWriter creates a new TestLogWriter for redirecting command output to test logs
func NewTestLogWriter(t *testing.T, prefix string) *TestLogWriter {
	return &TestLogWriter{t: t, prefix: prefix}
}

func (w *TestLogWriter) Write(p []byte) (n int, err error) {
	w.buf = append(w.buf, p...)
	for {
		idx := strings.IndexByte(string(w.buf), '\n')
		if idx == -1 {
			break
		}
		line := strings.TrimRight(string(w.buf[:idx]), "\r\n")
		if line != "" {
			Debugf(w.t, "%s%s", w.prefix, line)
		}
		w.buf = w.buf[idx+1:]
	}
	return len(p), nil
}

func (w *TestLogWriter) Close() error {
	if len(w.buf) > 0 {
		line := strings.TrimRight(string(w.buf), "\r\n")
		if line != "" {
			w.t.Logf("%s%s", w.prefix, line)
		}
		w.buf = nil
	}
	return nil
}

// RunBinary runs the test binary with the given arguments
// If cacheDir is provided, it will be passed as --cache-dir flag
func (bm *BinaryManager) RunBinary(t *testing.T, mountPoint, srcDir string, cacheDir ...string) (*exec.Cmd, error) {
	t.Helper()

	if !bm.built {
		if err := bm.BuildBinary(); err != nil {
			return nil, err
		}
	}

	args := []string{}

	if len(cacheDir) > 0 && cacheDir[0] != "" {
		args = append(args, "--cache-dir", cacheDir[0])
	}
	args = append(args, mountPoint, srcDir)

	cmd := exec.Command(bm.binaryPath, args...)
	cmd.Env = os.Environ()

	stdoutWriter := NewTestLogWriter(t, "[stdout] ")
	stderrWriter := NewTestLogWriter(t, "[stderr] ")
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	return cmd, cmd.Start()
}

// GracefulShutdown gracefully shuts down a process and unmounts the mount point
//
// How it works:
// 1. Sends SIGTERM to the process (triggers graceful shutdown in shadowfs)
// 2. Waits up to 5 seconds for process to exit gracefully
// 3. If still running, sends SIGKILL to force termination
// 4. Unmounts the mount point
//
// The shadowfs process handles SIGTERM by:
// - Committing all pending Git changes (if auto-git enabled)
// - Unmounting the filesystem
// - Cleaning up resources
func GracefulShutdown(cmd *exec.Cmd, mountPoint string, t *testing.T) {
	t.Helper()

	if cmd != nil && cmd.Process != nil {
		// Send SIGTERM for graceful shutdown
		cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() {
			done <- cmd.Wait()
		}()

		select {
		case <-time.After(5 * time.Second):
			// Process didn't exit within timeout, force kill
			if cmd.Process != nil {
				t.Logf("Process didn't exit within 5s, forcing termination")
				cmd.Process.Kill()
				cmd.Wait()
			}
		case err := <-done:
			if err != nil {
				t.Logf("Process exited with error: %v", err)
			}
		}
	}

	// Unmount after process exits (in case process didn't unmount itself)
	// This is safe even if already unmounted
	exec.Command("umount", mountPoint).Run()
}

// stopDaemonViaCLI stops a daemon process using the shadowfs CLI command
// Used when filesystem was started in daemon mode (we don't have cmd handle)
func stopDaemonViaCLI(binaryPath, mountPoint string, t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "stop", "--mount-point", mountPoint)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			t.Logf("Stop command timed out, attempting direct unmount")
		} else {
			t.Logf("Stop command failed: %v, output: %s", err, string(output))
		}
		// Fallback to direct unmount
		exec.Command("umount", mountPoint).Run()
	}
}

// SetupFilesystemWithCache creates a standardized filesystem setup for tests
// Returns BinaryManager, mountPoint, srcDir, and cmd
// Optionally accepts a cache directory - if not provided, uses BinaryManager's default cache
// Automatically waits for filesystem to be ready
func SetupFilesystemWithCache(t *testing.T, binaryPath string, cacheDir ...string) (*BinaryManager, string, string, *exec.Cmd) {
	t.Helper()
	testMgr := NewBinaryManager(t, binaryPath)
	mountPoint := testMgr.MntDir()
	srcDir := testMgr.SrcDir()

	var cache string
	if len(cacheDir) > 0 && cacheDir[0] != "" {
		cache = cacheDir[0]
	} else {
		cache = testMgr.CacheDir()
	}

	cmd, err := testMgr.RunBinary(t, mountPoint, srcDir, cache)
	if err != nil {
		t.Fatalf("Failed to start filesystem: %v", err)
	}

	WaitForFilesystemReady(0) // Use default 100ms

	return testMgr, mountPoint, srcDir, cmd
}

// SetupTestMain sets up TestMain with binary management
// Returns a function to call in TestMain's defer
// Note: This creates a temporary testing.T for setup, which is acceptable for TestMain
func SetupTestMain(m *testing.M, binaryPath string) func() {
	// Create a temporary test context for setup
	// We can't use *testing.T in TestMain, so we'll create directories manually
	tempDir, err := os.MkdirTemp("", "shadowfs-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create temp directory: %v\n", err)
		os.Exit(1)
	}

	srcDir := filepath.Join(tempDir, "src")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create source directory: %v\n", err)
		os.Exit(1)
	}
	mntDir := filepath.Join(tempDir, "mnt")
	if err := os.MkdirAll(mntDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create mount directory: %v\n", err)
		os.Exit(1)
	}

	bm := &BinaryManager{
		binaryPath: binaryPath,
		srcDir:     srcDir,
		mntDir:     mntDir,
		cacheDir:   filepath.Join(tempDir, "cache"),
	}

	if err := bm.BuildBinary(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to build binary: %v\n", err)
		os.Exit(1)
	}

	return func() {
		bm.Cleanup()
		os.RemoveAll(tempDir)
	}
}

// ============================================================================
// Test Filesystem Helpers (Reusable for Integration Tests)
// ============================================================================

// TestFilesystem represents a test filesystem setup with common operations
//
// How it works:
// - Filesystem runs in foreground mode (cmd.Start() keeps process handle)
// - Process runs until SIGTERM is sent (via Cleanup())
// - CLI commands (like version restore) are separate processes that access the mount point
// - All tests use defer fs.Cleanup() to ensure graceful shutdown
//
// Foreground vs Daemon mode:
// - Foreground (default): Better for tests - direct process control, easier cleanup
// - Daemon: Parent exits immediately, cleanup must use PID file or CLI command
type TestFilesystem struct {
	mountPoint string
	srcDir     string
	cacheDir   string
	cmd        *exec.Cmd
	binaryPath string // Store binary path for daemon mode cleanup
	isDaemon   bool   // Track if started in daemon mode
	t          *testing.T
}

// NewTestFilesystem creates a new test filesystem and starts it
func NewTestFilesystem(t *testing.T, binaryPath string, initialContent map[string]string) *TestFilesystem {
	t.Helper()

	testMgr := NewBinaryManager(t, binaryPath)

	mntDir := testMgr.MntDir()
	srcDir := testMgr.SrcDir()
	cacheDir := testMgr.CacheDir()

	if cacheDir != "" {
		os.Setenv("SHADOWFS_CACHE_DIR", cacheDir)
	}

	if initialContent != nil {
		CreateFilesInDirectory(srcDir, initialContent, t)
	}

	cmd, err := testMgr.RunBinary(t, mntDir, srcDir, cacheDir)
	if err != nil {
		Failf(t, "Failed to start filesystem: %v", err)
	}

	WaitForFilesystemReady(0)

	return &TestFilesystem{
		mountPoint: mntDir,
		srcDir:     srcDir,
		cacheDir:   cacheDir,
		cmd:        cmd,
		binaryPath: binaryPath,
		t:          t,
	}
}

// GitTestConfig holds configuration for Git-enabled test filesystems
type GitTestConfig struct {
	IdleTimeout  time.Duration // Git idle timeout (default: 30s, use shorter for faster tests)
	SafetyWindow time.Duration // Git safety window (default: 5s, use shorter for faster tests)
	Daemon       bool          // Run as daemon (default: false, foreground mode for tests)
}

// NewTestFilesystemWithGit creates a new test filesystem with Git enabled
// Uses default Git timing (30s idle, 5s safety window) for realistic behavior
func NewTestFilesystemWithGit(t *testing.T, binaryPath string, initialContent map[string]string) *TestFilesystem {
	return NewTestFilesystemWithGitConfig(t, binaryPath, GitTestConfig{}, initialContent)
}

// NewTestFilesystemWithGitConfig creates a new test filesystem with Git enabled and custom configuration
// Useful for tests that need faster commits (shorter idle timeout) or specific Git timing
func NewTestFilesystemWithGitConfig(t *testing.T, binaryPath string, config GitTestConfig, initialContent map[string]string) *TestFilesystem {
	t.Helper()

	testMgr := NewBinaryManager(t, binaryPath)
	mountPoint := testMgr.MntDir()
	srcDir := testMgr.SrcDir()

	if initialContent != nil {
		CreateFilesInDirectory(srcDir, initialContent, t)
	}

	// Set cache directory environment variable BEFORE building command
	// This ensures FindCacheDirectory can discover it when GetGitRepository is called
	// Both the filesystem process and test code need access to this
	cacheDir := testMgr.CacheDir()
	if cacheDir != "" {
		os.Setenv("SHADOWFS_CACHE_DIR", cacheDir)
	}

	// Build command arguments
	args := []string{"--auto-git"}

	// Add Git timing flags if specified (use defaults if not set)
	if config.IdleTimeout > 0 {
		args = append(args, "--git-idle-timeout", config.IdleTimeout.String())
	}
	if config.SafetyWindow > 0 {
		args = append(args, "--git-safety-window", config.SafetyWindow.String())
	}

	// Daemon mode support:
	// - In daemon mode, the parent process exits immediately (os.Exit(0))
	// - We lose the cmd handle, so cleanup must use PID file instead
	// - For tests, foreground mode is recommended (better control, easier cleanup)
	// - If daemon mode is used, Cleanup() will use stopDaemonViaCLI() instead
	if config.Daemon {
		args = append(args, "--daemon")
	}

	// Add cache directory flag if available
	if cacheDir != "" {
		args = append(args, "--cache-dir", cacheDir)
	}

	// Add positional arguments (mount point and source directory)
	args = append(args, mountPoint, srcDir)

	cmd := exec.Command(binaryPath, args...)
	cmd.Env = os.Environ()

	stdoutWriter := NewTestLogWriter(t, "[stdout] ")
	stderrWriter := NewTestLogWriter(t, "[stderr] ")
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	if err := cmd.Start(); err != nil {
		Failf(t, "Failed to start filesystem: %v", err)
	}

	WaitForFilesystemReady(0)

	return &TestFilesystem{
		mountPoint: mountPoint,
		srcDir:     srcDir,
		cacheDir:   testMgr.CacheDir(),
		cmd:        cmd,
		binaryPath: binaryPath,
		isDaemon:   config.Daemon,
		t:          t,
	}
}

// NewTestFilesystemFromExisting creates a TestFilesystem from existing setup
// Useful for tests that need custom filesystem initialization
func NewTestFilesystemFromExisting(t *testing.T, mountPoint, srcDir, cacheDir string, cmd *exec.Cmd, binaryPath string) *TestFilesystem {
	t.Helper()
	return &TestFilesystem{
		mountPoint: mountPoint,
		srcDir:     srcDir,
		cacheDir:   cacheDir,
		cmd:        cmd,
		binaryPath: binaryPath,
		isDaemon:   false, // Assume foreground unless specified
		t:          t,
	}
}

// Cleanup shuts down the test filesystem gracefully
//
// For foreground mode: Uses cmd handle to send SIGTERM directly
// For daemon mode: Uses CLI command (shadowfs stop) since we don't have cmd handle
//
// Ensures:
// - All pending Git commits are saved (if auto-git enabled)
// - Filesystem is unmounted
// - Resources are cleaned up
func (fs *TestFilesystem) Cleanup() {
	if fs.isDaemon {
		// Daemon mode: use CLI command to stop
		stopDaemonViaCLI(fs.binaryPath, fs.mountPoint, fs.t)
	} else {
		// Foreground mode: use direct process control
		GracefulShutdown(fs.cmd, fs.mountPoint, fs.t)
	}
}

// MountPoint returns the mount point path
func (fs *TestFilesystem) MountPoint() string {
	return fs.mountPoint
}

// SourceDir returns the source directory path
func (fs *TestFilesystem) SourceDir() string {
	return fs.srcDir
}

// CacheDir returns the cache directory path
func (fs *TestFilesystem) CacheDir() string {
	return fs.cacheDir
}

func (fs *TestFilesystem) CacheID() string {
	fs.t.Helper()
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fs.mountPoint+fs.srcDir)))
}

func (fs *TestFilesystem) CacheRoot() string {
	fs.t.Helper()
	return filepath.Join(fs.cacheDir, fs.CacheID(), ".root")
}

// MountPath returns the mount point path for a relative path
func (fs *TestFilesystem) MountPath(relPaths ...string) string {
	fs.t.Helper()
	return filepath.Join(fs.mountPoint, filepath.Join(relPaths...))
}

// SourcePath returns the source directory path for a relative path
func (fs *TestFilesystem) SourcePath(relPaths ...string) string {
	fs.t.Helper()
	return filepath.Join(fs.srcDir, filepath.Join(relPaths...))
}

func (fs *TestFilesystem) CachePath(relPaths ...string) string {
	fs.t.Helper()
	return filepath.Join(fs.cacheDir, filepath.Join(relPaths...))
}

// CreateSourceDir creates a directory in the source directory
func (fs *TestFilesystem) CreateSourceDir(relPath string) string {
	fs.t.Helper()
	if relPath == "" {
		return fs.SourceDir()
	}
	fullPath := fs.SourcePath(relPath)
	if err := os.MkdirAll(fullPath, 0755); err != nil {
		Failf(fs.t, "Failed to create source directory %s: %v", fullPath, err)
	}
	ShouldCreateDir(fullPath, fs.t)
	return fullPath
}

// CreateSourceFile creates a file in the source directory
func (fs *TestFilesystem) CreateSourceFile(relPath string, content []byte) string {
	fs.t.Helper()
	fullPath := filepath.Join(fs.CreateSourceDir(filepath.Dir(relPath)), filepath.Base(relPath))
	ShouldCreateFile(fullPath, string(content), fs.t)
	return fullPath
}

func (fs *TestFilesystem) CreateMountDir(relPath string) string {
	fs.t.Helper()
	if relPath == "" {
		return fs.MountPoint()
	}
	fullPath := fs.MountPath(relPath)
	if err := os.MkdirAll(fullPath, 0755); err != nil {
		Failf(fs.t, "Failed to create mount directory %s: %v", fullPath, err)
	}
	ShouldCreateDir(fullPath, fs.t)
	return fullPath
}

func (fs *TestFilesystem) CreateMountFile(relPath string, content []byte) string {
	fs.t.Helper()
	fullPath := filepath.Join(fs.CreateMountDir(filepath.Dir(relPath)), filepath.Base(relPath))
	ShouldCreateFile(fullPath, string(content), fs.t)
	return fullPath
}

// ReadFile reads a file from the mount point and returns its content
func (fs *TestFilesystem) ReadFile(relPath string) string {
	return ReadFileContent(fs.MountPath(relPath), fs.t)
}

// WriteFile writes content to a file in the mount point
// This overwrites existing files (truncates first)
// Note: The filesystem strips O_TRUNC flag and does copy-on-write in Write() when off==0,
// which can leave trailing bytes. We work around this by writing in a way that avoids COW.
func (fs *TestFilesystem) WriteFile(relPath string, content []byte) {
	fs.t.Helper()
	fullPath := fs.MountPath(relPath)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		Failf(fs.t, "Failed to create directory %s: %v", dir, err)
	}

	// Open file for writing (filesystem strips O_TRUNC)
	file, err := os.OpenFile(fullPath, os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		Failf(fs.t, "Failed to open file %s for writing: %v", relPath, err)
	}

	// Manually truncate to 0 to clear any existing content
	// This must happen BEFORE any write to avoid COW copying source content
	if err := file.Truncate(0); err != nil {
		file.Close()
		Failf(fs.t, "Failed to truncate file %s: %v", relPath, err)
	}

	// Seek to beginning
	if _, err := file.Seek(0, 0); err != nil {
		file.Close()
		Failf(fs.t, "Failed to seek file %s: %v", relPath, err)
	}

	// Write all content
	written, err := file.Write(content)
	if err != nil {
		file.Close()
		Failf(fs.t, "Failed to write file %s: %v", relPath, err)
	}

	// Truncate again after write to ensure no trailing bytes
	// This handles the case where COW copied source content
	if err := file.Truncate(int64(len(content))); err != nil {
		file.Close()
		Failf(fs.t, "Failed to truncate file %s after write: %v", relPath, err)
	}

	// Sync and close
	if err := file.Sync(); err != nil {
		file.Close()
		Failf(fs.t, "Failed to sync file %s: %v", relPath, err)
	}
	if err := file.Close(); err != nil {
		Failf(fs.t, "Failed to close file %s: %v", relPath, err)
	}

	if written != len(content) {
		Failf(fs.t, "Failed to write all content to %s: wrote %d of %d bytes", relPath, written, len(content))
	}

	// Small delay to ensure FUSE has processed the write
	time.Sleep(50 * time.Millisecond)

	// Verify the write succeeded by reading back
	actualContent := fs.ReadFile(relPath)
	if actualContent != string(content) {
		Failf(fs.t, "File content mismatch after write: expected %q (%d bytes), got %q (%d bytes)",
			string(content), len(content), actualContent, len(actualContent))
	}
}

// WriteFileInMount writes content to a file in the mount point (simple version without COW handling)
func (fs *TestFilesystem) WriteFileInMount(relPath string, content []byte) {
	fs.t.Helper()

	fullPath := fs.MountPath(relPath)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		Failf(fs.t, "Failed to create directory %s: %v", dir, err)
	}
	if err := os.WriteFile(fullPath, content, 0644); err != nil {
		Failf(fs.t, "Failed to write file %s: %v", relPath, err)
	}
	ShouldHaveSameContent(fullPath, string(content), fs.t)
}

// RemoveFile removes a file from the mount point
func (fs *TestFilesystem) RemoveFile(relPath string) {
	ShouldRemoveFile(fs.MountPath(relPath), fs.t)
}

// Mkdir creates a directory in the mount point
func (fs *TestFilesystem) Mkdir(relPath string) {
	ShouldCreateDir(fs.MountPath(relPath), fs.t)
}

// Rename renames a file/directory in the mount point
func (fs *TestFilesystem) Rename(oldPath, newPath string) {
	fs.t.Helper()
	oldFullPath := fs.MountPath(oldPath)
	newFullPath := fs.MountPath(newPath)

	// Check if it's a directory to use appropriate helper
	if info, err := os.Stat(oldFullPath); err == nil && info.IsDir() {
		ShouldRenameDir(oldFullPath, newFullPath, fs.t)
	} else {
		ShouldRenameFile(oldFullPath, newFullPath, fs.t)
	}
}

// AssertFileExists verifies a file exists in the mount point
func (fs *TestFilesystem) AssertFileExists(relPath string) {
	ShouldExist(fs.MountPath(relPath), fs.t)
}

// AssertFileNotExists verifies a file does not exist in the mount point
func (fs *TestFilesystem) AssertFileNotExists(relPath string) {
	ShouldNotExist(fs.MountPath(relPath), fs.t)
}

// AssertFileContent verifies file content matches expected
func (fs *TestFilesystem) AssertFileContent(relPath, expected string) {
	fs.t.Helper()
	ShouldHaveSameContent(fs.MountPath(relPath), expected, fs.t)
}

// AssertSourceUnchanged verifies source file content is unchanged
func (fs *TestFilesystem) AssertSourceUnchanged(relPath, expected string) {
	ShouldHaveSameContent(fs.SourcePath(relPath), expected, fs.t)
}

// AssertSourceExists verifies source file exists
func (fs *TestFilesystem) AssertSourceExists(relPath string) {
	ShouldExist(fs.SourcePath(relPath), fs.t)
}

// AssertSourceNotExists verifies source file does not exist
func (fs *TestFilesystem) AssertSourceNotExists(relPath string) {
	ShouldNotExist(fs.SourcePath(relPath), fs.t)
}

// ListDir lists directory entries from mount point
func (fs *TestFilesystem) ListDir(relPath string) []os.DirEntry {
	return ListDir(fs.MountPath(relPath), fs.t)
}

// GetCachePath returns the cache path for a given relative path
func (fs *TestFilesystem) GetCachePath(relPath string) string {
	return GetCachePath(fs.mountPoint, fs.srcDir, relPath)
}

// Restart restarts the filesystem
func (fs *TestFilesystem) Restart() {
	fs.t.Helper()
	fs.Cleanup()
	WaitForFilesystemReady(0)

	testMgr := NewBinaryManager(fs.t, fs.binaryPath)
	cmd, err := testMgr.RunBinary(fs.t, fs.mountPoint, fs.srcDir, testMgr.CacheDir())
	if err != nil {
		Failf(fs.t, "Failed to restart filesystem: %v", err)
	}
	fs.cmd = cmd
	WaitForFilesystemReady(0)
}

// ============================================================================
// Git Helper Functions
// ============================================================================

// GetGitDir returns the Git directory path for a mount point
func GetGitDir(mountPoint string, gitofsName string) string {
	return filepath.Join(mountPoint, gitofsName, ".git")
}

// GetAllCommits gets all commit hashes in chronological order (oldest first)
// Uses timeout to prevent hangs when accessing Git through FUSE mount
// Accesses Git directory through cache directory to avoid FUSE deadlocks
// findCacheDirFn is a function to find cache directory (e.g., cache.FindCacheDirectoryForMount)
// This avoids import cycle: testings doesn't import cache, caller passes the function
func GetAllCommits(t *testing.T, mountPoint string, gitofsName string, findCacheDirFn func(string) (string, error)) []string {
	t.Helper()

	// Normalize mount point path
	absMountPoint, err := filepath.Abs(mountPoint)
	if err != nil {
		Failf(t, "Invalid mount point: %v", err)
		return []string{}
	}

	// Resolve symlinks
	if resolvedPath, err := filepath.EvalSymlinks(absMountPoint); err == nil {
		absMountPoint = resolvedPath
	}

	// Find cache directory using provided function (avoids import cycle)
	cacheDir, err := findCacheDirFn(absMountPoint)
	if err != nil {
		Failf(t, "Failed to find cache directory for mount point %s: %v", mountPoint, err)
		return []string{}
	}

	// Access Git directory through cache directory, not mount point
	// Git repository is at .root/.gitofs/.git relative to the session directory
	// (all files in mount point are stored under .root in the cache)
	cachePath := filepath.Join(cacheDir, rootName)
	gitDir := filepath.Join(cachePath, gitofsName, ".git")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Use cache path (.root) as working directory for read-only Git operations (avoids FUSE)
	cmd := exec.CommandContext(ctx, "git", "--git-dir", gitDir, "-C", cachePath, "log", "--oneline", "--format=%H", "--reverse")
	output, err := cmd.CombinedOutput()
	outputStr := string(output)
	if isCommitEmpty(outputStr) {
		return []string{}
	}
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			Failf(t, "Git log command timed out after 10s when accessing %s. This may indicate a FUSE deadlock.", gitDir)
		}
		Failf(t, "Failed to get commits: %v, output: %s", err, outputStr)
	}
	lines := strings.Split(outputStr, "\n")
	commits := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			commits = append(commits, line)
		}
	}
	return commits
}

// GetLatestCommit gets the latest commit hash
// Uses timeout to prevent hangs when accessing Git through FUSE mount
// Accesses Git directory through cache directory to avoid FUSE deadlocks
// findCacheDirFn is a function to find cache directory (e.g., cache.FindCacheDirectoryForMount)
// This avoids import cycle: testings doesn't import cache, caller passes the function
func GetLatestCommit(t *testing.T, mountPoint string, gitofsName string, findCacheDirFn func(string) (string, error)) string {
	t.Helper()

	// Normalize mount point path
	absMountPoint, err := filepath.Abs(mountPoint)
	if err != nil {
		Failf(t, "Invalid mount point: %v", err)
		return ""
	}

	// Resolve symlinks
	if resolvedPath, err := filepath.EvalSymlinks(absMountPoint); err == nil {
		absMountPoint = resolvedPath
	}

	// Find cache directory using provided function (avoids import cycle)
	cacheDir, err := findCacheDirFn(absMountPoint)
	if err != nil {
		Failf(t, "Failed to find cache directory for mount point %s: %v", mountPoint, err)
		return ""
	}

	// Access Git directory through cache directory, not mount point
	// Git repository is at .root/.gitofs/.git relative to the session directory
	// (all files in mount point are stored under .root in the cache)
	cachePath := filepath.Join(cacheDir, rootName)
	gitDir := filepath.Join(cachePath, gitofsName, ".git")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Use cache path (.root) as working directory for read-only Git operations (avoids FUSE)
	cmd := exec.CommandContext(ctx, "git", "--git-dir", gitDir, "-C", cachePath, "log", "--oneline", "-1", "--format=%H")
	output, err := cmd.CombinedOutput()
	outputStr := string(output)
	if isCommitEmpty(outputStr) {
		return ""
	}
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			Failf(t, "Git log command timed out after 10s when accessing %s. This may indicate a FUSE deadlock.", gitDir)
		}
		Failf(t, "Failed to get commit hash: %v, output: %s", err, outputStr)
	}

	return strings.TrimSpace(outputStr)
}

// WaitForAutoCommit waits for auto-commit to complete (idle timeout + safety window)
// Uses default timing: 30s idle timeout + 5s safety window = 35s total
// For faster tests, use NewTestFilesystemWithGitConfig with shorter timeouts
func WaitForAutoCommit() {
	// Default idle timeout is 30s, safety window is 5s, so wait 35s for safety
	time.Sleep(35 * time.Second)
}

// WaitForAutoCommitWithTiming waits for auto-commit with custom timing
// Useful when using NewTestFilesystemWithGitConfig with custom timeouts
func WaitForAutoCommitWithTiming(idleTimeout, safetyWindow time.Duration) {
	// Wait for idle timeout + safety window + small buffer
	totalWait := idleTimeout + safetyWindow + (1 * time.Second)
	time.Sleep(totalWait)
}

// RestorePath restores a path to a specific commit using shadowfs CLI
// Uses a timeout to prevent hangs
func RestorePath(t *testing.T, binaryPath, mountPoint, relPath, commitHash string) {
	t.Helper()
	t.Logf("RestoreFile: Starting restore command: %s version restore --mount-point %s --path %s %s", binaryPath, mountPoint, relPath, commitHash)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "version", "restore", "--mount-point", mountPoint, "--path", relPath, commitHash)
	// Inherit environment variables (including SHADOWFS_LOG_LEVEL for debug mode)
	cmd.Env = os.Environ()

	// Capture both stdout and stderr separately for better debugging
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	t.Logf("RestoreFile: Executing restore command...")
	err := cmd.Run()

	// Always log output for debugging
	if stdout.Len() > 0 {
		t.Logf("RestoreFile stdout: %s", stdout.String())
	}
	if stderr.Len() > 0 {
		t.Logf("RestoreFile stderr: %s", stderr.String())
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			Failf(t, "Restore command timed out after 30s: %v, stdout: %s, stderr: %s", err, stdout.String(), stderr.String())
		}
		Failf(t, "Restore command failed: %v, stdout: %s, stderr: %s", err, stdout.String(), stderr.String())
	}
	t.Logf("RestoreFile: Restore command completed successfully")
}

// RestorePathWithForce restores a path to a specific commit using shadowfs CLI with --force flag
// Uses a timeout to prevent hangs
func RestorePathWithForce(t *testing.T, binaryPath, mountPoint, relPath, commitHash string) {
	t.Helper()
	t.Logf("RestoreFile: Starting restore command: %s version restore --force --mount-point %s --path %s %s", binaryPath, mountPoint, relPath, commitHash)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "version", "restore", "--force", "--mount-point", mountPoint, "--path", relPath, commitHash)
	// Inherit environment variables (including SHADOWFS_LOG_LEVEL for debug mode)
	cmd.Env = os.Environ()

	// Capture both stdout and stderr separately for better debugging
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	t.Logf("RestoreFile: Executing restore command...")
	err := cmd.Run()

	// Always log output for debugging
	if stdout.Len() > 0 {
		t.Logf("RestoreFile stdout: %s", stdout.String())
	}
	if stderr.Len() > 0 {
		t.Logf("RestoreFile stderr: %s", stderr.String())
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			Failf(t, "Restore command timed out after 30s: %v, stdout: %s, stderr: %s", err, stdout.String(), stderr.String())
		}
		Failf(t, "Restore command failed: %v, stdout: %s, stderr: %s", err, stdout.String(), stderr.String())
	}
	t.Logf("RestoreFile: Restore command completed successfully")
}

// RestoreDirectory restores a directory to a specific commit using shadowfs CLI
// Uses a timeout to prevent hangs
func RestoreDirectory(t *testing.T, binaryPath, mountPoint, relPath, commitHash string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "version", "restore", "--mount-point", mountPoint, "--dir", relPath, commitHash)
	// Inherit environment variables (including SHADOWFS_LOG_LEVEL for debug mode)
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			Failf(t, "Restore command timed out after 30s: %v, output: %s", err, string(output))
		}
		Failf(t, "Restore command failed: %v, output: %s", err, string(output))
	}
}

func ListVersions(t *testing.T, binaryPath, mountPoint string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "version", "list", "--mount-point", mountPoint)
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		Failf(t, "List version command failed: %v, output: %s", err, string(output))
	}
	return string(output)
}

// RestoreWorkspace restores entire workspace to a specific commit using shadowfs CLI
// Uses a timeout to prevent hangs
func RestoreWorkspace(t *testing.T, binaryPath, mountPoint, commitHash string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second) // Longer timeout for workspace restore
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "version", "restore", "--mount-point", mountPoint, commitHash)
	// Inherit environment variables (including SHADOWFS_LOG_LEVEL for debug mode)
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			Failf(t, "Restore command timed out after 60s: %v, output: %s", err, string(output))
		}
		Failf(t, "Restore command failed: %v, output: %s", err, string(output))
	}
}

func isCommitEmpty(str string) bool {
	return strings.Contains(str, "No commits")
}

// GetAllCommitsFS gets all commit hashes for this filesystem
// Uses the same CLI command that the real CLI uses (shadowfs version log)
func (fs *TestFilesystem) GetAllCommitsFS() []string {
	// Use CLI command to avoid import cycle (testings can't import parent fs package)
	// This uses the same code path as the real CLI: shadowfs version log --format=%H
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, fs.binaryPath, "version", "log", "--mount-point", fs.mountPoint, "--format=%H", "--reverse")
	env := os.Environ()
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	outputStr := stdout.String()
	stderrStr := stderr.String()

	if isCommitEmpty(stderrStr) || isCommitEmpty(outputStr) {
		return []string{}
	}

	if err != nil {
		// Check if it's "no commits" error (this is OK)
		Failf(fs.t, "Failed to get commits: %v, stderr: %s", err, stderrStr)
		return []string{}
	}

	// Parse output: each line is a commit hash
	lines := strings.Split(outputStr, "\n")
	commits := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			commits = append(commits, line)
		}
	}
	return commits
}

// GetLatestCommitFS gets the latest commit hash for this filesystem
// Uses the same CLI command that the real CLI uses (shadowfs version log --limit=1)
func (fs *TestFilesystem) GetLatestCommitFS() string {
	// Use CLI command to avoid import cycle (testings can't import parent fs package)
	// This uses the same code path as the real CLI: shadowfs version log --format=%H --limit=1
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, fs.binaryPath, "version", "log", "--mount-point", fs.mountPoint, "--format=%H", "--limit=1")
	env := os.Environ()
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	outputStr := stdout.String()
	stderrStr := stderr.String()

	if isCommitEmpty(stderrStr) || isCommitEmpty(outputStr) {
		return ""
	}

	if err != nil {
		Failf(fs.t, "Failed to get latest commit: %v, stderr: %s", err, stderrStr)
		return ""
	}

	return strings.TrimSpace(outputStr)
}

// WriteFileAndWait writes content and waits for auto-commit
func (fs *TestFilesystem) WriteFileInMountAndWait(relPath string, content []byte) {
	fs.WriteFileInMount(relPath, content)
	WaitForAutoCommit()
}

// RenameAndWait renames and waits for auto-commit
func (fs *TestFilesystem) RenameAndWait(oldPath, newPath string) {
	fs.Rename(oldPath, newPath)
	WaitForAutoCommit()
}

// RemoveFileAndWait removes file and waits for auto-commit
func (fs *TestFilesystem) RemoveFileAndWait(relPath string) {
	fs.RemoveFile(relPath)
	WaitForAutoCommit()
}

// RestoreFileFS restores a file to a specific commit
func (fs *TestFilesystem) RestorePathFS(relPath, commitHash string) {
	RestorePath(fs.t, fs.binaryPath, fs.mountPoint, relPath, commitHash)
}

// RestorePathFSWithForce restores a path to a specific commit with --force flag
func (fs *TestFilesystem) RestorePathFSWithForce(relPath, commitHash string) {
	RestorePathWithForce(fs.t, fs.binaryPath, fs.mountPoint, relPath, commitHash)
}

// RestoreWorkspaceFS restores entire workspace to a specific commit
func (fs *TestFilesystem) RestoreWorkspaceFS(commitHash string) {
	RestoreWorkspace(fs.t, fs.binaryPath, fs.mountPoint, commitHash)
}

func (fs *TestFilesystem) ListVersionsFS() string {
	return ListVersions(fs.t, fs.binaryPath, fs.mountPoint)
}
