//go:build linux
// +build linux

package fs

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// TestSetup provides common test infrastructure
type TestSetup struct {
	T          *testing.T
	MountPoint string
	SrcDir     string
	CacheDir   string
	Root       *ShadowNode
}

// NewTestSetup creates a new test environment
func NewTestSetup(t *testing.T) *TestSetup {
	mountPoint := t.TempDir()
	srcDir := t.TempDir()

	// Create test source directory structure
	createTestFilesystem(t, srcDir)

	root, err := NewShadowRoot(mountPoint, srcDir, "")
	if err != nil {
		t.Fatalf("Failed to create shadow root: %v", err)
	}

	shadowRoot := root.(*ShadowNode)

	return &TestSetup{
		T:          t,
		MountPoint: mountPoint,
		SrcDir:     srcDir,
		CacheDir:   shadowRoot.cachePath,
		Root:       shadowRoot,
	}
}

// createTestFilesystem creates a test directory structure
func createTestFilesystem(t *testing.T, baseDir string) {
	// Create test files
	testFiles := map[string]string{
		"file1.txt":       "content1",
		"file2.txt":       "content2",
		"dir1/nested.txt": "nested content",
		"dir2/file.txt":   "dir2 content",
	}

	for path, content := range testFiles {
		fullPath := filepath.Join(baseDir, path)
		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("Failed to create directory %s: %v", dir, err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to create file %s: %v", fullPath, err)
		}
	}
}

// Cleanup performs test cleanup
func (ts *TestSetup) Cleanup() {
	// Cleanup handled by t.TempDir() automatically
}

// CreateTestFile creates a test file in the source directory
func (ts *TestSetup) CreateTestFile(path, content string) {
	fullPath := filepath.Join(ts.SrcDir, path)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		ts.T.Fatalf("Failed to create directory %s: %v", dir, err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		ts.T.Fatalf("Failed to create file %s: %v", fullPath, err)
	}
}

// CreateTestDir creates a test directory in the source directory
func (ts *TestSetup) CreateTestDir(path string) {
	fullPath := filepath.Join(ts.SrcDir, path)
	if err := os.MkdirAll(fullPath, 0755); err != nil {
		ts.T.Fatalf("Failed to create directory %s: %v", fullPath, err)
	}
}

// AssertFileExists checks if a file exists
func (ts *TestSetup) AssertFileExists(path string) {
	fullPath := filepath.Join(ts.SrcDir, path)
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		ts.T.Errorf("Expected file %s to exist", path)
	}
}

// AssertFileNotExists checks if a file does not exist
func (ts *TestSetup) AssertFileNotExists(path string) {
	fullPath := filepath.Join(ts.SrcDir, path)
	if _, err := os.Stat(fullPath); err == nil {
		ts.T.Errorf("Expected file %s to not exist", path)
	}
}

// AssertFileContent checks file content
func (ts *TestSetup) AssertFileContent(path, expectedContent string) {
	fullPath := filepath.Join(ts.SrcDir, path)
	content, err := os.ReadFile(fullPath)
	if err != nil {
		ts.T.Fatalf("Failed to read file %s: %v", fullPath, err)
	}
	if string(content) != expectedContent {
		ts.T.Errorf("File %s content mismatch. Expected: %s, Got: %s",
			path, expectedContent, string(content))
	}
}

// AssertXAttr checks extended attribute
func (ts *TestSetup) AssertXAttr(path string, expectedAttr ShadowXAttr) {
	fullPath := filepath.Join(ts.CacheDir, path)
	var attr ShadowXAttr
	exists, errno := GetShadowXAttr(fullPath, &attr)
	if errno != 0 {
		ts.T.Fatalf("Failed to get xattr for %s: %v", fullPath, errno)
	}
	if !exists {
		ts.T.Errorf("Expected xattr to exist for %s", fullPath)
		return
	}
	if attr.ShadowPathStatus != expectedAttr.ShadowPathStatus {
		ts.T.Errorf("XAttr status mismatch for %s. Expected: %v, Got: %v",
			fullPath, expectedAttr.ShadowPathStatus, attr.ShadowPathStatus)
	}
}

// MockContext provides a mock FUSE context
func MockContext() context.Context {
	return context.Background()
}

// MockEntryOut provides a mock FUSE entry output
func MockEntryOut() *fuse.EntryOut {
	return &fuse.EntryOut{}
}

// MockAttrOut provides a mock FUSE attribute output
func MockAttrOut() *fuse.AttrOut {
	return &fuse.AttrOut{}
}

// MockSetAttrIn provides a mock FUSE set attribute input
func MockSetAttrIn() *fuse.SetAttrIn {
	return &fuse.SetAttrIn{}
}

// TestFileHandle provides a mock file handle for testing
type TestFileHandle struct {
	content []byte
	pos     int64
}

func (tfh *TestFileHandle) Read(ctx context.Context, dest []byte) (uint32, syscall.Errno) {
	if tfh.pos >= int64(len(tfh.content)) {
		return 0, 0 // EOF
	}

	n := copy(dest, tfh.content[tfh.pos:])
	tfh.pos += int64(n)
	return uint32(n), fs.OK
}

func (tfh *TestFileHandle) Write(ctx context.Context, data []byte) (uint32, syscall.Errno) {
	tfh.content = append(tfh.content, data...)
	return uint32(len(data)), fs.OK
}

func (tfh *TestFileHandle) Getattr(ctx context.Context, out *fuse.AttrOut) syscall.Errno {
	out.Size = uint64(len(tfh.content))
	return fs.OK
}

func (tfh *TestFileHandle) Setattr(ctx context.Context, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if sz, ok := in.GetSize(); ok {
		if sz < uint64(len(tfh.content)) {
			tfh.content = tfh.content[:sz]
		} else {
			padding := make([]byte, sz-uint64(len(tfh.content)))
			tfh.content = append(tfh.content, padding...)
		}
	}
	return tfh.Getattr(ctx, out)
}

func (tfh *TestFileHandle) Release(ctx context.Context) syscall.Errno {
	return fs.OK
}

// NewTestFileHandle creates a new test file handle
func NewTestFileHandle(content string) *TestFileHandle {
	return &TestFileHandle{
		content: []byte(content),
		pos:     0,
	}
}
