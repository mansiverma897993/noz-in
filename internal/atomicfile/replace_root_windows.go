//go:build windows

package atomicfile

import (
	"errors"
	"fmt"
	"os"
)

func syncRootReplacement(root *os.Root, destination string) error {
	// os.Root.Rename is handle-relative and therefore retains confinement if
	// the presented directory path is swapped. Windows cannot flush that
	// unexported root directory handle, so flush the renamed file handle. This
	// is the strongest rooted persistence barrier available without reopening
	// root.Name(), which would reintroduce a path-substitution race.
	file, err := root.OpenFile(destination, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open rooted replacement %q for persistence: %w", destination, err)
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("persist rooted replacement %q: %w", destination, err)
	}
	return nil
}
