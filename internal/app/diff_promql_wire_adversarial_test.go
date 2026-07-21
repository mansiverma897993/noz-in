package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This black-box capture mirrors SigNoz v0.133.0: the dashboard frontend
// omits an unset PromQL step, and the backend selects 300 seconds for an exact
// 24-hour range.
func TestBoundDifferentialMirrorsSigNozPromQLWireAndEffectiveStep(t *testing.T) {
	input := filepath.Join(t.TempDir(), "promql.json")
	require.NoError(t, os.WriteFile(input, []byte(`{
		"title":"PromQL wire",
		"panels":[{
			"title":"Offset rate",
			"type":"timeseries",
			"targets":[{"refId":"A","expr":"sum(rate(http_requests_total[5m] offset 1h))"}]
		}]
	}`), 0o600))

	results, err := MigrateGrafana(context.Background(), []string{input}, GrafanaOptions{
		OutputDirectory: t.TempDir(),
	})
	require.NoError(t, err)
	require.Len(t, results, 1)

	var sourceStep string
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/api/v1/query_range", request.URL.Path)
		sourceStep = request.URL.Query().Get("step")
		writeJSONResponse(t, writer, map[string]any{
			"status": "success",
			"data":   map[string]any{"resultType": "matrix", "result": []any{}},
		})
	}))
	t.Cleanup(source.Close)

	var targetWire map[string]any
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/api/v5/query_range", request.URL.Path)
		require.NoError(t, json.NewDecoder(request.Body).Decode(&targetWire))
		writeJSONResponse(t, writer, map[string]any{
			"status": "success",
			"data":   map[string]any{"data": map[string]any{"results": []any{}}},
		})
	}))
	t.Cleanup(target.Close)

	const (
		expectedStart = int64(1_799_913_600_000)
		expectedEnd   = int64(1_800_000_000_000)
	)
	differential, err := ValidateGrafanaDifferential(context.Background(), input, DifferentialOptions{
		SourceURL: source.URL, TargetURL: target.URL, TargetAPIKey: "key",
		HTTPClient: target.Client(), MigrationReportPath: results[0].ReportPath,
		Now: time.Unix(1_800_000_000, 0), Range: 24 * time.Hour, Step: time.Minute,
		Workers: 1,
	})
	require.NoError(t, err)
	require.Len(t, differential.Comparisons, 1)

	comparison := differential.Comparisons[0]
	assert.Equal(t, int64((5 * time.Minute).Milliseconds()), comparison.EvaluationStepMillis)
	assert.Equal(t, "300", sourceStep)
	assert.Equal(t, float64(expectedStart), targetWire["start"])
	assert.Equal(t, float64(expectedEnd), targetWire["end"])
	assert.Equal(t, map[string]any{
		"formatTableResultForUI": false,
		"fillGaps":               false,
	}, targetWire["formatOptions"])
	assert.Equal(t, map[string]any{}, targetWire["variables"])
	assert.NotContains(t, targetWire, "noCache")

	composite, ok := targetWire["compositeQuery"].(map[string]any)
	require.True(t, ok)
	queries, ok := composite["queries"].([]any)
	require.True(t, ok)
	require.Len(t, queries, 1)
	envelope, ok := queries[0].(map[string]any)
	require.True(t, ok)
	spec, ok := envelope["spec"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, spec, "step", "undefined PromQL step is omitted by JSON.stringify")
}

// Dynamic is semantically important: SigNoz only applies the __all__ matcher
// removal behavior to variables whose wire type is dynamic.
func TestBoundDifferentialPreservesEmittedDynamicVariableType(t *testing.T) {
	input := filepath.Join(t.TempDir(), "dynamic-all.json")
	require.NoError(t, os.WriteFile(input, []byte(`{
		"title":"Dynamic all",
		"templating":{"list":[{
			"name":"job","type":"query","query":"label_values(up, job)",
			"includeAll":true,"allValue":".*","current":{"value":["All"]}
		}]},
		"panels":[{
			"title":"Availability","type":"timeseries",
			"targets":[{"refId":"A","expr":"sum(up{job=~\"$job\"})"}]
		}]
	}`), 0o600))

	results, err := MigrateGrafana(context.Background(), []string{input}, GrafanaOptions{
		OutputDirectory: t.TempDir(),
	})
	require.NoError(t, err)
	require.Len(t, results, 1)

	storedJSON, err := os.ReadFile(results[0].DashboardPath)
	require.NoError(t, err)
	var stored struct {
		Variables map[string]struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"variables"`
	}
	require.NoError(t, json.Unmarshal(storedJSON, &stored))
	require.Len(t, stored.Variables, 1)
	for _, variable := range stored.Variables {
		assert.Equal(t, "job", variable.Name)
		assert.Equal(t, "DYNAMIC", variable.Type)
	}

	var sourceExpression string
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		sourceExpression = request.URL.Query().Get("query")
		writeJSONResponse(t, writer, map[string]any{
			"status": "success",
			"data":   map[string]any{"resultType": "matrix", "result": []any{}},
		})
	}))
	t.Cleanup(source.Close)

	var targetWire map[string]any
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.NoError(t, json.NewDecoder(request.Body).Decode(&targetWire))
		writeJSONResponse(t, writer, map[string]any{
			"status": "success",
			"data":   map[string]any{"data": map[string]any{"results": []any{}}},
		})
	}))
	t.Cleanup(target.Close)

	_, err = ValidateGrafanaDifferential(context.Background(), input, DifferentialOptions{
		SourceURL: source.URL, TargetURL: target.URL, TargetAPIKey: "key",
		HTTPClient: target.Client(), MigrationReportPath: results[0].ReportPath,
		Now: time.Unix(180, 0), Range: 2 * time.Minute, Step: time.Minute, Workers: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, `sum(up{job=~".*"})`, sourceExpression)

	variables, ok := targetWire["variables"].(map[string]any)
	require.True(t, ok)
	job, ok := variables["job"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "dynamic", job["type"])
	assert.Equal(t, "__all__", job["value"])
}

func TestBoundDifferentialUsesMigrationOverrideAsArtifactDefault(t *testing.T) {
	input := filepath.Join(t.TempDir(), "bound-override.json")
	require.NoError(t, os.WriteFile(input, []byte(`{
		"title":"Bound override",
		"templating":{"list":[{
			"name":"env","type":"query","query":"label_values(up, env)",
			"current":{"value":"prod"}
		}]},
		"panels":[{
			"title":"Availability","type":"timeseries",
			"targets":[{"refId":"A","expr":"sum(up{env=\"$env\"})"}]
		}]
	}`), 0o600))

	results, err := MigrateGrafana(context.Background(), []string{input}, GrafanaOptions{
		OutputDirectory: t.TempDir(),
		Variables:       map[string]string{"env": "staging"},
	})
	require.NoError(t, err)
	require.Len(t, results, 1)

	var sourceExpression string
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		sourceExpression = request.URL.Query().Get("query")
		writeJSONResponse(t, writer, map[string]any{
			"status": "success",
			"data":   map[string]any{"resultType": "matrix", "result": []any{}},
		})
	}))
	t.Cleanup(source.Close)

	var targetWire map[string]any
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.NoError(t, json.NewDecoder(request.Body).Decode(&targetWire))
		writeJSONResponse(t, writer, map[string]any{
			"status": "success",
			"data":   map[string]any{"data": map[string]any{"results": []any{}}},
		})
	}))
	t.Cleanup(target.Close)

	_, err = ValidateGrafanaDifferential(context.Background(), input, DifferentialOptions{
		SourceURL: source.URL, TargetURL: target.URL, TargetAPIKey: "key",
		HTTPClient: target.Client(), MigrationReportPath: results[0].ReportPath,
		Now: time.Unix(180, 0), Range: 2 * time.Minute, Step: time.Minute, Workers: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, `sum(up{env="staging"})`, sourceExpression)

	variables, ok := targetWire["variables"].(map[string]any)
	require.True(t, ok)
	env, ok := variables["env"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "dynamic", env["type"])
	assert.Equal(t, "staging", env["value"])
}
