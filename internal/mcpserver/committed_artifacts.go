package mcpserver

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mansiverma897993/noz-in/internal/artifactset"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
)

type dashboardReportSnapshot struct {
	Evidence reporttypes.Report
	Members  map[string][]byte
}

// readDashboardReport reads the report and any requested members through one
// descriptor-pinned migration generation. Reports with an artifact-set binding
// are accepted only after the complete committed set has been verified.
func (service *Service) readDashboardReport(
	id string,
	state manifest,
	requested ...string,
) (dashboardReportSnapshot, error) {
	root, displayPath, err := service.openMigrationGenerationRoot(id, state.Generation)
	if err != nil {
		return dashboardReportSnapshot{}, err
	}
	defer func() { _ = root.Close() }()

	reportData, err := readRootedArtifactBounded(root, state.Report, displayPath, maxMCPArtifactSize)
	if err != nil {
		return dashboardReportSnapshot{}, err
	}
	evidence, err := decodeDashboardReport(reportData)
	if err != nil {
		return dashboardReportSnapshot{}, err
	}

	if evidence.ArtifactSet == nil {
		members := make(map[string][]byte, len(requested))
		for _, name := range requested {
			if err := validateManifestName("artifact", name); err != nil {
				return dashboardReportSnapshot{}, err
			}
			if _, found := members[name]; found {
				continue
			}
			data, err := readRootedArtifactBounded(root, name, displayPath, maxMCPArtifactSize)
			if err != nil {
				return dashboardReportSnapshot{}, err
			}
			members[name] = data
		}
		return dashboardReportSnapshot{Evidence: evidence, Members: members}, nil
	}

	committed, err := artifactset.ReadCommittedRoot(
		root,
		state.Report,
		reportData,
		evidence.ArtifactSet,
		artifactset.KindDashboard,
		requested,
		artifactset.MaxMemberSize,
	)
	if err != nil {
		return dashboardReportSnapshot{}, fmt.Errorf("verify committed dashboard artifact set: %w", err)
	}
	if err := verifyDashboardManifestBindings(state, evidence, committed.Manifest); err != nil {
		return dashboardReportSnapshot{}, err
	}
	return dashboardReportSnapshot{Evidence: evidence, Members: committed.Data}, nil
}

func (service *Service) openMigrationGenerationRoot(id, generation string) (*os.Root, string, error) {
	if _, err := service.migrationDirectory(id); err != nil {
		return nil, "", err
	}
	relative := id
	if generation != "" {
		if err := validateManifestName("generation", generation); err != nil {
			return nil, "", err
		}
		relative = filepath.Join(id, generation)
	}
	parent, err := openVerifiedRoot(service.config.OutputRoot, service.outputRootInfo)
	if err != nil {
		return nil, "", err
	}
	before, err := parent.Lstat(relative)
	if err != nil {
		_ = parent.Close()
		return nil, "", fmt.Errorf("inspect migration artifact directory %q: %w", relative, err)
	}
	if !before.IsDir() {
		_ = parent.Close()
		return nil, "", fmt.Errorf("migration artifact directory %q is not a real directory", relative)
	}
	root, err := parent.OpenRoot(relative)
	if err != nil {
		_ = parent.Close()
		return nil, "", fmt.Errorf("open migration artifact directory %q: %w", relative, err)
	}
	after, err := root.Stat(".")
	if err != nil || !after.IsDir() || !os.SameFile(before, after) {
		_ = root.Close()
		_ = parent.Close()
		if err != nil {
			return nil, "", fmt.Errorf("inspect opened migration artifact directory %q: %w", relative, err)
		}
		return nil, "", fmt.Errorf("migration artifact directory %q changed while it was opened", relative)
	}
	if err := parent.Close(); err != nil {
		_ = root.Close()
		return nil, "", fmt.Errorf("close migration output root: %w", err)
	}
	return root, filepath.Join(service.config.OutputRoot, relative), nil
}

func readRootedArtifactBounded(root *os.Root, name, displayDirectory string, maxSize int64) ([]byte, error) {
	if err := validateManifestName("artifact", name); err != nil {
		return nil, err
	}
	before, err := root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("inspect artifact %q: %w", filepath.Join(displayDirectory, name), err)
	}
	if !before.Mode().IsRegular() || before.Size() > maxSize {
		return nil, fmt.Errorf("artifact %q is not a supported regular file", filepath.Join(displayDirectory, name))
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open artifact %q: %w", filepath.Join(displayDirectory, name), err)
	}
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		_ = file.Close()
		if err != nil {
			return nil, fmt.Errorf("inspect opened artifact %q: %w", filepath.Join(displayDirectory, name), err)
		}
		return nil, fmt.Errorf("artifact %q changed while it was opened", filepath.Join(displayDirectory, name))
	}
	return readBoundedFile(file, filepath.Join(displayDirectory, name), maxSize)
}

func verifyDashboardManifestBindings(
	state manifest,
	evidence reporttypes.Report,
	committed artifactset.Manifest,
) error {
	if evidence.PrimaryArtifact == nil {
		return fmt.Errorf("committed dashboard report has no primary artifact binding")
	}
	foundPrimary := false
	foundReport := false
	foundHTML := false
	for _, entry := range committed.Artifacts {
		switch entry.Role {
		case artifactset.RolePrimary:
			foundPrimary = true
			if entry.Path != state.Dashboard {
				return fmt.Errorf("migration manifest dashboard %q does not match committed primary %q", state.Dashboard, entry.Path)
			}
			expected := reporttypes.ArtifactBinding{Path: entry.Path, SHA256: entry.SHA256, SizeBytes: entry.SizeBytes}
			if *evidence.PrimaryArtifact != expected {
				return fmt.Errorf("dashboard primary binding does not match committed primary member")
			}
		case artifactset.RoleReport:
			foundReport = true
			if entry.Path != state.Report {
				return fmt.Errorf("migration manifest report %q does not match committed report %q", state.Report, entry.Path)
			}
		case artifactset.RoleHTML:
			foundHTML = true
			if entry.Path != state.HTML {
				return fmt.Errorf("migration manifest HTML %q does not match committed HTML %q", state.HTML, entry.Path)
			}
		}
	}
	if !foundPrimary || !foundReport || !foundHTML {
		return fmt.Errorf("committed dashboard artifact set is incomplete")
	}
	return nil
}
