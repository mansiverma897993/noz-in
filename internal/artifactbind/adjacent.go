// Package artifactbind verifies exact adjacent artifacts named by migration
// evidence without depending on a source or target schema.
package artifactbind

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
)

// ValidateAdjacent verifies a portable, regular artifact next to reportPath.
func ValidateAdjacent(
	reportPath string,
	binding *reporttypes.ArtifactBinding,
	suffix string,
	maxSize int64,
) error {
	_, err := ReadAdjacent(reportPath, binding, suffix, maxSize)
	return err
}

// ReadAdjacent opens and verifies a portable artifact through a directory
// handle, returning the exact bytes that were hashed. Callers never need to
// reopen a path after validation.
func ReadAdjacent(
	reportPath string,
	binding *reporttypes.ArtifactBinding,
	suffix string,
	maxSize int64,
) ([]byte, error) {
	if binding == nil {
		return nil, fmt.Errorf("report has no primary artifact binding; rerun migration")
	}
	if binding.Path == "" || binding.Path == "." || filepath.IsAbs(binding.Path) ||
		filepath.Base(binding.Path) != binding.Path || strings.ContainsAny(binding.Path, `/\`) {
		return nil, fmt.Errorf("primary artifact path %q is not a portable adjacent filename", binding.Path)
	}
	if suffix != "" && !strings.HasSuffix(binding.Path, suffix) {
		return nil, fmt.Errorf("primary artifact path %q does not end in %q", binding.Path, suffix)
	}
	if binding.SizeBytes <= 0 || binding.SizeBytes > maxSize {
		return nil, fmt.Errorf("primary artifact size %d is outside the supported range", binding.SizeBytes)
	}
	directory := filepath.Dir(reportPath)
	path := filepath.Join(directory, binding.Path)
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("open primary artifact directory %q: %w", directory, err)
	}
	defer func() { _ = root.Close() }()
	before, err := root.Lstat(binding.Path)
	if err != nil {
		return nil, fmt.Errorf("inspect primary artifact %q: %w", path, err)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("primary artifact %q is not a regular file", path)
	}
	file, err := root.Open(binding.Path)
	if err != nil {
		return nil, fmt.Errorf("open primary artifact %q: %w", path, err)
	}
	after, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect opened primary artifact %q: %w", path, err)
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		_ = file.Close()
		return nil, fmt.Errorf("primary artifact %q changed while it was opened", path)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxSize+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read primary artifact %q: %w", path, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close primary artifact %q: %w", path, closeErr)
	}
	if int64(len(data)) != binding.SizeBytes {
		return nil, fmt.Errorf(
			"primary artifact %q size %d does not match report size %d",
			path, len(data), binding.SizeBytes,
		)
	}
	digest := sha256.Sum256(data)
	actual := fmt.Sprintf("%x", digest[:])
	if actual != binding.SHA256 {
		return nil, fmt.Errorf(
			"primary artifact %q SHA-256 %q does not match report SHA-256 %q",
			path, actual, binding.SHA256,
		)
	}
	return data, nil
}
