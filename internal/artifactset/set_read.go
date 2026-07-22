package artifactset

// This file resolves and verifies committed generations for readers.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
)

// ReadCommitted verifies a declared generation and all of its members under
// the same lock. reportData must be the exact bytes the caller intends to use;
// requested member bytes are returned from that same committed snapshot.
func ReadCommitted(
	reportPath string,
	reportData []byte,
	binding *reporttypes.ArtifactSetBinding,
	kind Kind,
	requested []string,
	maxArtifactSize int64,
) (Snapshot, error) {
	if binding == nil {
		return Snapshot{}, fmt.Errorf("migration report has no artifact-set binding")
	}
	if err := validateBinding(reportPath, *binding, kind); err != nil {
		return Snapshot{}, err
	}
	if maxArtifactSize <= 0 {
		return Snapshot{}, fmt.Errorf("maximum artifact size must be positive")
	}
	if maxArtifactSize > MaxMemberSize {
		return Snapshot{}, fmt.Errorf("maximum artifact size exceeds artifact-set contract limit %d", MaxMemberSize)
	}
	directory, lock, err := openLockedPinnedDirectory(filepath.Dir(reportPath), lockName(binding.Path))
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = directory.Close() }()
	defer func() { _ = lock.Close() }()
	return ReadCommittedRoot(
		directory.root,
		filepath.Base(reportPath),
		reportData,
		binding,
		kind,
		requested,
		maxArtifactSize,
	)
}

// ReadCommittedRoot verifies a complete declared generation beneath an
// already-pinned root. Callers are responsible for excluding concurrent
// writers to that root; immutable published generation directories satisfy
// that requirement without creating a lock file.
func ReadCommittedRoot(
	root *os.Root,
	reportName string,
	reportData []byte,
	binding *reporttypes.ArtifactSetBinding,
	kind Kind,
	requested []string,
	maxArtifactSize int64,
) (Snapshot, error) {
	if root == nil {
		return Snapshot{}, fmt.Errorf("artifact root is nil")
	}
	if binding == nil {
		return Snapshot{}, fmt.Errorf("migration report has no artifact-set binding")
	}
	if err := validateBinding(reportName, *binding, kind); err != nil {
		return Snapshot{}, err
	}
	if maxArtifactSize <= 0 {
		return Snapshot{}, fmt.Errorf("maximum artifact size must be positive")
	}
	if maxArtifactSize > MaxMemberSize {
		return Snapshot{}, fmt.Errorf("maximum artifact size exceeds artifact-set contract limit %d", MaxMemberSize)
	}
	pointer, hasPointer, err := readGenerationPointer(root, binding.Path)
	if err != nil {
		return Snapshot{}, err
	}
	generationRoot := root
	if hasPointer {
		if pointer.Generation == binding.Generation {
			if err := verifyStableFacades(root, reportData, *binding, kind, pointer); err != nil {
				return Snapshot{}, err
			}
		} else if pointer.PreviousGeneration != binding.Generation {
			return Snapshot{}, fmt.Errorf(
				"report generation %q is neither current nor the recoverable previous generation",
				binding.Generation,
			)
		}
		var immutable bool
		generationRoot, immutable, err = openGenerationRoot(root, binding.Path, binding.Generation)
		if err != nil {
			return Snapshot{}, err
		}
		if !immutable {
			return Snapshot{}, fmt.Errorf("artifact generation %q declared by the report is unavailable", binding.Generation)
		}
		defer func() { _ = generationRoot.Close() }()
	}
	manifestData, err := readRegular(generationRoot, binding.Path, maxManifestSize)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read artifact commit manifest %q: %w", binding.Path, err)
	}
	manifest, err := decodeManifest(manifestData, binding.Path, kind, binding.Generation)
	if err != nil {
		return Snapshot{}, err
	}
	if hasPointer && pointer.Generation == binding.Generation {
		if err := verifyPointerManifest(pointer, manifestData); err != nil {
			return Snapshot{}, fmt.Errorf("immutable generation manifest does not match current pointer: %w", err)
		}
	} else if hasPointer {
		if err := verifyPreviousPointerManifest(pointer, manifestData); err != nil {
			return Snapshot{}, fmt.Errorf("immutable previous-generation manifest does not match current pointer: %w", err)
		}
	}
	reportEntry, found := entryForRole(manifest, RoleReport)
	if !found {
		return Snapshot{}, fmt.Errorf("commit manifest has no report member")
	}
	if err := verifyBytes(reportEntry, reportData); err != nil {
		return Snapshot{}, fmt.Errorf("report bytes do not match commit manifest: %w", err)
	}
	wanted := make(map[string]bool, len(requested))
	for _, name := range requested {
		if !portableName(name) {
			return Snapshot{}, fmt.Errorf("requested artifact %q is not a portable filename", name)
		}
		wanted[name] = true
	}
	data, err := verifyMembersLocked(
		generationRoot,
		manifest,
		wanted,
		maxArtifactSize,
		min(MaxSetSize, maxArtifactSize*int64(len(manifest.Artifacts))),
		false,
	)
	if err != nil {
		return Snapshot{}, err
	}
	for name := range wanted {
		if _, found := data[name]; !found {
			return Snapshot{}, fmt.Errorf("requested artifact %q is not in the commit manifest", name)
		}
	}
	return Snapshot{Manifest: manifest, Data: data}, nil
}

func readGenerationPointer(root *os.Root, manifestName string) (generationPointer, bool, error) {
	name := currentPointerName(manifestName)
	data, err := readRegular(root, name, maxPointerSize)
	if errors.Is(err, os.ErrNotExist) {
		return generationPointer{}, false, nil
	}
	if err != nil {
		return generationPointer{}, false, fmt.Errorf("read artifact generation pointer %q: %w", name, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var pointer generationPointer
	if err := decoder.Decode(&pointer); err != nil {
		return generationPointer{}, false, fmt.Errorf("decode artifact generation pointer %q: %w", name, err)
	}
	if err := requireJSONEnd(decoder); err != nil {
		return generationPointer{}, false, fmt.Errorf("decode artifact generation pointer %q: %w", name, err)
	}
	previousAbsent := pointer.PreviousGeneration == "" && pointer.PreviousManifestSHA256 == "" &&
		pointer.PreviousManifestSizeBytes == 0
	previousPresent := validGeneration(pointer.PreviousGeneration) &&
		pointer.PreviousGeneration != pointer.Generation && validSHA256(pointer.PreviousManifestSHA256) &&
		pointer.PreviousManifestSizeBytes > 0 && pointer.PreviousManifestSizeBytes <= maxManifestSize
	previousValid := previousAbsent || previousPresent
	if pointer.SchemaVersion != pointerSchemaVersion || pointer.ManifestPath != manifestName ||
		!validGeneration(pointer.Generation) || !validSHA256(pointer.ManifestSHA256) ||
		!previousValid || pointer.ManifestSizeBytes <= 0 || pointer.ManifestSizeBytes > maxManifestSize {
		return generationPointer{}, false, fmt.Errorf("artifact generation pointer %q is invalid", name)
	}
	return pointer, true, nil
}

func verifyStableFacades(
	root *os.Root,
	reportData []byte,
	binding reporttypes.ArtifactSetBinding,
	kind Kind,
	pointer generationPointer,
) error {
	manifestData, err := readRegular(root, binding.Path, maxManifestSize)
	if err != nil {
		return fmt.Errorf("read current artifact commit manifest %q: %w", binding.Path, err)
	}
	if err := verifyPointerManifest(pointer, manifestData); err != nil {
		return fmt.Errorf("current artifact commit manifest does not match generation pointer: %w", err)
	}
	manifest, err := decodeManifest(manifestData, binding.Path, kind, binding.Generation)
	if err != nil {
		return err
	}
	reportEntry, found := entryForRole(manifest, RoleReport)
	if !found {
		return fmt.Errorf("commit manifest has no report member")
	}
	if err := verifyBytes(reportEntry, reportData); err != nil {
		return fmt.Errorf("report bytes do not match current artifact facade: %w", err)
	}
	if _, err := verifyMembersLocked(root, manifest, nil, MaxMemberSize, MaxSetSize, false); err != nil {
		return fmt.Errorf("verify current artifact facades: %w", err)
	}
	return nil
}

func verifyPointerManifest(pointer generationPointer, manifestData []byte) error {
	return verifyManifestBinding(manifestData, pointer.ManifestSHA256, pointer.ManifestSizeBytes)
}

func verifyPreviousPointerManifest(pointer generationPointer, manifestData []byte) error {
	return verifyManifestBinding(
		manifestData,
		pointer.PreviousManifestSHA256,
		pointer.PreviousManifestSizeBytes,
	)
}

func verifyManifestBinding(manifestData []byte, expectedSHA256 string, expectedSize int64) error {
	if int64(len(manifestData)) != expectedSize {
		return fmt.Errorf("size %d does not match pointer size %d", len(manifestData), expectedSize)
	}
	digest := sha256.Sum256(manifestData)
	actual := hex.EncodeToString(digest[:])
	if actual != expectedSHA256 {
		return fmt.Errorf("SHA-256 %q does not match pointer SHA-256 %q", actual, expectedSHA256)
	}
	return nil
}

// readGenerationLocked loads all bytes from one immutable generation. Flat
// v1 sets remain readable so an existing installation can transition without
// rewriting its reports out of band.
func readGenerationLocked(
	root *os.Root,
	binding reporttypes.ArtifactSetBinding,
	kind Kind,
) (Manifest, map[string][]byte, error) {
	pointer, hasPointer, err := readGenerationPointer(root, binding.Path)
	if err != nil {
		return Manifest{}, nil, err
	}
	generationRoot, immutable, err := openGenerationRoot(root, binding.Path, binding.Generation)
	if err != nil {
		return Manifest{}, nil, err
	}
	if hasPointer && !immutable {
		return Manifest{}, nil, fmt.Errorf("artifact generation %q is unavailable", binding.Generation)
	}
	if immutable {
		defer func() { _ = generationRoot.Close() }()
	}
	manifestData, err := readRegular(generationRoot, binding.Path, maxManifestSize)
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("read artifact commit manifest %q: %w", binding.Path, err)
	}
	manifest, err := decodeManifest(manifestData, binding.Path, kind, binding.Generation)
	if err != nil {
		return Manifest{}, nil, err
	}
	if hasPointer {
		switch binding.Generation {
		case pointer.Generation:
			err = verifyPointerManifest(pointer, manifestData)
		case pointer.PreviousGeneration:
			err = verifyPreviousPointerManifest(pointer, manifestData)
		default:
			err = fmt.Errorf("generation is neither current nor the recoverable previous generation")
		}
		if err != nil {
			return Manifest{}, nil, fmt.Errorf("verify immutable generation %q: %w", binding.Generation, err)
		}
	}
	data, err := verifyMembersLocked(generationRoot, manifest, nil, MaxMemberSize, MaxSetSize, true)
	if err != nil {
		return Manifest{}, nil, err
	}
	return manifest, data, nil
}

// openGenerationRoot returns the immutable generation root when it exists,
// otherwise the supplied root for compatibility with flat v1 publications.
// Existing non-directory storage paths fail closed rather than falling back.
func openGenerationRoot(
	root *os.Root,
	manifestName string,
	generation string,
) (*os.Root, bool, error) {
	container := generationContainerName(manifestName)
	containerInfo, err := root.Lstat(container)
	if errors.Is(err, os.ErrNotExist) {
		return root, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inspect artifact generation container %q: %w", container, err)
	}
	if !containerInfo.IsDir() {
		return nil, false, fmt.Errorf("artifact generation container %q is not a directory", container)
	}
	generationPath := filepath.Join(container, generation)
	generationInfo, err := root.Lstat(generationPath)
	if errors.Is(err, os.ErrNotExist) {
		return root, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inspect artifact generation %q: %w", generation, err)
	}
	if !generationInfo.IsDir() {
		return nil, false, fmt.Errorf("artifact generation %q is not a directory", generation)
	}
	generationRoot, err := root.OpenRoot(generationPath)
	if err != nil {
		return nil, false, fmt.Errorf("open artifact generation %q: %w", generation, err)
	}
	openedInfo, err := generationRoot.Stat(".")
	if err != nil || !os.SameFile(generationInfo, openedInfo) {
		_ = generationRoot.Close()
		if err != nil {
			return nil, false, fmt.Errorf("inspect opened artifact generation %q: %w", generation, err)
		}
		return nil, false, fmt.Errorf("artifact generation %q changed while it was opened", generation)
	}
	return generationRoot, true, nil
}
