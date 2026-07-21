package artifactset

import (
	"path/filepath"
	"sort"

	"github.com/mansiverma897993/signoz/pkg/reporttypes"
)

// ReservedPathsForReport returns every stable or hidden path name reserved by
// an artifact set, including optional members that are absent in this
// generation. The returned paths are absolute or relative in the same way as
// reportPath.
func ReservedPathsForReport(reportPath string, kind Kind) ([]string, error) {
	reportName := filepath.Base(reportPath)
	manifestName, err := expectedManifestName(reportName, kind)
	if err != nil {
		return nil, err
	}
	names, err := expectedArtifactNames(reportName, kind)
	if err != nil {
		return nil, err
	}
	directory := filepath.Dir(reportPath)
	paths := make([]string, 0, len(names)+4)
	for _, name := range names {
		paths = append(paths, filepath.Join(directory, name))
	}
	paths = append(paths,
		filepath.Join(directory, manifestName),
		filepath.Join(directory, currentPointerName(manifestName)),
		filepath.Join(directory, generationContainerName(manifestName)),
		filepath.Join(directory, lockName(manifestName)),
	)
	return uniqueSortedPaths(paths), nil
}

// ProtectedPathsForReport returns the bounded, verified path inventory reserved
// by a committed artifact set. It protects the stable and hidden reserved
// names, the pointer-bound current and previous generations, and one classified
// crash orphan. Ambiguous, malformed, or oversized generation trees fail
// closed instead of being recursively walked.
func ProtectedPathsForReport(
	reportPath string,
	binding reporttypes.ArtifactSetBinding,
	kind Kind,
) ([]string, error) {
	if err := validateBinding(reportPath, binding, kind); err != nil {
		return nil, err
	}
	reserved, err := ReservedPathsForReport(reportPath, kind)
	if err != nil {
		return nil, err
	}
	directoryPath := filepath.Dir(reportPath)
	layout, err := StorageLayoutForBinding(binding)
	if err != nil {
		return nil, err
	}
	directory, lock, err := openLockedPinnedDirectory(directoryPath, layout.Lock)
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()
	defer func() { _ = lock.Close() }()

	retained, err := InspectRetainedStorage(directory.root, binding, kind)
	if err != nil {
		return nil, err
	}
	paths := append([]string(nil), reserved...)
	for _, relative := range retained.Directories {
		paths = append(paths, filepath.Join(directoryPath, relative))
	}
	for _, relative := range retained.Files {
		paths = append(paths, filepath.Join(directoryPath, relative))
	}
	for _, relative := range retained.Facades {
		paths = append(paths, filepath.Join(directoryPath, relative))
	}
	if retained.OrphanDirectory != "" {
		paths = append(paths, filepath.Join(directoryPath, retained.OrphanDirectory))
	}
	for _, relative := range retained.OrphanFiles {
		paths = append(paths, filepath.Join(directoryPath, relative))
	}
	return uniqueSortedPaths(paths), nil
}

func uniqueSortedPaths(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		path = filepath.Clean(path)
		if seen[path] {
			continue
		}
		seen[path] = true
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}
