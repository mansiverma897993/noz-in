//go:build !windows

package atomicfile

import "os"

func syncRootReplacement(*os.Root, string) error {
	// The caller flushes its already-opened directory after all replacements.
	return nil
}
