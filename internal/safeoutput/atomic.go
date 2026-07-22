package safeoutput

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mansiverma897993/noz-in/internal/atomicfile"
)

// WriteFileAtomic writes data through an identity-pinned parent directory and
// atomically replaces an absent or regular destination. It never re-resolves
// the parent path after pinning it, and persists the replacement before return.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	return writeFileAtomic(path, data, perm, nil)
}

func writeFileAtomic(
	path string,
	data []byte,
	perm os.FileMode,
	afterPin func(*PinnedDirectory) error,
) error {
	if perm&^os.FileMode(0o777) != 0 {
		return fmt.Errorf("write output file %q: unsupported permissions %v", path, perm)
	}
	destination := filepath.Base(path)
	if destination == "." || destination == string(filepath.Separator) || destination == "" {
		return fmt.Errorf("output destination %q has no filename", path)
	}
	directory, err := OpenDirectory(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("pin output parent for %q: %w", path, err)
	}
	defer func() {
		if directory != nil {
			_ = directory.Close()
		}
	}()
	if afterPin != nil {
		if err := afterPin(directory); err != nil {
			return fmt.Errorf("after pinning output parent for %q: %w", path, err)
		}
	}
	info, err := directory.root.Lstat(destination)
	if err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("refuse output destination %q: existing path is not a regular file", path)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect rooted output destination %q: %w", path, err)
	}

	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("generate temporary output name for %q: %w", path, err)
	}
	temporary := ".promcast-write-" + hex.EncodeToString(nonce[:])
	file, err := directory.root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return fmt.Errorf("create rooted temporary output for %q: %w", path, err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = directory.root.Remove(temporary)
		}
	}()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write rooted temporary output for %q: %w", path, err)
	}
	if err := file.Chmod(perm); err != nil {
		_ = file.Close()
		return fmt.Errorf("set rooted temporary output permissions for %q: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync rooted temporary output for %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close rooted temporary output for %q: %w", path, err)
	}
	if err := atomicfile.ReplaceRoot(directory.root, temporary, destination); err != nil {
		return fmt.Errorf("publish rooted output %q: %w", path, err)
	}
	cleanup = false
	if err := atomicfile.SyncOpenedDirectory(directory.file); err != nil {
		return fmt.Errorf("persist rooted output %q: %w", path, err)
	}
	if err := directory.Close(); err != nil {
		return fmt.Errorf("close pinned output parent for %q: %w", path, err)
	}
	directory = nil
	return nil
}
