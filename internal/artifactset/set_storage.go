package artifactset

// This file inventories and verifies the retained immutable generations owned
// by one artifact-set binding.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
)

// StorageLayout contains the exact hidden root names owned by one artifact
// manifest. Ephemeral stages are intentionally absent.
type StorageLayout struct {
	Pointer     string
	Generations string
	Lock        string
}

// RetainedStorageTree is a verified closed inventory suitable for relocating
// a committed artifact set without copying arbitrary hidden files.
type RetainedStorageTree struct {
	Layout           StorageLayout
	Directories      []string
	Files            []string
	Facades          []string
	OrphanGeneration string
	OrphanDirectory  string
	OrphanFiles      []string
}

// StorageLayoutForBinding derives the hidden names bound to one report-declared
// manifest path.
func StorageLayoutForBinding(binding reporttypes.ArtifactSetBinding) (StorageLayout, error) {
	if !portableName(binding.Path) || !validGeneration(binding.Generation) {
		return StorageLayout{}, fmt.Errorf("artifact-set binding is invalid")
	}
	return StorageLayout{
		Pointer: currentPointerName(binding.Path), Generations: generationContainerName(binding.Path),
		Lock: lockName(binding.Path),
	}, nil
}

// InspectRetainedStorage verifies every immutable generation, rejects
// unmanifested members, and returns only the current and recoverable previous
// generations owned by binding. One fully verified generation left by an
// interrupted pre-pointer publication is classified but deliberately omitted
// from the relocatable inventory. More than one unreferenced generation is an
// ambiguous state and fails closed.
func InspectRetainedStorage(
	root *os.Root,
	binding reporttypes.ArtifactSetBinding,
	kind Kind,
) (RetainedStorageTree, error) {
	if root == nil {
		return RetainedStorageTree{}, fmt.Errorf("artifact root is nil")
	}
	reportName, err := reportNameForManifest(binding.Path, kind)
	if err != nil {
		return RetainedStorageTree{}, err
	}
	if err := validateBinding(reportName, binding, kind); err != nil {
		return RetainedStorageTree{}, err
	}
	layout, err := StorageLayoutForBinding(binding)
	if err != nil {
		return RetainedStorageTree{}, err
	}
	pointer, found, err := readGenerationPointer(root, binding.Path)
	if err != nil {
		return RetainedStorageTree{}, err
	}
	if !found || pointer.Generation != binding.Generation {
		return RetainedStorageTree{}, fmt.Errorf("artifact pointer does not name report generation %q", binding.Generation)
	}
	reportData, err := readRegular(root, reportName, MaxMemberSize)
	if err != nil {
		return RetainedStorageTree{}, fmt.Errorf("read current artifact report %q: %w", reportName, err)
	}
	if err := verifyStableFacades(root, reportData, binding, kind, pointer); err != nil {
		return RetainedStorageTree{}, fmt.Errorf("verify current artifact facades: %w", err)
	}
	inventory, foundContainer, err := inspectGenerationInventory(root, layout, binding.Path, kind)
	if err != nil {
		return RetainedStorageTree{}, fmt.Errorf("inspect retained artifact generations: %w", err)
	}
	if !foundContainer {
		return RetainedStorageTree{}, fmt.Errorf("artifact generation container %q is missing", layout.Generations)
	}
	protected, err := verifyPointerInventory(pointer, inventory)
	if err != nil {
		return RetainedStorageTree{}, err
	}
	tree := RetainedStorageTree{
		Layout: layout, Directories: []string{layout.Generations}, Files: []string{layout.Pointer},
	}
	for _, generation := range sortedGenerationNames(inventory) {
		retained := inventory[generation]
		if !protected[generation] {
			if tree.OrphanGeneration != "" {
				return RetainedStorageTree{}, fmt.Errorf(
					"artifact generation container has multiple unreferenced generations %q and %q",
					tree.OrphanGeneration,
					generation,
				)
			}
			tree.OrphanGeneration = generation
			tree.OrphanDirectory = retained.directory
			tree.OrphanFiles = append([]string(nil), retained.files...)
			continue
		}
		if generation == pointer.Generation {
			tree.Facades = append(tree.Facades, binding.Path)
			for _, artifact := range retained.manifest.Artifacts {
				tree.Facades = append(tree.Facades, artifact.Path)
			}
		}
		tree.Directories = append(tree.Directories, retained.directory)
		tree.Files = append(tree.Files, retained.files...)
	}
	sort.Strings(tree.Directories)
	sort.Strings(tree.Files)
	sort.Strings(tree.Facades)
	sort.Strings(tree.OrphanFiles)
	return tree, nil
}

type retainedGeneration struct {
	directory    string
	files        []string
	manifest     Manifest
	manifestData []byte
}

func inspectGenerationInventory(
	root *os.Root,
	layout StorageLayout,
	manifestName string,
	kind Kind,
) (map[string]retainedGeneration, bool, error) {
	info, err := root.Lstat(layout.Generations)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]retainedGeneration{}, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inspect artifact generation container %q: %w", layout.Generations, err)
	}
	if !info.IsDir() {
		return nil, false, fmt.Errorf("artifact generation container %q is not a real directory", layout.Generations)
	}
	entries, err := readRealDirectoryBounded(
		root,
		layout.Generations,
		maxRetainedGenerationDirectories,
	)
	if err != nil {
		return nil, false, fmt.Errorf("list artifact generation container %q: %w", layout.Generations, err)
	}
	inventory := make(map[string]retainedGeneration, len(entries))
	for _, entry := range entries {
		generation := entry.Name()
		if !validGeneration(generation) {
			return nil, false, fmt.Errorf("artifact generation container has invalid member %q", generation)
		}
		if _, exists := inventory[generation]; exists {
			return nil, false, fmt.Errorf("artifact generation container has duplicate member %q", generation)
		}
		directory, files, manifest, manifestData, err := inspectRetainedGeneration(
			root,
			layout,
			manifestName,
			generation,
			kind,
		)
		if err != nil {
			return nil, false, err
		}
		inventory[generation] = retainedGeneration{
			directory: directory, files: files, manifest: manifest, manifestData: manifestData,
		}
	}
	return inventory, true, nil
}

func verifyPointerInventory(
	pointer generationPointer,
	inventory map[string]retainedGeneration,
) (map[string]bool, error) {
	protected := make(map[string]bool, 2)
	current, found := inventory[pointer.Generation]
	if !found {
		return nil, fmt.Errorf("current artifact generation %q is missing", pointer.Generation)
	}
	if err := verifyPointerManifest(pointer, current.manifestData); err != nil {
		return nil, fmt.Errorf("verify retained generation %q pointer binding: %w", pointer.Generation, err)
	}
	protected[pointer.Generation] = true
	if pointer.PreviousGeneration == "" {
		return protected, nil
	}
	previous, found := inventory[pointer.PreviousGeneration]
	if !found {
		return nil, fmt.Errorf("previous artifact generation %q is missing", pointer.PreviousGeneration)
	}
	if err := verifyPreviousPointerManifest(pointer, previous.manifestData); err != nil {
		return nil, fmt.Errorf(
			"verify retained generation %q pointer binding: %w",
			pointer.PreviousGeneration,
			err,
		)
	}
	protected[pointer.PreviousGeneration] = true
	return protected, nil
}

func sortedGenerationNames(inventory map[string]retainedGeneration) []string {
	names := make([]string, 0, len(inventory))
	for name := range inventory {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func inspectRetainedGeneration(
	root *os.Root,
	layout StorageLayout,
	manifestName string,
	generation string,
	kind Kind,
) (string, []string, Manifest, []byte, error) {
	generationPath := filepath.Join(layout.Generations, generation)
	before, err := root.Lstat(generationPath)
	if err != nil {
		return "", nil, Manifest{}, nil, fmt.Errorf("inspect retained artifact generation %q: %w", generation, err)
	}
	if !before.IsDir() {
		return "", nil, Manifest{}, nil, fmt.Errorf("retained artifact generation %q is not a real directory", generation)
	}
	generationRoot, err := root.OpenRoot(generationPath)
	if err != nil {
		return "", nil, Manifest{}, nil, fmt.Errorf("open retained artifact generation %q: %w", generation, err)
	}
	defer func() { _ = generationRoot.Close() }()
	after, err := generationRoot.Stat(".")
	if err != nil || !after.IsDir() || !os.SameFile(before, after) {
		if err != nil {
			return "", nil, Manifest{}, nil, fmt.Errorf("inspect opened retained artifact generation %q: %w", generation, err)
		}
		return "", nil, Manifest{}, nil, fmt.Errorf("retained artifact generation %q changed while it was opened", generation)
	}
	manifestData, err := readRegular(generationRoot, manifestName, maxManifestSize)
	if err != nil {
		return "", nil, Manifest{}, nil, fmt.Errorf("read retained generation manifest %q: %w", manifestName, err)
	}
	manifest, err := decodeManifest(manifestData, manifestName, kind, generation)
	if err != nil {
		return "", nil, Manifest{}, nil, err
	}
	expected := make(map[string]bool, len(manifest.Artifacts)+1)
	expected[manifestName] = true
	for _, entry := range manifest.Artifacts {
		expected[entry.Path] = true
	}
	ownedMembers := make([]string, 0, len(expected))
	for name := range expected {
		ownedMembers = append(ownedMembers, name)
	}
	_, hasOwnership, err := readGenerationOwnership(
		generationRoot,
		manifestName,
		generation,
		ownedMembers,
	)
	if err != nil {
		return "", nil, Manifest{}, nil, fmt.Errorf("validate retained generation ownership %q: %w", generation, err)
	}
	if hasOwnership {
		expected[stageOwnerName] = true
	}
	entries, err := readRealDirectoryBounded(generationRoot, ".", len(expected))
	if err != nil {
		return "", nil, Manifest{}, nil, fmt.Errorf("list retained artifact generation %q: %w", generation, err)
	}
	if len(entries) != len(expected) {
		return "", nil, Manifest{}, nil, fmt.Errorf("retained artifact generation %q contains unmanifested or missing members", generation)
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !expected[entry.Name()] {
			return "", nil, Manifest{}, nil, fmt.Errorf(
				"retained artifact generation %q contains unmanifested member %q",
				generation,
				entry.Name(),
			)
		}
		info, err := generationRoot.Lstat(entry.Name())
		if err != nil {
			return "", nil, Manifest{}, nil, fmt.Errorf("inspect retained generation member %q: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			return "", nil, Manifest{}, nil, fmt.Errorf("retained generation member %q is not a regular file", entry.Name())
		}
		files = append(files, filepath.Join(generationPath, entry.Name()))
	}
	data, err := verifyMembersLocked(generationRoot, manifest, nil, MaxMemberSize, MaxSetSize, true)
	if err != nil {
		return "", nil, Manifest{}, nil, fmt.Errorf("verify retained artifact generation %q: %w", generation, err)
	}
	primary, found := entryForRole(manifest, RolePrimary)
	if !found {
		return "", nil, Manifest{}, nil, fmt.Errorf("retained artifact generation %q has no primary member", generation)
	}
	reportEntry, found := entryForRole(manifest, RoleReport)
	if !found {
		return "", nil, Manifest{}, nil, fmt.Errorf("retained artifact generation %q has no report member", generation)
	}
	binding := reporttypes.ArtifactSetBinding{Path: manifestName, Generation: generation}
	if err := verifyDeclaredBindings(
		data[reportEntry.Path],
		binding,
		Artifact{Role: RolePrimary, Path: primary.Path, Data: data[primary.Path]},
	); err != nil {
		return "", nil, Manifest{}, nil, fmt.Errorf("verify retained artifact generation %q report binding: %w", generation, err)
	}
	return generationPath, files, manifest, manifestData, nil
}
