package ipc

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Command constants
const (
	CmdMarkDirty   = "MARK_DIRTY"
	CmdRefresh     = "REFRESH"
	CmdRestoreFile = "RESTORE_FILE"
)

// Response constants
const (
	RespOK    = "OK"
	RespError = "ERROR"
)

// ControlServer handles IPC commands from CLI processes
type ControlServer struct {
	listener net.Listener
	root     interface {
		MarkDirty(filePath string)
		Refresh(path string)
		RestoreFile(path string)
	} // Interface to avoid circular dependency
	socketPath string
	wg         sync.WaitGroup
	mu         sync.RWMutex
	closed     bool
}

// NewControlServer creates a new IPC control server
func NewControlServer(socketPath string, root interface {
	MarkDirty(filePath string)
	Refresh(path string)
	RestoreFile(path string)
}) (*ControlServer, error) {
	listener, err := listenSocket(socketPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create IPC listener: %w", err)
	}

	return &ControlServer{
		listener:   listener,
		root:       root,
		socketPath: socketPath,
	}, nil
}

// Start starts the IPC server in a goroutine
func (s *ControlServer) Start() {
	s.wg.Add(1)
	go s.acceptLoop()
}

// Stop stops the IPC server and removes the socket file
func (s *ControlServer) Stop() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}

	s.closed = true
	listener := s.listener
	s.mu.Unlock()

	// Close listener outside of lock to avoid blocking
	if err := listener.Close(); err != nil {
		// Continue even if close fails
	}

	// Wait for acceptLoop to finish (with timeout to avoid hanging)
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// acceptLoop finished
	case <-time.After(2 * time.Second):
		// Timeout - acceptLoop didn't finish in time, continue anyway
	}

	// Remove socket file
	if err := removeSocketFile(s.socketPath); err != nil {
		return fmt.Errorf("failed to remove socket file: %w", err)
	}

	return nil
}

// acceptLoop accepts incoming connections
func (s *ControlServer) acceptLoop() {
	defer s.wg.Done()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			s.mu.RLock()
			closed := s.closed
			s.mu.RUnlock()
			if closed {
				return
			}
			continue
		}

		s.wg.Add(1)
		go s.handleConnection(conn)
	}
}

// handleConnection handles a single client connection
func (s *ControlServer) handleConnection(conn net.Conn) {
	defer conn.Close()
	defer s.wg.Done()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		response := s.handleCommand(line)
		if _, err := fmt.Fprintf(conn, "%s\n", response); err != nil {
			return
		}
	}
}

// handleCommand processes a single command
func (s *ControlServer) handleCommand(cmdLine string) string {
	parts := strings.Fields(cmdLine)
	if len(parts) == 0 {
		return fmt.Sprintf("%s empty command", RespError)
	}

	cmd := parts[0]
	args := parts[1:]

	switch cmd {
	case CmdMarkDirty:
		if len(args) != 1 {
			return fmt.Sprintf("%s MARK_DIRTY requires one argument", RespError)
		}
		mountPointPath := args[0]
		s.root.MarkDirty(mountPointPath)
		return RespOK

	case CmdRefresh:
		if len(args) != 1 {
			return fmt.Sprintf("%s REFRESH requires one argument", RespError)
		}
		mountPointPath := args[0]
		s.root.Refresh(mountPointPath)
		return RespOK

	case CmdRestoreFile:
		if len(args) != 1 {
			return fmt.Sprintf("%s RESTORE_FILE requires one argument", RespError)
		}
		mountPointPath := args[0]
		s.root.RestoreFile(mountPointPath)
		return RespOK

	default:
		return fmt.Sprintf("%s unknown command: %s", RespError, cmd)
	}
}

// ControlClient sends IPC commands to the FUSE server
type ControlClient struct {
	conn net.Conn
	mu   sync.Mutex
}

// NewControlClient creates a new IPC client and connects to the server
func NewControlClient(socketPath string) (*ControlClient, error) {
	conn, err := dialSocket(socketPath)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to IPC server: %w", err)
	}

	return &ControlClient{
		conn: conn,
	}, nil
}

// MarkDirty sends a MARK_DIRTY command to mark a file as dirty
func (c *ControlClient) MarkDirty(path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	cmd := fmt.Sprintf("%s %s\n", CmdMarkDirty, path)
	if _, err := c.conn.Write([]byte(cmd)); err != nil {
		return fmt.Errorf("failed to send MARK_DIRTY command: %w", err)
	}

	// Read response
	scanner := bufio.NewScanner(c.conn)
	if !scanner.Scan() {
		return fmt.Errorf("failed to read response")
	}

	response := strings.TrimSpace(scanner.Text())
	if response != RespOK {
		return fmt.Errorf("server returned error: %s", response)
	}

	return nil
}

// Refresh sends a REFRESH command to invalidate FUSE cache for a path
func (c *ControlClient) Refresh(path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	cmd := fmt.Sprintf("%s %s\n", CmdRefresh, path)
	if _, err := c.conn.Write([]byte(cmd)); err != nil {
		return fmt.Errorf("failed to send REFRESH command: %w", err)
	}

	// Read response
	scanner := bufio.NewScanner(c.conn)
	if !scanner.Scan() {
		return fmt.Errorf("failed to read response")
	}

	response := strings.TrimSpace(scanner.Text())
	if response != RespOK {
		return fmt.Errorf("server returned error: %s", response)
	}

	return nil
}

// RestoreFile sends a RESTORE_FILE command to restore a file and invalidate directory entry
// This is used when a file is restored from git - it marks the file as dirty AND
// invalidates the parent directory entry so the kernel knows about the new file
func (c *ControlClient) RestoreFile(path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	cmd := fmt.Sprintf("%s %s\n", CmdRestoreFile, path)
	if _, err := c.conn.Write([]byte(cmd)); err != nil {
		return fmt.Errorf("failed to send RESTORE_FILE command: %w", err)
	}

	// Read response
	scanner := bufio.NewScanner(c.conn)
	if !scanner.Scan() {
		return fmt.Errorf("failed to read response")
	}

	response := strings.TrimSpace(scanner.Text())
	if response != RespOK {
		return fmt.Errorf("server returned error: %s", response)
	}

	return nil
}

// Close closes the IPC client connection
func (c *ControlClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}

// SocketExists checks if a socket exists and is accessible
// Handles both regular filesystem sockets and abstract Unix sockets
func SocketExists(socketPath string) bool {
	// For abstract sockets (Linux), we can't stat them, just try to connect
	// Abstract sockets use @ prefix and just the socket name (mountID), not the full path
	// Note: getAbstractSocketName is defined in ipc_unix.go (platform-specific)
	if len(socketPath) > 100 {
		// Extract socket name from path (mountID from mountID.sock)
		// This will use the platform-specific implementation from ipc_unix.go
		filename := filepath.Base(socketPath)
		socketName := filename
		if strings.HasSuffix(filename, ".sock") {
			socketName = strings.TrimSuffix(filename, ".sock")
		}
		abstractPath := "@" + socketName
		conn, err := net.Dial("unix", abstractPath)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}

	// For regular filesystem sockets, check if file exists first
	if _, err := os.Stat(socketPath); err != nil {
		return false
	}

	// Try to connect to verify the socket is active
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
