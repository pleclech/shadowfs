//go:build !linux
// +build !linux

package rootinit

import "fmt"

// IsMountPointActive checks if a mount point is currently mounted
// This is a stub implementation for non-Linux platforms
func IsMountPointActive(mountPoint string) (bool, error) {
	return false, fmt.Errorf("mount detection not implemented on this platform")
}
