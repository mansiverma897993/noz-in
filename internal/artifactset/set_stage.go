package artifactset

// This file manages owned stage directories and their ownership records.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const stageOwnerName = ".promcast-stage-owner.json"

type stageOwnership struct {
	SchemaVersion int      `json:"schemaVersion"`
	ManifestPath  string   `json:"manifestPath"`
	Generation    string   `json:"generation"`
	Nonce         string   `json:"nonce"`
	Purpose       string   `json:"purpose"`
	Stage         string   `json:"stage"`
	Members       []string `json:"members"`
}

func preflightDestinations(directory *pinnedDirectory, manifestName string, kind Kind, artifacts []Artifact) error {
	paths := make([]string, 0, len(artifacts)+2)
	for _, artifact := range artifacts {
		paths = append(paths, filepath.Base(artifact.Path))
	}
	paths = append(paths, manifestName, currentPointerName(manifestName))
	if kind == KindDashboard && !hasRole(artifacts, RoleCandidate) {
		for _, artifact := range artifacts {
			if artifact.Role == RoleReport {
				paths = append(paths, expectedCandidateName(filepath.Base(artifact.Path)))
				break
			}
		}
	}
	for _, name := range paths {
		info, err := directory.root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect artifact destination %q: %w", filepath.Join(directory.path, name), err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refuse artifact destination %q: existing path is not a regular file", filepath.Join(directory.path, name))
		}
	}
	return nil
}

func cleanupStages(directory *pinnedDirectory, manifestName string) error {
	entries, err := readRealDirectoryBounded(directory.root, ".", maxArtifactRootEntries)
	if err != nil {
		return fmt.Errorf(
			"inspect artifact directory %q for stale stages: %w; split the run across multiple --out directories",
			directory.path, err,
		)
	}
	candidates := 0
	for _, entry := range entries {
		if _, _, _, ok := parseOwnedStageName(entry.Name(), manifestName); !ok {
			continue
		}
		candidates++
		if candidates > maxOwnedStages {
			return fmt.Errorf("artifact directory contains more than %d owned-stage candidates", maxOwnedStages)
		}
		err := removeOwnedStage(directory, manifestName, entry.Name(), nil)
		if err != nil {
			return fmt.Errorf("remove verified stale artifact stage %q: %w", entry.Name(), err)
		}
	}
	return nil
}

func createOwnedStage(
	directory *pinnedDirectory,
	manifestName string,
	generation string,
	purpose string,
	members []string,
) (string, error) {
	nonce, err := newStageNonce()
	if err != nil {
		return "", err
	}
	stage := ownedStageName(manifestName, generation, purpose, nonce)
	owner, err := newStageOwnership(manifestName, generation, nonce, purpose, stage, members)
	if err != nil {
		return "", err
	}
	if err := directory.root.Mkdir(stage, 0o700); err != nil {
		return "", fmt.Errorf("create owned artifact stage %q: %w", stage, err)
	}
	if err := directory.Sync(); err != nil {
		return "", fmt.Errorf("persist owned artifact stage %q: %w", stage, err)
	}
	data, err := json.Marshal(owner)
	if err != nil {
		return "", fmt.Errorf("encode artifact stage ownership: %w", err)
	}
	if err := writeStageFile(directory.root, filepath.Join(stage, stageOwnerName), append(data, '\n')); err != nil {
		return "", fmt.Errorf("write artifact stage ownership: %w", err)
	}
	if err := syncRootDirectory(directory.root, stage, "owned artifact stage"); err != nil {
		return "", err
	}
	return stage, nil
}

func newStageOwnership(
	manifestName string,
	generation string,
	nonce string,
	purpose string,
	stage string,
	members []string,
) (stageOwnership, error) {
	if !portableName(manifestName) || !validGeneration(generation) || !validStageNonce(nonce) {
		return stageOwnership{}, fmt.Errorf("artifact stage ownership identity is invalid")
	}
	normalized, err := normalizeOwnedMembers(members)
	if err != nil {
		return stageOwnership{}, err
	}
	return stageOwnership{
		SchemaVersion: stageOwnerSchema,
		ManifestPath:  manifestName,
		Generation:    generation,
		Nonce:         nonce,
		Purpose:       purpose,
		Stage:         stage,
		Members:       normalized,
	}, nil
}

func normalizeOwnedMembers(members []string) ([]string, error) {
	if len(members) == 0 || len(members) > 8 {
		return nil, fmt.Errorf("artifact stage member inventory has invalid size %d", len(members))
	}
	seen := make(map[string]bool, len(members))
	normalized := append([]string(nil), members...)
	for _, name := range normalized {
		if !portableName(name) || name == stageOwnerName || seen[name] {
			return nil, fmt.Errorf("artifact stage member %q is invalid or duplicated", name)
		}
		seen[name] = true
	}
	sort.Strings(normalized)
	return normalized, nil
}

func openOwnedStage(
	root *os.Root,
	manifestName string,
	name string,
) (*os.Root, os.FileInfo, stageOwnership, error) {
	kind, generation, nonce, ok := parseOwnedStageName(name, manifestName)
	if !ok {
		return nil, nil, stageOwnership{}, fmt.Errorf("artifact stage name is invalid")
	}
	before, err := root.Lstat(name)
	if err != nil {
		return nil, nil, stageOwnership{}, err
	}
	if !before.IsDir() {
		return nil, nil, stageOwnership{}, fmt.Errorf("artifact stage is not a real directory")
	}
	stageRoot, err := root.OpenRoot(name)
	if err != nil {
		return nil, nil, stageOwnership{}, err
	}
	after, err := stageRoot.Stat(".")
	if err != nil || !after.IsDir() || !os.SameFile(before, after) {
		_ = stageRoot.Close()
		if err != nil {
			return nil, nil, stageOwnership{}, err
		}
		return nil, nil, stageOwnership{}, fmt.Errorf("artifact stage changed while it was opened")
	}
	data, err := readRegular(stageRoot, stageOwnerName, maxStageOwnerSize)
	if err != nil {
		_ = stageRoot.Close()
		return nil, nil, stageOwnership{}, err
	}
	owner, err := decodeStageOwnership(data)
	if err != nil {
		_ = stageRoot.Close()
		return nil, nil, stageOwnership{}, err
	}
	normalized, err := normalizeOwnedMembers(owner.Members)
	if err != nil || owner.SchemaVersion != stageOwnerSchema || owner.ManifestPath != manifestName ||
		owner.Generation != generation || owner.Nonce != nonce || owner.Stage != name ||
		(kind == "generation" && owner.Purpose != "generation") ||
		(kind == "facade" && !strings.HasPrefix(owner.Purpose, "facade:")) ||
		!equalStrings(normalized, owner.Members) {
		_ = stageRoot.Close()
		return nil, nil, stageOwnership{}, fmt.Errorf("artifact stage ownership record is invalid")
	}
	allowed := make(map[string]bool, len(owner.Members)+1)
	allowed[stageOwnerName] = true
	for _, member := range owner.Members {
		allowed[member] = true
	}
	entries, err := readRealDirectoryBounded(stageRoot, ".", len(allowed))
	if err != nil {
		_ = stageRoot.Close()
		return nil, nil, stageOwnership{}, err
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			_ = stageRoot.Close()
			return nil, nil, stageOwnership{}, fmt.Errorf("artifact stage contains unowned member %q", entry.Name())
		}
		info, err := stageRoot.Lstat(entry.Name())
		if err != nil || !info.Mode().IsRegular() {
			_ = stageRoot.Close()
			if err != nil {
				return nil, nil, stageOwnership{}, err
			}
			return nil, nil, stageOwnership{}, fmt.Errorf("artifact stage member %q is not regular", entry.Name())
		}
	}
	return stageRoot, before, owner, nil
}

func removeOwnedStage(
	directory *pinnedDirectory,
	manifestName string,
	name string,
	hook commitBarrierHook,
) error {
	stageRoot, before, owner, err := openOwnedStage(directory.root, manifestName, name)
	if err != nil {
		// A prefix collision or an invalid/missing ownership record belongs
		// to the user. It is deliberately preserved.
		return nil
	}
	removedMember := false
	for _, member := range owner.Members {
		removed, err := removeRegularIfPresent(stageRoot, member)
		if err != nil {
			_ = stageRoot.Close()
			return err
		}
		if removed && !removedMember {
			removedMember = true
			if err := runCommitBarrier(hook, commitBarrier{phase: barrierGenerationPruneDelete}); err != nil {
				_ = stageRoot.Close()
				return err
			}
		}
	}
	if err := syncRootDirectory(stageRoot, ".", "owned artifact stage cleanup"); err != nil {
		_ = stageRoot.Close()
		return err
	}
	if _, err := removeRegularIfPresent(stageRoot, stageOwnerName); err != nil {
		_ = stageRoot.Close()
		return err
	}
	if err := syncRootDirectory(stageRoot, ".", "owned artifact stage ownership cleanup"); err != nil {
		_ = stageRoot.Close()
		return err
	}
	opened, err := stageRoot.Stat(".")
	if err != nil {
		_ = stageRoot.Close()
		return err
	}
	current, err := directory.root.Lstat(name)
	if err != nil || !os.SameFile(before, opened) || !os.SameFile(before, current) {
		_ = stageRoot.Close()
		if err != nil {
			return err
		}
		return fmt.Errorf("owned artifact stage %q changed during cleanup", name)
	}
	if err := stageRoot.Close(); err != nil {
		return err
	}
	if err := directory.root.Remove(name); err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		return err
	}
	return nil
}

func newStageNonce() (string, error) {
	value := make([]byte, stageNonceBytes)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate artifact stage nonce: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func validStageNonce(value string) bool {
	return len(value) == stageNonceBytes*2 && strings.ToLower(value) == value && validHex(value)
}

func validHex(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func decodeStageOwnership(data []byte) (stageOwnership, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var owner stageOwnership
	if err := decoder.Decode(&owner); err != nil {
		return stageOwnership{}, err
	}
	if err := requireJSONEnd(decoder); err != nil {
		return stageOwnership{}, err
	}
	return owner, nil
}

func readGenerationOwnership(
	root *os.Root,
	manifestName string,
	generation string,
	members []string,
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
	normalized, err := normalizeOwnedMembers(members)
	if err != nil {
		return stageOwnership{}, false, err
	}
	kind, stageGeneration, nonce, validStage := parseOwnedStageName(owner.Stage, manifestName)
	if owner.SchemaVersion != stageOwnerSchema || owner.ManifestPath != manifestName ||
		owner.Generation != generation || owner.Purpose != "generation" ||
		!validStage || kind != "generation" || stageGeneration != generation ||
		nonce != owner.Nonce || !equalStrings(normalized, owner.Members) {
		return stageOwnership{}, false, fmt.Errorf("generation ownership record is invalid")
	}
	return owner, true, nil
}

func ensureGenerationOwnership(
	root *os.Root,
	manifestName string,
	generation string,
	members []string,
) (stageOwnership, error) {
	owner, found, err := readGenerationOwnership(root, manifestName, generation, members)
	if err != nil || found {
		return owner, err
	}
	nonce, err := newStageNonce()
	if err != nil {
		return stageOwnership{}, err
	}
	stage := ownedStageName(manifestName, generation, "generation", nonce)
	owner, err = newStageOwnership(manifestName, generation, nonce, "generation", stage, members)
	if err != nil {
		return stageOwnership{}, err
	}
	data, err := json.Marshal(owner)
	if err != nil {
		return stageOwnership{}, fmt.Errorf("encode generation ownership: %w", err)
	}
	if err := writeStageFile(root, stageOwnerName, append(data, '\n')); err != nil {
		return stageOwnership{}, fmt.Errorf("write generation ownership: %w", err)
	}
	if err := syncRootDirectory(root, ".", "artifact generation ownership"); err != nil {
		return stageOwnership{}, err
	}
	return owner, nil
}
