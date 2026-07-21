package atomicfile

import "os"

// ReplaceRoot atomically replaces destination with source inside an already
// pinned filesystem root. Root.Rename uses replace-if-exists semantics on every
// supported platform, including Windows, without resolving the root path again.
// The destination is flushed on platforms that need an additional rooted file
// flush; callers remain responsible for persisting the containing directory.
func ReplaceRoot(root *os.Root, source, destination string) error {
	if err := root.Rename(source, destination); err != nil {
		return err
	}
	return syncRootReplacement(root, destination)
}
