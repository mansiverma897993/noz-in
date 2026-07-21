package artifactset

// This file removes unreferenced generations before and after publication,
// using crash-safe prune tombstones.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/mansiverma897993/signoz/pkg/reporttypes"
)

// preflightRetainedGenerationsLocked runs before a new immutable generation is
// created. It removes only the single state that has one mechanical
// interpretation: a fully verified generation that no durable pointer or
// legacy stable report names.
func preflightRetainedGenerationsLocked(
	directory *pinnedDirectory,
	reportName string,
	manifestName string,
	kind Kind,
	hook commitBarrierHook,
) error {
	layout := StorageLayout{
		Pointer: currentPointerName(manifestName), Generations: generationContainerName(manifestName),
		Lock: lockName(manifestName),
	}
	pointer, hasPointer, err := readGenerationPointer(directory.root, manifestName)
	if err != nil {
		return fmt.Errorf("validate retained artifact pointer before publication: %w", err)
	}
	inventory, _, err := inspectGenerationInventory(directory.root, layout, manifestName, kind)
	if err != nil {
		return fmt.Errorf("validate retained artifact generations before publication: %w", err)
	}
	protected := make(map[string]bool, 2)
	if hasPointer {
		protected, err = verifyPointerInventory(pointer, inventory)
		if err != nil {
			return fmt.Errorf("validate retained artifact pointer before publication: %w", err)
		}
	}
	var stableManifestData []byte
	var hasStableManifest bool
	if !hasPointer {
		stableManifestData, hasStableManifest, err = readStableManifestIfPresent(
			directory.root,
			manifestName,
		)
		if err != nil {
			return fmt.Errorf("validate unpointed stable artifact manifest before publication: %w", err)
		}
	}
	reportBinding, reportData, hasReportBinding, err := declaredStableReportBinding(
		directory.root,
		reportName,
		manifestName,
		!hasPointer && hasStableManifest,
	)
	if err != nil {
		return fmt.Errorf("validate retained artifact report before publication: %w", err)
	}
	if !hasPointer && hasStableManifest {
		if err := verifyUnpointedStableGeneration(
			directory.root,
			manifestName,
			kind,
			reportBinding,
			reportData,
			stableManifestData,
			inventory,
		); err != nil {
			return fmt.Errorf("validate unpointed stable artifact generation before publication: %w", err)
		}
	}
	if hasPointer && pointer.PreviousGeneration != "" && !hasReportBinding {
		return fmt.Errorf(
			"artifact pointer names previous generation %q but the stable report has no valid artifact-set binding",
			pointer.PreviousGeneration,
		)
	}
	if hasReportBinding {
		generation := reportBinding.Generation
		if hasPointer && !protected[generation] {
			return fmt.Errorf(
				"stable artifact report generation %q is neither current nor the recoverable previous generation",
				generation,
			)
		}
		if _, immutable := inventory[generation]; immutable {
			protected[generation] = true
		}
	}
	unreferenced := make([]string, 0, 1)
	for _, generation := range sortedGenerationNames(inventory) {
		if !protected[generation] {
			unreferenced = append(unreferenced, generation)
		}
	}
	if len(unreferenced) > 1 {
		return fmt.Errorf(
			"refuse artifact publication with %d unreferenced generations; retention state is ambiguous",
			len(unreferenced),
		)
	}
	if len(unreferenced) == 0 {
		return nil
	}
	return removeUnreferencedGenerationLocked(
		directory,
		manifestName,
		kind,
		unreferenced[0],
		pointer,
		hasPointer,
		barrierGenerationPreflightPrune,
		hook,
	)
}

// pruneCommittedGenerationsLocked runs after the report-last facade barrier.
// The complete new commit is re-verified before any old directory is removed.
func pruneCommittedGenerationsLocked(
	directory *pinnedDirectory,
	reportName string,
	binding reporttypes.ArtifactSetBinding,
	kind Kind,
	hook commitBarrierHook,
) error {
	pointer, hasPointer, err := readGenerationPointer(directory.root, binding.Path)
	if err != nil {
		return fmt.Errorf("validate committed artifact pointer before retention pruning: %w", err)
	}
	if !hasPointer || pointer.Generation != binding.Generation {
		return fmt.Errorf(
			"committed artifact pointer does not name published generation %q",
			binding.Generation,
		)
	}
	layout := StorageLayout{
		Pointer: currentPointerName(binding.Path), Generations: generationContainerName(binding.Path),
		Lock: lockName(binding.Path),
	}
	inventory, foundContainer, err := inspectGenerationInventory(directory.root, layout, binding.Path, kind)
	if err != nil {
		return fmt.Errorf("validate committed artifact generations before retention pruning: %w", err)
	}
	if !foundContainer {
		return fmt.Errorf("committed artifact generation container %q is missing", layout.Generations)
	}
	protected, err := verifyPointerInventory(pointer, inventory)
	if err != nil {
		return fmt.Errorf("validate committed artifact pointer before retention pruning: %w", err)
	}
	reportData, err := readRegular(directory.root, reportName, MaxMemberSize)
	if err != nil {
		return fmt.Errorf("read committed artifact report before retention pruning: %w", err)
	}
	if err := verifyStableFacades(directory.root, reportData, binding, kind, pointer); err != nil {
		return fmt.Errorf("validate committed artifact facades before retention pruning: %w", err)
	}
	for _, generation := range sortedGenerationNames(inventory) {
		if protected[generation] {
			continue
		}
		if err := removeUnreferencedGenerationLocked(
			directory,
			binding.Path,
			kind,
			generation,
			pointer,
			true,
			barrierGenerationPrune,
			hook,
		); err != nil {
			return err
		}
	}
	return nil
}

func declaredStableReportBinding(
	root *os.Root,
	reportName string,
	manifestName string,
	required bool,
) (reporttypes.ArtifactSetBinding, []byte, bool, error) {
	data, err := readRegular(root, reportName, MaxMemberSize)
	if errors.Is(err, os.ErrNotExist) {
		if required {
			return reporttypes.ArtifactSetBinding{}, nil, false, fmt.Errorf(
				"stable artifact manifest exists but report %q is missing",
				reportName,
			)
		}
		return reporttypes.ArtifactSetBinding{}, nil, false, nil
	}
	if err != nil {
		return reporttypes.ArtifactSetBinding{}, nil, false, err
	}
	var report struct {
		ArtifactSet *reporttypes.ArtifactSetBinding `json:"artifactSet"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		if required {
			return reporttypes.ArtifactSetBinding{}, nil, false, fmt.Errorf("decode stable artifact report: %w", err)
		}
		return reporttypes.ArtifactSetBinding{}, nil, false, nil
	}
	if report.ArtifactSet == nil {
		if required {
			return reporttypes.ArtifactSetBinding{}, nil, false, fmt.Errorf(
				"stable artifact manifest exists but report has no artifact-set binding",
			)
		}
		return reporttypes.ArtifactSetBinding{}, nil, false, nil
	}
	if report.ArtifactSet.Path != manifestName || !validGeneration(report.ArtifactSet.Generation) {
		return reporttypes.ArtifactSetBinding{}, nil, false, fmt.Errorf("stable artifact report has an invalid artifact-set binding")
	}
	return *report.ArtifactSet, data, true, nil
}

func readStableManifestIfPresent(root *os.Root, manifestName string) ([]byte, bool, error) {
	data, err := readRegular(root, manifestName, maxManifestSize)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func verifyUnpointedStableGeneration(
	root *os.Root,
	manifestName string,
	kind Kind,
	binding reporttypes.ArtifactSetBinding,
	reportData []byte,
	manifestData []byte,
	inventory map[string]retainedGeneration,
) error {
	manifest, err := decodeManifest(manifestData, manifestName, kind, binding.Generation)
	if err != nil {
		return err
	}
	data, err := verifyMembersLocked(root, manifest, nil, MaxMemberSize, MaxSetSize, true)
	if err != nil {
		return fmt.Errorf("verify stable artifact facades: %w", err)
	}
	reportEntry, found := entryForRole(manifest, RoleReport)
	if !found {
		return fmt.Errorf("stable artifact manifest has no report member")
	}
	if !bytes.Equal(data[reportEntry.Path], reportData) {
		return fmt.Errorf("stable artifact report changed while it was verified")
	}
	primary, found := entryForRole(manifest, RolePrimary)
	if !found {
		return fmt.Errorf("stable artifact manifest has no primary member")
	}
	if err := verifyDeclaredBindings(
		reportData,
		binding,
		Artifact{Role: RolePrimary, Path: primary.Path, Data: data[primary.Path]},
	); err != nil {
		return err
	}
	if immutable, found := inventory[binding.Generation]; found &&
		!bytes.Equal(immutable.manifestData, manifestData) {
		return fmt.Errorf("stable artifact manifest does not match immutable generation %q", binding.Generation)
	}
	return nil
}

func removeUnreferencedGenerationLocked(
	directory *pinnedDirectory,
	manifestName string,
	kind Kind,
	generation string,
	expectedPointer generationPointer,
	expectedPointerFound bool,
	barrierPhase string,
	hook commitBarrierHook,
) error {
	actualPointer, pointerFound, err := readGenerationPointer(directory.root, manifestName)
	if err != nil {
		return fmt.Errorf("revalidate artifact pointer before pruning generation %q: %w", generation, err)
	}
	if pointerFound != expectedPointerFound || (pointerFound && actualPointer != expectedPointer) {
		return fmt.Errorf("artifact pointer changed before pruning generation %q", generation)
	}
	if pointerFound && (generation == actualPointer.Generation || generation == actualPointer.PreviousGeneration) {
		return fmt.Errorf("refuse to prune pointer-bound artifact generation %q", generation)
	}
	layout := StorageLayout{
		Pointer: currentPointerName(manifestName), Generations: generationContainerName(manifestName),
		Lock: lockName(manifestName),
	}
	_, _, manifest, _, err := inspectRetainedGeneration(
		directory.root,
		layout,
		manifestName,
		generation,
		kind,
	)
	if err != nil {
		return fmt.Errorf("revalidate unreferenced artifact generation %q before pruning: %w", generation, err)
	}
	generationPath := filepath.Join(layout.Generations, generation)
	generationRoot, err := directory.root.OpenRoot(generationPath)
	if err != nil {
		return fmt.Errorf("open unreferenced artifact generation %q for pruning: %w", generation, err)
	}
	members := generationOwnedMembers(manifestName, manifest)
	owner, err := ensureGenerationOwnership(generationRoot, manifestName, generation, members)
	closeErr := generationRoot.Close()
	if err := errors.Join(err, closeErr); err != nil {
		return fmt.Errorf("prepare unreferenced artifact generation %q for crash-safe pruning: %w", generation, err)
	}
	tombstone := pruneTombstoneName(manifestName, generation, owner.Nonce)
	tombstonePath := filepath.Join(layout.Generations, tombstone)
	if _, err := directory.root.Lstat(tombstonePath); err == nil {
		return fmt.Errorf("artifact prune tombstone %q already exists", tombstone)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect artifact prune tombstone %q: %w", tombstone, err)
	}
	if err := directory.root.Rename(generationPath, tombstonePath); err != nil {
		return fmt.Errorf("tombstone unreferenced artifact generation %q: %w", generation, err)
	}
	if err := syncRootDirectory(directory.root, layout.Generations, "artifact generation retention update"); err != nil {
		return err
	}
	if err := runCommitBarrier(hook, commitBarrier{phase: barrierGenerationPruneRename}); err != nil {
		return err
	}
	if _, err := removePruneTombstone(directory, manifestName, tombstone, hook); err != nil {
		return fmt.Errorf("prune tombstoned artifact generation %q: %w", generation, err)
	}
	return runCommitBarrier(hook, commitBarrier{phase: barrierPhase})
}

func generationOwnedMembers(manifestName string, manifest Manifest) []string {
	members := make([]string, 0, len(manifest.Artifacts)+1)
	members = append(members, manifestName)
	for _, entry := range manifest.Artifacts {
		members = append(members, entry.Path)
	}
	normalized, _ := normalizeOwnedMembers(members)
	return normalized
}

func cleanupPruneTombstones(directory *pinnedDirectory, manifestName string) error {
	container := generationContainerName(manifestName)
	info, err := directory.root.Lstat(container)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect artifact generation container for prune recovery: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("artifact generation container %q is not a real directory", container)
	}
	entries, err := readRealDirectoryBounded(directory.root, container, maxGenerationContainerEntries)
	if err != nil {
		return fmt.Errorf("inspect artifact generation container for prune recovery: %w", err)
	}
	tombstones := 0
	for _, entry := range entries {
		if _, _, ok := parsePruneTombstoneName(entry.Name(), manifestName); !ok {
			continue
		}
		tombstones++
		if tombstones > 1 {
			return fmt.Errorf("artifact generation container has multiple prune tombstones")
		}
		removed, err := removePruneTombstone(directory, manifestName, entry.Name(), nil)
		if err != nil {
			return fmt.Errorf("recover artifact prune tombstone %q: %w", entry.Name(), err)
		}
		if !removed {
			return fmt.Errorf("artifact prune tombstone %q has no valid ownership proof", entry.Name())
		}
	}
	return nil
}

func removePruneTombstone(
	directory *pinnedDirectory,
	manifestName string,
	name string,
	hook commitBarrierHook,
) (bool, error) {
	generation, nonce, ok := parsePruneTombstoneName(name, manifestName)
	if !ok {
		return false, nil
	}
	container := generationContainerName(manifestName)
	path := filepath.Join(container, name)
	before, err := directory.root.Lstat(path)
	if err != nil {
		return false, err
	}
	if !before.IsDir() {
		return false, fmt.Errorf("prune tombstone is not a real directory")
	}
	root, err := directory.root.OpenRoot(path)
	if err != nil {
		return false, err
	}
	opened, err := root.Stat(".")
	if err != nil || !opened.IsDir() || !os.SameFile(before, opened) {
		_ = root.Close()
		if err != nil {
			return false, err
		}
		return false, fmt.Errorf("prune tombstone changed while it was opened")
	}
	owner, hasOwner, err := readTombstoneOwnership(root, manifestName, generation, nonce)
	if err != nil {
		_ = root.Close()
		return false, err
	}
	if !hasOwner {
		entries, err := readRealDirectoryBounded(root, ".", 0)
		if err != nil || len(entries) != 0 {
			_ = root.Close()
			if err != nil {
				return false, err
			}
			return false, nil
		}
	} else {
		members := append([]string(nil), owner.Members...)
		sort.SliceStable(members, func(left, right int) bool {
			return members[left] != manifestName && members[right] == manifestName
		})
		hookCalled := false
		for _, member := range members {
			removed, err := removeRegularIfPresent(root, member)
			if err != nil {
				_ = root.Close()
				return false, err
			}
			if removed && !hookCalled {
				hookCalled = true
				if err := runCommitBarrier(hook, commitBarrier{phase: barrierGenerationPruneDelete}); err != nil {
					_ = root.Close()
					return false, err
				}
			}
		}
		if err := syncRootDirectory(root, ".", "artifact prune tombstone members"); err != nil {
			_ = root.Close()
			return false, err
		}
		if _, err := removeRegularIfPresent(root, stageOwnerName); err != nil {
			_ = root.Close()
			return false, err
		}
		if err := syncRootDirectory(root, ".", "artifact prune tombstone ownership"); err != nil {
			_ = root.Close()
			return false, err
		}
	}
	current, err := directory.root.Lstat(path)
	if err != nil || !os.SameFile(before, current) {
		_ = root.Close()
		if err != nil {
			return false, err
		}
		return false, fmt.Errorf("prune tombstone changed during cleanup")
	}
	if err := root.Close(); err != nil {
		return false, err
	}
	if err := directory.root.Remove(path); err != nil {
		return false, err
	}
	if err := syncRootDirectory(directory.root, container, "artifact prune tombstone removal"); err != nil {
		return false, err
	}
	return true, nil
}

func readTombstoneOwnership(
	root *os.Root,
	manifestName string,
	generation string,
	nonce string,
) (stageOwnership, bool, error) {
	data, err := readRegular(root, stageOwnerName, maxStageOwnerSize)
	if errors.Is(err, os.ErrNotExist) {
		return stageOwnership{}, false, nil
	}
	if err != nil {
		return stageOwnership{}, false, err
	}
	owner, err := decodeStageOwnership(data)
	if err != nil {
		return stageOwnership{}, false, err
	}
	normalized, err := normalizeOwnedMembers(owner.Members)
	if err != nil {
		return stageOwnership{}, false, err
	}
	kind, stageGeneration, stageNonce, validStage := parseOwnedStageName(owner.Stage, manifestName)
	if owner.SchemaVersion != stageOwnerSchema || owner.ManifestPath != manifestName ||
		owner.Generation != generation || owner.Nonce != nonce || owner.Purpose != "generation" ||
		!validStage || kind != "generation" || stageGeneration != generation || stageNonce != nonce ||
		!equalStrings(normalized, owner.Members) {
		return stageOwnership{}, false, fmt.Errorf("prune tombstone ownership record is invalid")
	}
	allowed := make(map[string]bool, len(owner.Members)+1)
	allowed[stageOwnerName] = true
	for _, member := range owner.Members {
		allowed[member] = true
	}
	entries, err := readRealDirectoryBounded(root, ".", len(allowed))
	if err != nil {
		return stageOwnership{}, false, err
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return stageOwnership{}, false, fmt.Errorf("prune tombstone contains unowned member %q", entry.Name())
		}
		info, err := root.Lstat(entry.Name())
		if err != nil || !info.Mode().IsRegular() {
			if err != nil {
				return stageOwnership{}, false, err
			}
			return stageOwnership{}, false, fmt.Errorf("prune tombstone member %q is not regular", entry.Name())
		}
	}
	return owner, true, nil
}
