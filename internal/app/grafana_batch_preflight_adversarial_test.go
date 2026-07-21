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

func TestMigrateGrafanaPreflightsEveryBatchInputBeforeOutputOrNetwork(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	directory := t.TempDir()
	valid := filepath.Join(directory, "valid.json")
	invalid := filepath.Join(directory, "invalid.json")
	require.NoError(t, os.WriteFile(valid, []byte(`{
		"title":"Valid first",
		"uid":"valid-first",
		"panels":[{"title":"Up","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]}]
	}`), 0o600))
	require.NoError(t, os.WriteFile(invalid, []byte("{"), 0o600))
	output := filepath.Join(directory, "not-created")

	results, err := MigrateGrafana(context.Background(), []string{valid, invalid}, GrafanaOptions{
		OutputDirectory: output,
		TargetURL:       server.URL,
		APIKey:          "key",
		HTTPClient:      server.Client(),
		SourceNamespace: "grafana:test",
	})

	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "invalid.json")
	assert.Nil(t, results)
	assert.Zero(t, requests.Load())
	assert.NoDirExists(t, output)
}

func TestMigrateGrafanaRejectsDuplicateStableTargetsBeforeOutputOrNetwork(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	directory := t.TempDir()
	first := filepath.Join(directory, "first.json")
	second := filepath.Join(directory, "second.json")
	for path, title := range map[string]string{first: "First", second: "Second"} {
		require.NoError(t, os.WriteFile(path, []byte(`{
			"title":"`+title+`",
			"uid":"shared-uid",
			"panels":[]
		}`), 0o600))
	}
	output := filepath.Join(directory, "not-created")

	results, err := MigrateGrafana(context.Background(), []string{first, second}, GrafanaOptions{
		OutputDirectory: output,
		TargetURL:       server.URL,
		APIKey:          "key",
		HTTPClient:      server.Client(),
		SourceNamespace: "grafana:test",
	})

	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "same stable target identity")
	assert.Contains(t, err.Error(), "shared-uid")
	assert.Nil(t, results)
	assert.Zero(t, requests.Load())
	assert.NoDirExists(t, output)
}

func TestMigrateGrafanaPreflightsEveryBatchDestinationBeforeOutputOrNetwork(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	directory := t.TempDir()
	first := filepath.Join(directory, "first.json")
	second := filepath.Join(directory, "second.json")
	for path, body := range map[string]string{
		first:  `{"title":"First","uid":"first","panels":[]}`,
		second: `{"title":"Second","uid":"second","panels":[]}`,
	} {
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	}
	output := filepath.Join(directory, "out")
	require.NoError(t, os.Mkdir(output, 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(output, "second.report.json"), 0o700))

	results, err := MigrateGrafana(context.Background(), []string{first, second}, GrafanaOptions{
		OutputDirectory: output,
		TargetURL:       server.URL,
		APIKey:          "key",
		HTTPClient:      server.Client(),
		SourceNamespace: "grafana:test",
	})

	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "second.report.json")
	assert.Nil(t, results)
	assert.Zero(t, requests.Load())
	assert.NoFileExists(t, filepath.Join(output, "first.signoz.json"))
	assert.NoFileExists(t, filepath.Join(output, "first.report.json"))
}

func TestMigrateGrafanaRequiresNamespaceForLiveTargetBeforeInputOrOutput(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	output := filepath.Join(t.TempDir(), "not-created")

	results, err := MigrateGrafana(context.Background(), []string{"does-not-need-to-exist.json"}, GrafanaOptions{
		OutputDirectory: output,
		TargetURL:       server.URL,
		APIKey:          "key",
		HTTPClient:      server.Client(),
	})

	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "source namespace is required")
	assert.Nil(t, results)
	assert.Zero(t, requests.Load())
	assert.NoDirExists(t, output)
}

func TestMigrateGrafanaRejectsOutputAliasToInputBeforeNetwork(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	directory := t.TempDir()
	input := filepath.Join(directory, "dashboard.json")
	require.NoError(t, os.WriteFile(input, []byte(`{"title":"Protected input","uid":"protected","panels":[]}`), 0o600))
	require.NoError(t, os.Link(input, filepath.Join(directory, "dashboard.signoz.json")))

	results, err := MigrateGrafana(context.Background(), []string{input}, GrafanaOptions{
		OutputDirectory: directory,
		TargetURL:       server.URL,
		APIKey:          "key",
		HTTPClient:      server.Client(),
		SourceNamespace: "grafana:test",
	})

	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "aliases protected Grafana input")
	assert.Nil(t, results)
	assert.Zero(t, requests.Load())
	contents, readErr := os.ReadFile(input)
	require.NoError(t, readErr)
	assert.Contains(t, string(contents), "Protected input")
}

func TestMigrateGrafanaRejectsOutputSymlinkSubstitutedAfterPreflight(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	directory := t.TempDir()
	input := filepath.Join(directory, "dashboard.json")
	require.NoError(t, os.WriteFile(input, []byte(`{"title":"Protected output","uid":"protected","panels":[]}`), 0o600))
	output := filepath.Join(directory, "out")
	victim := filepath.Join(directory, "victim")
	require.NoError(t, os.Mkdir(victim, 0o700))

	results, err := MigrateGrafana(context.Background(), []string{input}, GrafanaOptions{
		OutputDirectory: output,
		TargetURL:       server.URL,
		APIKey:          "key",
		HTTPClient:      server.Client(),
		SourceNamespace: "grafana:test",
		outputPreCreateCheckpoint: func() error {
			return os.Symlink(victim, output)
		},
	})

	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "not a real directory")
	assert.Nil(t, results)
	assert.Zero(t, requests.Load())
	entries, readErr := os.ReadDir(victim)
	require.NoError(t, readErr)
	assert.Empty(t, entries)
}
