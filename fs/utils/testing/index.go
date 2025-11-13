package testing

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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
	Debug(t, fmt.Sprintf("Debug list the directory: %s", dir))
	lsCmd := exec.Command("ls", "-la")
	lsCmd.Dir = dir
	out, err := lsCmd.CombinedOutput()
	if err != nil {
		Failf(t, "Failed to list directory: %v, output: %s", err, string(out))
	}
	Info(t, fmt.Sprintf("Directory list: %s", string(out)))
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
