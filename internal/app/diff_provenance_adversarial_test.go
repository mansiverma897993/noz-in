package app

import (
	"context"
	"crypto/sha256"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mansiverma897993/signoz/internal/diff"
	"github.com/mansiverma897993/signoz/internal/target/signoz"
	"github.com/mansiverma897993/signoz/pkg/reporttypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrationEvidenceBindsExactPrimaryArtifactBytes(t *testing.T) {
	t.Parallel()

	results, err := MigrateGrafana(context.Background(), []string{"../source/grafana/testdata/modern.json"}, GrafanaOptions{
		OutputDirectory: t.TempDir(),
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NotNil(t, results[0].Evidence.PrimaryArtifact)

	data, err := os.ReadFile(filepath.Clean(results[0].DashboardPath))
	require.NoError(t, err)
	digest := sha256.Sum256(data)
	binding := results[0].Evidence.PrimaryArtifact
	assert.Equal(t, filepath.Base(results[0].DashboardPath), binding.Path)
	assert.Equal(t, int64(len(data)), binding.SizeBytes)
	assert.Equal(t, strings.ToLower(binding.SHA256), strings.ToLower(stringHex(digest[:])))

	var persisted reporttypes.Report
	decodeFile(t, results[0].ReportPath, &persisted)
	assert.Equal(t, results[0].Evidence.PrimaryArtifact, persisted.PrimaryArtifact)
}

func TestAttachDifferentialEvidenceRejectsChangedPrimaryArtifact(t *testing.T) {
	t.Parallel()

	results, err := MigrateGrafana(context.Background(), []string{"../source/grafana/testdata/modern.json"}, GrafanaOptions{
		OutputDirectory: t.TempDir(),
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	differential := differentialFromEvidence(t, results[0].Evidence)

	data, err := os.ReadFile(filepath.Clean(results[0].DashboardPath))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(results[0].DashboardPath, append(data, ' '), 0o644))

	err = AttachDifferentialEvidence(results[0].ReportPath, differential)
	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "size")
	assert.Contains(t, err.Error(), "does not match migration evidence")
}

func TestValidateDifferentialRejectsChangedPrimaryBeforeNetworkCalls(t *testing.T) {
	t.Parallel()

	results, err := MigrateGrafana(context.Background(), []string{"../source/grafana/testdata/modern.json"}, GrafanaOptions{
		OutputDirectory: t.TempDir(),
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	data, err := os.ReadFile(filepath.Clean(results[0].DashboardPath))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(results[0].DashboardPath, append(data, '\n'), 0o644))

	var networkCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		networkCalls.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	_, err = ValidateGrafanaDifferential(context.Background(), "../source/grafana/testdata/modern.json", DifferentialOptions{
		SourceURL: server.URL, TargetURL: server.URL, HTTPClient: server.Client(),
		MigrationReportPath: results[0].ReportPath,
	})
	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "does not match migration evidence")
	assert.Zero(t, networkCalls.Load(), "artifact provenance must fail closed before any metadata or query request")
}

func TestAttachDifferentialEvidenceBindsTargetAndRetainsRunProvenance(t *testing.T) {
	t.Parallel()

	results, err := MigrateGrafana(context.Background(), []string{"../source/grafana/testdata/modern.json"}, GrafanaOptions{
		OutputDirectory: t.TempDir(),
	})
	require.NoError(t, err)
	require.Len(t, results, 1)

	evidence := results[0].Evidence
	evidence.Run.Target = "http://127.0.0.1:4318/signoz/"
	require.NoError(t, updateDashboardReportArtifactSet(results[0].ReportPath, &evidence))
	differential := differentialFromEvidence(t, evidence)
	differential.TargetURL = "http://localhost:4318/signoz"
	err = AttachDifferentialEvidence(results[0].ReportPath, differential)
	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "does not match migration target endpoint")

	differential.TargetURL = "HTTP://127.0.0.1:4318/signoz/"
	require.NoError(t, AttachDifferentialEvidence(results[0].ReportPath, differential))
	decodeFile(t, results[0].ReportPath, &evidence)
	require.NotNil(t, evidence.Differential)
	assert.Equal(t, "http://prometheus.example", evidence.Differential.SourceURL)
	assert.Equal(t, "http://127.0.0.1:4318/signoz", evidence.Differential.TargetURL)
	assert.Equal(t, string(diff.TargetProvenanceOTelPrometheusReceiver), evidence.Differential.TargetProvenance)
	assert.Equal(t, *evidence.PrimaryArtifact, evidence.Differential.PrimaryArtifact)
	assert.Equal(t, differential.Materialization, evidence.Differential.Materialization)
	assert.Equal(t, differential.Window.StepMillis, evidence.Differential.Window.StepMillis)
	assert.Equal(t, differential.Tolerances.MinimumMatchedPoints, evidence.Differential.Tolerances.MinimumMatchedPoints)
	assert.Equal(t, differential.Summary.Queries, evidence.Differential.Summary.Queries)
}

func TestDifferentialUsesOnlyTheBoundPrimaryAfterPartialImport(t *testing.T) {
	t.Parallel()

	var phaseMu sync.Mutex
	diffPhase := false
	var sourceExpressions []string
	var targetExpressions []string
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			metricType := "gauge"
			if strings.Contains(request.URL.Query().Get("metricName"), "http_requests") {
				metricType = "sum"
			}
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{
				"type": metricType, "temporality": "cumulative", "isMonotonic": metricType == "sum",
			}})
		case "/api/v2/metrics/attributes":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v5/query_range/preview":
			requestBody := decodeQueryRangeRequest(t, request.Body)
			if requestContainsPromQL(requestBody, "sum(up") {
				writer.WriteHeader(http.StatusBadRequest)
				writeJSONResponse(t, writer, map[string]any{"error": map[string]any{
					"code": "widget_rejected", "message": "reject availability only",
				}})
				return
			}
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{
				"compositeQuery": map[string]any{"A": map[string]any{"valid": true}},
			}})
		case "/api/v5/query_range":
			requestBody := decodeQueryRangeRequest(t, request.Body)
			phaseMu.Lock()
			if diffPhase {
				targetExpressions = append(targetExpressions, requestPromQL(requestBody)...)
			}
			phaseMu.Unlock()
			writeTargetSeries(t, writer, requestBody)
		case "/api/v1/dashboards":
			if request.Method == http.MethodGet {
				writeJSONResponse(t, writer, map[string]any{"data": []any{}})
				return
			}
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"id": "partial"}})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(target.Close)

	output := t.TempDir()
	results, err := MigrateGrafana(context.Background(), []string{"../source/grafana/testdata/modern.json"}, GrafanaOptions{
		OutputDirectory: output, TargetURL: target.URL, APIKey: "key", HTTPClient: target.Client(),
		Validate: true, Variables: map[string]string{"job": "api"}, SourceNamespace: "grafana:test",
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, []string{"/panels/0"}, results[0].ValidationRejected)

	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		phaseMu.Lock()
		sourceExpressions = append(sourceExpressions, request.URL.Query().Get("query"))
		phaseMu.Unlock()
		writeJSONResponse(t, writer, map[string]any{
			"status": "success", "data": map[string]any{"resultType": "matrix", "result": []any{map[string]any{
				"metric": map[string]any{}, "values": []any{[]any{60.0, "1"}, []any{120.0, "1"}, []any{180.0, "1"}},
			}}},
		})
	}))
	t.Cleanup(source.Close)
	phaseMu.Lock()
	diffPhase = true
	phaseMu.Unlock()

	differential, err := ValidateGrafanaDifferential(context.Background(), "../source/grafana/testdata/modern.json", DifferentialOptions{
		SourceURL: source.URL, TargetURL: target.URL, TargetAPIKey: "key", HTTPClient: target.Client(),
		SourceVariables: map[string]string{"job": "api"}, TargetVariables: map[string]string{"job": "api"},
		Now: time.Unix(180, 0), Range: 2 * time.Minute, Step: time.Minute, MinimumMatchedPoints: 1,
		MigrationReportPath: results[0].ReportPath,
	})
	require.NoError(t, err)
	require.Equal(t, results[0].Evidence.PrimaryArtifact, differential.PrimaryArtifact)
	rejected := comparisonBySourcePath(t, differential, "/panels/0/targets/0")
	assert.Equal(t, targetKindNone, rejected.TargetKind)
	assert.Equal(t, diff.StatusSkipped, rejected.Stats.Status)
	assert.Empty(t, rejected.TargetArtifact)
	assert.NotEmpty(t, rejected.SkippedReason)

	phaseMu.Lock()
	defer phaseMu.Unlock()
	for _, expression := range append(append([]string(nil), sourceExpressions...), targetExpressions...) {
		assert.NotContains(t, expression, "sum(up", "the validation-pruned candidate widget must never be measured")
	}
	require.NoError(t, AttachDifferentialEvidence(results[0].ReportPath, differential))
}

func TestBoundDifferentialUsesMigrationMacroSettingsNotComparisonWindow(t *testing.T) {
	t.Parallel()

	input := filepath.Join(t.TempDir(), "range.json")
	require.NoError(t, os.WriteFile(input, []byte(`{
		"title":"Range binding","panels":[{"title":"Increase","type":"timeseries","targets":[{
			"refId":"A","expr":"sum(increase(context_switches_total[$__range]))"
		}]}]
	}`), 0o600))
	results, err := MigrateGrafana(context.Background(), []string{input}, GrafanaOptions{
		OutputDirectory: t.TempDir(), Range: 6 * time.Hour,
	})
	require.NoError(t, err)
	require.Len(t, results, 1)

	var sourceExpression string
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		sourceExpression = request.URL.Query().Get("query")
		writeJSONResponse(t, writer, map[string]any{
			"status": "success", "data": map[string]any{"resultType": "matrix", "result": []any{}},
		})
	}))
	t.Cleanup(source.Close)
	var metadataCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v5/query_range":
			writeJSONResponse(t, writer, map[string]any{
				"status": "success", "data": map[string]any{"data": map[string]any{"results": []any{}}},
			})
		case "/api/v2/metrics/metadata", "/api/v2/metrics/attributes":
			metadataCalls.Add(1)
			writer.WriteHeader(http.StatusInternalServerError)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(target.Close)

	differential, err := ValidateGrafanaDifferential(context.Background(), input, DifferentialOptions{
		SourceURL: source.URL, TargetURL: target.URL, TargetAPIKey: "key", HTTPClient: target.Client(),
		Range: 2 * time.Minute, Step: time.Minute, Now: time.Unix(180, 0), MinimumMatchedPoints: 1,
		MigrationReportPath: results[0].ReportPath,
	})
	require.NoError(t, err)
	assert.Contains(t, sourceExpression, "[6h]")
	assert.NotContains(t, sourceExpression, "[2m]")
	assert.Zero(t, metadataCalls.Load(), "a bound target artifact must not be regenerated from fresh metadata")
	assert.Equal(t, (6 * time.Hour).String(), differential.Materialization.Range)
	assert.Equal(t, int64(time.Minute/time.Millisecond), differential.Window.StepMillis)
}

func stringHex(value []byte) string {
	const alphabet = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for index, current := range value {
		result[index*2] = alphabet[current>>4]
		result[index*2+1] = alphabet[current&0x0f]
	}
	return string(result)
}

func requestContainsPromQL(request signoz.QueryRangeRequest, fragment string) bool {
	for _, expression := range requestPromQL(request) {
		if strings.Contains(expression, fragment) {
			return true
		}
	}
	return false
}

func decodeQueryRangeRequest(t *testing.T, body io.Reader) signoz.QueryRangeRequest {
	t.Helper()
	data, err := io.ReadAll(body)
	require.NoError(t, err)
	request, err := decodeTargetArtifact(data)
	require.NoError(t, err)
	return request
}

func requestPromQL(request signoz.QueryRangeRequest) []string {
	var expressions []string
	for _, query := range request.CompositeQuery.Queries {
		if spec, ok := query.Spec.(signoz.PromQLSpec); ok {
			expressions = append(expressions, spec.Query)
		}
	}
	return expressions
}

func writeTargetSeries(t *testing.T, writer http.ResponseWriter, request signoz.QueryRangeRequest) {
	t.Helper()
	results := make([]any, 0, len(request.CompositeQuery.Queries))
	for _, query := range request.CompositeQuery.Queries {
		name := "A"
		switch spec := query.Spec.(type) {
		case signoz.PromQLSpec:
			name = spec.Name
		case signoz.BuilderQuerySpec:
			name = spec.Name
		case signoz.FormulaSpec:
			name = spec.Name
		}
		results = append(results, map[string]any{
			"queryName": name,
			"series": []any{map[string]any{"values": []any{
				map[string]any{"timestamp": request.Start, "value": 1},
				map[string]any{"timestamp": request.End, "value": 1},
			}}},
		})
	}
	writeJSONResponse(t, writer, map[string]any{
		"status": "success", "data": map[string]any{"data": map[string]any{"results": results}},
	})
}

func comparisonBySourcePath(t *testing.T, report DifferentialReport, sourcePath string) DifferentialQuery {
	t.Helper()
	for _, comparison := range report.Comparisons {
		if comparison.SourcePath == sourcePath {
			return comparison
		}
	}
	require.FailNow(t, "comparison not found", sourcePath)
	return DifferentialQuery{}
}
