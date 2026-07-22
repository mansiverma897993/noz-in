package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDashboardAttemptedCheckpointSurvivesInterruptedFinalFacadePublication(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "checkpoint.json")
	require.NoError(t, os.WriteFile(input, []byte(`{
		"title":"Crash recovery",
		"panels":[{"title":"Up","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]}]
	}`), 0o600))
	output := filepath.Join(directory, "out")
	reportPath := filepath.Join(output, "checkpoint.report.json")
	var attemptedReport []byte
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v1/dashboards":
			if request.Method == http.MethodGet {
				writeJSONResponse(t, writer, map[string]any{"data": []any{}})
				return
			}
			var err error
			attemptedReport, err = os.ReadFile(reportPath)
			require.NoError(t, err)
			var attempted reporttypes.Report
			require.NoError(t, json.Unmarshal(attemptedReport, &attempted))
			assert.Equal(t, true, attempted.Run.Flags["importAttempted"])
			assert.Equal(t, "attempted", attempted.Run.Flags["targetAction"])
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"id": "created-id"}})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	results, err := MigrateGrafana(context.Background(), []string{input}, GrafanaOptions{
		OutputDirectory: output,
		TargetURL:       server.URL,
		APIKey:          "key",
		HTTPClient:      server.Client(),
		SourceNamespace: "grafana:test",
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NotEmpty(t, attemptedReport)
	assert.True(t, results[0].ImportSucceeded)

	// This is the persistent state at a crash after the final generation and
	// pointer barriers but before the report-facade barrier: the pointer is
	// final while the user-facing report is still the attempted checkpoint.
	require.NoError(t, os.WriteFile(reportPath, attemptedReport, 0o600))
	recovered, reportData, err := readMigrationEvidence(reportPath)
	require.NoError(t, err)
	assert.Equal(t, "attempted", recovered.Run.Flags["targetAction"])
	_, _, err = readBoundPrimaryDashboard(reportPath, reportData, recovered)
	require.NoError(t, err)
}

func TestRuleAttemptedCheckpointSurvivesInterruptedFinalFacadePublication(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "checkpoint.yaml")
	require.NoError(t, os.WriteFile(input, []byte(`groups:
- name: availability
  rules:
  - alert: ServiceDown
    expr: up == 0
`), 0o600))
	output := filepath.Join(directory, "out")
	reportPath := filepath.Join(output, "checkpoint.rules-report.json")
	const namespace = "prometheus:test"
	inventory := targetRuleInventoryForSource(t, input, "existing-id")
	var attemptedReport []byte
	server := newRuleWriteServerWithInventory(t, inventory, func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, http.MethodPut, request.Method)
		assert.Equal(t, "/api/v2/rules/existing-id", request.URL.Path)
		var err error
		attemptedReport, err = os.ReadFile(reportPath)
		require.NoError(t, err)
		var attempted reporttypes.RuleReport
		require.NoError(t, json.Unmarshal(attemptedReport, &attempted))
		assert.Equal(t, true, attempted.Run.Flags["writeAttempted"])
		assert.Equal(t, "attempted", attempted.Run.Flags["targetAction"])
		assert.Equal(t, "attempting_update", attempted.Groups[0].Rules[0].Write.Action)
		assert.True(t, attempted.Groups[0].Rules[0].Write.Attempted)
		assert.False(t, attempted.Groups[0].Rules[0].Write.Succeeded)
		writer.WriteHeader(http.StatusNoContent)
	})
	t.Cleanup(server.Close)

	results, err := MigratePrometheusRules(context.Background(), []string{input}, RuleOptions{
		OutputDirectory: output,
		TargetURL:       server.URL,
		APIKey:          "key",
		HTTPClient:      server.Client(),
		SourceNamespace: namespace,
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NotEmpty(t, attemptedReport)
	assert.True(t, results[0].WriteSucceeded)

	require.NoError(t, os.WriteFile(reportPath, attemptedReport, 0o600))
	var recovered reporttypes.RuleReport
	require.NoError(t, json.Unmarshal(attemptedReport, &recovered))
	assert.Equal(t, "attempted", recovered.Run.Flags["targetAction"])
	require.NoError(t, ValidateStoredRuleArtifact(reportPath, recovered))
}
