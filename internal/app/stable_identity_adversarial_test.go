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

func TestMigrateGrafanaRejectsUnsafeNamespaceBeforeCreatingOutput(t *testing.T) {
	t.Parallel()

	output := filepath.Join(t.TempDir(), "not-created")
	results, err := MigrateGrafana(context.Background(), []string{"unused.json"}, GrafanaOptions{
		OutputDirectory: output,
		SourceNamespace: "grafana\x00production",
	})
	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Empty(t, results)
	_, statErr := os.Stat(output)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestMigrateGrafanaRejectsUnsafeUIDBeforeTargetOrArtifactPublication(t *testing.T) {
	t.Parallel()

	var targetCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		targetCalls.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	input := filepath.Join(t.TempDir(), "unsafe-uid.json")
	require.NoError(t, os.WriteFile(input, []byte(`{
		"uid":"victim\u000a",
		"title":"Unsafe UID",
		"panels":[{"title":"Up","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]}]
	}`), 0o600))
	output := t.TempDir()
	results, err := MigrateGrafana(context.Background(), []string{input}, GrafanaOptions{
		OutputDirectory: output, TargetURL: server.URL, APIKey: "key", HTTPClient: server.Client(),
		SourceNamespace: "grafana:production",
	})
	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Empty(t, results)
	assert.Equal(t, int32(0), targetCalls.Load())
	entries, readErr := os.ReadDir(output)
	require.NoError(t, readErr)
	assert.Empty(t, entries)
}

func TestMigratePrometheusRulesRejectsUnsafeNamespaceBeforeCreatingOutput(t *testing.T) {
	t.Parallel()

	output := filepath.Join(t.TempDir(), "not-created")
	results, err := MigratePrometheusRules(context.Background(), []string{"unused.yaml"}, RuleOptions{
		OutputDirectory: output,
		SourceNamespace: "prometheus\u202eproduction",
	})
	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Empty(t, results)
	_, statErr := os.Stat(output)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}
