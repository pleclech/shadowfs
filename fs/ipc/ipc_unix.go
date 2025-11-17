//go:build !windows

package ipc

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// isAbstractSocket checks if a path should use abstract Unix sockets
// Abstract sockets use @ prefix and don't have filesystem path length limits
func isAbstractSocket(path string) bool {
	// Use abstract socket if path would exceed Unix socket path limit (~108 chars)
	// Abstract sockets are Linux-specific but solve the path length problem
	return len(path) > 100
}

// getAbstractSocketName extracts the socket name from a filesystem path for abstract sockets
// Abstract sockets use just the filename (mountID.sock) without the directory path
func getAbstractSocketName(path string) string {
	// Extract just the filename (e.g., "mountID.sock")
	filename := filepath.Base(path)
	// Remove .sock extension to get just the mountID
	if strings.HasSuffix(filename, ".sock") {
		return strings.TrimSuffix(filename, ".sock")
	}
	return filename
}

// listenSocket creates a Unix socket listener
// Uses abstract Unix sockets on Linux if path is too long (>100 chars)
func listenSocket(path string) (net.Listener, error) {
	// Check if we should use abstract socket (Linux-specific)
	if isAbstractSocket(path) {
		// Use abstract Unix socket (Linux-specific, no path length limit)
		// Abstract sockets use @ prefix and just the socket name (mountID), not the full path
		socketName := getAbstractSocketName(path)
		abstractPath := "@" + socketName
		listener, err := net.Listen("unix", abstractPath)
		if err != nil {
			return nil, err
		}
		return listener, nil
	}

	// Use regular filesystem socket
	// Remove existing socket file if it exists (handles stale sockets from crashes)
	if err := syscall.Unlink(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}

	// Set socket file permissions (0600 - owner read/write only)
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		syscall.Unlink(path)
		return nil, err
	}

	return listener, nil
}

// dialSocket connects to a Unix socket
// Handles both regular and abstract sockets
func dialSocket(path string) (net.Conn, error) {
	// Check if we should use abstract socket
	if isAbstractSocket(path) {
		// Use abstract socket with just the socket name (mountID)
		socketName := getAbstractSocketName(path)
		abstractPath := "@" + socketName
		return net.Dial("unix", abstractPath)
	}
	return net.Dial("unix", path)
}

// removeSocketFile removes the socket file (only for filesystem sockets)
func removeSocketFile(path string) error {
	// Abstract sockets don't have filesystem representation, nothing to remove
	if isAbstractSocket(path) {
		return nil
	}
	return syscall.Unlink(path)
}

