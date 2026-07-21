package releasegate

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxArchiveEntries = 4096
	maxExpandedBytes  = 64 << 20
)

var requiredDirectories = [...]string{
	"corpus",
	"corpus/top",
	"corpus/mixin",
	"corpus-complex",
}

type entryKind uint8

const (
	entryRegular entryKind = iota + 1
	entryDirectory
)

type archiveEntry struct {
	kind entryKind
	size int64
}

// VerifyAndExtractCorpus validates the complete gzip-compressed tar archive
// before extracting its regular files into an existing empty directory.
func VerifyAndExtractCorpus(archivePath, destination string) error {
	entries, err := inspectCorpusArchive(archivePath)
	if err != nil {
		return err
	}
	if err := requireEmptyDirectory(destination); err != nil {
		return err
	}
	return extractCorpusArchive(archivePath, destination, entries)
}

func inspectCorpusArchive(archivePath string) (map[string]archiveEntry, error) {
	reader, closeReader, err := openTarGzip(archivePath)
	if err != nil {
		return nil, err
	}
	defer closeReader()

	entries := make(map[string]archiveEntry)
	var expandedBytes int64
	for entryCount := 1; ; entryCount++ {
		header, nextErr := reader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return nil, fmt.Errorf("read corpus archive header: %w", nextErr)
		}
		if entryCount > maxArchiveEntries {
			return nil, fmt.Errorf("corpus archive exceeds %d entries", maxArchiveEntries)
		}

		name, nameErr := validateEntryName(header.Name)
		if nameErr != nil {
			return nil, nameErr
		}
		if _, duplicate := entries[name]; duplicate {
			return nil, fmt.Errorf("corpus archive contains duplicate path %q", name)
		}

		switch header.Typeflag {
		case tar.TypeReg, byte(0):
			if name == "corpus" || name == "corpus-complex" {
				return nil, fmt.Errorf("corpus archive root %q must be a directory", name)
			}
			if header.Size > maxExpandedBytes-expandedBytes {
				return nil, fmt.Errorf("corpus archive expands beyond %d bytes", maxExpandedBytes)
			}
			expandedBytes += header.Size
			entries[name] = archiveEntry{kind: entryRegular, size: header.Size}
			copied, copyErr := io.Copy(io.Discard, reader)
			if copyErr != nil {
				return nil, fmt.Errorf("read corpus archive entry %q: %w", name, copyErr)
			}
			if copied != header.Size {
				return nil, fmt.Errorf("corpus archive entry %q has size %d, read %d", name, header.Size, copied)
			}
		case tar.TypeDir:
			if header.Size != 0 {
				return nil, fmt.Errorf("corpus archive directory %q has nonzero size", name)
			}
			entries[name] = archiveEntry{kind: entryDirectory}
		default:
			return nil, fmt.Errorf("corpus archive entry %q has unsupported type %d", name, header.Typeflag)
		}
	}

	if len(entries) == 0 {
		return nil, fmt.Errorf("corpus archive is empty")
	}
	for _, required := range requiredDirectories {
		if entries[required].kind != entryDirectory {
			return nil, fmt.Errorf("corpus archive is missing required directory %q", required)
		}
	}
	if err := rejectFileDirectoryConflicts(entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func validateEntryName(rawName string) (string, error) {
	name := strings.TrimSuffix(rawName, "/")
	if name == "" || !utf8.ValidString(name) || !fs.ValidPath(name) || path.Clean(name) != name {
		return "", fmt.Errorf("corpus archive contains unsafe path %q", rawName)
	}
	if strings.ContainsRune(name, '\\') || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("corpus archive contains unsafe path %q", rawName)
	}
	root, _, _ := strings.Cut(name, "/")
	if root != "corpus" && root != "corpus-complex" {
		return "", fmt.Errorf("corpus archive path %q is outside the allowed roots", rawName)
	}
	return name, nil
}

func rejectFileDirectoryConflicts(entries map[string]archiveEntry) error {
	for name := range entries {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if entries[parent].kind == entryRegular {
				return fmt.Errorf("corpus archive regular file %q is also used as a directory", parent)
			}
		}
	}
	return nil
}

func requireEmptyDirectory(destination string) error {
	info, err := os.Lstat(destination)
	if err != nil {
		return fmt.Errorf("inspect corpus extraction directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("corpus extraction destination is not a directory")
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		return fmt.Errorf("read corpus extraction directory: %w", err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("corpus extraction destination is not empty")
	}
	return nil
}

func extractCorpusArchive(archivePath, destination string, expected map[string]archiveEntry) error {
	reader, closeReader, err := openTarGzip(archivePath)
	if err != nil {
		return err
	}
	defer closeReader()

	seen := make(map[string]bool, len(expected))
	for {
		header, nextErr := reader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return fmt.Errorf("read corpus archive for extraction: %w", nextErr)
		}
		name, nameErr := validateEntryName(header.Name)
		if nameErr != nil {
			return nameErr
		}
		entry, ok := expected[name]
		if !ok || seen[name] {
			return fmt.Errorf("corpus archive changed after validation at %q", name)
		}
		actualKind := entryRegular
		if header.Typeflag == tar.TypeDir {
			actualKind = entryDirectory
		} else if header.Typeflag != tar.TypeReg && header.Typeflag != 0 {
			return fmt.Errorf("corpus archive changed after validation at %q", name)
		}
		if actualKind != entry.kind || header.Size != entry.size {
			return fmt.Errorf("corpus archive changed after validation at %q", name)
		}
		seen[name] = true
		destinationPath := filepath.Join(destination, filepath.FromSlash(name))
		switch entry.kind {
		case entryDirectory:
			if err := os.MkdirAll(destinationPath, 0o700); err != nil {
				return fmt.Errorf("create corpus directory %q: %w", name, err)
			}
		case entryRegular:
			if err := os.MkdirAll(filepath.Dir(destinationPath), 0o700); err != nil {
				return fmt.Errorf("create parent for corpus file %q: %w", name, err)
			}
			output, openErr := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if openErr != nil {
				return fmt.Errorf("create corpus file %q: %w", name, openErr)
			}
			copied, copyErr := io.Copy(output, reader)
			closeErr := output.Close()
			if copyErr != nil {
				return fmt.Errorf("extract corpus file %q: %w", name, copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close corpus file %q: %w", name, closeErr)
			}
			if copied != header.Size {
				return fmt.Errorf("corpus file %q extracted %d bytes, expected %d", name, copied, header.Size)
			}
		}
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("corpus archive changed after validation")
	}
	return nil
}

func openTarGzip(archivePath string) (*tar.Reader, func(), error) {
	archive, err := os.Open(archivePath)
	if err != nil {
		return nil, nil, fmt.Errorf("open corpus archive: %w", err)
	}
	gzipReader, err := gzip.NewReader(archive)
	if err != nil {
		_ = archive.Close()
		return nil, nil, fmt.Errorf("open corpus gzip stream: %w", err)
	}
	closeReader := func() {
		_ = gzipReader.Close()
		_ = archive.Close()
	}
	return tar.NewReader(gzipReader), closeReader, nil
}
