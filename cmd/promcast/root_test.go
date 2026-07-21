package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/mansiverma897993/signoz/internal/app"
	"github.com/mansiverma897993/signoz/internal/model"
	"github.com/mansiverma897993/signoz/pkg/reporttypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionCommandWritesMachineReadableOutput(t *testing.T) {
	output, err := runCommand(t, "--json", "version")
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"promcast","version":"dev","commit":"none"}`, output)
}

func TestGrafanaOfflineCommandWritesArtifactsAndReviewStatus(t *testing.T) {
	outputDirectory := t.TempDir()
	output, err := runCommand(t,
		"grafana", "../../internal/source/grafana/testdata/modern.json",
		"--offline", "--out", outputDirectory,
	)
	assert.Equal(t, 2, commandExitCode(err))
	assert.Contains(t, output, "Service overview")
	assert.FileExists(t, filepath.Join(outputDirectory, "modern.signoz.json"))
	assert.FileExists(t, filepath.Join(outputDirectory, "modern.report.json"))
	assert.FileExists(t, filepath.Join(outputDirectory, "modern.report.html"))
}

func TestRulesOfflineCommandWritesArtifactsAndReviewStatus(t *testing.T) {
	outputDirectory := t.TempDir()
	_, err := runCommand(t,
		"rules", "../../internal/source/prometheus/testdata/rules.yaml",
		"--offline", "--out", outputDirectory,
	)
	assert.Equal(t, 2, commandExitCode(err))
	assert.FileExists(t, filepath.Join(outputDirectory, "rules.signoz-rules.json"))
	assert.FileExists(t, filepath.Join(outputDirectory, "rules.rules-report.json"))
	assert.FileExists(t, filepath.Join(outputDirectory, "rules.rules-report.html"))
}

func TestRulesCommandFailsClosedForUnsupportedGroupSemantics(t *testing.T) {
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "hostile.yaml")
	require.NoError(t, os.WriteFile(sourcePath, []byte(`groups:
- name: hostile
  interval: 10m
  query_offset: 30m
  limit: 5
  labels:
    cluster: production
    owner: platform
  rules:
  - alert: Delayed
    expr: up == 0
    for: 5m
    labels:
      owner: service-team
      severity: warning
`), 0o600))
	outputDirectory := filepath.Join(directory, "out")

	_, err := runCommand(t, "rules", sourcePath, "--offline", "--out", outputDirectory)
	assert.Equal(t, 2, commandExitCode(err))

	data, readErr := os.ReadFile(filepath.Join(outputDirectory, "hostile.rules-report.json"))
	require.NoError(t, readErr)
	var evidence reporttypes.RuleReport
	require.NoError(t, json.Unmarshal(data, &evidence))
	require.Len(t, evidence.Groups, 1)
	assert.Equal(t, "30m", evidence.Groups[0].QueryOffset)
	assert.Equal(t, 5, evidence.Groups[0].Limit)
	assert.Equal(t, "production", evidence.Groups[0].Labels["cluster"])
	require.Len(t, evidence.Groups[0].Rules, 1)
	rule := evidence.Groups[0].Rules[0]
	assert.Equal(t, string(model.VerdictNeedsReview), rule.Verdict)
	assert.True(t, rule.Disabled)
	for _, reason := range []string{
		string(model.ReasonRuleGroupQueryOffset),
		string(model.ReasonRuleGroupLimit),
		string(model.ReasonRuleGroupInterval),
		string(model.ReasonAlertForWindow),
	} {
		assert.Contains(t, rule.ReasonCodes, reason)
	}

	payloadData, readErr := os.ReadFile(filepath.Join(outputDirectory, "hostile.signoz-rules.json"))
	require.NoError(t, readErr)
	var payloads []map[string]any
	require.NoError(t, json.Unmarshal(payloadData, &payloads))
	require.Len(t, payloads, 1)
	labels := payloads[0]["labels"].(map[string]any)
	assert.Equal(t, "production", labels["cluster"])
	assert.Equal(t, "service-team", labels["owner"])
	assert.Equal(t, true, payloads[0]["disabled"])
}

func TestRulesCommandReturnsInputExitWithoutArtifactsForInvalidPrometheusRule(t *testing.T) {
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "invalid.yaml")
	require.NoError(t, os.WriteFile(sourcePath, []byte(`groups:
- name: hostile
  rules:
  - alert: Both
    record: invalid_record
    expr: up == 0
`), 0o600))
	outputDirectory := filepath.Join(directory, "out")

	output, err := runCommand(t, "rules", sourcePath, "--offline", "--out", outputDirectory)
	assert.Equal(t, 3, commandExitCode(err))
	assert.Empty(t, output)
	assert.NoDirExists(t, outputDirectory)
}

func TestRulesCommandRequiresNamespaceForLiveTargetWithoutOutputOrNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	outputDirectory := filepath.Join(t.TempDir(), "not-created")

	output, err := runCommand(t,
		"rules", "../../internal/source/prometheus/testdata/rules.yaml",
		"--target", server.URL,
		"--api-key", "test-key",
		"--out", outputDirectory,
	)

	assert.Equal(t, 3, commandExitCode(err))
	assert.Empty(t, output)
	assert.Contains(t, err.Error(), "source namespace is required")
	assert.Zero(t, requests.Load())
	assert.NoDirExists(t, outputDirectory)
}

func TestReportCommandRegeneratesHTML(t *testing.T) {
	outputDirectory := t.TempDir()
	results, err := app.MigrateGrafana(context.Background(), []string{"../../internal/source/grafana/testdata/modern.json"}, app.GrafanaOptions{
		OutputDirectory: outputDirectory,
	})
	require.NoError(t, err)
	destination := filepath.Join(outputDirectory, "review.html")
	output, err := runCommand(t, "report", results[0].ReportPath, "--out", destination)
	require.NoError(t, err)
	assert.Equal(t, destination+"\n", output)
	assert.FileExists(t, destination)
}

func TestUserConfigurationErrorsUseInputExitCode(t *testing.T) {
	for name, arguments := range map[string][]string{
		"missing dashboard": {"grafana"},
		"conflicting mode":  {"grafana", "dashboard.json", "--offline", "--target", "http://localhost:8080"},
		"mcp transport":     {"mcp", "--transport", "websocket"},
		"invalid flag":      {"version", "--not-a-flag"},
		"diff options": {"diff", "../../internal/source/grafana/testdata/modern.json", "--source", "http://localhost:9090",
			"--target", "http://localhost:8080", "--api-key", "key", "--max-queries", "-1"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := runCommand(t, arguments...)
			assert.Equal(t, 3, commandExitCode(err), err)
		})
	}
}

func runCommand(t *testing.T, arguments ...string) (string, error) {
	t.Helper()
	t.Setenv("SIGNOZ_URL", "")
	t.Setenv("SIGNOZ_API_KEY", "")
	t.Setenv("PROMCAST_OUT", "")
	t.Setenv("PROMCAST_SOURCE_NAMESPACE", "")

	previous := outputWriter
	var output bytes.Buffer
	outputWriter = &output
	defer func() { outputWriter = previous }()

	command := newRootCommand()
	command.SetArgs(arguments)
	err := command.Execute()
	return output.String(), err
}
