package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/mansiverma897993/signoz/internal/app"
	"github.com/mansiverma897993/signoz/internal/artifactset"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiffOutputAliasPreflightProtectsCommittedEvidenceBeforeNetwork(t *testing.T) {
	sourcePath := "../../internal/source/grafana/testdata/modern.json"
	outputDirectory := t.TempDir()
	results, err := app.MigrateGrafana(context.Background(), []string{sourcePath}, app.GrafanaOptions{
		OutputDirectory: outputDirectory,
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	result := results[0]
	require.NotNil(t, result.Evidence.ArtifactSet)

	protected, err := artifactset.ProtectedPathsForReport(
		result.ReportPath,
		*result.Evidence.ArtifactSet,
		artifactset.KindDashboard,
	)
	require.NoError(t, err)
	before := snapshotCLIRegularFiles(t, protected)

	hardlink := filepath.Join(outputDirectory, "report-hardlink.differential.json")
	require.NoError(t, os.Link(result.ReportPath, hardlink))
	symlink := filepath.Join(outputDirectory, "report-symlink.differential.json")
	require.NoError(t, os.Symlink(result.ReportPath, symlink))
	destinations := append(append([]string(nil), protected...), hardlink, symlink, sourcePath)

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	t.Cleanup(server.Close)

	for _, destination := range destinations {
		_, commandErr := runCommand(t,
			"diff", sourcePath,
			"--source", server.URL,
			"--target", server.URL,
			"--migration-report", result.ReportPath,
			"--out", destination,
		)
		require.Error(t, commandErr, destination)
		assert.Equal(t, 3, commandExitCode(commandErr), destination)
		assert.Contains(t, commandErr.Error(), "aliases protected", destination)
		assert.Zero(t, requests.Load(), destination)
		assertCLIRegularFilesUnchanged(t, before)
	}
}

func TestReportCommandRejectsDestructiveOutputAliasWithoutMutation(t *testing.T) {
	sourcePath := "../../internal/source/grafana/testdata/modern.json"
	results, err := app.MigrateGrafana(context.Background(), []string{sourcePath}, app.GrafanaOptions{
		OutputDirectory: t.TempDir(),
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	reportPath := results[0].ReportPath
	before, err := os.ReadFile(reportPath)
	require.NoError(t, err)

	_, commandErr := runCommand(t, "report", reportPath, "--out", reportPath)
	require.Error(t, commandErr)
	assert.Equal(t, 3, commandExitCode(commandErr))
	assert.Contains(t, commandErr.Error(), "aliases protected input migration report")
	after, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func snapshotCLIRegularFiles(t *testing.T, paths []string) map[string][]byte {
	t.Helper()
	result := make(map[string][]byte)
	for _, path := range paths {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
		if !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		result[path] = data
	}
	return result
}

func assertCLIRegularFilesUnchanged(t *testing.T, expected map[string][]byte) {
	t.Helper()
	for path, before := range expected {
		after, err := os.ReadFile(path)
		require.NoError(t, err, path)
		assert.Equal(t, before, after, path)
	}
}
