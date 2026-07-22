package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mansiverma897993/noz-in/internal/artifactset"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadDashboardReportUsesSelectedCommittedGeneration(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	service, err := New(Config{Root: root, OutputRoot: filepath.Join(root, "out")})
	require.NoError(t, err)
	state, primary := seedCommittedDashboardGeneration(t, service, "migration-committed")
	writeMigrationStateForTest(t, service, state)

	selected, err := service.readManifest(state.MigrationID)
	require.NoError(t, err)
	snapshot, err := service.readDashboardReport(state.MigrationID, selected, selected.Dashboard)
	require.NoError(t, err)
	assert.Equal(t, primary, snapshot.Members[selected.Dashboard])
	assert.NotNil(t, snapshot.Evidence.ArtifactSet)
}

func TestPublishedMCPGenerationRequiresInnerImmutableArtifactBytes(t *testing.T) {
	root := t.TempDir()
	service, err := New(Config{Root: root, OutputRoot: filepath.Join(root, "out")})
	require.NoError(t, err)
	result, err := service.handleMigrateDashboard(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{
			"grafana_json": `{"uid":"inner-storage","title":"Inner storage","panels":[]}`,
		}},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	encoded, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	var response migrateResponse
	require.NoError(t, json.Unmarshal(encoded, &response))

	state, err := service.readManifest(response.MigrationID)
	require.NoError(t, err)
	reportPath := filepath.Join(service.config.OutputRoot, response.MigrationID, state.Generation, state.Report)
	reportData, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	var evidence reporttypes.Report
	require.NoError(t, json.Unmarshal(reportData, &evidence))
	require.NotNil(t, evidence.ArtifactSet)
	layout, err := artifactset.StorageLayoutForBinding(*evidence.ArtifactSet)
	require.NoError(t, err)
	innerPrimary := filepath.Join(
		service.config.OutputRoot,
		response.MigrationID,
		state.Generation,
		layout.Generations,
		evidence.ArtifactSet.Generation,
		state.Dashboard,
	)
	assert.FileExists(t, innerPrimary)
	stablePrimary := filepath.Join(service.config.OutputRoot, response.MigrationID, state.Generation, state.Dashboard)
	stableData, err := os.ReadFile(stablePrimary)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(innerPrimary, []byte("tampered immutable bytes"), 0o600))

	_, err = service.readDashboardReport(response.MigrationID, state, state.Dashboard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "commit manifest")
	unchangedStableData, err := os.ReadFile(stablePrimary)
	require.NoError(t, err)
	assert.Equal(t, stableData, unchangedStableData)
}

func TestReadDashboardReportRejectsStaleOrMismatchedMigrationManifest(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	service, err := New(Config{Root: root, OutputRoot: filepath.Join(root, "out")})
	require.NoError(t, err)
	state, _ := seedCommittedDashboardGeneration(t, service, "migration-mismatch")

	t.Run("stale generation", func(t *testing.T) {
		stale := state
		stale.Generation = attemptGeneration
		writeMigrationStateForTest(t, service, stale)
		selected, readErr := service.readManifest(state.MigrationID)
		require.NoError(t, readErr)
		_, readErr = service.readDashboardReport(state.MigrationID, selected, selected.Dashboard)
		require.Error(t, readErr)
		assert.Contains(t, readErr.Error(), attemptGeneration)
	})

	t.Run("dashboard outside committed topology", func(t *testing.T) {
		mismatched, _ := seedCommittedDashboardGeneration(t, service, "migration-mismatch-dashboard")
		mismatched.Dashboard = "other.signoz.json"
		writeMigrationStateForTest(t, service, mismatched)
		selected, readErr := service.readManifest(mismatched.MigrationID)
		require.NoError(t, readErr)
		_, readErr = service.readDashboardReport(mismatched.MigrationID, selected, selected.Dashboard)
		require.Error(t, readErr)
		assert.Contains(t, readErr.Error(), "not in the commit manifest")
	})
}

func TestReadDashboardReportRejectsStaleArtifactSetManifest(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	service, err := New(Config{Root: root, OutputRoot: filepath.Join(root, "out")})
	require.NoError(t, err)
	state, _ := seedCommittedDashboardGeneration(t, service, "migration-stale-set")
	writeMigrationStateForTest(t, service, state)

	reportData, err := os.ReadFile(filepath.Join(service.config.OutputRoot, state.MigrationID, state.Generation, state.Report))
	require.NoError(t, err)
	var evidence reporttypes.Report
	require.NoError(t, json.Unmarshal(reportData, &evidence))
	require.NotNil(t, evidence.ArtifactSet)
	manifestPath := filepath.Join(service.config.OutputRoot, state.MigrationID, state.Generation, evidence.ArtifactSet.Path)
	manifestData, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	var committed artifactset.Manifest
	require.NoError(t, json.Unmarshal(manifestData, &committed))
	committed.Generation = strings.Repeat("0", 32)
	require.NoError(t, writeJSONAtomic(manifestPath, committed))

	selected, err := service.readManifest(state.MigrationID)
	require.NoError(t, err)
	_, err = service.readDashboardReport(state.MigrationID, selected, selected.Dashboard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match generation pointer")
}

func TestValidationArtifactsReceiveFreshCommittedBinding(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	old := reporttypes.ArtifactSetBinding{Path: "source.artifacts.json", Generation: strings.Repeat("1", 32)}
	evidence := reporttypes.Report{
		SchemaVersion: "1",
		Dashboard:     reporttypes.DashboardInfo{Title: "Validation"},
		ArtifactSet:   &old,
	}
	dashboard := []byte("{\"schemaVersion\":\"v5\",\"title\":\"Validation\"}\n")
	staged, err := stageValidationArtifactSet(directory, &evidence, dashboard)
	require.NoError(t, err)
	require.NotNil(t, evidence.ArtifactSet)
	assert.NotEqual(t, old, *evidence.ArtifactSet)
	assert.Equal(t, "validated.artifacts.json", evidence.ArtifactSet.Path)
	assert.Equal(t, "validated.signoz.json", evidence.PrimaryArtifact.Path)

	reportData, err := os.ReadFile(staged.report)
	require.NoError(t, err)
	snapshot, err := artifactset.ReadCommitted(
		staged.report,
		reportData,
		evidence.ArtifactSet,
		artifactset.KindDashboard,
		[]string{filepath.Base(staged.dashboard), filepath.Base(staged.html)},
		artifactset.MaxMemberSize,
	)
	require.NoError(t, err)
	assert.Equal(t, dashboard, snapshot.Data[filepath.Base(staged.dashboard)])
}

func seedCommittedDashboardGeneration(
	t *testing.T,
	service *Service,
	id string,
) (manifest, []byte) {
	t.Helper()
	require.NoError(t, service.createOutputDirectory(id))
	require.NoError(t, service.createOutputDirectory(filepath.Join(id, resultGeneration)))
	directory := filepath.Join(service.config.OutputRoot, id, resultGeneration)
	reportPath := filepath.Join(directory, "migration.report.json")
	primaryPath := filepath.Join(directory, "migration.signoz.json")
	htmlPath := filepath.Join(directory, "migration.report.html")
	primary := []byte("{\"schemaVersion\":\"v5\",\"title\":\"Committed\"}\n")
	digest := sha256.Sum256(primary)
	binding, err := artifactset.NewBindingForReport(reportPath, artifactset.KindDashboard)
	require.NoError(t, err)
	evidence := reporttypes.Report{
		SchemaVersion: "1",
		Dashboard:     reporttypes.DashboardInfo{Title: "Committed"},
		PrimaryArtifact: &reporttypes.ArtifactBinding{
			Path: filepath.Base(primaryPath), SHA256: fmt.Sprintf("%x", digest[:]), SizeBytes: int64(len(primary)),
		},
		ArtifactSet: &binding,
	}
	reportData, err := json.MarshalIndent(evidence, "", "  ")
	require.NoError(t, err)
	reportData = append(reportData, '\n')
	require.NoError(t, artifactset.Commit(reportPath, binding, artifactset.KindDashboard, []artifactset.Artifact{
		{Role: artifactset.RolePrimary, Path: primaryPath, Data: primary},
		{Role: artifactset.RoleReport, Path: reportPath, Data: reportData},
		{Role: artifactset.RoleHTML, Path: htmlPath, Data: []byte("<html></html>\n")},
	}))
	return manifest{
		SchemaVersion: 1,
		MigrationID:   id,
		Generation:    resultGeneration,
		Source:        "source.json",
		Report:        filepath.Base(reportPath),
		Dashboard:     filepath.Base(primaryPath),
		HTML:          filepath.Base(htmlPath),
		RateInterval:  "5m",
	}, primary
}

func writeMigrationStateForTest(t *testing.T, service *Service, state manifest) {
	t.Helper()
	data, err := json.MarshalIndent(state, "", "  ")
	require.NoError(t, err)
	require.NoError(t, service.writeOutputAtomic(
		filepath.Join(state.MigrationID, "migration-result.json"),
		append(data, '\n'),
	))
}
