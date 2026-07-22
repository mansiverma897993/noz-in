package artifactset

// This file contains low-level rooted filesystem helpers shared by the
// publication, read, and pruning paths.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mansiverma897993/noz-in/internal/atomicfile"
)

func readRealDirectoryBounded(root *os.Root, path string, maxEntries int) ([]os.DirEntry, error) {
	if maxEntries < 0 {
		return nil, fmt.Errorf("maximum directory entry count must not be negative")
	}
	before, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() {
		return nil, fmt.Errorf("path is not a real directory")
	}
	directory, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	after, err := directory.Stat()
	if err != nil || !after.IsDir() || !os.SameFile(before, after) {
		_ = directory.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("directory changed while it was opened")
	}
	entries, readErr := directory.ReadDir(maxEntries + 1)
	closeErr := directory.Close()
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	if len(entries) > maxEntries {
		return nil, fmt.Errorf("directory contains more than %d entries", maxEntries)
	}
	return entries, nil
}

func syncRootDirectory(root *os.Root, name string, description string) error {
	directory, err := root.Open(name)
	if err != nil {
		return fmt.Errorf("open %s: %w", description, err)
	}
	syncErr := atomicfile.SyncOpenedDirectory(directory)
	closeErr := directory.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("persist %s: %w", description, err)
	}
	return nil
}

func writeStageFile(root *os.Root, path string, data []byte) error {
	file, err := root.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create staged artifact %q: %w", filepath.Base(path), err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write staged artifact %q: %w", filepath.Base(path), err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync staged artifact %q: %w", filepath.Base(path), err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close staged artifact %q: %w", filepath.Base(path), err)
	}
	if err := root.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("set staged artifact permissions %q: %w", filepath.Base(path), err)
	}
	return nil
}

func readRegular(root *os.Root, path string, limit int64) ([]byte, error) {
	info, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("path is not a regular file")
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(info, after) {
		return nil, fmt.Errorf("file changed while it was opened")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return data, nil
}

func removeRegularIfPresent(root *os.Root, path string) (bool, error) {
	info, err := root.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect stale artifact %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("refuse to remove stale artifact %q: path is not a regular file", path)
	}
	if err := root.Remove(path); err != nil {
		return false, fmt.Errorf("remove stale artifact %q: %w", path, err)
	}
	return true, nil
}
