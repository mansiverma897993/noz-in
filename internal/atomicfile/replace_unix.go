//go:build !windows

package atomicfile

import "os"

// Replace atomically publishes source at destination on the same filesystem.
// Callers must fsync source before calling Replace and SyncDirectory after it.
func Replace(source, destination string) error {
	return os.Rename(source, destination)
}
