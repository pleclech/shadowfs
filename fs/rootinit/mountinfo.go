//go:build !linux
// +build !linux

package rootinit

import (
	"fmt"
)

// FindSourceFromMounts attempts to find source directory from system mount information
// On non-Linux platforms, this returns an error indicating platform-specific implementation is needed
func FindSourceFromMounts(mountPoint string) (string, error) {
	return "", fmt.Errorf("cannot determine source directory automatically on this platform, please specify --source-dir")
}

