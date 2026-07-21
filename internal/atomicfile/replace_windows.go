//go:build windows

package atomicfile

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// Replace publishes source at destination with replacement and write-through
// semantics. os.Rename does not replace an existing destination on Windows.
func Replace(source, destination string) error {
	sourceUTF16, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return fmt.Errorf("encode replacement source path: %w", err)
	}
	destinationUTF16, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return fmt.Errorf("encode replacement destination path: %w", err)
	}
	if err := windows.MoveFileEx(
		sourceUTF16,
		destinationUTF16,
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	); err != nil {
		return fmt.Errorf("replace destination: %w", err)
	}
	return nil
}
