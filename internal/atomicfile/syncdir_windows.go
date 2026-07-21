//go:build windows

// Package atomicfile contains the platform-specific durability step required
// after atomically replacing a file.
package atomicfile

// SyncDirectory is a no-op on Windows, where Go cannot fsync a directory
// handle. The replaced file is still flushed before it is published.
func SyncDirectory(string) error {
	return nil
}
