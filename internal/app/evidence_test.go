package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mansiverma897993/noz-in/internal/target/signoz"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateStoredDashboardEvidenceRestoresPathsAndBindsPrimaryQueries(t *testing.T) {
	results, err := MigrateGrafana(context.Background(), []string{"../source/grafana/testdata/modern.json"}, GrafanaOptions{
		OutputDirectory: t.TempDir(),
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	var dashboard signoz.DashboardV5
	decodeFile(t, results[0].DashboardPath, &dashboard)
	for _, widget := range dashboard.Widgets {
		assert.Empty(t, widget.SourcePath)
	}

	require.NoError(t, ValidateStoredDashboardEvidence(&dashboard, results[0].Evidence))
	for _, widget := range dashboard.Widgets {
		assert.NotEmpty(t, widget.SourcePath)
	}
}

func TestValidateStoredDashboardEvidenceRejectsMissingOrChangedPrimaryArtifact(t *testing.T) {
	results, err := MigrateGrafana(context.Background(), []string{"../source/grafana/testdata/modern.json"}, GrafanaOptions{
		OutputDirectory: t.TempDir(),
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	loadDashboard := func() signoz.DashboardV5 {
		var dashboard signoz.DashboardV5
		decodeFile(t, results[0].DashboardPath, &dashboard)
		return dashboard
	}

	t.Run("missing primary widget", func(t *testing.T) {
		dashboard := loadDashboard()
		require.NotEmpty(t, dashboard.Widgets)
		dashboard.Widgets = dashboard.Widgets[1:]
		err := ValidateStoredDashboardEvidence(&dashboard, results[0].Evidence)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "primary widget")
		assert.Contains(t, err.Error(), "is missing")
	})

	t.Run("changed query body", func(t *testing.T) {
		dashboard := loadDashboard()
		require.NotEmpty(t, dashboard.Widgets)
		widget := &dashboard.Widgets[0]
		switch widget.Query.QueryType {
		case "promql":
			require.NotEmpty(t, widget.Query.PromQL)
			widget.Query.PromQL[0].Query += " + vector(1)"
		case "builder":
			if len(widget.Query.Builder.QueryFormulas) > 0 {
				widget.Query.Builder.QueryFormulas[0].Expression += " + 1"
			} else {
				require.NotEmpty(t, widget.Query.Builder.QueryData)
				widget.Query.Builder.QueryData[0].StepInterval++
			}
		default:
			t.Fatalf("unexpected query type %q", widget.Query.QueryType)
		}
		err := ValidateStoredDashboardEvidence(&dashboard, results[0].Evidence)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "emitted specification does not match")
	})

	t.Run("legacy report without spec hash", func(t *testing.T) {
		dashboard := loadDashboard()
		evidence := cloneEvidenceForTest(t, results[0].Evidence)
		evidence.Panels[0].Queries[0].EmittedSpecSHA256 = ""
		err := ValidateStoredDashboardEvidence(&dashboard, evidence)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no valid emitted specification SHA-256")
	})
}

func TestValidateStoredDashboardEvidenceRejectsLegacyRawVariableEscapingMismatch(t *testing.T) {
	results, err := MigrateGrafana(context.Background(), []string{"../source/grafana/testdata/modern.json"}, GrafanaOptions{
		OutputDirectory: t.TempDir(),
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	var dashboard signoz.DashboardV5
	decodeFile(t, results[0].DashboardPath, &dashboard)
	evidence := cloneEvidenceForTest(t, results[0].Evidence)

	variableFound := false
	for index := range evidence.Variables {
		if evidence.Variables[index].Name == "job" {
			evidence.Variables[index].Current = []string{"api.prod"}
			variableFound = true
		}
	}
	require.True(t, variableFound)
	storedFound := false
	for id, variable := range dashboard.Variables {
		if variable.Name == "job" {
			variable.SelectedValue = []any{"api.prod"}
			variable.DefaultValue = "api.prod"
			dashboard.Variables[id] = variable
			storedFound = true
		}
	}
	require.True(t, storedFound)

	err = ValidateStoredDashboardEvidence(&dashboard, evidence)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "diverges from pinned SigNoz raw selectedValue substitution")
}

func TestValidateStoredDashboardEvidenceAcceptsExplicitRawPipeInterpolation(t *testing.T) {
	input := filepath.Join(t.TempDir(), "pipe.json")
	require.NoError(t, os.WriteFile(input, []byte(`{
		"title":"Raw pipe",
		"templating":{"list":[{
			"name":"job","type":"query","query":"label_values(up, job)",
			"multi":true,"current":{"value":["api.prod","worker|canary"]}
		}]},
		"panels":[{"title":"Availability","type":"timeseries","targets":[{
			"refId":"A","expr":"sum(up{job=~\"${job:pipe}\"})"
		}]}]
	}`), 0o600))
	results, err := MigrateGrafana(context.Background(), []string{input}, GrafanaOptions{
		OutputDirectory: t.TempDir(),
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	var dashboard signoz.DashboardV5
	decodeFile(t, results[0].DashboardPath, &dashboard)

	require.NoError(t, ValidateStoredDashboardEvidence(&dashboard, results[0].Evidence))
}

func cloneEvidenceForTest(t *testing.T, evidence reporttypes.Report) reporttypes.Report {
	t.Helper()
	data, err := json.Marshal(evidence)
	require.NoError(t, err)
	var cloned reporttypes.Report
	require.NoError(t, json.Unmarshal(data, &cloned))
	return cloned
}
