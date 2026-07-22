package report

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/mansiverma897993/noz-in/internal/artifactset"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderFileRegeneratesDashboardHTML(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	input := filepath.Join(directory, "migration.report.json")
	output := filepath.Join(directory, "migration.report.html")
	artifact := []byte("{}\n")
	artifactPath := filepath.Join(directory, "migration.signoz.json")
	require.NoError(t, os.WriteFile(artifactPath, artifact, 0o600))
	digest := sha256.Sum256(artifact)
	evidence := reporttypes.Report{
		SchemaVersion: "1", Dashboard: reporttypes.DashboardInfo{Title: "Hosts"}, Panels: []reporttypes.PanelRecord{},
		PrimaryArtifact: &reporttypes.ArtifactBinding{
			Path: filepath.Base(artifactPath), SHA256: fmt.Sprintf("%x", digest[:]), SizeBytes: int64(len(artifact)),
		},
	}
	reportData, err := json.Marshal(evidence)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(input, reportData, 0o600))

	require.NoError(t, RenderFile(input, output))
	assert.FileExists(t, output)
	assert.Equal(t, output, DefaultHTMLPath(input))
}

func TestRenderFileRejectsChangedPrimaryArtifact(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	input := filepath.Join(directory, "migration.report.json")
	artifactPath := filepath.Join(directory, "migration.signoz.json")
	original := []byte("{}\n")
	digest := sha256.Sum256(original)
	evidence := reporttypes.Report{
		SchemaVersion: "1", Dashboard: reporttypes.DashboardInfo{Title: "Hosts"}, Panels: []reporttypes.PanelRecord{},
		PrimaryArtifact: &reporttypes.ArtifactBinding{
			Path: filepath.Base(artifactPath), SHA256: fmt.Sprintf("%x", digest[:]), SizeBytes: int64(len(original)),
		},
	}
	data, err := json.Marshal(evidence)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(input, data, 0o600))
	require.NoError(t, os.WriteFile(artifactPath, []byte("[]\n"), 0o600))

	err = RenderFile(input, input+".html")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match report")
}

func TestRenderFileRejectsUnsupportedSchema(t *testing.T) {
	t.Parallel()

	input := filepath.Join(t.TempDir(), "migration.report.json")
	require.NoError(t, os.WriteFile(input, []byte(`{"schemaVersion":"v1","dashboard":{"title":"Hosts"},"panels":[]}`), 0o644))
	err := RenderFile(input, input+".html")
	require.ErrorContains(t, err, "unsupported schema version")
}

func TestRenderFileRejectsMixedDeclaredArtifactGeneration(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	input := filepath.Join(directory, "migration.report.json")
	output := filepath.Join(directory, "migration.report.html")
	primaryPath := filepath.Join(directory, "migration.signoz.json")
	primary := []byte("{}\n")
	digest := sha256.Sum256(primary)
	binding, err := artifactset.NewBindingForReport(input, artifactset.KindDashboard)
	require.NoError(t, err)
	evidence := reporttypes.Report{
		SchemaVersion: "1", Dashboard: reporttypes.DashboardInfo{Title: "Hosts"}, Panels: []reporttypes.PanelRecord{},
		ArtifactSet: &binding,
		PrimaryArtifact: &reporttypes.ArtifactBinding{
			Path: filepath.Base(primaryPath), SHA256: fmt.Sprintf("%x", digest[:]), SizeBytes: int64(len(primary)),
		},
	}
	reportData, err := encodedReport(evidence)
	require.NoError(t, err)
	htmlData, err := DashboardHTMLBytes(evidence)
	require.NoError(t, err)
	require.NoError(t, artifactset.Commit(input, binding, artifactset.KindDashboard, []artifactset.Artifact{
		{Role: artifactset.RolePrimary, Path: primaryPath, Data: primary},
		{Role: artifactset.RoleReport, Path: input, Data: reportData},
		{Role: artifactset.RoleHTML, Path: output, Data: htmlData},
	}))
	require.NoError(t, os.WriteFile(output, []byte("mixed"), 0o600))

	err = RenderFile(input, output)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match commit manifest")
}

func TestRenderFileAdvancesCommittedGenerationWhenReplacingSetHTML(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	input := filepath.Join(directory, "migration.report.json")
	output := filepath.Join(directory, "migration.report.html")
	primaryPath := filepath.Join(directory, "migration.signoz.json")
	primary := []byte("{}\n")
	digest := sha256.Sum256(primary)
	first, err := artifactset.NewBindingForReport(input, artifactset.KindDashboard)
	require.NoError(t, err)
	evidence := reporttypes.Report{
		SchemaVersion: "1", Dashboard: reporttypes.DashboardInfo{Title: "Hosts"}, Panels: []reporttypes.PanelRecord{},
		ArtifactSet: &first,
		PrimaryArtifact: &reporttypes.ArtifactBinding{
			Path: filepath.Base(primaryPath), SHA256: fmt.Sprintf("%x", digest[:]), SizeBytes: int64(len(primary)),
		},
	}
	reportData, err := encodedReport(evidence)
	require.NoError(t, err)
	htmlData, err := DashboardHTMLBytes(evidence)
	require.NoError(t, err)
	require.NoError(t, artifactset.Commit(input, first, artifactset.KindDashboard, []artifactset.Artifact{
		{Role: artifactset.RolePrimary, Path: primaryPath, Data: primary},
		{Role: artifactset.RoleReport, Path: input, Data: reportData},
		{Role: artifactset.RoleHTML, Path: output, Data: htmlData},
	}))

	require.NoError(t, RenderFile(input, output))
	committedReport, err := os.ReadFile(input)
	require.NoError(t, err)
	var current reporttypes.Report
	require.NoError(t, json.Unmarshal(committedReport, &current))
	require.NotNil(t, current.ArtifactSet)
	assert.NotEqual(t, first.Generation, current.ArtifactSet.Generation)
	_, err = artifactset.ReadCommitted(
		input, committedReport, current.ArtifactSet, artifactset.KindDashboard, nil, maxReportSize,
	)
	require.NoError(t, err)
}
