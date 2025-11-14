package testing

import (
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

func IndentMessage(level int, message string) string {
	indent := strings.Repeat("  ", level) // 2 spaces per level
	lines := strings.Split(message, "\n")
	indented := make([]string, len(lines))
	for i, line := range lines {
		indented[i] = indent + line
	}
	return strings.Join(indented, "\n")
}

func DirListUsingLs(dir string, t *testing.T) {
	t.Helper()
	Debug(t, fmt.Sprintf("Debug list the directory: %s", dir))
	lsCmd := exec.Command("ls", "-la")
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
	if err := os.Mkdir(path, 0755); err != nil {
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

func DirShouldContainsEntries(dir string, expectedEntries []string, t *testing.T, levels ...int) {
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

// testLogWriter wraps t.Logf to implement io.Writer
type testLogWriter struct {
	t      *testing.T
	prefix string
	buf    []byte
}

func (w *testLogWriter) Write(p []byte) (n int, err error) {
	w.buf = append(w.buf, p...)
	for {
		idx := strings.IndexByte(string(w.buf), '\n')
		if idx == -1 {
			break
		}
		line := strings.TrimRight(string(w.buf[:idx]), "\r\n")
		if line != "" {
			w.t.Logf("%s%s", w.prefix, line)
		}
		w.buf = w.buf[idx+1:]
	}
	return len(p), nil
}

func (w *testLogWriter) Close() error {
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

	args := []string{"--debug"}
	if len(cacheDir) > 0 && cacheDir[0] != "" {
		args = append(args, "--cache-dir", cacheDir[0])
	}
	args = append(args, mountPoint, srcDir)

	cmd := exec.Command(bm.binaryPath, args...)

	stdoutWriter := &testLogWriter{t: t, prefix: "[stdout] "}
	stderrWriter := &testLogWriter{t: t, prefix: "[stderr] "}
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	return cmd, cmd.Start()
}

// GracefulShutdown gracefully shuts down a process and unmounts the mount point
func GracefulShutdown(cmd *exec.Cmd, mountPoint string, t *testing.T) {
	t.Helper()

	if cmd.Process != nil {
		cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() {
			done <- cmd.Wait()
		}()

		select {
		case <-time.After(5 * time.Second):
			if cmd.Process != nil {
				cmd.Process.Kill()
				cmd.Wait()
			}
		case err := <-done:
			if err != nil {
				t.Logf("Process exited with error: %v", err)
			}
		}
	}

	// Unmount after process exits
	exec.Command("umount", mountPoint).Run()
}

// SetupTestMain sets up TestMain with binary management
// Returns a function to call in TestMain's defer
// Note: This creates a temporary testing.T for setup, which is acceptable for TestMain
func SetupTestMain(m *testing.M, binaryPath string, logLevel string) func() {
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

	os.Setenv("SHADOWFS_LOG_LEVEL", logLevel)

	if err := bm.BuildBinary(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to build binary: %v\n", err)
		os.Exit(1)
	}

	return func() {
		bm.Cleanup()
		os.RemoveAll(tempDir)
	}
}
