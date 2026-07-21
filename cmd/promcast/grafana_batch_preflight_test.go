package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGrafanaCommandRejectsMalformedBatchWithoutResultsOutputArtifactsOrNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	directory := t.TempDir()
	valid := filepath.Join(directory, "valid.json")
	invalid := filepath.Join(directory, "invalid.json")
	require.NoError(t, os.WriteFile(valid, []byte(`{"title":"Valid","uid":"valid","panels":[]}`), 0o600))
	require.NoError(t, os.WriteFile(invalid, []byte("{"), 0o600))
	outputDirectory := filepath.Join(directory, "not-created")

	output, err := runCommand(t,
		"grafana", valid, invalid,
		"--target", server.URL,
		"--api-key", "test-key",
		"--source-namespace", "grafana:test",
		"--out", outputDirectory,
	)

	assert.Equal(t, 3, commandExitCode(err))
	assert.Empty(t, output)
	assert.Contains(t, err.Error(), "invalid.json")
	assert.Zero(t, requests.Load())
	assert.NoDirExists(t, outputDirectory)
}

func TestGrafanaCommandRequiresNamespaceForLiveTargetWithoutOutputOrNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	outputDirectory := filepath.Join(t.TempDir(), "not-created")

	output, err := runCommand(t,
		"grafana", "../../internal/source/grafana/testdata/modern.json",
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

func TestGrafanaCommandDoesNotReplaceMetricMapWithGeneratedArtifact(t *testing.T) {
	directory := t.TempDir()
	dashboard := filepath.Join(directory, "dashboard.json")
	require.NoError(t, os.WriteFile(dashboard, []byte(`{"title":"Protected config","uid":"protected","panels":[]}`), 0o600))
	metricMap := filepath.Join(directory, "dashboard.signoz.json")
	metricMapBytes := []byte("up: up\n")
	require.NoError(t, os.WriteFile(metricMap, metricMapBytes, 0o600))

	output, err := runCommand(t,
		"grafana", dashboard,
		"--offline",
		"--metric-name-map", metricMap,
		"--out", directory,
	)

	assert.Equal(t, 3, commandExitCode(err))
	assert.Empty(t, output)
	assert.Contains(t, err.Error(), "aliases protected metric-name map")
	contents, readErr := os.ReadFile(metricMap)
	require.NoError(t, readErr)
	assert.Equal(t, metricMapBytes, contents)
}

func TestRulesCommandDoesNotReplaceMetricMapWithGeneratedArtifact(t *testing.T) {
	directory := t.TempDir()
	rules := filepath.Join(directory, "alerts.yaml")
	require.NoError(t, os.WriteFile(rules, []byte(`groups:
- name: protected
  rules:
  - alert: TargetDown
    expr: up == 0
`), 0o600))
	metricMap := filepath.Join(directory, "alerts.signoz-rules.json")
	metricMapBytes := []byte("up: up\n")
	require.NoError(t, os.WriteFile(metricMap, metricMapBytes, 0o600))

	output, err := runCommand(t,
		"rules", rules,
		"--offline",
		"--metric-name-map", metricMap,
		"--out", directory,
	)

	assert.Equal(t, 3, commandExitCode(err))
	assert.Empty(t, output)
	assert.Contains(t, err.Error(), "aliases protected metric-name map")
	contents, readErr := os.ReadFile(metricMap)
	require.NoError(t, readErr)
	assert.Equal(t, metricMapBytes, contents)
}

func TestRulesCommandDoesNotReplaceAPIKeyFileBeforeTargetAccess(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	directory := t.TempDir()
	rules := filepath.Join(directory, "alerts.yaml")
	require.NoError(t, os.WriteFile(rules, []byte(`groups:
- name: protected
  rules:
  - alert: TargetDown
    expr: up == 0
`), 0o600))
	apiKeyFile := filepath.Join(directory, "alerts.rules-report.html")
	apiKeyBytes := []byte("private-api-key\n")
	require.NoError(t, os.WriteFile(apiKeyFile, apiKeyBytes, 0o600))

	output, err := runCommand(t,
		"rules", rules,
		"--target", server.URL,
		"--api-key-file", apiKeyFile,
		"--source-namespace", "prometheus:test",
		"--validate=false",
		"--out", directory,
	)

	assert.Equal(t, 3, commandExitCode(err))
	assert.Empty(t, output)
	assert.Contains(t, err.Error(), "aliases protected API-key file")
	assert.Zero(t, requests.Load())
	contents, readErr := os.ReadFile(apiKeyFile)
	require.NoError(t, readErr)
	assert.Equal(t, apiKeyBytes, contents)
}
