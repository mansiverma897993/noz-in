package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	reportpkg "github.com/mansiverma897993/signoz/internal/report"
	"github.com/mansiverma897993/signoz/internal/target/signoz"
	"github.com/mansiverma897993/signoz/pkg/reporttypes"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrateAndExplainToolsShareReportState(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(filepath.Join("..", "source", "grafana", "testdata", "modern.json"))
	require.NoError(t, err)
	service, err := New(Config{
		Root:       "..",
		OutputRoot: t.TempDir(),
		Now:        func() time.Time { return time.Date(2026, 7, 15, 12, 30, 0, 0, time.UTC) },
	})
	require.NoError(t, err)

	result, err := service.handleMigrateDashboard(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{"grafana_json": string(data)}},
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	encoded, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	var migration migrateResponse
	require.NoError(t, json.Unmarshal(encoded, &migration))
	assert.NotEmpty(t, migration.MigrationID)
	assert.Equal(t, "Service overview", migration.DashboardTitle)
	assert.False(t, migration.ImportRequested)
	assert.Equal(t, migrateTargetNotRequested, migration.TargetStatus)
	assert.Empty(t, migration.TargetSkippedReason)
	assert.FileExists(t, migration.Artifacts.ReportJSON)
	assert.FileExists(t, migration.Artifacts.ReportHTML)

	explanation, err := service.handleExplainVerdict(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{
			"migration_id": migration.MigrationID,
			"panel":        "Availability",
			"query":        "A",
		}},
	})
	require.NoError(t, err)
	require.False(t, explanation.IsError)
	explanationJSON, err := json.Marshal(explanation.StructuredContent)
	require.NoError(t, err)
	var details explainResponse
	require.NoError(t, json.Unmarshal(explanationJSON, &details))
	require.Len(t, details.Items, 1)
	assert.Equal(t, "A", details.Items[0].Query)
	assert.NotEmpty(t, details.Items[0].Explanation)
}

func TestMCPInputPathCannotEscapeRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	service, err := New(Config{Root: root, OutputRoot: filepath.Join(root, "out")})
	require.NoError(t, err)

	_, err = service.readInputBounded(filepath.Join("..", "outside.json"))
	require.ErrorContains(t, err, "resolve input path")
}

func TestMCPInputReadCannotFollowSymlinkOutsideRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.json")
	require.NoError(t, os.WriteFile(outside, []byte(`{"secret":true}`), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape.json")))
	service, err := New(Config{Root: root, OutputRoot: filepath.Join(root, "out")})
	require.NoError(t, err)

	_, err = service.readInputBounded("escape.json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "beneath")
}

func TestMCPInputReadRejectsReplacedConfiguredRoot(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	require.NoError(t, os.Mkdir(root, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "input.json"), []byte(`{"safe":true}`), 0o600))
	service, err := New(Config{Root: root, OutputRoot: t.TempDir()})
	require.NoError(t, err)

	require.NoError(t, os.Rename(root, filepath.Join(parent, "original-root")))
	require.NoError(t, os.Mkdir(root, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "input.json"), []byte(`{"attacker":true}`), 0o600))
	_, err = service.readInputBounded("input.json")
	require.ErrorContains(t, err, "replaced after server initialization")
}

func TestMCPWriteRejectsReplacedConfiguredOutputRoot(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	output := filepath.Join(parent, "out")
	require.NoError(t, os.Mkdir(root, 0o700))
	require.NoError(t, os.Mkdir(output, 0o700))
	service, err := New(Config{Root: root, OutputRoot: output})
	require.NoError(t, err)

	require.NoError(t, os.Rename(output, filepath.Join(parent, "original-out")))
	require.NoError(t, os.Mkdir(output, 0o700))
	_, _, _, err = service.beginMigrationWork([]byte(`{"safe":true}`), time.Now(), false)
	require.ErrorContains(t, err, "replaced after server initialization")
}

func TestMCPRootedPublishCannotFollowReplacedMigrationDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	output := filepath.Join(root, "out")
	service, err := New(Config{Root: root, OutputRoot: output})
	require.NoError(t, err)
	id := "dashboard-rooted-publish"
	require.NoError(t, service.createOutputDirectory(id))
	directory := filepath.Join(output, id)
	require.NoError(t, os.Rename(directory, directory+"-original"))
	outside := filepath.Join(t.TempDir(), "outside")
	require.NoError(t, os.Mkdir(outside, 0o700))
	if err := os.Symlink(outside, directory); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}

	err = service.writeOutputAtomic(filepath.Join(id, "probe.json"), []byte("{}\n"))
	require.ErrorContains(t, err, "not a real directory")
	_, statErr := os.Stat(filepath.Join(outside, "probe.json"))
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestMCPValidationDirectoryRejectsSymlink(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	output := filepath.Join(root, "out")
	service, err := New(Config{Root: root, OutputRoot: output})
	require.NoError(t, err)
	id := "dashboard-validation-test"
	directory, err := service.migrationDirectory(id)
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(directory, 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(output, "redirect-target"), 0o700))
	if err := os.Symlink(filepath.Join(output, "redirect-target"), filepath.Join(directory, "validations")); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}

	_, _, _, err = service.beginValidationWork(id)
	require.ErrorContains(t, err, "not a real directory")
}

func TestReadManifestRejectsTrailingJSONAndPathTraversal(t *testing.T) {
	t.Parallel()

	valid := `{"schemaVersion":1,"migrationId":"dashboard-1","source":"source.json","report":"report.json","dashboard":"dashboard.json","html":"report.html","rateInterval":"5m"}`
	for name, contents := range map[string]string{
		"trailing":           valid + ` {}`,
		"traversal":          `{"schemaVersion":1,"migrationId":"dashboard-1","source":"../source.json","report":"report.json","dashboard":"dashboard.json","html":"report.html","rateInterval":"5m"}`,
		"dot dot":            `{"schemaVersion":1,"migrationId":"dashboard-1","source":"..","report":"report.json","dashboard":"dashboard.json","html":"report.html","rateInterval":"5m"}`,
		"portable backslash": `{"schemaVersion":1,"migrationId":"dashboard-1","source":"folder\\source.json","report":"report.json","dashboard":"dashboard.json","html":"report.html","rateInterval":"5m"}`,
	} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(directory, "migration.json"), []byte(contents), 0o600))
			_, err := readManifestForTest(directory)
			require.Error(t, err)
		})
	}
}

func TestDecodeManifestKeepsLegacyRuleMigrationsReadable(t *testing.T) {
	t.Parallel()
	legacy := []byte(`{"schemaVersion":1,"migrationId":"dashboard-legacy","source":"source.json","rules":["source.rules.001.yaml"],"report":"report.json","dashboard":"dashboard.json","html":"report.html","rateInterval":"5m"}`)
	state, err := decodeManifest(legacy)
	require.NoError(t, err)
	assert.Equal(t, []string{"source.rules.001.yaml"}, state.Rules)
	assert.Empty(t, state.RuleBindings)
}

func TestReadManifestRequiresRequestedMigrationIdentity(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	output := filepath.Join(root, "out")
	service, err := New(Config{Root: root, OutputRoot: output})
	require.NoError(t, err)
	directory := filepath.Join(output, "requested-id")
	require.NoError(t, os.Mkdir(directory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "migration.json"), []byte(
		`{"schemaVersion":1,"migrationId":"different-id","source":"source.json","report":"report.json","dashboard":"dashboard.json","html":"report.html","rateInterval":"5m"}`,
	), 0o600))

	_, err = service.readManifest("requested-id")
	require.ErrorContains(t, err, "does not match requested migration_id")
}

func TestRuleInputsEnforceCountAndAggregateBounds(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	service, err := New(Config{Root: root, OutputRoot: filepath.Join(root, "out")})
	require.NoError(t, err)
	tooMany := make([]string, maxMCPRuleInputs+1)
	_, err = service.ruleInputs(tooMany)
	require.ErrorContains(t, err, "maximum")

	first := filepath.Join(root, "first.rules.yaml")
	second := filepath.Join(root, "second.rules.yaml")
	require.NoError(t, os.WriteFile(first, []byte("small"), 0o600))
	large, err := os.Create(second)
	require.NoError(t, err)
	require.NoError(t, large.Truncate(maxMCPArtifactSize))
	require.NoError(t, large.Close())
	_, err = service.ruleInputs([]string{"first.rules.yaml", "second.rules.yaml"})
	require.ErrorContains(t, err, "exceeds")
}

func TestGrafanaDownloadHostPolicy(t *testing.T) {
	t.Parallel()

	assert.True(t, isGrafanaHost("grafana.com"))
	assert.True(t, isGrafanaHost("cdn.grafana.com."))
	assert.False(t, isGrafanaHost("grafana.com.example.org"))
	assert.False(t, isGrafanaHost("example.org"))
}

func TestValidateQueriesRequiresTargetConfiguration(t *testing.T) {
	t.Parallel()

	service, err := New(Config{Root: t.TempDir(), OutputRoot: t.TempDir()})
	require.NoError(t, err)
	result, err := service.handleValidateQueries(context.Background(), mcp.CallToolRequest{})
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestMCPToolWorkingSetIsSerializedAndContextBound(t *testing.T) {
	t.Parallel()

	service, err := New(Config{Root: t.TempDir(), OutputRoot: t.TempDir()})
	require.NoError(t, err)
	release, err := service.acquireTool(context.Background())
	require.NoError(t, err)
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = service.acquireTool(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

func TestMCPValidationWorkersAreBounded(t *testing.T) {
	t.Parallel()

	service, err := New(Config{Root: t.TempDir(), OutputRoot: t.TempDir()})
	require.NoError(t, err)
	assert.Equal(t, MaxValidationWorkers, service.config.Workers)

	_, err = New(Config{Workers: -1})
	require.ErrorContains(t, err, "zero (default) or between")
	assert.True(t, IsConfigError(err))

	_, err = New(Config{Workers: MaxValidationWorkers + 1})
	require.ErrorContains(t, err, "must not exceed")
	assert.True(t, IsConfigError(err))
}

func TestMigrateDashboardAnnotationDeclaresPossibleTargetMutation(t *testing.T) {
	t.Parallel()

	service, err := New(Config{Root: t.TempDir(), OutputRoot: t.TempDir()})
	require.NoError(t, err)
	tool := service.Server().GetTool("migrate_dashboard")
	require.NotNil(t, tool)
	require.NotNil(t, tool.Tool.Annotations.ReadOnlyHint)
	require.NotNil(t, tool.Tool.Annotations.DestructiveHint)
	require.NotNil(t, tool.Tool.Annotations.IdempotentHint)
	assert.False(t, *tool.Tool.Annotations.ReadOnlyHint)
	assert.True(t, *tool.Tool.Annotations.DestructiveHint)
	assert.False(t, *tool.Tool.Annotations.IdempotentHint, "each call allocates a new evidence directory even when the target upsert is stable")
}

func TestValidateQueriesPreservesAuthoritativeMigrationArtifacts(t *testing.T) {
	t.Parallel()

	var returnData atomic.Bool
	returnData.Store(true)
	var lastPromQL atomic.Value
	lastPromQL.Store("")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		write := func(value any) {
			writer.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(writer).Encode(value))
		}
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			write(map[string]any{"data": map[string]any{
				"type": "sum", "temporality": "cumulative", "isMonotonic": true,
			}})
		case "/api/v2/metrics/attributes":
			write(map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v5/query_range/preview":
			lastPromQL.Store(readPromQLFromRequest(t, request))
			write(map[string]any{"data": map[string]any{"compositeQuery": map[string]any{
				"A": map[string]any{"valid": true},
			}}})
		case "/api/v5/query_range":
			lastPromQL.Store(readPromQLFromRequest(t, request))
			series := []any{}
			if returnData.Load() {
				series = []any{map[string]any{"values": []any{map[string]any{"timestamp": 1, "value": 1}}}}
			}
			write(map[string]any{
				"status": "success",
				"data": map[string]any{"data": map[string]any{"results": []any{map[string]any{
					"queryName": "A", "series": series,
				}}}},
			})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	service, err := New(Config{
		Root: root, OutputRoot: filepath.Join(root, "migrations"),
		TargetURL: server.URL, APIKey: "key", HTTPClient: server.Client(), Workers: 1,
	})
	require.NoError(t, err)
	migrated, err := service.handleMigrateDashboard(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{
			"grafana_json":     `{"uid":"stable","title":"Artifact identity","panels":[{"title":"Rate","type":"timeseries","targets":[{"refId":"A","expr":"sum(increase(counter_total[$__range]))"}]}]}`,
			"source_namespace": "grafana:test",
		}},
	})
	require.NoError(t, err)
	require.False(t, migrated.IsError)
	encodedMigration, err := json.Marshal(migrated.StructuredContent)
	require.NoError(t, err)
	var migration migrateResponse
	require.NoError(t, json.Unmarshal(encodedMigration, &migration))
	assert.False(t, migration.ImportRequested)
	assert.Equal(t, migrateTargetDryRun, migration.TargetStatus)
	assert.NotEmpty(t, migration.TargetSkippedReason)
	originalReport, err := os.ReadFile(migration.Artifacts.ReportJSON)
	require.NoError(t, err)
	originalDashboard, err := os.ReadFile(migration.Artifacts.DashboardV5)
	require.NoError(t, err)

	returnData.Store(false)
	lastPromQL.Store("")
	validated, err := service.handleValidateQueries(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{
			"migration_id": migration.MigrationID, "window": "30m",
		}},
	})
	require.NoError(t, err)
	require.False(t, validated.IsError)
	encodedValidation, err := json.Marshal(validated.StructuredContent)
	require.NoError(t, err)
	var validation validateResponse
	require.NoError(t, json.Unmarshal(encodedValidation, &validation))

	currentReport, err := os.ReadFile(migration.Artifacts.ReportJSON)
	require.NoError(t, err)
	currentDashboard, err := os.ReadFile(migration.Artifacts.DashboardV5)
	require.NoError(t, err)
	assert.Equal(t, originalReport, currentReport)
	assert.Equal(t, originalDashboard, currentDashboard)
	assert.NotEqual(t, migration.Artifacts.ReportJSON, validation.Artifacts.ReportJSON)
	assert.NotEqual(t, migration.Artifacts.DashboardV5, validation.Artifacts.DashboardV5)
	assert.Contains(t, validation.Artifacts.ReportJSON, string(filepath.Separator)+"validations"+string(filepath.Separator))
	assert.FileExists(t, validation.Artifacts.ReportJSON)
	assert.FileExists(t, validation.Artifacts.ReportHTML)
	assert.FileExists(t, validation.Artifacts.DashboardV5)
	renderedValidation := filepath.Join(t.TempDir(), "rendered-validation.html")
	require.NoError(t, reportpkg.RenderFile(validation.Artifacts.ReportJSON, renderedValidation))
	assert.FileExists(t, renderedValidation)
	assert.Equal(t, 1, validation.Delta.DataNoLongerPresent)
	assert.Equal(t, 0, validation.Totals.DataReturned)
	assert.Contains(t, lastPromQL.Load().(string), "[1h]", "revalidation must execute the stored query artifact")
	assert.NotContains(t, lastPromQL.Load().(string), "[30m]", "the requested evidence window must not trigger retranslation")
}

func TestMigrateDashboardPreservesStructuredFailedImportEvidence(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		write := func(status int, value any) {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(status)
			require.NoError(t, json.NewEncoder(writer).Encode(value))
		}
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			write(http.StatusOK, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			write(http.StatusOK, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v5/query_range/preview":
			write(http.StatusOK, map[string]any{"data": map[string]any{"compositeQuery": map[string]any{
				"A": map[string]any{"valid": true},
			}}})
		case "/api/v5/query_range":
			write(http.StatusOK, map[string]any{
				"status": "success", "data": map[string]any{"data": map[string]any{"results": []any{map[string]any{
					"queryName": "A", "series": []any{map[string]any{"values": []any{map[string]any{"timestamp": 1, "value": 1}}}},
				}}}},
			})
		case "/api/v1/dashboards":
			if request.Method == http.MethodGet {
				write(http.StatusOK, map[string]any{"data": []any{}})
				return
			}
			require.Equal(t, http.MethodPost, request.Method)
			write(http.StatusUnprocessableEntity, map[string]any{"error": map[string]any{
				"code": "invalid_dashboard", "message": "target rejected dashboard",
			}})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	service, err := New(Config{
		Root: root, OutputRoot: filepath.Join(root, "migrations"), TargetURL: server.URL,
		APIKey: "key", HTTPClient: server.Client(), Workers: 1,
	})
	require.NoError(t, err)
	result, err := service.handleMigrateDashboard(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{
			"grafana_json":     `{"uid":"failed-import","title":"Failed import","panels":[{"title":"Up","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]}]}`,
			"source_namespace": "grafana:test",
			"import":           true,
		}},
	})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.NotNil(t, result.StructuredContent)
	encoded, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	var migration migrateResponse
	require.NoError(t, json.Unmarshal(encoded, &migration))

	assert.True(t, migration.ImportRequested)
	assert.Equal(t, migrateTargetFailed, migration.TargetStatus)
	assert.Contains(t, migration.TargetSkippedReason, "target import failed")
	assert.Contains(t, migration.TargetError, "target rejected dashboard")
	require.NotNil(t, migration.Failure)
	assert.Equal(t, "MIGRATION_FAILED", migration.Failure.Code)
	assert.Contains(t, migration.Failure.Message, "target rejected dashboard")
	assert.FileExists(t, migration.Artifacts.ReportJSON)
	assert.FileExists(t, migration.Artifacts.ReportHTML)
	assert.FileExists(t, migration.Artifacts.DashboardV5)
	directory := filepath.Dir(migration.Artifacts.ReportJSON)
	assert.FileExists(t, filepath.Join(directory, "migration.json"))

	persisted, err := readDashboardReportForTest(migration.Artifacts.ReportJSON)
	require.NoError(t, err)
	assert.Equal(t, true, persisted.Run.Flags["importRequested"])
	assert.Equal(t, true, persisted.Run.Flags["importAttempted"])
	assert.Equal(t, false, persisted.Run.Flags["importSucceeded"])
	assert.Equal(t, "failed", persisted.Run.Flags["targetAction"])
	assert.Contains(t, persisted.Run.Flags["targetError"], "target rejected dashboard")

	explanation, err := service.handleExplainVerdict(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{
			"migration_id": migration.MigrationID, "panel": "Up", "query": "A",
		}},
	})
	require.NoError(t, err)
	assert.False(t, explanation.IsError, "failed imports must retain a usable migration manifest")
}

func TestValidateQueriesRefreshesMetricMetadata(t *testing.T) {
	t.Parallel()

	var metricPresent atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		write := func(status int, value any) {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(status)
			require.NoError(t, json.NewEncoder(writer).Encode(value))
		}
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			if !metricPresent.Load() {
				write(http.StatusNotFound, map[string]any{"error": map[string]any{
					"code": "metric_not_found", "message": "metric up was not found",
				}})
				return
			}
			write(http.StatusOK, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			write(http.StatusOK, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v5/query_range/preview":
			write(http.StatusOK, map[string]any{"data": map[string]any{"compositeQuery": map[string]any{
				"A": map[string]any{"valid": true},
			}}})
		case "/api/v5/query_range":
			write(http.StatusOK, map[string]any{
				"status": "success", "data": map[string]any{"data": map[string]any{"results": []any{map[string]any{
					"queryName": "A", "series": []any{map[string]any{"values": []any{map[string]any{"timestamp": 1, "value": 1}}}},
				}}}},
			})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	service, err := New(Config{
		Root: root, OutputRoot: filepath.Join(root, "migrations"), TargetURL: server.URL,
		APIKey: "key", HTTPClient: server.Client(), Workers: 1,
	})
	require.NoError(t, err)
	migrated, err := service.handleMigrateDashboard(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{
			"grafana_json":     `{"uid":"metadata-refresh","title":"Metadata refresh","panels":[{"title":"Up","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]}]}`,
			"source_namespace": "grafana:test",
		}},
	})
	require.NoError(t, err)
	require.False(t, migrated.IsError)
	encodedMigration, err := json.Marshal(migrated.StructuredContent)
	require.NoError(t, err)
	var migration migrateResponse
	require.NoError(t, json.Unmarshal(encodedMigration, &migration))
	previous, err := readDashboardReportForTest(migration.Artifacts.ReportJSON)
	require.NoError(t, err)
	require.Len(t, previous.Panels, 1)
	require.Len(t, previous.Panels[0].Queries, 1)
	assert.True(t, previous.Panels[0].Queries[0].Validation.MetricChecked)
	assert.False(t, previous.Panels[0].Queries[0].Validation.MetricFound)

	metricPresent.Store(true)
	validated, err := service.handleValidateQueries(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{
			"migration_id": migration.MigrationID, "window": "30m",
		}},
	})
	require.NoError(t, err)
	require.False(t, validated.IsError)
	encodedValidation, err := json.Marshal(validated.StructuredContent)
	require.NoError(t, err)
	var validation validateResponse
	require.NoError(t, json.Unmarshal(encodedValidation, &validation))
	assert.Equal(t, 1, validation.Totals.MetricExists)
	assert.Equal(t, 1, validation.Delta.NewDataPresent)
	assert.Equal(t, 0, validation.Delta.DataNoLongerPresent)
}

func TestValidateQueriesRejectsChangedOrMissingPrimaryArtifact(t *testing.T) {
	t.Parallel()

	var validationCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		write := func(value any) {
			writer.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(writer).Encode(value))
		}
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			write(map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			write(map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v5/query_range/preview":
			validationCalls.Add(1)
			write(map[string]any{"data": map[string]any{"compositeQuery": map[string]any{
				"A": map[string]any{"valid": true},
			}}})
		case "/api/v5/query_range":
			validationCalls.Add(1)
			write(map[string]any{
				"status": "success", "data": map[string]any{"data": map[string]any{"results": []any{map[string]any{
					"queryName": "A", "series": []any{map[string]any{"values": []any{map[string]any{"timestamp": 1, "value": 1}}}},
				}}}},
			})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	service, err := New(Config{
		Root: root, OutputRoot: filepath.Join(root, "migrations"), TargetURL: server.URL,
		APIKey: "key", HTTPClient: server.Client(), Workers: 1,
	})
	require.NoError(t, err)
	migrated, err := service.handleMigrateDashboard(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{
			"grafana_json":     `{"uid":"artifact-provenance","title":"Artifact provenance","panels":[{"title":"Up","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]}]}`,
			"source_namespace": "grafana:test",
		}},
	})
	require.NoError(t, err)
	require.False(t, migrated.IsError)
	encodedMigration, err := json.Marshal(migrated.StructuredContent)
	require.NoError(t, err)
	var migration migrateResponse
	require.NoError(t, json.Unmarshal(encodedMigration, &migration))
	original, err := os.ReadFile(migration.Artifacts.DashboardV5)
	require.NoError(t, err)
	baselineCalls := validationCalls.Load()

	callValidation := func(t *testing.T) *mcp.CallToolResult {
		t.Helper()
		result, callErr := service.handleValidateQueries(context.Background(), mcp.CallToolRequest{
			Params: mcp.CallToolParams{Arguments: map[string]any{"migration_id": migration.MigrationID}},
		})
		require.NoError(t, callErr)
		require.True(t, result.IsError)
		encoded, marshalErr := json.Marshal(result.Content)
		require.NoError(t, marshalErr)
		assert.Contains(t, string(encoded), "ARTIFACT_PROVENANCE_INVALID")
		assert.Equal(t, baselineCalls, validationCalls.Load(), "provenance failure must precede target validation")
		return result
	}

	t.Run("changed query", func(t *testing.T) {
		var dashboard signoz.DashboardV5
		require.NoError(t, json.Unmarshal(original, &dashboard))
		require.NotEmpty(t, dashboard.Widgets)
		widget := &dashboard.Widgets[0]
		switch widget.Query.QueryType {
		case "promql":
			require.NotEmpty(t, widget.Query.PromQL)
			widget.Query.PromQL[0].Query += " + vector(1)"
		case "builder":
			if len(widget.Query.Builder.QueryData) > 0 {
				widget.Query.Builder.QueryData[0].StepInterval++
			} else {
				require.NotEmpty(t, widget.Query.Builder.QueryFormulas)
				widget.Query.Builder.QueryFormulas[0].Expression += " + 1"
			}
		default:
			t.Fatalf("unexpected query type %q", widget.Query.QueryType)
		}
		require.NoError(t, writeJSONAtomic(migration.Artifacts.DashboardV5, dashboard))
		callValidation(t)
	})

	require.NoError(t, writeAtomic(migration.Artifacts.DashboardV5, original))
	t.Run("changed title only", func(t *testing.T) {
		var dashboard signoz.DashboardV5
		require.NoError(t, json.Unmarshal(original, &dashboard))
		dashboard.Title += " tampered"
		require.NoError(t, writeJSONAtomic(migration.Artifacts.DashboardV5, dashboard))
		callValidation(t)
	})

	require.NoError(t, writeAtomic(migration.Artifacts.DashboardV5, original))
	t.Run("missing widget", func(t *testing.T) {
		var dashboard signoz.DashboardV5
		require.NoError(t, json.Unmarshal(original, &dashboard))
		require.NotEmpty(t, dashboard.Widgets)
		dashboard.Widgets = dashboard.Widgets[1:]
		require.NoError(t, writeJSONAtomic(migration.Artifacts.DashboardV5, dashboard))
		callValidation(t)
	})
}

func TestMigrateDashboardWithoutUIDIsIdempotentAcrossMCPArtifactDirectories(t *testing.T) {
	t.Parallel()

	var mutex sync.Mutex
	var stored *signoz.DashboardV5
	var posts atomic.Int32
	var puts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		write := func(status int, value any) {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(status)
			require.NoError(t, json.NewEncoder(writer).Encode(value))
		}
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			write(http.StatusOK, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			write(http.StatusOK, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v5/query_range/preview":
			write(http.StatusOK, map[string]any{"data": map[string]any{"compositeQuery": map[string]any{
				"A": map[string]any{"valid": true},
			}}})
		case "/api/v5/query_range":
			write(http.StatusOK, map[string]any{
				"status": "success", "data": map[string]any{"data": map[string]any{"results": []any{map[string]any{
					"queryName": "A", "series": []any{map[string]any{"values": []any{map[string]any{"timestamp": 1, "value": 1}}}},
				}}}},
			})
		case "/api/v1/dashboards":
			mutex.Lock()
			defer mutex.Unlock()
			if request.Method == http.MethodGet {
				if stored == nil {
					write(http.StatusOK, map[string]any{"data": []any{}})
					return
				}
				write(http.StatusOK, map[string]any{"data": []any{map[string]any{
					"id": "dashboard-1", "data": stored, "locked": false,
				}}})
				return
			}
			require.Equal(t, http.MethodPost, request.Method)
			var dashboard signoz.DashboardV5
			require.NoError(t, json.NewDecoder(request.Body).Decode(&dashboard))
			stored = &dashboard
			posts.Add(1)
			write(http.StatusCreated, map[string]any{"data": map[string]any{"id": "dashboard-1"}})
		case "/api/v1/dashboards/dashboard-1":
			require.Equal(t, http.MethodPut, request.Method)
			var dashboard signoz.DashboardV5
			require.NoError(t, json.NewDecoder(request.Body).Decode(&dashboard))
			mutex.Lock()
			stored = &dashboard
			mutex.Unlock()
			puts.Add(1)
			write(http.StatusOK, map[string]any{"data": map[string]any{"id": "dashboard-1"}})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	service, err := New(Config{
		Root: root, OutputRoot: filepath.Join(root, "migrations"), TargetURL: server.URL,
		APIKey: "key", HTTPClient: server.Client(), Workers: 1,
		Now: func() time.Time { return time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, err)
	input := `{"title":"No UID","panels":[{"title":"Up","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]}]}`
	var responses []migrateResponse
	for range 2 {
		result, callErr := service.handleMigrateDashboard(context.Background(), mcp.CallToolRequest{
			Params: mcp.CallToolParams{Arguments: map[string]any{
				"grafana_json": input, "source_namespace": "grafana:test",
				"source_identity": "dashboards/no-uid", "import": true,
			}},
		})
		require.NoError(t, callErr)
		require.False(t, result.IsError)
		encoded, marshalErr := json.Marshal(result.StructuredContent)
		require.NoError(t, marshalErr)
		var response migrateResponse
		require.NoError(t, json.Unmarshal(encoded, &response))
		responses = append(responses, response)
	}

	require.NotNil(t, responses[0].Imported)
	require.NotNil(t, responses[1].Imported)
	assert.True(t, responses[0].ImportRequested)
	assert.True(t, responses[1].ImportRequested)
	assert.Equal(t, migrateTargetImported, responses[0].TargetStatus)
	assert.Equal(t, migrateTargetImported, responses[1].TargetStatus)
	assert.Equal(t, "created", responses[0].Imported.Action)
	assert.Equal(t, "updated", responses[1].Imported.Action)
	assert.Equal(t, responses[0].Imported.DashboardID, responses[1].Imported.DashboardID)
	assert.Equal(t, int32(1), posts.Load())
	assert.Equal(t, int32(1), puts.Load())
}

func TestValidateQueriesPanelScopePrecedesEveryTargetCall(t *testing.T) {
	t.Parallel()

	var revalidation atomic.Bool
	var nowUnix atomic.Int64
	nowUnix.Store(time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC).Unix())
	var callsMu sync.Mutex
	var revalidationCalls []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		write := func(status int, value any) {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(status)
			require.NoError(t, json.NewEncoder(writer).Encode(value))
		}
		identity := request.URL.Query().Get("metricName")
		if request.URL.Path == "/api/v5/query_range/preview" || request.URL.Path == "/api/v5/query_range" {
			identity = readPromQLFromRequest(t, request)
		}
		if revalidation.Load() {
			callsMu.Lock()
			revalidationCalls = append(revalidationCalls, request.URL.Path+" "+identity)
			callsMu.Unlock()
			if strings.Contains(identity, "unrelated_metric") {
				write(http.StatusInternalServerError, map[string]any{"error": map[string]any{
					"code": "unrelated_panel_loaded", "message": "the unrelated panel must not be validated",
				}})
				return
			}
		}
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			write(http.StatusOK, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			write(http.StatusOK, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v5/query_range/preview":
			write(http.StatusOK, map[string]any{"data": map[string]any{"compositeQuery": map[string]any{
				"A": map[string]any{"valid": true},
			}}})
		case "/api/v5/query_range":
			write(http.StatusOK, map[string]any{
				"status": "success", "data": map[string]any{"data": map[string]any{"results": []any{map[string]any{
					"queryName": "A", "series": []any{map[string]any{"values": []any{map[string]any{"timestamp": 1, "value": 1}}}},
				}}}},
			})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	service, err := New(Config{
		Root: root, OutputRoot: filepath.Join(root, "migrations"), TargetURL: server.URL,
		APIKey: "key", HTTPClient: server.Client(), Workers: 1,
		Now: func() time.Time { return time.Unix(nowUnix.Load(), 0).UTC() },
	})
	require.NoError(t, err)
	migrated, err := service.handleMigrateDashboard(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{
			"grafana_json": `{"uid":"panel-scope","title":"Panel scope","panels":[
				{"title":"Selected","type":"timeseries","targets":[{"refId":"A","expr":"selected_metric"}]},
				{"title":"Unrelated","type":"timeseries","targets":[{"refId":"A","expr":"unrelated_metric"}]}
			]}`,
			"source_namespace": "grafana:test",
		}},
	})
	require.NoError(t, err)
	require.False(t, migrated.IsError)
	encodedMigration, err := json.Marshal(migrated.StructuredContent)
	require.NoError(t, err)
	var migration migrateResponse
	require.NoError(t, json.Unmarshal(encodedMigration, &migration))
	previous, err := readDashboardReportForTest(migration.Artifacts.ReportJSON)
	require.NoError(t, err)
	require.Len(t, previous.Panels, 2)
	unrelatedBefore := previous.Panels[1].Queries[0].Validation

	revalidation.Store(true)
	nowUnix.Store(time.Date(2026, 7, 16, 13, 0, 0, 0, time.UTC).Unix())
	resetCalls := func() {
		callsMu.Lock()
		revalidationCalls = nil
		callsMu.Unlock()
	}
	readCalls := func() []string {
		callsMu.Lock()
		defer callsMu.Unlock()
		return append([]string(nil), revalidationCalls...)
	}

	t.Run("invalid selector makes no target calls", func(t *testing.T) {
		resetCalls()
		result, callErr := service.handleValidateQueries(context.Background(), mcp.CallToolRequest{
			Params: mcp.CallToolParams{Arguments: map[string]any{
				"migration_id": migration.MigrationID, "panel": "missing-panel",
			}},
		})
		require.NoError(t, callErr)
		require.True(t, result.IsError)
		encoded, marshalErr := json.Marshal(result.Content)
		require.NoError(t, marshalErr)
		assert.Contains(t, string(encoded), "PANEL_NOT_FOUND")
		assert.Empty(t, readCalls())
	})

	t.Run("selected panel cannot load or fail from an unrelated panel", func(t *testing.T) {
		resetCalls()
		result, callErr := service.handleValidateQueries(context.Background(), mcp.CallToolRequest{
			Params: mcp.CallToolParams{Arguments: map[string]any{
				"migration_id": migration.MigrationID, "window": "30m", "panel": "Selected",
			}},
		})
		require.NoError(t, callErr)
		require.False(t, result.IsError)
		encoded, marshalErr := json.Marshal(result.StructuredContent)
		require.NoError(t, marshalErr)
		var response validateResponse
		require.NoError(t, json.Unmarshal(encoded, &response))
		assert.Equal(t, 1, response.Totals.SourceQueries)
		assert.Equal(t, 1, response.Totals.EligibleQueries)
		assert.NotEmpty(t, readCalls())
		for _, call := range readCalls() {
			assert.NotContains(t, call, "unrelated_metric")
		}

		validated, readErr := readDashboardReportForTest(response.Artifacts.ReportJSON)
		require.NoError(t, readErr)
		require.Len(t, validated.Panels, 2)
		assert.Equal(t, unrelatedBefore, validated.Panels[1].Queries[0].Validation,
			"panel-scoped validation must retain unrelated evidence without refreshing it")
	})
}

func readPromQLFromRequest(t *testing.T, request *http.Request) string {
	t.Helper()
	var body struct {
		CompositeQuery struct {
			Queries []struct {
				Spec struct {
					Query string `json:"query"`
				} `json:"spec"`
			} `json:"queries"`
		} `json:"compositeQuery"`
	}
	require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
	for _, query := range body.CompositeQuery.Queries {
		if query.Spec.Query != "" {
			return query.Spec.Query
		}
	}
	return ""
}

func readManifestForTest(directory string) (manifest, error) {
	data, err := os.ReadFile(filepath.Join(directory, "migration.json"))
	if err != nil {
		return manifest{}, err
	}
	return decodeManifest(data)
}

func readDashboardReportForTest(path string) (reporttypes.Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return reporttypes.Report{}, err
	}
	return decodeDashboardReport(data)
}
