package artifactset

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type keyedMutex struct {
	mutex sync.Mutex
	refs  int
}

var lockRegistry = struct {
	sync.Mutex
	locks map[string]*keyedMutex
}{locks: make(map[string]*keyedMutex)}

type setLock struct {
	file         *os.File
	unlockNative func() error
	local        *keyedMutex
	key          string
	closed       bool
}

func acquireRooted(root *os.Root, directoryPath, name string) (*setLock, error) {
	if root == nil {
		return nil, fmt.Errorf("artifact root is nil")
	}
	absolute, err := filepath.Abs(filepath.Join(directoryPath, name))
	if err != nil {
		return nil, fmt.Errorf("resolve artifact-set lock path %q: %w", name, err)
	}
	key := filepath.Clean(absolute)
	lockRegistry.Lock()
	local := lockRegistry.locks[key]
	if local == nil {
		local = &keyedMutex{}
		lockRegistry.locks[key] = local
	}
	local.refs++
	lockRegistry.Unlock()
	local.mutex.Lock()

	file, err := openRootedLockFile(root, name, key)
	if err != nil {
		releaseLocal(key, local)
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		_ = file.Close()
		releaseLocal(key, local)
		return nil, fmt.Errorf("inspect opened artifact-set lock %q: %w", key, err)
	}
	current, err := root.Lstat(name)
	if err != nil || !opened.Mode().IsRegular() || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		_ = file.Close()
		releaseLocal(key, local)
		if err != nil {
			return nil, fmt.Errorf("reinspect artifact-set lock %q: %w", key, err)
		}
		return nil, fmt.Errorf("artifact-set lock %q changed while it was opened", key)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		releaseLocal(key, local)
		return nil, fmt.Errorf("set artifact-set lock permissions %q: %w", key, err)
	}
	unlockNative, err := lockFile(file)
	if err != nil {
		_ = file.Close()
		releaseLocal(key, local)
		return nil, fmt.Errorf("lock artifact set %q: %w", key, err)
	}
	current, err = root.Lstat(name)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		_ = unlockNative()
		_ = file.Close()
		releaseLocal(key, local)
		if err != nil {
			return nil, fmt.Errorf("reinspect locked artifact set %q: %w", key, err)
		}
		return nil, fmt.Errorf("artifact-set lock %q changed while it was acquired", key)
	}
	return &setLock{file: file, unlockNative: unlockNative, local: local, key: key}, nil
}

func openRootedLockFile(root *os.Root, name, displayPath string) (*os.File, error) {
	for range 8 {
		info, err := root.Lstat(name)
		switch {
		case errors.Is(err, os.ErrNotExist):
			file, createErr := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
			if errors.Is(createErr, os.ErrExist) || errors.Is(createErr, os.ErrNotExist) {
				continue
			}
			if createErr != nil {
				return nil, fmt.Errorf("create artifact-set lock %q: %w", displayPath, createErr)
			}
			return file, nil
		case err != nil:
			return nil, fmt.Errorf("inspect artifact-set lock %q: %w", displayPath, err)
		case !info.Mode().IsRegular():
			return nil, fmt.Errorf("refuse artifact-set lock %q: existing path is not a regular file", displayPath)
		default:
			file, openErr := root.OpenFile(name, os.O_RDWR, 0)
			if errors.Is(openErr, os.ErrNotExist) {
				continue
			}
			if openErr != nil {
				return nil, fmt.Errorf("open artifact-set lock %q: %w", displayPath, openErr)
			}
			return file, nil
		}
	}
	return nil, fmt.Errorf("artifact-set lock %q changed repeatedly while it was opened", displayPath)
}

func (lock *setLock) verifyRoot(root *os.Root, name string) error {
	opened, err := lock.file.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened artifact-set lock: %w", err)
	}
	rooted, err := root.Lstat(name)
	if err != nil {
		return fmt.Errorf("inspect rooted artifact-set lock %q: %w", name, err)
	}
	if !rooted.Mode().IsRegular() || !os.SameFile(opened, rooted) {
		return fmt.Errorf("artifact-set lock %q is outside the pinned output directory", name)
	}
	return nil
}

func (lock *setLock) Close() error {
	if lock == nil || lock.closed {
		return nil
	}
	lock.closed = true
	unlockErr := lock.unlockNative()
	closeErr := lock.file.Close()
	releaseLocal(lock.key, lock.local)
	if unlockErr != nil {
		return fmt.Errorf("unlock artifact set: %w", unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close artifact-set lock: %w", closeErr)
	}
	return nil
}

func releaseLocal(key string, local *keyedMutex) {
	local.mutex.Unlock()
	lockRegistry.Lock()
	local.refs--
	if local.refs == 0 {
		delete(lockRegistry.locks, key)
	}
	lockRegistry.Unlock()
}
