//go:build windows

package atomicfile

import "os"

// SyncOpenedDirectory is a no-op on Windows, where Go cannot flush a directory
// handle. The published file itself is flushed before the rename.
func SyncOpenedDirectory(*os.File) error {
	return nil
}
