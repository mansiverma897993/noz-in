//go:build !windows

package atomicfile

import "os"

// SyncOpenedDirectory persists entry changes through an already-opened,
// descriptor-pinned directory.
func SyncOpenedDirectory(directory *os.File) error {
	return directory.Sync()
}
