//go:build !windows

// Package atomicfile contains the platform-specific durability step required
// after atomically replacing a file.
package atomicfile

import (
	"errors"
	"fmt"
	"os"
)

// SyncDirectory persists a directory entry update after rename or removal.
func SyncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open artifact directory %q: %w", path, err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("sync artifact directory %q: %w", path, err)
	}
	return nil
}
