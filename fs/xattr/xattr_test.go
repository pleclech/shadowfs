package xattr

import (
	"testing"

	tu "github.com/pleclech/shadowfs/fs/utils/testings"
)

func TestXAttr_ToBytes_FromBytes(t *testing.T) {
	attr := XAttr{
		PathStatus: PathStatusDeleted,
	}

	// Convert to bytes
	bytes := ToBytes(&attr)
	if len(bytes) != Size {
		tu.Failf(t, "Expected bytes length %d, got %d", Size, len(bytes))
	}

	// Convert back from bytes
	attr2 := FromBytes(bytes)
	if attr2.PathStatus != PathStatusDeleted {
		tu.Failf(t, "Expected PathStatus %d, got %d", PathStatusDeleted, attr2.PathStatus)
	}
}

func TestXAttr_FromBytes_Short(t *testing.T) {
	// Test with short byte slice
	shortBytes := []byte{0, 1}
	attr := FromBytes(shortBytes)

	// Should return zero-initialized struct
	if attr.PathStatus != PathStatusNone {
		tu.Failf(t, "Expected PathStatus %d for short bytes, got %d", PathStatusNone, attr.PathStatus)
	}
}

func TestIsPathDeleted(t *testing.T) {
	tests := []struct {
		name     string
		attr     XAttr
		expected bool
	}{
		{
			name:     "deleted path",
			attr:     XAttr{PathStatus: PathStatusDeleted},
			expected: true,
		},
		{
			name:     "none status",
			attr:     XAttr{PathStatus: PathStatusNone},
			expected: false,
		},
		{
			name:     "zero value",
			attr:     XAttr{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsPathDeleted(tt.attr)
			if result != tt.expected {
				tu.Failf(
					t, "IsPathDeleted() = %v, expected %v", result, tt.expected)
			}
		})
	}
}
