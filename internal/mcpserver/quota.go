package mcpserver

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sync"
)

const (
	// DefaultMaxOutputEntries bounds retained MCP files and directories. Every
	// migration generation and validation run counts; the server never evicts
	// an existing entry to make room.
	DefaultMaxOutputEntries = 10_000
	// DefaultMaxOutputBytes bounds the logical size of all regular files under
	// the MCP output root. Logical size is deliberately conservative for hard
	// links and sparse files.
	DefaultMaxOutputBytes int64 = 10 << 30
)

type outputQuota struct {
	mu         sync.Mutex
	maxEntries int64
	maxBytes   int64
}

type outputUsage struct {
	entries int64
	bytes   int64
}

func newOutputQuota(maxEntries int, maxBytes int64) (*outputQuota, error) {
	if maxEntries < 0 {
		return nil, configErrorf("MCP max output entries must be zero (default) or greater")
	}
	if maxBytes < 0 {
		return nil, configErrorf("MCP max output bytes must be zero (default) or greater")
	}
	if maxEntries == 0 {
		maxEntries = DefaultMaxOutputEntries
	}
	if maxBytes == 0 {
		maxBytes = DefaultMaxOutputBytes
	}
	return &outputQuota{maxEntries: int64(maxEntries), maxBytes: maxBytes}, nil
}

// reserveOutputQuota serializes output admission within this process, measures
// the pinned output tree without following symlinks, and holds the admission
// lock until the caller finishes its mutation. The returned release function
// must be called even if that mutation fails.
func (service *Service) reserveOutputQuota(additionalEntries int64, additionalBytes int64) (func(), error) {
	if additionalEntries < 0 || additionalBytes < 0 {
		return nil, fmt.Errorf("invalid negative MCP output quota reservation")
	}
	if service.outputQuota == nil {
		return nil, fmt.Errorf("MCP output quota is not initialized")
	}
	service.outputQuota.mu.Lock()
	usage, err := service.measureOutputUsage()
	if err != nil {
		service.outputQuota.mu.Unlock()
		return nil, fmt.Errorf("measure MCP output quota: %w", err)
	}
	if exceedsQuota(usage.entries, additionalEntries, service.outputQuota.maxEntries) {
		service.outputQuota.mu.Unlock()
		return nil, fmt.Errorf(
			"MCP output entry quota would be exceeded (current %d, requested %d, limit %d); archive or remove artifacts explicitly before retrying",
			usage.entries,
			additionalEntries,
			service.outputQuota.maxEntries,
		)
	}
	if exceedsQuota(usage.bytes, additionalBytes, service.outputQuota.maxBytes) {
		service.outputQuota.mu.Unlock()
		return nil, fmt.Errorf(
			"MCP output byte quota would be exceeded (current %d, requested %d, limit %d); archive or remove artifacts explicitly before retrying",
			usage.bytes,
			additionalBytes,
			service.outputQuota.maxBytes,
		)
	}
	return service.outputQuota.mu.Unlock, nil
}

func exceedsQuota(current, additional, limit int64) bool {
	return current > limit || additional > limit-current
}

func (service *Service) measureOutputUsage() (outputUsage, error) {
	root, err := openVerifiedRoot(service.config.OutputRoot, service.outputRootInfo)
	if err != nil {
		return outputUsage{}, err
	}
	defer func() { _ = root.Close() }()
	return measureRootedDirectory(root, ".")
}

func measureRootedDirectory(root *os.Root, displayPath string) (outputUsage, error) {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return outputUsage{}, fmt.Errorf("list output directory %q: %w", displayPath, err)
	}
	usage := outputUsage{}
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(displayPath, name)
		info, err := root.Lstat(name)
		if err != nil {
			return outputUsage{}, fmt.Errorf("inspect output entry %q: %w", path, err)
		}
		usage.entries++
		if info.Mode()&os.ModeSymlink != 0 {
			return outputUsage{}, fmt.Errorf("refuse symbolic link in MCP output accounting at %q", path)
		}
		switch {
		case info.IsDir():
			child, err := root.OpenRoot(name)
			if err != nil {
				return outputUsage{}, fmt.Errorf("open output directory %q: %w", path, err)
			}
			openedInfo, statErr := child.Stat(".")
			if statErr != nil {
				_ = child.Close()
				return outputUsage{}, fmt.Errorf("inspect opened output directory %q: %w", path, statErr)
			}
			if !openedInfo.IsDir() || !os.SameFile(info, openedInfo) {
				_ = child.Close()
				return outputUsage{}, fmt.Errorf("output directory %q changed during quota accounting", path)
			}
			childUsage, walkErr := measureRootedDirectory(child, path)
			closeErr := child.Close()
			if err := errors.Join(walkErr, closeErr); err != nil {
				return outputUsage{}, err
			}
			currentInfo, err := root.Lstat(name)
			if err != nil || !currentInfo.IsDir() || !os.SameFile(info, currentInfo) {
				return outputUsage{}, fmt.Errorf("output directory %q changed during quota accounting", path)
			}
			if usage.entries > math.MaxInt64-childUsage.entries || usage.bytes > math.MaxInt64-childUsage.bytes {
				return outputUsage{}, fmt.Errorf("MCP output usage overflow while accounting %q", path)
			}
			usage.entries += childUsage.entries
			usage.bytes += childUsage.bytes
		case info.Mode().IsRegular():
			file, err := root.Open(name)
			if err != nil {
				return outputUsage{}, fmt.Errorf("open output artifact %q: %w", path, err)
			}
			openedInfo, statErr := file.Stat()
			closeErr := file.Close()
			if err := errors.Join(statErr, closeErr); err != nil {
				return outputUsage{}, fmt.Errorf("inspect output artifact %q: %w", path, err)
			}
			if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) || openedInfo.Size() != info.Size() {
				return outputUsage{}, fmt.Errorf("output artifact %q changed during quota accounting", path)
			}
			currentInfo, err := root.Lstat(name)
			if err != nil || !currentInfo.Mode().IsRegular() || !os.SameFile(info, currentInfo) || currentInfo.Size() != info.Size() {
				return outputUsage{}, fmt.Errorf("output artifact %q changed during quota accounting", path)
			}
			if info.Size() < 0 || usage.bytes > math.MaxInt64-info.Size() {
				return outputUsage{}, fmt.Errorf("MCP output byte usage overflow while accounting %q", path)
			}
			usage.bytes += info.Size()
		default:
			return outputUsage{}, fmt.Errorf("refuse non-regular MCP output entry %q during quota accounting", path)
		}
	}
	return usage, nil
}
