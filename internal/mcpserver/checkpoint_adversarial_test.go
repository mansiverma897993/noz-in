package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPAttemptGenerationIsDurableBeforeDashboardUpsert(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outputRoot := filepath.Join(root, "migrations")
	observed := make(chan error, 2)
	var dashboardRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeCheckpointJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			writeCheckpointJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v5/query_range/preview":
			writeCheckpointJSONResponse(t, writer, map[string]any{"data": map[string]any{"compositeQuery": map[string]any{
				"A": map[string]any{"valid": true},
			}}})
		case "/api/v5/query_range":
			writeCheckpointJSONResponse(t, writer, map[string]any{
				"status": "success", "data": map[string]any{"data": map[string]any{"results": []any{map[string]any{
					"queryName": "A", "series": []any{map[string]any{"values": []any{map[string]any{"timestamp": 1, "value": 1}}}},
				}}}},
			})
		case "/api/v1/dashboards":
			dashboardRequests.Add(1)
			observed <- inspectDurableAttempt(outputRoot)
			if request.Method == http.MethodGet {
				writeCheckpointJSONResponse(t, writer, map[string]any{"data": []any{}})
				return
			}
			writeCheckpointJSONResponse(t, writer, map[string]any{"data": map[string]any{"id": "checkpoint-id"}})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	service, err := New(Config{
		Root: root, OutputRoot: outputRoot, TargetURL: server.URL,
		APIKey: "key", HTTPClient: server.Client(), Workers: 1,
	})
	require.NoError(t, err)
	result, err := service.handleMigrateDashboard(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{
			"grafana_json":     `{"uid":"checkpoint","title":"Checkpoint","panels":[{"title":"Up","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]}]}`,
			"source_namespace": "grafana:production", "import": true,
		}},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	assert.Equal(t, int32(2), dashboardRequests.Load())
	for range 2 {
		assert.NoError(t, <-observed)
	}
}

func TestMCPAttemptPublicationFailurePreventsDashboardUpsert(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outputRoot := filepath.Join(root, "migrations")
	var dashboardRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeCheckpointJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			writeCheckpointJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v5/query_range/preview":
			writeCheckpointJSONResponse(t, writer, map[string]any{"data": map[string]any{"compositeQuery": map[string]any{
				"A": map[string]any{"valid": true},
			}}})
		case "/api/v5/query_range":
			writeCheckpointJSONResponse(t, writer, map[string]any{
				"status": "success", "data": map[string]any{"data": map[string]any{"results": []any{map[string]any{
					"queryName": "A", "series": []any{map[string]any{"values": []any{map[string]any{"timestamp": 1, "value": 1}}}},
				}}}},
			})
		case "/api/v1/dashboards":
			dashboardRequests.Add(1)
			writer.WriteHeader(http.StatusInternalServerError)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	service, err := New(Config{
		Root: root, OutputRoot: outputRoot, TargetURL: server.URL,
		APIKey: "key", HTTPClient: server.Client(), Workers: 1, MaxOutputEntries: 8,
	})
	require.NoError(t, err)
	result, err := service.handleMigrateDashboard(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{
			"grafana_json":     `{"uid":"checkpoint-failure","title":"Checkpoint failure","panels":[{"title":"Up","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]}]}`,
			"source_namespace": "grafana:production", "import": true,
		}},
	})
	require.NoError(t, err)
	require.True(t, result.IsError)
	assert.Equal(t, int32(0), dashboardRequests.Load())
	encoded, err := json.Marshal(result.Content)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), "ARTIFACT_WRITE_FAILED")
	entries, err := os.ReadDir(outputRoot)
	require.NoError(t, err)
	assert.Empty(t, entries, "a failed pre-target checkpoint must not leave a visible incomplete migration")
}

func TestMCPFinalPublicationFailureRetainsAttemptedEvidence(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outputRoot := filepath.Join(root, "migrations")
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeCheckpointJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			writeCheckpointJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v5/query_range/preview":
			writeCheckpointJSONResponse(t, writer, map[string]any{"data": map[string]any{"compositeQuery": map[string]any{
				"A": map[string]any{"valid": true},
			}}})
		case "/api/v5/query_range":
			writeCheckpointJSONResponse(t, writer, map[string]any{
				"status": "success", "data": map[string]any{"data": map[string]any{"results": []any{map[string]any{
					"queryName": "A", "series": []any{map[string]any{"values": []any{map[string]any{"timestamp": 1, "value": 1}}}},
				}}}},
			})
		case "/api/v1/dashboards":
			if request.Method == http.MethodGet {
				writeCheckpointJSONResponse(t, writer, map[string]any{"data": []any{}})
				return
			}
			posts.Add(1)
			writeCheckpointJSONResponse(t, writer, map[string]any{"data": map[string]any{"id": "created-but-unrecorded"}})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	service, err := New(Config{
		Root: root, OutputRoot: outputRoot, TargetURL: server.URL,
		APIKey: "key", HTTPClient: server.Client(), Workers: 1,
	})
	require.NoError(t, err)
	service.publicationFault = func(stage string) error {
		if stage == "migration-result" {
			return fmt.Errorf("injected final publication failure")
		}
		return nil
	}
	result, err := service.handleMigrateDashboard(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{
			"grafana_json":     `{"uid":"final-checkpoint-failure","title":"Final checkpoint failure","panels":[{"title":"Up","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]}]}`,
			"source_namespace": "grafana:production", "import": true,
		}},
	})
	require.NoError(t, err)
	require.True(t, result.IsError)
	assert.Equal(t, int32(1), posts.Load())
	encoded, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	var response migrateResponse
	require.NoError(t, json.Unmarshal(encoded, &response))
	assert.Equal(t, migrateTargetFailed, response.TargetStatus)
	assert.Contains(t, response.TargetSkippedReason, "final outcome")
	require.NotNil(t, response.Failure)
	assert.Contains(t, response.Failure.Message, "injected final publication failure")
	assert.Contains(t, response.Artifacts.ReportJSON, string(filepath.Separator)+attemptGeneration+string(filepath.Separator))
	assert.FileExists(t, response.Artifacts.ReportJSON)
	assert.NoError(t, inspectDurableAttempt(outputRoot))
	_, statErr := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(response.Artifacts.ReportJSON)), "migration-result.json"))
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func inspectDurableAttempt(outputRoot string) error {
	entries, err := os.ReadDir(outputRoot)
	if err != nil {
		return err
	}
	migrations := make([]os.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() != mcpWorkRootName {
			migrations = append(migrations, entry)
		}
	}
	if len(migrations) != 1 || !migrations[0].IsDir() {
		return fmt.Errorf("expected one migration directory, got %d entries", len(migrations))
	}
	directory := filepath.Join(outputRoot, migrations[0].Name())
	data, err := os.ReadFile(filepath.Join(directory, "migration.json"))
	if err != nil {
		return err
	}
	value, err := decodeManifest(data)
	if err != nil {
		return err
	}
	if value.Generation != attemptGeneration {
		return fmt.Errorf("attempt manifest points to generation %q", value.Generation)
	}
	for _, name := range []string{value.Report, value.Dashboard, value.HTML} {
		info, err := os.Lstat(filepath.Join(directory, value.Generation, name))
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("attempt artifact %q is not a regular file", name)
		}
	}
	return nil
}

func writeCheckpointJSONResponse(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(writer).Encode(value))
}
