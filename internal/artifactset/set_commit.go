package artifactset

// This file publishes new immutable generations and refreshes the stable,
// user-facing facades.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/mansiverma897993/signoz/internal/atomicfile"
	"github.com/mansiverma897993/signoz/pkg/reporttypes"
)

// commitBarrier exists so package tests can stop a publication after each
// durability boundary. Production callers always use a nil hook. Keeping the
// hook on the call stack, rather than in package-global state, means the race
// detector can exercise concurrent commits without test-only synchronization.
type commitBarrier struct {
	phase string
	role  Role
}

type commitBarrierHook func(commitBarrier) error

const (
	barrierGenerationMember         = "generation-member"
	barrierGenerationManifest       = "generation-manifest"
	barrierGenerationPublish        = "generation-publish"
	barrierGenerationPointer        = "generation-pointer"
	barrierGenerationPreflightPrune = "generation-preflight-prune"
	barrierGenerationPrune          = "generation-prune"
	barrierGenerationPruneRename    = "generation-prune-rename"
	barrierGenerationPruneDelete    = "generation-prune-delete"
	barrierFacadeMember             = "facade-member"
	barrierFacadeManifest           = "facade-manifest"
	barrierFacadeReport             = "facade-report"
)

// Commit first publishes a complete immutable generation and then refreshes
// the stable, user-facing filenames. The report facade is replaced last, so it
// can only declare a generation whose files and manifest were already made
// durable. Readers resolve the generation declared by the report rather than
// trusting whichever facade happens to be current during a concurrent update.
//
// On platforms that support flushing directory handles, generation and facade
// rename barriers are persisted before Commit returns. Windows flushes each
// replacement file, but Go cannot flush its containing directory handle.
func Commit(
	reportPath string,
	binding reporttypes.ArtifactSetBinding,
	kind Kind,
	artifacts []Artifact,
) error {
	return commitWithBarrier(reportPath, binding, kind, artifacts, nil)
}

func commitWithBarrier(
	reportPath string,
	binding reporttypes.ArtifactSetBinding,
	kind Kind,
	artifacts []Artifact,
	hook commitBarrierHook,
) error {
	normalized, err := normalizeArtifacts(reportPath, binding, kind, artifacts)
	if err != nil {
		return err
	}
	directory, lock, err := openLockedPinnedDirectory(filepath.Dir(reportPath), lockName(binding.Path))
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	defer func() { _ = lock.Close() }()
	return commitLocked(directory, filepath.Base(reportPath), binding, kind, normalized, hook)
}

// Update publishes a new generation by replacing selected members and carrying
// forward every other member from the verified current generation.
func Update(
	reportPath string,
	current reporttypes.ArtifactSetBinding,
	next reporttypes.ArtifactSetBinding,
	kind Kind,
	replacements []Artifact,
) error {
	if err := validateBinding(reportPath, current, kind); err != nil {
		return err
	}
	if err := validateBinding(reportPath, next, kind); err != nil {
		return err
	}
	if current.Path != next.Path || current.Generation == next.Generation {
		return fmt.Errorf("artifact-set update must retain the manifest path and advance the generation")
	}
	directory, lock, err := openLockedPinnedDirectory(filepath.Dir(reportPath), lockName(current.Path))
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	defer func() { _ = lock.Close() }()
	manifest, currentData, err := readGenerationLocked(directory.root, current, kind)
	if err != nil {
		return fmt.Errorf("load current artifact generation: %w", err)
	}

	byRole := make(map[Role]Artifact, len(manifest.Artifacts))
	for _, entry := range manifest.Artifacts {
		byRole[entry.Role] = Artifact{
			Role: entry.Role,
			Path: filepath.Join(directory.path, entry.Path),
			Data: currentData[entry.Path],
		}
	}
	seenReplacement := make(map[Role]bool, len(replacements))
	for _, replacement := range replacements {
		if seenReplacement[replacement.Role] {
			return fmt.Errorf("artifact-set update contains duplicate %q replacement", replacement.Role)
		}
		seenReplacement[replacement.Role] = true
		currentArtifact, found := byRole[replacement.Role]
		if !found {
			return fmt.Errorf("artifact-set update cannot add absent %q role", replacement.Role)
		}
		if !samePath(currentArtifact.Path, replacement.Path) {
			return fmt.Errorf("artifact-set update cannot move %q from %q to %q", replacement.Role, currentArtifact.Path, replacement.Path)
		}
		byRole[replacement.Role] = Artifact{
			Role: replacement.Role,
			Path: replacement.Path,
			Data: replacement.Data,
		}
	}
	if !seenReplacement[RoleReport] || !seenReplacement[RoleHTML] {
		return fmt.Errorf("artifact-set update must replace both report and HTML members")
	}
	merged := make([]Artifact, 0, len(byRole))
	for _, artifact := range byRole {
		merged = append(merged, artifact)
	}
	normalized, err := normalizeArtifacts(reportPath, next, kind, merged)
	if err != nil {
		return err
	}
	return commitLocked(directory, filepath.Base(reportPath), next, kind, normalized, nil)
}

func commitLocked(
	directory *pinnedDirectory,
	reportName string,
	binding reporttypes.ArtifactSetBinding,
	kind Kind,
	artifacts []Artifact,
	hooks ...commitBarrierHook,
) error {
	var hook commitBarrierHook
	if len(hooks) > 0 {
		hook = hooks[0]
	}
	if err := directory.file.Chmod(0o700); err != nil {
		return fmt.Errorf("set artifact directory permissions %q: %w", directory.path, err)
	}
	if err := cleanupPruneTombstones(directory, binding.Path); err != nil {
		return err
	}
	if err := cleanupStages(directory, binding.Path); err != nil {
		return err
	}
	if err := preflightDestinations(directory, binding.Path, kind, artifacts); err != nil {
		return err
	}
	if err := preflightRetainedGenerationsLocked(
		directory,
		reportName,
		binding.Path,
		kind,
		hook,
	); err != nil {
		return err
	}
	// The first commit after upgrading snapshots a valid flat generation before
	// any facade can be replaced. This closes the only transition window in
	// which an old report would otherwise depend on mutable stable members.
	if err := preserveFlatGenerationLocked(directory, binding.Path, kind); err != nil {
		return err
	}
	manifestData, err := publishImmutableGenerationLocked(directory, binding, kind, artifacts, hook)
	if err != nil {
		return err
	}
	if err := publishFacadesLocked(directory, reportName, binding, kind, artifacts, manifestData, hook); err != nil {
		return err
	}
	return pruneCommittedGenerationsLocked(directory, reportName, binding, kind, hook)
}

func preserveFlatGenerationLocked(directory *pinnedDirectory, manifestName string, kind Kind) error {
	manifestData, err := readRegular(directory.root, manifestName, maxManifestSize)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read existing artifact commit manifest %q: %w", manifestName, err)
	}
	var probe struct {
		Generation string `json:"generation"`
	}
	if err := json.Unmarshal(manifestData, &probe); err != nil || !validGeneration(probe.Generation) {
		if err != nil {
			return fmt.Errorf("decode existing artifact commit manifest %q: %w", manifestName, err)
		}
		return fmt.Errorf("existing artifact commit manifest %q has an invalid generation", manifestName)
	}
	binding := reporttypes.ArtifactSetBinding{Path: manifestName, Generation: probe.Generation}
	generationRoot, immutable, err := openGenerationRoot(directory.root, manifestName, probe.Generation)
	if err != nil {
		return err
	}
	if immutable {
		return generationRoot.Close()
	}
	manifest, err := decodeManifest(manifestData, manifestName, kind, probe.Generation)
	if err != nil {
		return err
	}
	data, err := verifyMembersLocked(directory.root, manifest, nil, MaxMemberSize, MaxSetSize, true)
	if err != nil {
		return fmt.Errorf("verify flat artifact generation %q: %w", probe.Generation, err)
	}
	artifacts := make([]Artifact, 0, len(manifest.Artifacts))
	for _, entry := range manifest.Artifacts {
		artifacts = append(artifacts, Artifact{
			Role: entry.Role, Path: filepath.Join(directory.path, entry.Path), Data: data[entry.Path],
		})
	}
	if _, err := publishImmutableGenerationLocked(directory, binding, kind, artifacts, nil); err != nil {
		return fmt.Errorf("preserve flat artifact generation %q: %w", probe.Generation, err)
	}
	return nil
}

func publishImmutableGenerationLocked(
	directory *pinnedDirectory,
	binding reporttypes.ArtifactSetBinding,
	kind Kind,
	artifacts []Artifact,
	hook commitBarrierHook,
) ([]byte, error) {
	container := generationContainerName(binding.Path)
	if err := ensureGenerationContainer(directory, container); err != nil {
		return nil, err
	}
	generationPath := filepath.Join(container, binding.Generation)
	if _, err := directory.root.Lstat(generationPath); err == nil {
		return nil, fmt.Errorf("artifact generation %q already exists", binding.Generation)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect artifact generation %q: %w", binding.Generation, err)
	}

	ownedMembers := make([]string, 0, len(artifacts)+1)
	ownedMembers = append(ownedMembers, binding.Path)
	for _, artifact := range artifacts {
		ownedMembers = append(ownedMembers, filepath.Base(artifact.Path))
	}
	stage, err := createOwnedStage(
		directory,
		binding.Path,
		binding.Generation,
		"generation",
		ownedMembers,
	)
	if err != nil {
		return nil, fmt.Errorf("create artifact generation stage in %q: %w", directory.path, err)
	}
	defer func() { _ = removeOwnedStage(directory, binding.Path, stage, nil) }()

	manifest := Manifest{
		SchemaVersion: manifestSchemaVersion,
		Kind:          kind,
		Generation:    binding.Generation,
		Artifacts:     make([]Entry, 0, len(artifacts)),
	}
	for _, artifact := range artifacts {
		digest := sha256.Sum256(artifact.Data)
		manifest.Artifacts = append(manifest.Artifacts, Entry{
			Role: artifact.Role, Path: filepath.Base(artifact.Path),
			SHA256: hex.EncodeToString(digest[:]), SizeBytes: int64(len(artifact.Data)),
		})
		memberPath := filepath.Join(stage, filepath.Base(artifact.Path))
		if err := writeStageFile(directory.root, memberPath, artifact.Data); err != nil {
			return nil, err
		}
		if err := runCommitBarrier(hook, commitBarrier{phase: barrierGenerationMember, role: artifact.Role}); err != nil {
			return nil, err
		}
	}
	sort.Slice(manifest.Artifacts, func(left, right int) bool {
		return manifest.Artifacts[left].Path < manifest.Artifacts[right].Path
	})
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode artifact commit manifest: %w", err)
	}
	manifestData = append(manifestData, '\n')
	if err := writeStageFile(directory.root, filepath.Join(stage, binding.Path), manifestData); err != nil {
		return nil, err
	}
	if err := runCommitBarrier(hook, commitBarrier{phase: barrierGenerationManifest}); err != nil {
		return nil, err
	}
	if err := syncRootDirectory(directory.root, stage, "staged artifact generation"); err != nil {
		return nil, err
	}
	if err := directory.root.Rename(stage, generationPath); err != nil {
		return nil, fmt.Errorf("publish immutable artifact generation %q: %w", binding.Generation, err)
	}
	if err := syncRootDirectory(directory.root, container, "artifact generation container"); err != nil {
		return nil, err
	}
	if err := runCommitBarrier(hook, commitBarrier{phase: barrierGenerationPublish}); err != nil {
		return nil, err
	}
	return manifestData, nil
}

func publishFacadesLocked(
	directory *pinnedDirectory,
	reportName string,
	binding reporttypes.ArtifactSetBinding,
	kind Kind,
	artifacts []Artifact,
	manifestData []byte,
	hook commitBarrierHook,
) error {
	previousGeneration, previousManifestData, err := stableReportGeneration(
		directory.root,
		reportName,
		binding.Path,
		kind,
	)
	if err != nil {
		return err
	}
	pointerData, err := encodeGenerationPointer(
		binding,
		previousGeneration,
		previousManifestData,
		manifestData,
	)
	if err != nil {
		return err
	}
	if err := replaceFacadeLocked(
		directory,
		binding.Path,
		binding.Generation,
		"pointer",
		currentPointerName(binding.Path),
		pointerData,
	); err != nil {
		return err
	}
	if err := runCommitBarrier(hook, commitBarrier{phase: barrierGenerationPointer}); err != nil {
		return err
	}
	for _, artifact := range artifacts {
		if artifact.Role == RoleReport {
			continue
		}
		if err := replaceFacadeLocked(
			directory,
			binding.Path,
			binding.Generation,
			artifact.Role,
			filepath.Base(artifact.Path),
			artifact.Data,
		); err != nil {
			return err
		}
		if err := runCommitBarrier(hook, commitBarrier{phase: barrierFacadeMember, role: artifact.Role}); err != nil {
			return err
		}
	}
	if kind == KindDashboard && !hasRole(artifacts, RoleCandidate) {
		candidateName := expectedCandidateName(reportName)
		removed, err := removeRegularIfPresent(directory.root, candidateName)
		if err != nil {
			return err
		}
		if removed {
			if err := directory.Sync(); err != nil {
				return fmt.Errorf("persist removal of stale candidate artifact %q: %w", candidateName, err)
			}
			if err := runCommitBarrier(hook, commitBarrier{phase: barrierFacadeMember, role: RoleCandidate}); err != nil {
				return err
			}
		}
	}
	if err := replaceFacadeLocked(
		directory,
		binding.Path,
		binding.Generation,
		"manifest",
		binding.Path,
		manifestData,
	); err != nil {
		return err
	}
	if err := runCommitBarrier(hook, commitBarrier{phase: barrierFacadeManifest}); err != nil {
		return err
	}
	reportArtifact := normalizedArtifact(artifacts, RoleReport)
	if err := replaceFacadeLocked(
		directory,
		binding.Path,
		binding.Generation,
		RoleReport,
		filepath.Base(reportArtifact.Path),
		reportArtifact.Data,
	); err != nil {
		return err
	}
	return runCommitBarrier(hook, commitBarrier{phase: barrierFacadeReport, role: RoleReport})
}

func stableReportGeneration(
	root *os.Root,
	reportName string,
	manifestName string,
	kind Kind,
) (string, []byte, error) {
	data, err := readRegular(root, reportName, MaxMemberSize)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, fmt.Errorf("read previous artifact report %q: %w", reportName, err)
	}
	var report struct {
		ArtifactSet *reporttypes.ArtifactSetBinding `json:"artifactSet"`
	}
	if err := json.Unmarshal(data, &report); err != nil || report.ArtifactSet == nil {
		return "", nil, nil
	}
	if report.ArtifactSet.Path != manifestName || !validGeneration(report.ArtifactSet.Generation) {
		return "", nil, nil
	}
	generationRoot, immutable, err := openGenerationRoot(root, manifestName, report.ArtifactSet.Generation)
	if err != nil {
		return "", nil, err
	}
	if immutable {
		defer func() { _ = generationRoot.Close() }()
	}
	manifestData, err := readRegular(generationRoot, manifestName, maxManifestSize)
	if err != nil {
		return "", nil, fmt.Errorf("read previous immutable manifest %q: %w", manifestName, err)
	}
	if _, err := decodeManifest(manifestData, manifestName, kind, report.ArtifactSet.Generation); err != nil {
		return "", nil, err
	}
	return report.ArtifactSet.Generation, manifestData, nil
}

func encodeGenerationPointer(
	binding reporttypes.ArtifactSetBinding,
	previousGeneration string,
	previousManifestData []byte,
	manifestData []byte,
) ([]byte, error) {
	digest := sha256.Sum256(manifestData)
	pointer := generationPointer{
		SchemaVersion:      pointerSchemaVersion,
		ManifestPath:       binding.Path,
		Generation:         binding.Generation,
		PreviousGeneration: previousGeneration,
		ManifestSHA256:     hex.EncodeToString(digest[:]),
		ManifestSizeBytes:  int64(len(manifestData)),
	}
	if previousGeneration != "" {
		previousDigest := sha256.Sum256(previousManifestData)
		pointer.PreviousManifestSHA256 = hex.EncodeToString(previousDigest[:])
		pointer.PreviousManifestSizeBytes = int64(len(previousManifestData))
	}
	data, err := json.MarshalIndent(pointer, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode artifact generation pointer: %w", err)
	}
	return append(data, '\n'), nil
}

func replaceFacadeLocked(
	directory *pinnedDirectory,
	manifestName string,
	generation string,
	role Role,
	destination string,
	data []byte,
) error {
	const payloadName = "payload"
	stage, err := createOwnedStage(
		directory,
		manifestName,
		generation,
		"facade:"+string(role),
		[]string{payloadName},
	)
	if err != nil {
		return err
	}
	defer func() { _ = removeOwnedStage(directory, manifestName, stage, nil) }()
	temporary := filepath.Join(stage, payloadName)
	if err := writeStageFile(directory.root, temporary, data); err != nil {
		return err
	}
	if err := syncRootDirectory(directory.root, stage, "facade artifact stage"); err != nil {
		return err
	}
	if err := atomicfile.ReplaceRoot(directory.root, temporary, destination); err != nil {
		return fmt.Errorf("publish %s artifact facade %q: %w", role, destination, err)
	}
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("persist %s artifact facade %q: %w", role, destination, err)
	}
	return nil
}

func ensureGenerationContainer(directory *pinnedDirectory, name string) error {
	info, err := directory.root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		if err := directory.root.Mkdir(name, 0o700); err != nil {
			return fmt.Errorf("create artifact generation container %q: %w", name, err)
		}
		if err := directory.Sync(); err != nil {
			return fmt.Errorf("persist artifact generation container %q: %w", name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect artifact generation container %q: %w", name, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("artifact generation container %q is not a directory", name)
	}
	return nil
}

func runCommitBarrier(hook commitBarrierHook, barrier commitBarrier) error {
	if hook == nil {
		return nil
	}
	if err := hook(barrier); err != nil {
		return fmt.Errorf("artifact publication stopped after %s barrier: %w", barrier.phase, err)
	}
	return nil
}
