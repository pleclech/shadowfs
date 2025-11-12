package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/hanwen/go-fuse/v2/fs"

	"github.com/pleclech/shadowfs/fs/xattr"
)

// BenchmarkPathRebasing compares old vs new path rebasing performance
func BenchmarkPathRebasing(b *testing.B) {
	// Setup test data
	srcDir := "/test/source"
	cacheDir := "/test/cache"

	// Create test paths
	paths := make([]string, 1000)
	for i := 0; i < 1000; i++ {
		paths[i] = fmt.Sprintf("/test/source/dir%d/subdir%d/file%d.txt", i%10, i%5, i)
	}

	b.Run("Original", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			path := paths[i%len(paths)]
			// Original implementation
			if strings.HasPrefix(path, srcDir) {
				relPath := strings.TrimPrefix(path, srcDir)
				if relPath == "" {
					_ = cacheDir
				} else {
					relPath = strings.TrimPrefix(relPath, "/")
					_ = filepath.Join(cacheDir, relPath)
				}
			}
		}
	})

	b.Run("Optimized", func(b *testing.B) {
		pm := NewPathManager(srcDir, cacheDir)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			path := paths[i%len(paths)]
			_ = pm.RebaseToCache(path)
		}
	})
}

// BenchmarkStatOperations compares cached vs uncached stat performance
func BenchmarkStatOperations(b *testing.B) {
	// Setup test files
	tempDir := b.TempDir()
	testFile := filepath.Join(tempDir, "test.txt")

	// Create test file
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		b.Fatal(err)
	}

	b.Run("DirectSyscall", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var st syscall.Stat_t
			_ = syscall.Lstat(testFile, &st)
		}
	})

}

// BenchmarkFileCopy compares different buffer sizes for file copying
func BenchmarkFileCopy(b *testing.B) {
	// Create test file with content
	tempDir := b.TempDir()
	srcFile := filepath.Join(tempDir, "src.txt")
	dstFile := filepath.Join(tempDir, "dst.txt")

	// Create 1MB test file
	content := make([]byte, 1024*1024)
	for i := range content {
		content[i] = byte(i % 256)
	}

	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		b.Fatal(err)
	}

	b.Run("4KB_Buffer", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			srcFd, err := syscall.Open(srcFile, syscall.O_RDONLY, 0)
			if err != nil {
				b.Fatal(err)
			}
			defer syscall.Close(srcFd)

			dstFd, err := syscall.Open(dstFile, syscall.O_CREAT|syscall.O_WRONLY|syscall.O_TRUNC, 0644)
			if err != nil {
				b.Fatal(err)
			}
			defer syscall.Close(dstFd)

			buf := make([]byte, 4096) // 4KB buffer
			for {
				n, err := syscall.Read(srcFd, buf)
				if err != nil && err != syscall.EINTR && err != syscall.EAGAIN {
					break
				}
				if n == 0 {
					break
				}

				offset := 0
				for offset < n {
					written, err := syscall.Write(dstFd, buf[offset:n])
					if err != nil {
						break
					}
					offset += written
				}
			}
		}
	})

	b.Run("64KB_Buffer", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			srcFd, err := syscall.Open(srcFile, syscall.O_RDONLY, 0)
			if err != nil {
				b.Fatal(err)
			}
			defer syscall.Close(srcFd)

			dstFd, err := syscall.Open(dstFile, syscall.O_CREAT|syscall.O_WRONLY|syscall.O_TRUNC, 0644)
			if err != nil {
				b.Fatal(err)
			}
			defer syscall.Close(dstFd)

			buf := make([]byte, 64*1024) // 64KB buffer
			for {
				n, err := syscall.Read(srcFd, buf)
				if err != nil && err != syscall.EINTR && err != syscall.EAGAIN {
					break
				}
				if n == 0 {
					break
				}

				offset := 0
				for offset < n {
					written, err := syscall.Write(dstFd, buf[offset:n])
					if err != nil {
						break
					}
					offset += written
				}
			}
		}
	})
}

// BenchmarkXattrOperations compares original vs helper methods
func BenchmarkXattrOperations(b *testing.B) {
	// Setup test file
	tempDir := b.TempDir()
	testFile := filepath.Join(tempDir, "test.txt")

	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		b.Fatal(err)
	}

	// Set initial xattr
	attr := xattr.XAttr{PathStatus: xattr.PathStatusNone}
	if errno := xattr.Set(testFile, &attr); errno != 0 {
		b.Fatal("Failed to set initial xattr")
	}

	b.Run("Original", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			attr := xattr.XAttr{}
			exists, errno := xattr.Get(testFile, &attr)
			if errno == 0 && exists {
				_ = xattr.IsPathDeleted(attr)
			}
		}
	})

	b.Run("WithHelper", func(b *testing.B) {
		// Create a mock ShadowNode for testing
		rootData := &fs.LoopbackRoot{Path: tempDir}
		node := &ShadowNode{
			LoopbackNode: fs.LoopbackNode{RootData: rootData},
			srcDir:       tempDir,
			cachePath:    tempDir,
		}
		helpers := NewShadowNodeHelpers(node)

		for i := 0; i < b.N; i++ {
			_, _ = helpers.CheckFileDeleted(testFile)
		}
	})
}

// BenchmarkMemoryUsage measures memory allocation patterns
func BenchmarkMemoryUsage(b *testing.B) {
	paths := make([]string, 1000)
	for i := 0; i < 1000; i++ {
		paths[i] = fmt.Sprintf("/test/dir%d/subdir%d/file%d.txt", i%10, i%5, i)
	}

	b.Run("PathManager", func(b *testing.B) {
		var m1, m2 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m1)

		pm := NewPathManager("/test", "/cache")
		for i := 0; i < b.N; i++ {
			path := paths[i%len(paths)]
			pm.RebaseToCache(path)
			pm.RebaseToSource(path)
		}

		runtime.GC()
		runtime.ReadMemStats(&m2)
		b.ReportMetric(float64(m2.Alloc-m1.Alloc), "bytes")
	})

	b.Run("DirectStringOps", func(b *testing.B) {
		var m1, m2 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m1)

		for i := 0; i < b.N; i++ {
			path := paths[i%len(paths)]
			// Direct string operations
			if strings.HasPrefix(path, "/test") {
				relPath := strings.TrimPrefix(path, "/test")
				if relPath != "" && strings.HasPrefix(relPath, "/") {
					relPath = relPath[1:]
				}
				_ = "/cache/" + relPath
			}
		}

		runtime.GC()
		runtime.ReadMemStats(&m2)
		b.ReportMetric(float64(m2.Alloc-m1.Alloc), "bytes")
	})
}

// BenchmarkConcurrentOperations tests performance under concurrent load
func BenchmarkConcurrentOperations(b *testing.B) {
	tempDir := b.TempDir()

	// Create test files
	for i := 0; i < 100; i++ {
		file := filepath.Join(tempDir, fmt.Sprintf("file%d.txt", i))
		if err := os.WriteFile(file, []byte(fmt.Sprintf("content %d", i)), 0644); err != nil {
			b.Fatal(err)
		}
	}

	b.Run("ConcurrentPathRebase", func(b *testing.B) {
		pm := NewPathManager(tempDir, tempDir+"/cache")
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				path := fmt.Sprintf("%s/file%d.txt", tempDir, i%100)
				_ = pm.RebaseToCache(path)
				i++
			}
		})
	})
}
