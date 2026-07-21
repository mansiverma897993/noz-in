package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigratePrometheusRulesPreflightsEveryBatchDestinationBeforeNetwork(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	directory := t.TempDir()
	first := filepath.Join(directory, "first.yaml")
	second := filepath.Join(directory, "second.yaml")
	writeRulePreflightFixture(t, first, "first-group", "FirstDown")
	writeRulePreflightFixture(t, second, "second-group", "SecondDown")
	output := filepath.Join(directory, "out")
	require.NoError(t, os.Mkdir(output, 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(output, "second.rules-report.json"), 0o700))

	results, err := MigratePrometheusRules(context.Background(), []string{first, second}, RuleOptions{
		OutputDirectory: output,
		TargetURL:       server.URL,
		APIKey:          "key",
		HTTPClient:      server.Client(),
		SourceNamespace: "prometheus:test",
	})

	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "second.rules-report.json")
	assert.Nil(t, results)
	assert.Zero(t, requests.Load())
	assert.NoFileExists(t, filepath.Join(output, "first.signoz-rules.json"))
	assert.NoFileExists(t, filepath.Join(output, "first.rules-report.json"))
}

func TestMigratePrometheusRulesRejectsOutputAliasToRuleInputBeforeNetwork(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	directory := t.TempDir()
	input := filepath.Join(directory, "alerts.yaml")
	writeRulePreflightFixture(t, input, "alerts", "TargetDown")
	require.NoError(t, os.Link(input, filepath.Join(directory, "alerts.signoz-rules.json")))

	results, err := MigratePrometheusRules(context.Background(), []string{input}, RuleOptions{
		OutputDirectory: directory,
		TargetURL:       server.URL,
		APIKey:          "key",
		HTTPClient:      server.Client(),
		SourceNamespace: "prometheus:test",
	})

	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "aliases protected Prometheus rule input")
	assert.Nil(t, results)
	assert.Zero(t, requests.Load())
	contents, readErr := os.ReadFile(input)
	require.NoError(t, readErr)
	assert.Contains(t, string(contents), "TargetDown")
}

func TestMigratePrometheusRulesProtectsCallerDecodedInputPath(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	input := filepath.Join(directory, "alerts.yaml")
	writeRulePreflightFixture(t, input, "alerts", "TargetDown")
	protected := filepath.Join(directory, "alerts.rules-report.json")
	protectedBytes := []byte("authoritative configuration\n")
	require.NoError(t, os.WriteFile(protected, protectedBytes, 0o600))

	results, err := MigratePrometheusRules(context.Background(), []string{input}, RuleOptions{
		OutputDirectory: directory,
		ProtectedInputs: []ProtectedInputPath{{Path: protected, Purpose: "test configuration"}},
	})

	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "aliases protected test configuration")
	assert.Nil(t, results)
	contents, readErr := os.ReadFile(protected)
	require.NoError(t, readErr)
	assert.Equal(t, protectedBytes, contents)
}

func TestMigratePrometheusRulesRejectsOutputSymlinkSubstitutedAfterPreflight(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	directory := t.TempDir()
	input := filepath.Join(directory, "alerts.yaml")
	writeRulePreflightFixture(t, input, "alerts", "TargetDown")
	output := filepath.Join(directory, "out")
	victim := filepath.Join(directory, "victim")
	require.NoError(t, os.Mkdir(victim, 0o700))

	results, err := MigratePrometheusRules(context.Background(), []string{input}, RuleOptions{
		OutputDirectory: output,
		TargetURL:       server.URL,
		APIKey:          "key",
		HTTPClient:      server.Client(),
		SourceNamespace: "prometheus:test",
		outputPreCreateCheckpoint: func() error {
			return os.Symlink(victim, output)
		},
	})

	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "not a real directory")
	assert.Nil(t, results)
	assert.Zero(t, requests.Load(), "a substituted output root must fail before target access")
	entries, readErr := os.ReadDir(victim)
	require.NoError(t, readErr)
	assert.Empty(t, entries, "no lock or artifact may be created through the substituted symlink")
}

func writeRulePreflightFixture(t *testing.T, path, group, alert string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(`groups:
- name: `+group+`
  rules:
  - alert: `+alert+`
    expr: up == 0
`), 0o600))
}
