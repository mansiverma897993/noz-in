package artifactset

// This file validates artifact inputs, manifests, and committed member bytes.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/mansiverma897993/signoz/pkg/reporttypes"
)

func normalizeArtifacts(
	reportPath string,
	binding reporttypes.ArtifactSetBinding,
	kind Kind,
	artifacts []Artifact,
) ([]Artifact, error) {
	if err := validateBinding(reportPath, binding, kind); err != nil {
		return nil, err
	}
	expected, err := expectedArtifactNames(filepath.Base(reportPath), kind)
	if err != nil {
		return nil, err
	}
	if len(artifacts) == 0 {
		return nil, fmt.Errorf("artifact set is empty")
	}
	directory, err := filepath.Abs(filepath.Dir(reportPath))
	if err != nil {
		return nil, fmt.Errorf("resolve artifact directory: %w", err)
	}
	seenRoles := make(map[Role]bool, len(artifacts))
	seenPaths := make(map[string]bool, len(artifacts))
	normalized := make([]Artifact, 0, len(artifacts))
	var totalSize int64
	for _, artifact := range artifacts {
		name, allowed := expected[artifact.Role]
		if !allowed {
			return nil, fmt.Errorf("artifact role %q is not allowed for %s sets", artifact.Role, kind)
		}
		if seenRoles[artifact.Role] {
			return nil, fmt.Errorf("artifact set contains duplicate %q role", artifact.Role)
		}
		seenRoles[artifact.Role] = true
		absolute, err := filepath.Abs(artifact.Path)
		if err != nil {
			return nil, fmt.Errorf("resolve %s artifact path %q: %w", artifact.Role, artifact.Path, err)
		}
		if !platformPathEqual(filepath.Dir(absolute), directory) || !platformPathEqual(filepath.Base(absolute), name) {
			return nil, fmt.Errorf("%s artifact path %q must be adjacent as %q", artifact.Role, artifact.Path, name)
		}
		if seenPaths[name] {
			return nil, fmt.Errorf("artifact set contains duplicate path %q", name)
		}
		seenPaths[name] = true
		if len(artifact.Data) == 0 {
			return nil, fmt.Errorf("%s artifact %q is empty", artifact.Role, artifact.Path)
		}
		size := int64(len(artifact.Data))
		if size > MaxMemberSize {
			return nil, fmt.Errorf("%s artifact %q exceeds %d bytes", artifact.Role, artifact.Path, MaxMemberSize)
		}
		if totalSize > MaxSetSize-size {
			return nil, fmt.Errorf("artifact set exceeds %d bytes", MaxSetSize)
		}
		totalSize += size
		normalized = append(normalized, Artifact{
			Role: artifact.Role, Path: absolute, Data: append([]byte(nil), artifact.Data...),
		})
	}
	for _, required := range []Role{RolePrimary, RoleReport, RoleHTML} {
		if !seenRoles[required] {
			return nil, fmt.Errorf("artifact set has no %s member", required)
		}
	}
	reportArtifact := normalizedArtifact(normalized, RoleReport)
	primaryArtifact := normalizedArtifact(normalized, RolePrimary)
	if err := verifyDeclaredBindings(reportArtifact.Data, binding, primaryArtifact); err != nil {
		return nil, err
	}
	sort.Slice(normalized, func(left, right int) bool {
		return filepath.Base(normalized[left].Path) < filepath.Base(normalized[right].Path)
	})
	return normalized, nil
}

func validateBinding(reportPath string, binding reporttypes.ArtifactSetBinding, kind Kind) error {
	expected, err := expectedManifestName(filepath.Base(reportPath), kind)
	if err != nil {
		return err
	}
	if binding.Path != expected || !portableName(binding.Path) {
		return fmt.Errorf("artifact-set manifest path %q must be %q", binding.Path, expected)
	}
	if !validGeneration(binding.Generation) {
		return fmt.Errorf("artifact-set generation %q is invalid", binding.Generation)
	}
	return nil
}

func verifyDeclaredBindings(
	data []byte,
	expectedSet reporttypes.ArtifactSetBinding,
	primary Artifact,
) error {
	var report struct {
		ArtifactSet     *reporttypes.ArtifactSetBinding `json:"artifactSet"`
		PrimaryArtifact *reporttypes.ArtifactBinding    `json:"primaryArtifact"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		return fmt.Errorf("decode report artifact-set binding: %w", err)
	}
	if report.ArtifactSet == nil {
		return fmt.Errorf("report does not declare its artifact-set binding")
	}
	if *report.ArtifactSet != expectedSet {
		return fmt.Errorf("report artifact-set binding does not match generation being committed")
	}
	if report.PrimaryArtifact == nil {
		return fmt.Errorf("report does not declare its primary artifact binding")
	}
	digest := sha256.Sum256(primary.Data)
	expectedPrimary := reporttypes.ArtifactBinding{
		Path: filepath.Base(primary.Path), SHA256: hex.EncodeToString(digest[:]), SizeBytes: int64(len(primary.Data)),
	}
	if *report.PrimaryArtifact != expectedPrimary {
		return fmt.Errorf("report primary artifact binding does not match primary member being committed")
	}
	return nil
}

func decodeManifest(data []byte, name string, kind Kind, generation string) (Manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode artifact commit manifest %q: %w", name, err)
	}
	if err := requireJSONEnd(decoder); err != nil {
		return Manifest{}, fmt.Errorf("decode artifact commit manifest %q: %w", name, err)
	}
	if manifest.SchemaVersion != manifestSchemaVersion {
		return Manifest{}, fmt.Errorf("artifact commit manifest uses unsupported schema version %d", manifest.SchemaVersion)
	}
	if manifest.Kind != kind {
		return Manifest{}, fmt.Errorf("artifact commit manifest kind %q does not match %q", manifest.Kind, kind)
	}
	if manifest.Generation != generation {
		return Manifest{}, fmt.Errorf(
			"artifact commit manifest generation %q does not match report generation %q",
			manifest.Generation,
			generation,
		)
	}
	if err := validateManifestTopology(manifest, name); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func validateManifestTopology(manifest Manifest, manifestName string) error {
	reportName, err := reportNameForManifest(manifestName, manifest.Kind)
	if err != nil {
		return err
	}
	expected, err := expectedArtifactNames(reportName, manifest.Kind)
	if err != nil {
		return err
	}
	seenRoles := make(map[Role]bool, len(manifest.Artifacts))
	seenPaths := make(map[string]bool, len(manifest.Artifacts))
	var totalSize int64
	for _, entry := range manifest.Artifacts {
		name, allowed := expected[entry.Role]
		if !allowed || entry.Path != name || !portableName(entry.Path) {
			return fmt.Errorf("commit manifest has invalid %q member path %q", entry.Role, entry.Path)
		}
		if seenRoles[entry.Role] || seenPaths[entry.Path] {
			return fmt.Errorf("commit manifest contains duplicate member %q", entry.Path)
		}
		seenRoles[entry.Role] = true
		seenPaths[entry.Path] = true
		if entry.SizeBytes <= 0 || entry.SizeBytes > MaxMemberSize || !validSHA256(entry.SHA256) {
			return fmt.Errorf("commit manifest member %q has an invalid size or SHA-256", entry.Path)
		}
		if totalSize > MaxSetSize-entry.SizeBytes {
			return fmt.Errorf("commit manifest artifact set exceeds %d bytes", MaxSetSize)
		}
		totalSize += entry.SizeBytes
	}
	for _, required := range []Role{RolePrimary, RoleReport, RoleHTML} {
		if !seenRoles[required] {
			return fmt.Errorf("commit manifest has no %s member", required)
		}
	}
	return nil
}

func verifyMembersLocked(
	root *os.Root,
	manifest Manifest,
	wanted map[string]bool,
	maxArtifactSize int64,
	maxTotalSize int64,
	keepAll bool,
) (map[string][]byte, error) {
	if maxArtifactSize <= 0 || maxTotalSize <= 0 {
		return nil, fmt.Errorf("artifact verification limits must be positive")
	}
	result := make(map[string][]byte)
	var total int64
	for _, entry := range manifest.Artifacts {
		if entry.SizeBytes > maxArtifactSize {
			return nil, fmt.Errorf("committed artifact %q exceeds %d bytes", entry.Path, maxArtifactSize)
		}
		total += entry.SizeBytes
		if total > maxTotalSize {
			return nil, fmt.Errorf("committed artifact set exceeds %d bytes", maxTotalSize)
		}
		keep := keepAll || wanted[entry.Path]
		data, err := readAndVerifyRooted(root, entry, keep)
		if err != nil {
			return nil, err
		}
		if keep {
			result[entry.Path] = data
		}
	}
	return result, nil
}

func readAndVerifyRooted(root *os.Root, entry Entry, keep bool) ([]byte, error) {
	before, err := root.Lstat(entry.Path)
	if err != nil {
		return nil, fmt.Errorf("inspect committed artifact %q: %w", entry.Path, err)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("committed artifact %q is not a regular file", entry.Path)
	}
	if before.Size() != entry.SizeBytes {
		return nil, fmt.Errorf(
			"committed artifact %q size %d does not match commit manifest size %d",
			entry.Path,
			before.Size(),
			entry.SizeBytes,
		)
	}
	file, err := root.Open(entry.Path)
	if err != nil {
		return nil, fmt.Errorf("open committed artifact %q: %w", entry.Path, err)
	}
	defer func() { _ = file.Close() }()
	after, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened committed artifact %q: %w", entry.Path, err)
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, fmt.Errorf("committed artifact %q changed while it was opened", entry.Path)
	}
	hash := sha256.New()
	var buffer bytes.Buffer
	writer := io.Writer(hash)
	if keep {
		writer = io.MultiWriter(hash, &buffer)
	}
	written, err := io.Copy(writer, io.LimitReader(file, entry.SizeBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read committed artifact %q: %w", entry.Path, err)
	}
	if written != entry.SizeBytes {
		return nil, fmt.Errorf(
			"committed artifact %q size %d does not match commit manifest size %d",
			entry.Path,
			written,
			entry.SizeBytes,
		)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != entry.SHA256 {
		return nil, fmt.Errorf(
			"committed artifact %q SHA-256 %q does not match commit manifest SHA-256 %q",
			entry.Path,
			actual,
			entry.SHA256,
		)
	}
	return buffer.Bytes(), nil
}

func verifyBytes(entry Entry, data []byte) error {
	if int64(len(data)) != entry.SizeBytes {
		return fmt.Errorf("size %d does not match committed size %d", len(data), entry.SizeBytes)
	}
	digest := sha256.Sum256(data)
	actual := hex.EncodeToString(digest[:])
	if actual != entry.SHA256 {
		return fmt.Errorf("SHA-256 %q does not match committed SHA-256 %q", actual, entry.SHA256)
	}
	return nil
}
