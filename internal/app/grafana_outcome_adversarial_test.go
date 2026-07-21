package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/mansiverma897993/signoz/pkg/reporttypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrateGrafanaExternalCheckpointPrecedesDashboardUpsert(t *testing.T) {
	t.Parallel()

	var attemptedCheckpoint atomic.Bool
	var dashboardRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v1/dashboards":
			dashboardRequests.Add(1)
			assert.True(t, attemptedCheckpoint.Load(), "attempted evidence must cross the external durability boundary first")
			if request.Method == http.MethodGet {
				writeJSONResponse(t, writer, map[string]any{"data": []any{}})
				return
			}
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"id": "checkpoint-id"}})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	input := filepath.Join(t.TempDir(), "checkpoint.json")
	require.NoError(t, os.WriteFile(input, []byte(`{
		"title":"External checkpoint",
		"panels":[{"title":"Up","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]}]
	}`), 0o600))
	var phases []string
	results, err := MigrateGrafana(context.Background(), []string{input}, GrafanaOptions{
		OutputDirectory: t.TempDir(), TargetURL: server.URL, APIKey: "key", HTTPClient: server.Client(),
		SourceNamespace: "grafana:test",
		ArtifactCheckpoint: func(result GrafanaResult) error {
			phases = append(phases, result.TargetAction)
			if result.ImportAttempted && result.TargetAction == "attempted" {
				attemptedCheckpoint.Store(true)
			}
			return nil
		},
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, []string{"ready", "attempted", "created"}, phases)
	assert.Equal(t, int32(2), dashboardRequests.Load())
	assert.True(t, results[0].ImportSucceeded)
}

func TestMigrateGrafanaExternalCheckpointFailurePreventsDashboardUpsert(t *testing.T) {
	t.Parallel()

	var dashboardRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v1/dashboards":
			dashboardRequests.Add(1)
			writer.WriteHeader(http.StatusInternalServerError)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	input := filepath.Join(t.TempDir(), "checkpoint-failure.json")
	require.NoError(t, os.WriteFile(input, []byte(`{
		"title":"External checkpoint failure",
		"panels":[{"title":"Up","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]}]
	}`), 0o600))
	output := t.TempDir()
	results, err := MigrateGrafana(context.Background(), []string{input}, GrafanaOptions{
		OutputDirectory: output, TargetURL: server.URL, APIKey: "key", HTTPClient: server.Client(),
		SourceNamespace: "grafana:test",
		ArtifactCheckpoint: func(result GrafanaResult) error {
			if result.ImportAttempted && result.TargetAction == "attempted" {
				return errors.New("durability boundary unavailable")
			}
			return nil
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "durability boundary unavailable")
	assert.Equal(t, int32(0), dashboardRequests.Load())
	require.Len(t, results, 1)
	assert.False(t, results[0].ImportAttempted)
	assert.False(t, results[0].ImportSucceeded)
	assert.Equal(t, "skipped", results[0].TargetAction)
	stored, reportData, readErr := readMigrationEvidence(results[0].ReportPath)
	require.NoError(t, readErr)
	assert.Equal(t, false, stored.Run.Flags["importAttempted"])
	assert.Equal(t, "skipped", stored.Run.Flags["targetAction"])
	_, _, readErr = readBoundPrimaryDashboard(results[0].ReportPath, reportData, stored)
	require.NoError(t, readErr)
}

func TestMigrateGrafanaReadyCheckpointFailurePersistsSkippedOutcome(t *testing.T) {
	t.Parallel()

	var dashboardRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v1/dashboards":
			dashboardRequests.Add(1)
			writer.WriteHeader(http.StatusInternalServerError)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	input := filepath.Join(t.TempDir(), "ready-checkpoint-failure.json")
	require.NoError(t, os.WriteFile(input, []byte(`{
		"title":"Ready checkpoint failure",
		"panels":[{"title":"Up","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]}]
	}`), 0o600))
	results, err := MigrateGrafana(context.Background(), []string{input}, GrafanaOptions{
		OutputDirectory: t.TempDir(), TargetURL: server.URL, APIKey: "key", HTTPClient: server.Client(),
		SourceNamespace: "grafana:test",
		ArtifactCheckpoint: func(result GrafanaResult) error {
			if result.TargetAction == "ready" {
				return errors.New("ready durability boundary unavailable")
			}
			return nil
		},
	})
	require.Error(t, err)
	assert.Equal(t, int32(0), dashboardRequests.Load())
	require.Len(t, results, 1)
	stored, reportData, readErr := readMigrationEvidence(results[0].ReportPath)
	require.NoError(t, readErr)
	assert.Equal(t, false, stored.Run.Flags["importAttempted"])
	assert.Equal(t, "skipped", stored.Run.Flags["targetAction"])
	_, _, readErr = readBoundPrimaryDashboard(results[0].ReportPath, reportData, stored)
	require.NoError(t, readErr)
}

func TestMigrateGrafanaRemovesStaleCandidateAfterCleanRerun(t *testing.T) {
	t.Parallel()

	var reject atomic.Bool
	reject.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v5/query_range/preview":
			if reject.Load() {
				writer.WriteHeader(http.StatusBadRequest)
				writeJSONResponse(t, writer, map[string]any{"error": map[string]any{
					"code": "rejected_once", "message": "first-run rejection",
				}})
				return
			}
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{
				"compositeQuery": map[string]any{"A": map[string]any{"valid": true}},
			}})
		case "/api/v5/query_range":
			writeJSONResponse(t, writer, map[string]any{
				"status": "success",
				"data": map[string]any{"data": map[string]any{"results": []any{map[string]any{
					"queryName": "A",
					"series":    []any{map[string]any{"values": []any{map[string]any{"timestamp": 1, "value": 1}}}},
				}}}},
			})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	input := filepath.Join(t.TempDir(), "stale.json")
	require.NoError(t, os.WriteFile(input, []byte(`{
		"title":"Stale candidate",
		"panels":[{"title":"Up","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]}]
	}`), 0o600))
	output := t.TempDir()
	options := GrafanaOptions{
		OutputDirectory: output, TargetURL: server.URL, APIKey: "key", HTTPClient: server.Client(),
		SourceNamespace: "grafana:test",
		Validate:        true, DryRun: true,
	}

	first, err := MigrateGrafana(context.Background(), []string{input}, options)
	require.NoError(t, err)
	require.Len(t, first, 1)
	require.NotEmpty(t, first[0].CandidateDashboardPath)
	assert.FileExists(t, first[0].CandidateDashboardPath)

	reject.Store(false)
	second, err := MigrateGrafana(context.Background(), []string{input}, options)
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.Empty(t, second[0].CandidateDashboardPath)
	_, statErr := os.Stat(filepath.Join(output, "stale.candidate.signoz.json"))
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestMigrateGrafanaPreflightsEvidenceBeforeTargetMutation(t *testing.T) {
	t.Parallel()

	var dashboardCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v1/dashboards":
			dashboardCalls.Add(1)
			writeJSONResponse(t, writer, map[string]any{"data": []any{}})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	input := filepath.Join(t.TempDir(), "preflight.json")
	require.NoError(t, os.WriteFile(input, []byte(`{
		"title":"Preflight",
		"panels":[{"title":"Up","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]}]
	}`), 0o600))
	output := t.TempDir()
	staleCandidate := filepath.Join(output, "preflight.candidate.signoz.json")
	staleBytes := []byte("previous candidate evidence\n")
	require.NoError(t, os.WriteFile(staleCandidate, staleBytes, 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(output, "preflight.report.json"), 0o700))

	results, err := MigrateGrafana(context.Background(), []string{input}, GrafanaOptions{
		OutputDirectory: output, TargetURL: server.URL, APIKey: "key", HTTPClient: server.Client(),
		SourceNamespace: "grafana:test",
	})

	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "preflight.report.json")
	assert.Equal(t, int32(0), dashboardCalls.Load(), "target must not be read or mutated before evidence preflight succeeds")
	assert.Nil(t, results)
	preserved, readErr := os.ReadFile(staleCandidate)
	require.NoError(t, readErr)
	assert.Equal(t, staleBytes, preserved, "failed replacement runs must not destroy prior candidate evidence")
}

func TestMigrateGrafanaWriteAheadMarksAttemptBeforeFirstTargetRequest(t *testing.T) {
	t.Parallel()

	output := t.TempDir()
	input := filepath.Join(t.TempDir(), "write-ahead.json")
	require.NoError(t, os.WriteFile(input, []byte(`{
		"title":"Write ahead",
		"panels":[{"title":"Up","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]}]
	}`), 0o600))
	reportPath := filepath.Join(output, "write-ahead.report.json")
	observed := make(chan reporttypes.Report, 1)
	var inspected atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v1/dashboards":
			if inspected.CompareAndSwap(false, true) {
				var evidence reporttypes.Report
				decodeFile(t, reportPath, &evidence)
				observed <- evidence
			}
			if request.Method == http.MethodGet {
				writeJSONResponse(t, writer, map[string]any{"data": []any{}})
				return
			}
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"id": "write-ahead-id"}})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	results, err := MigrateGrafana(context.Background(), []string{input}, GrafanaOptions{
		OutputDirectory: output, TargetURL: server.URL, APIKey: "key", HTTPClient: server.Client(),
		SourceNamespace: "grafana:test",
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	writeAhead := <-observed
	assert.Equal(t, true, writeAhead.Run.Flags["importRequested"])
	assert.Equal(t, true, writeAhead.Run.Flags["importAttempted"])
	assert.Equal(t, false, writeAhead.Run.Flags["importSucceeded"])
	assert.Equal(t, "attempted", writeAhead.Run.Flags["targetAction"])
}

func TestMigrateGrafanaContinuesBatchAfterTargetFailure(t *testing.T) {
	t.Parallel()

	var posts atomic.Int32
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
			if posts.Add(1) == 1 {
				writer.WriteHeader(http.StatusBadRequest)
				writeJSONResponse(t, writer, map[string]any{"error": map[string]any{
					"code": "first_rejected", "message": "first target failure",
				}})
				return
			}
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"id": "third-dashboard"}})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	directory := t.TempDir()
	first := filepath.Join(directory, "first.json")
	third := filepath.Join(directory, "third.json")
	for path, title := range map[string]string{first: "First", third: "Third"} {
		require.NoError(t, os.WriteFile(path, []byte(`{
			"title":"`+title+`",
			"panels":[{"title":"Up","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]}]
		}`), 0o600))
	}

	results, err := MigrateGrafana(context.Background(), []string{first, third}, GrafanaOptions{
		OutputDirectory: t.TempDir(), TargetURL: server.URL, APIKey: "key", HTTPClient: server.Client(),
		SourceNamespace: "grafana:test",
	})
	require.Error(t, err)
	assert.Equal(t, ErrorTarget, KindOf(err))
	assert.Contains(t, err.Error(), "first target failure")
	require.Len(t, results, 2)
	assert.Equal(t, "failed", results[0].TargetAction)
	assert.True(t, results[1].ImportSucceeded)
	assert.Equal(t, "third-dashboard", results[1].TargetDashboardID)
	assert.Equal(t, int32(2), posts.Load())
}

func TestMigrateGrafanaRejectsInvalidPrimaryDestinationBeforeTargetAccess(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.URL.Path == "/api/v2/metrics/metadata" {
			writer.WriteHeader(http.StatusUnauthorized)
			writeJSONResponse(t, writer, map[string]any{"error": map[string]any{
				"code": "bad_credentials", "message": "distinct metadata failure",
			}})
			return
		}
		writer.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	input := filepath.Join(t.TempDir(), "structured.json")
	require.NoError(t, os.WriteFile(input, []byte(`{
		"title":"Structured failure",
		"panels":[{"title":"Up","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]}]
	}`), 0o600))
	output := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(output, "structured.signoz.json"), 0o700))

	results, err := MigrateGrafana(context.Background(), []string{input}, GrafanaOptions{
		OutputDirectory: output, TargetURL: server.URL, APIKey: "bad", HTTPClient: server.Client(),
		SourceNamespace: "grafana:test",
	})
	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "structured.signoz.json")
	assert.Nil(t, results)
	assert.Zero(t, requests.Load())
}

func TestWriteJSONRefusesSymlinkDestination(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	victim := filepath.Join(directory, "victim.json")
	destination := filepath.Join(directory, "artifact.json")
	require.NoError(t, os.WriteFile(victim, []byte("unchanged\n"), 0o600))
	if err := os.Symlink(victim, destination); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	err := writeJSON(destination, map[string]any{"changed": true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a regular file")
	contents, readErr := os.ReadFile(victim)
	require.NoError(t, readErr)
	assert.Equal(t, "unchanged\n", string(contents))
}

func TestMigrateGrafanaRejectsEarlierInputBeforeTargetFailure(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
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
			writer.WriteHeader(http.StatusBadRequest)
			writeJSONResponse(t, writer, map[string]any{"error": map[string]any{
				"code": "target_rejected", "message": "target rejected valid dashboard",
			}})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	directory := t.TempDir()
	invalid := filepath.Join(directory, "invalid.json")
	valid := filepath.Join(directory, "valid.json")
	require.NoError(t, os.WriteFile(invalid, []byte("{"), 0o600))
	require.NoError(t, os.WriteFile(valid, []byte(`{
		"title":"Valid",
		"panels":[{"title":"Up","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]}]
	}`), 0o600))

	results, err := MigrateGrafana(context.Background(), []string{invalid, valid}, GrafanaOptions{
		OutputDirectory: t.TempDir(), TargetURL: server.URL, APIKey: "key", HTTPClient: server.Client(),
		SourceNamespace: "grafana:test",
	})

	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "invalid.json")
	assert.Nil(t, results)
	assert.Zero(t, requests.Load())
}

func TestRemoveStaleArtifactRefusesNonRegularPath(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "candidate.signoz.json")
	require.NoError(t, os.Mkdir(path, 0o700))
	err := removeStaleArtifact(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a regular file")
	assert.DirExists(t, path)
}
