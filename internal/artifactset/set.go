// Package artifactset publishes and verifies migration artifacts as committed
// generations while preserving their stable, user-facing filenames.
package artifactset

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mansiverma897993/noz-in/internal/atomicfile"
	"github.com/mansiverma897993/noz-in/internal/safeoutput"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
)

const (
	manifestSchemaVersion = 1
	maxManifestSize       = 64 << 10
	pointerSchemaVersion  = 1
	maxPointerSize        = 4 << 10
	maxStageOwnerSize     = 4 << 10
	stageOwnerSchema      = 1
	stageNonceBytes       = 16
	maxOwnedStages        = 8
	// maxArtifactRootEntries bounds the stale-stage directory scan. Each migrated
	// dashboard set occupies roughly eight entries, so this supports on the order
	// of eight thousand dashboards in a single output directory before an operator
	// must split the run across directories.
	maxArtifactRootEntries = 65536
	// A valid container has the pointer-bound current and optional previous
	// generation plus, only after an interrupted pre-pointer publication, one
	// fully written orphan.
	maxRetainedGenerationDirectories = 3
	maxGenerationContainerEntries    = maxRetainedGenerationDirectories + 1

	// MaxMemberSize is the largest artifact that can be published in a set.
	// The limit is shared by writers and readers so Commit cannot create a
	// generation that the application and MCP consumers cannot later verify.
	MaxMemberSize int64 = 64 << 20
	// MaxSetSize bounds the aggregate member bytes retained while verifying or
	// carrying a generation forward. Reserving maxManifestSize keeps the whole
	// on-disk set, including its commit manifest, inside 256 MiB.
	MaxSetSize int64 = 4*MaxMemberSize - maxManifestSize
)

// Kind identifies the topology and stable names of an artifact set.
type Kind string

const (
	KindDashboard Kind = "dashboard"
	KindRules     Kind = "rules"
)

// Role identifies one member of a committed artifact generation.
type Role string

const (
	RolePrimary   Role = "primary"
	RoleCandidate Role = "candidate"
	RoleReport    Role = "report"
	RoleHTML      Role = "html"
)

// Artifact is one fully rendered member to publish.
type Artifact struct {
	Role Role
	Path string
	Data []byte
}

// Entry binds an artifact's stable filename to its exact committed bytes.
type Entry struct {
	Role      Role   `json:"role"`
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"sizeBytes"`
}

// Manifest is the final commit record for one complete generation.
type Manifest struct {
	SchemaVersion int     `json:"schemaVersion"`
	Kind          Kind    `json:"kind"`
	Generation    string  `json:"generation"`
	Artifacts     []Entry `json:"artifacts"`
}

type generationPointer struct {
	SchemaVersion             int    `json:"schemaVersion"`
	ManifestPath              string `json:"manifestPath"`
	Generation                string `json:"generation"`
	PreviousGeneration        string `json:"previousGeneration,omitempty"`
	ManifestSHA256            string `json:"manifestSHA256"`
	ManifestSizeBytes         int64  `json:"manifestSizeBytes"`
	PreviousManifestSHA256    string `json:"previousManifestSHA256,omitempty"`
	PreviousManifestSizeBytes int64  `json:"previousManifestSizeBytes,omitempty"`
}

// Snapshot contains requested member bytes from one verified generation.
type Snapshot struct {
	Manifest Manifest
	Data     map[string][]byte
}

type pinnedDirectory struct {
	path      string
	root      *os.Root
	file      *os.File
	directory *safeoutput.PinnedDirectory
}

func openPinnedDirectory(path string) (*pinnedDirectory, error) {
	directory, err := safeoutput.OpenDirectory(path)
	if err != nil {
		return nil, fmt.Errorf("open artifact directory %q: %w", path, err)
	}
	return &pinnedDirectory{
		path: directory.Path(), root: directory.Root(), file: directory.File(), directory: directory,
	}, nil
}

func (directory *pinnedDirectory) Close() error {
	return directory.directory.Close()
}

func (directory *pinnedDirectory) Sync() error {
	if err := atomicfile.SyncOpenedDirectory(directory.file); err != nil {
		return fmt.Errorf("sync pinned artifact directory %q: %w", directory.path, err)
	}
	return nil
}

func openLockedPinnedDirectory(path, lockFileName string) (*pinnedDirectory, *setLock, error) {
	directory, err := openPinnedDirectory(path)
	if err != nil {
		return nil, nil, err
	}
	lock, err := acquireRooted(directory.root, directory.path, lockFileName)
	if err != nil {
		_ = directory.Close()
		return nil, nil, err
	}
	if err := lock.verifyRoot(directory.root, lockFileName); err != nil {
		_ = lock.Close()
		_ = directory.Close()
		return nil, nil, err
	}
	return directory, lock, nil
}

// NewBindingForReport returns a new generation binding using the canonical
// manifest name adjacent to reportPath.
func NewBindingForReport(reportPath string, kind Kind) (reporttypes.ArtifactSetBinding, error) {
	manifestName, err := expectedManifestName(filepath.Base(reportPath), kind)
	if err != nil {
		return reporttypes.ArtifactSetBinding{}, err
	}
	if !portableName(manifestName) {
		return reporttypes.ArtifactSetBinding{}, fmt.Errorf("artifact-set manifest path %q is not portable", manifestName)
	}
	expected, err := expectedArtifactNames(filepath.Base(reportPath), kind)
	if err != nil {
		return reporttypes.ArtifactSetBinding{}, err
	}
	for _, name := range expected {
		if !portableName(name) {
			return reporttypes.ArtifactSetBinding{}, fmt.Errorf("artifact path %q is not portable", name)
		}
	}
	generation, err := newGeneration()
	if err != nil {
		return reporttypes.ArtifactSetBinding{}, err
	}
	return reporttypes.ArtifactSetBinding{Path: manifestName, Generation: generation}, nil
}

// NextBinding advances an existing set while retaining its stable manifest
// filename.
func NextBinding(current reporttypes.ArtifactSetBinding) (reporttypes.ArtifactSetBinding, error) {
	if !portableName(current.Path) {
		return reporttypes.ArtifactSetBinding{}, fmt.Errorf("artifact-set manifest path %q is not a portable filename", current.Path)
	}
	generation, err := newGeneration()
	if err != nil {
		return reporttypes.ArtifactSetBinding{}, err
	}
	return reporttypes.ArtifactSetBinding{Path: current.Path, Generation: generation}, nil
}
