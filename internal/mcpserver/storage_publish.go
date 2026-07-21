package mcpserver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mansiverma897993/signoz/internal/artifactset"
	"github.com/mansiverma897993/signoz/pkg/reporttypes"
)

const (
	// A published MCP artifact must remain readable by every MCP consumer.
	// Keeping this equal to maxMCPArtifactSize makes publication fail before a
	// target write instead of creating an artifact that explain/validate cannot
	// subsequently verify.
	maxMCPPublishedArtifactSize = maxMCPArtifactSize
	// A final private migration staging directory can contain one 64 MiB source
	// dashboard, 64 MiB of rule inputs, two pointer-bound four-member artifact
	// generations (current and recoverable previous), and the four stable
	// facades. The allowance covers generation manifests and the current pointer.
	maxMCPPublishedSetSize = 14*maxMCPArtifactSize + (256 << 10)
)

func (service *Service) publishPrivateStagingDirectory(
	stagingDirectory string,
	destinationRelative string,
	binding *reporttypes.ArtifactSetBinding,
	extraFiles []string,
) error {
	if err := service.ensureOutputDirectoryStable(destinationRelative); err != nil {
		return err
	}
	root, err := os.OpenRoot(stagingDirectory)
	if err != nil {
		return fmt.Errorf("open private MCP staging directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	if binding == nil {
		return fmt.Errorf("private MCP staging report has no artifact-set binding")
	}
	retained, err := artifactset.InspectRetainedStorage(root, *binding, artifactset.KindDashboard)
	if err != nil {
		return fmt.Errorf("verify retained private MCP artifact storage: %w", err)
	}
	directories, files, err := inspectPrivateStagingTree(root, retained, extraFiles)
	if err != nil {
		return err
	}
	var totalBytes int64
	for _, entry := range files {
		if entry.info.Size() < 0 || entry.info.Size() > maxMCPPublishedArtifactSize ||
			totalBytes > int64(maxMCPPublishedSetSize)-entry.info.Size() {
			return fmt.Errorf("private MCP staging member %q exceeds publication limits", entry.relative)
		}
		totalBytes += entry.info.Size()
	}
	releaseQuota, err := service.reserveOutputQuota(int64(len(directories)+len(files)), totalBytes)
	if err != nil {
		return err
	}
	releaseQuota()
	for _, relative := range directories {
		if err := service.createOutputDirectory(filepath.Join(destinationRelative, relative)); err != nil {
			return err
		}
	}
	for _, entry := range files {
		data, err := readPrivateStagingMember(root, stagingDirectory, entry)
		if err != nil {
			return err
		}
		if err := service.writeOutputAtomic(filepath.Join(destinationRelative, entry.relative), data); err != nil {
			return err
		}
	}
	if err := service.verifyPublishedRetainedStorage(destinationRelative, *binding); err != nil {
		return fmt.Errorf("verify published private MCP artifact storage: %w", err)
	}
	return nil
}

func (service *Service) verifyPublishedRetainedStorage(
	relative string,
	binding reporttypes.ArtifactSetBinding,
) error {
	parent, err := openVerifiedRoot(service.config.OutputRoot, service.outputRootInfo)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	before, err := parent.Lstat(relative)
	if err != nil {
		return fmt.Errorf("inspect published artifact storage %q: %w", relative, err)
	}
	if !before.IsDir() {
		return fmt.Errorf("published artifact storage %q is not a real directory", relative)
	}
	root, err := parent.OpenRoot(relative)
	if err != nil {
		return fmt.Errorf("open published artifact storage %q: %w", relative, err)
	}
	defer func() { _ = root.Close() }()
	after, err := root.Stat(".")
	if err != nil || !after.IsDir() || !os.SameFile(before, after) {
		if err != nil {
			return fmt.Errorf("inspect opened artifact storage %q: %w", relative, err)
		}
		return fmt.Errorf("published artifact storage %q changed while it was opened", relative)
	}
	_, err = artifactset.InspectRetainedStorage(root, binding, artifactset.KindDashboard)
	return err
}

type privateStagingFile struct {
	relative string
	info     os.FileInfo
	priority int
}

func inspectPrivateStagingTree(
	root *os.Root,
	retained artifactset.RetainedStorageTree,
	extraFiles []string,
) ([]string, []privateStagingFile, error) {
	entries, err := readPrivateStagingDirectory(root, ".")
	if err != nil {
		return nil, nil, fmt.Errorf("list private MCP staging directory: %w", err)
	}
	directories := append([]string(nil), retained.Directories...)
	var files []privateStagingFile
	allowedVisible := make(map[string]bool, len(retained.Facades)+len(extraFiles))
	for _, name := range append(append([]string(nil), retained.Facades...), extraFiles...) {
		if err := validateManifestName("private MCP staging member", name); err != nil {
			return nil, nil, err
		}
		if allowedVisible[name] {
			return nil, nil, fmt.Errorf("private MCP staging inventory contains duplicate member %q", name)
		}
		allowedVisible[name] = true
	}
	seenVisible := make(map[string]bool, len(allowedVisible))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, ".") {
			if !allowedVisible[name] {
				return nil, nil, fmt.Errorf("private MCP staging contains unrecognized root entry %q", name)
			}
			file, err := inspectPrivateStagingFile(root, name, stagingFilePriority(name))
			if err != nil {
				return nil, nil, err
			}
			files = append(files, file)
			seenVisible[name] = true
			continue
		}
		switch name {
		case retained.Layout.Pointer, retained.Layout.Generations:
			continue
		case retained.Layout.Lock:
			if _, err := inspectPrivateStagingFile(root, name, 0); err != nil {
				return nil, nil, err
			}
		default:
			return nil, nil, fmt.Errorf("private MCP staging contains unrecognized hidden entry %q", name)
		}
	}
	for name := range allowedVisible {
		if !seenVisible[name] {
			return nil, nil, fmt.Errorf("private MCP staging is missing inventory member %q", name)
		}
	}
	for _, relative := range retained.Files {
		priority := 0
		if relative == retained.Layout.Pointer {
			priority = 1
		}
		file, err := inspectPrivateStagingFile(root, relative, priority)
		if err != nil {
			return nil, nil, err
		}
		files = append(files, file)
	}
	sort.Slice(directories, func(left, right int) bool {
		leftDepth := strings.Count(directories[left], string(filepath.Separator))
		rightDepth := strings.Count(directories[right], string(filepath.Separator))
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return directories[left] < directories[right]
	})
	sort.SliceStable(files, func(left, right int) bool {
		if files[left].priority != files[right].priority {
			return files[left].priority < files[right].priority
		}
		return files[left].relative < files[right].relative
	})
	return directories, files, nil
}

func readPrivateStagingDirectory(root *os.Root, relative string) ([]os.DirEntry, error) {
	directory, err := root.Open(relative)
	if err != nil {
		return nil, err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	return entries, nil
}

func inspectPrivateStagingFile(root *os.Root, relative string, priority int) (privateStagingFile, error) {
	info, err := root.Lstat(relative)
	if err != nil {
		return privateStagingFile{}, fmt.Errorf("inspect private MCP staging member %q: %w", relative, err)
	}
	if !info.Mode().IsRegular() {
		return privateStagingFile{}, fmt.Errorf("private MCP staging member %q is not a regular file", relative)
	}
	return privateStagingFile{relative: relative, info: info, priority: priority}, nil
}

func readPrivateStagingMember(
	root *os.Root,
	stagingDirectory string,
	entry privateStagingFile,
) ([]byte, error) {
	file, err := root.Open(entry.relative)
	if err != nil {
		return nil, fmt.Errorf("open private MCP staging member %q: %w", entry.relative, err)
	}
	openedInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect opened private MCP staging member %q: %w", entry.relative, err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(entry.info, openedInfo) {
		_ = file.Close()
		return nil, fmt.Errorf("private MCP staging member %q changed while it was opened", entry.relative)
	}
	return readBoundedFile(file, filepath.Join(stagingDirectory, entry.relative), entry.info.Size())
}

func stagingFilePriority(name string) int {
	switch {
	case strings.HasSuffix(name, ".artifacts.json"):
		return 3
	case strings.HasSuffix(name, ".report.json") || strings.HasSuffix(name, ".rules-report.json"):
		return 4
	default:
		return 2
	}
}
