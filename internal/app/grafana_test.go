package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mansiverma897993/signoz/internal/target/signoz"
	"github.com/mansiverma897993/signoz/pkg/reporttypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrateGrafanaWritesArtifacts(t *testing.T) {
	t.Parallel()

	output := t.TempDir()
	results, err := MigrateGrafana(
		context.Background(),
		[]string{"../source/grafana/testdata/modern.json"},
		GrafanaOptions{OutputDirectory: output},
	)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.FileExists(t, results[0].DashboardPath)
	assert.FileExists(t, results[0].ReportPath)

	var dashboard signoz.DashboardV5
	decodeFile(t, results[0].DashboardPath, &dashboard)
	assert.Equal(t, "v5", dashboard.Version)
	assert.Len(t, dashboard.Widgets, 3)

	var report reporttypes.Report
	decodeFile(t, results[0].ReportPath, &report)
	assert.Equal(t, 3, report.Summary.Panels)
	assert.Equal(t, 3, report.Summary.Queries)
	assert.Equal(t, report.Summary.Queries, report.Summary.Native+report.Summary.Passthrough+report.Summary.NeedsReview)
	assert.Equal(t, report.Summary.Native, report.Summary.Builder+report.Summary.Formula)
}

func TestMigrateGrafanaPreflightsAuxiliaryRuleFilesBeforeAnySideEffect(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	invalidRules := filepath.Join(directory, "invalid-rules.yaml")
	require.NoError(t, os.WriteFile(invalidRules, []byte(`groups:
- name: invalid
  rules:
  - alert: Both
    record: both_metric
    expr: up
`), 0o600))

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	output := filepath.Join(directory, "not-created")

	results, err := MigrateGrafana(
		context.Background(),
		[]string{"../source/grafana/testdata/modern.json"},
		GrafanaOptions{
			OutputDirectory: output,
			RuleFiles:       []string{invalidRules},
			TargetURL:       server.URL,
			APIKey:          "key",
			HTTPClient:      server.Client(),
			SourceNamespace: "grafana:test",
		},
	)

	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Empty(t, results)
	assert.Zero(t, requests.Load(), "auxiliary input preflight must precede target construction and metadata access")
	assert.NoDirExists(t, output, "auxiliary input preflight must precede output-directory creation")
}

func TestMigrateGrafanaImportsValidatedRemainderAfterWidgetAPIError(t *testing.T) {
	t.Parallel()

	imported := make(chan signoz.DashboardV5, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			metricType := "gauge"
			temporality := ""
			monotonic := false
			if strings.Contains(request.URL.Query().Get("metricName"), "http_requests") {
				metricType = "sum"
				temporality = "cumulative"
				monotonic = true
			}
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{
				"type": metricType, "temporality": temporality, "isMonotonic": monotonic,
			}})
		case "/api/v2/metrics/attributes":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v5/query_range/preview":
			var body struct {
				CompositeQuery struct {
					Queries []struct {
						Spec struct {
							Name  string `json:"name"`
							Query string `json:"query"`
						} `json:"spec"`
					} `json:"queries"`
				} `json:"compositeQuery"`
			}
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			for _, query := range body.CompositeQuery.Queries {
				if strings.Contains(query.Spec.Query, "sum(up") {
					writer.Header().Set("Content-Type", "application/json")
					writer.WriteHeader(http.StatusBadRequest)
					require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{"error": map[string]any{
						"code": "widget_rejected", "message": "bad widget query",
					}}))
					return
				}
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
		case "/api/v1/dashboards":
			if request.Method == http.MethodGet {
				writeJSONResponse(t, writer, map[string]any{"data": []any{}})
				return
			}
			require.Equal(t, http.MethodPost, request.Method)
			var dashboard signoz.DashboardV5
			require.NoError(t, json.NewDecoder(request.Body).Decode(&dashboard))
			imported <- dashboard
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"id": "partial-dashboard"}})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	output := t.TempDir()
	results, err := MigrateGrafana(context.Background(), []string{"../source/grafana/testdata/modern.json"}, GrafanaOptions{
		OutputDirectory:   output,
		TargetURL:         server.URL,
		APIKey:            "key",
		HTTPClient:        server.Client(),
		SourceNamespace:   "grafana:test",
		Validate:          true,
		ValidationWorkers: 2,
		Variables:         map[string]string{"job": "api"},
	})

	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.FileExists(t, results[0].DashboardPath)
	assert.FileExists(t, results[0].CandidateDashboardPath)
	assert.FileExists(t, results[0].ReportPath)
	assert.FileExists(t, results[0].HTMLPath)
	require.NotNil(t, results[0].Target)
	assert.Equal(t, "partial-dashboard", results[0].Target.ID)
	assert.Empty(t, results[0].TargetSkipped)
	assert.Equal(t, 2, results[0].ImportedWidgets)
	assert.Equal(t, []string{"/panels/0"}, results[0].ValidationRejected)

	var written signoz.DashboardV5
	decodeFile(t, results[0].DashboardPath, &written)
	var candidate signoz.DashboardV5
	decodeFile(t, results[0].CandidateDashboardPath, &candidate)
	require.Len(t, candidate.Widgets, 3)
	require.Len(t, written.Widgets, 2)
	assert.NotContains(t, widgetTitles(written), "Availability")
	assert.Contains(t, widgetTitles(written), "Request rate")
	assert.Equal(t, written, <-imported, "the primary artifact must be the exact payload sent to SigNoz")
	assertDashboardLayoutsReferenceKeptWidgets(t, written)

	var evidence reporttypes.Report
	decodeFile(t, results[0].ReportPath, &evidence)
	require.Len(t, evidence.Panels, 3)
	assert.Equal(t, "widget_rejected", evidence.Panels[0].Queries[0].Validation.ErrorCode)
	assert.Contains(t, evidence.Panels[0].Queries[0].Validation.Error, "bad widget query")
	assert.True(t, evidence.Panels[2].Queries[0].Validation.PreviewOK)
	assert.True(t, evidence.Panels[2].Queries[0].Validation.Executed)
	assert.Equal(t, 1, evidence.Summary.PreviewInvalid)
	assert.Equal(t, 1, evidence.Summary.PreviewValid)
	assert.NotContains(t, evidence.Run.Flags, "partialImport", "preflight eligibility must not masquerade as an import outcome")
	assert.Equal(t, true, evidence.Run.Flags["partialImportEligible"])
	assert.Equal(t, true, evidence.Run.Flags["partialImportPerformed"])
	assert.Equal(t, true, evidence.Run.Flags["importRequested"])
	assert.Equal(t, true, evidence.Run.Flags["importAttempted"])
	assert.Equal(t, true, evidence.Run.Flags["importSucceeded"])
	assert.Equal(t, "created", evidence.Run.Flags["targetAction"])
	assert.Equal(t, "partial-dashboard", evidence.Run.Flags["targetDashboardID"])
	assert.Equal(t, float64(3), evidence.Run.Flags["candidateWidgets"])
	assert.Equal(t, float64(2), evidence.Run.Flags["importableWidgets"])
}

func TestMigrateGrafanaValidatesAndImportsMultiVariableArray(t *testing.T) {
	t.Parallel()

	var previewCalls atomic.Int32
	var queryCalls atomic.Int32
	var importCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{
				"type": "gauge", "temporality": "", "isMonotonic": false,
			}})
		case "/api/v2/metrics/attributes":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v5/query_range/preview":
			previewCalls.Add(1)
			var body struct {
				Variables map[string]struct {
					Value any `json:"value"`
				} `json:"variables"`
				CompositeQuery struct {
					Queries []struct {
						Spec struct {
							Name  string `json:"name"`
							Query string `json:"query"`
						} `json:"spec"`
					} `json:"queries"`
				} `json:"compositeQuery"`
			}
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			require.Contains(t, body.Variables, "job")
			assert.Equal(t, []any{"api", "worker"}, body.Variables["job"].Value)
			for _, query := range body.CompositeQuery.Queries {
				if strings.Contains(query.Spec.Query, "job") {
					assert.Contains(t, query.Spec.Query, "$job", "the target backend must receive the dashboard variable reference")
				}
			}
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{
				"compositeQuery": map[string]any{"A": map[string]any{"valid": true}},
			}})
		case "/api/v5/query_range":
			queryCalls.Add(1)
			var body struct {
				Variables map[string]struct {
					Value any `json:"value"`
				} `json:"variables"`
			}
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			assert.Equal(t, []any{"api", "worker"}, body.Variables["job"].Value)
			writeJSONResponse(t, writer, map[string]any{
				"status": "success", "data": map[string]any{"data": map[string]any{"results": []any{map[string]any{
					"queryName": "A", "series": []any{map[string]any{"values": []any{map[string]any{"timestamp": 1, "value": 1}}}},
				}}}},
			})
		case "/api/v1/dashboards":
			if request.Method == http.MethodGet {
				writeJSONResponse(t, writer, map[string]any{"data": []any{}})
				return
			}
			importCalls.Add(1)
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"id": "multi-variable-dashboard"}})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	results, err := MigrateGrafana(context.Background(), []string{"../source/grafana/testdata/modern.json"}, GrafanaOptions{
		OutputDirectory: t.TempDir(), TargetURL: server.URL, APIKey: "key", HTTPClient: server.Client(),
		SourceNamespace: "grafana:test",
		Validate:        true, ValidationWorkers: 2,
	})

	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NotNil(t, results[0].Target)
	assert.Equal(t, "multi-variable-dashboard", results[0].Target.ID)
	assert.Empty(t, results[0].TargetSkipped)
	assert.Equal(t, int32(1), importCalls.Load())
	assert.Equal(t, int32(2), previewCalls.Load())
	assert.Equal(t, int32(2), queryCalls.Load())
	validated := results[0].Evidence.Panels[0].Queries[0]
	assert.Empty(t, validated.Validation.ErrorCode)
	assert.True(t, validated.Validation.PreviewOK)
	assert.True(t, validated.Validation.Executed)
	assert.NotContains(t, validated.Validation.ReasonCodes, "MULTI_VARIABLE_VALUE_UNRESOLVED")
	assert.NotContains(t, results[0].Evidence.Run.Flags, "partialImport")
	assert.Equal(t, false, results[0].Evidence.Run.Flags["partialImportEligible"])
	assert.Equal(t, false, results[0].Evidence.Run.Flags["partialImportPerformed"])
	assert.Equal(t, true, results[0].Evidence.Run.Flags["importRequested"])
	assert.Equal(t, true, results[0].Evidence.Run.Flags["importAttempted"])
	assert.Equal(t, true, results[0].Evidence.Run.Flags["importSucceeded"])
	assert.Equal(t, "created", results[0].Evidence.Run.Flags["targetAction"])
}

func TestMigrateGrafanaPinsCustomReloadValueBeforeValidationAndImport(t *testing.T) {
	t.Parallel()

	var previewCalls atomic.Int32
	var queryCalls atomic.Int32
	var importCalls atomic.Int32
	imported := make(chan signoz.DashboardV5, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{
				"type": "gauge", "temporality": "", "isMonotonic": false,
			}})
		case "/api/v2/metrics/attributes":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v5/query_range/preview":
			previewCalls.Add(1)
			var body signoz.QueryRangeRequest
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			assert.Equal(t, []any{"prod"}, body.Variables["environment"].Value)
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{
				"compositeQuery": map[string]any{"A": map[string]any{"valid": true}},
			}})
		case "/api/v5/query_range":
			queryCalls.Add(1)
			var body signoz.QueryRangeRequest
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			assert.Equal(t, []any{"prod"}, body.Variables["environment"].Value)
			writeJSONResponse(t, writer, map[string]any{
				"status": "success", "data": map[string]any{"data": map[string]any{"results": []any{map[string]any{
					"queryName": "A", "series": []any{map[string]any{"values": []any{map[string]any{"timestamp": 1, "value": 1}}}},
				}}}},
			})
		case "/api/v1/dashboards":
			if request.Method == http.MethodGet {
				writeJSONResponse(t, writer, map[string]any{"data": []any{}})
				return
			}
			importCalls.Add(1)
			var dashboard signoz.DashboardV5
			require.NoError(t, json.NewDecoder(request.Body).Decode(&dashboard))
			imported <- dashboard
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"id": "custom-variable-dashboard"}})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	input := filepath.Join(t.TempDir(), "custom-variable.json")
	require.NoError(t, os.WriteFile(input, []byte(`{
		"title":"Custom variable",
		"templating":{"list":[{
			"name":"environment","type":"custom","query":"prod,stage",
			"multi":true,"current":{"value":["prod"]}
		}]},
		"panels":[{"title":"Availability","type":"timeseries","targets":[
			{"refId":"A","expr":"sum(up{environment=~\"$environment\"})"}
		]}]
	}`), 0o600))

	results, err := MigrateGrafana(context.Background(), []string{input}, GrafanaOptions{
		OutputDirectory: t.TempDir(), TargetURL: server.URL, APIKey: "key", HTTPClient: server.Client(),
		SourceNamespace: "grafana:test", Validate: true, ValidationWorkers: 1,
	})

	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NotNil(t, results[0].Target)
	assert.Equal(t, "custom-variable-dashboard", results[0].Target.ID)
	assert.Equal(t, int32(1), previewCalls.Load())
	assert.Equal(t, int32(1), queryCalls.Load())
	assert.Equal(t, int32(1), importCalls.Load())

	dashboard := <-imported
	require.Len(t, dashboard.Variables, 1)
	for _, variable := range dashboard.Variables {
		assert.Equal(t, "CUSTOM", variable.Type)
		assert.Equal(t, "prod", variable.CustomValue)
		assert.Equal(t, []any{"prod"}, variable.SelectedValue)
		assert.Equal(t, "prod", variable.DefaultValue)
	}
	require.Len(t, results[0].Evidence.Variables, 1)
	assert.Contains(t, results[0].Evidence.Variables[0].ReasonCodes, "CUSTOM_VARIABLE_RELOAD_SEMANTICS")
	assert.Contains(t, results[0].Evidence.Variables[0].Notes,
		`Custom variable "environment" options were reduced to the proven current selection so target reload executes the value that was validated.`)
}

func TestMigrateGrafanaCustomOverrideRewritesPersistedReloadValue(t *testing.T) {
	t.Parallel()

	input := filepath.Join(t.TempDir(), "custom-variable.json")
	require.NoError(t, os.WriteFile(input, []byte(`{
		"title":"Custom override",
		"templating":{"list":[{
			"name":"environment","type":"custom","query":"prod,stage",
			"multi":true,"current":{"value":["All"]},"includeAll":true
		}]},
		"panels":[{"title":"Availability","type":"timeseries","targets":[
			{"refId":"A","expr":"sum(up{environment=~\"$environment\"})"}
		]}]
	}`), 0o600))

	results, err := MigrateGrafana(context.Background(), []string{input}, GrafanaOptions{
		OutputDirectory: t.TempDir(), Variables: map[string]string{"environment": "stage"},
	})
	require.NoError(t, err)
	require.Len(t, results, 1)

	var dashboard signoz.DashboardV5
	decodeFile(t, results[0].DashboardPath, &dashboard)
	require.Len(t, dashboard.Variables, 1)
	for _, variable := range dashboard.Variables {
		assert.Equal(t, "stage", variable.CustomValue)
		assert.Equal(t, []any{"stage"}, variable.SelectedValue)
		assert.Equal(t, "stage", variable.DefaultValue)
		assert.False(t, variable.AllSelected)
	}
}

func TestMigrateGrafanaRejectsUnpersistableVariableBindingsBeforeTargetRequests(t *testing.T) {
	t.Parallel()

	var previewCalls atomic.Int32
	var queryCalls atomic.Int32
	var importCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v5/query_range/preview":
			previewCalls.Add(1)
			writer.WriteHeader(http.StatusInternalServerError)
		case "/api/v5/query_range":
			queryCalls.Add(1)
			writer.WriteHeader(http.StatusInternalServerError)
		case "/api/v1/dashboards":
			if request.Method == http.MethodPost {
				importCalls.Add(1)
			}
			writeJSONResponse(t, writer, map[string]any{"data": []any{}})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	path := filepath.Join(t.TempDir(), "unknown-variable.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"title":"Unknown variable",
		"panels":[{"title":"Availability","type":"timeseries","targets":[
			{"refId":"A","expr":"sum(up{job=~\"$ghost\"})"}
		]}]
	}`), 0o600))

	results, err := MigrateGrafana(context.Background(), []string{path}, GrafanaOptions{
		OutputDirectory: t.TempDir(), TargetURL: server.URL, APIKey: "key", HTTPClient: server.Client(),
		SourceNamespace: "grafana:test", Validate: true, Variables: map[string]string{"ghost": "api"},
	})

	require.NoError(t, err)
	require.Len(t, results, 1)
	result := results[0]
	assert.Nil(t, result.Target)
	assert.Contains(t, result.TargetSkipped, "unresolved dashboard variable")
	assert.Zero(t, previewCalls.Load(), "an override must not validate a variable absent from the persisted dashboard")
	assert.Zero(t, queryCalls.Load())
	assert.Zero(t, importCalls.Load())
	require.Len(t, result.Evidence.Panels, 1)
	require.Len(t, result.Evidence.Panels[0].Queries, 1)
	queryEvidence := result.Evidence.Panels[0].Queries[0]
	assert.Contains(t, queryEvidence.Validation.ReasonCodes, "MISSING_VARIABLE_VALUE")
	assert.Contains(t, queryEvidence.Validation.Error, "no persisted dashboard variable definition")

	contradictoryPath := filepath.Join(t.TempDir(), "contradictory-multi.json")
	require.NoError(t, os.WriteFile(contradictoryPath, []byte(`{
		"title":"Contradictory multi selection",
		"templating":{"list":[{
			"name":"job","type":"query","query":"label_values(up, job)",
			"multi":false,"current":{"value":["api","worker"]}
		}]},
		"panels":[{"title":"Availability","type":"timeseries","targets":[
			{"refId":"A","expr":"sum(up{job=~\"$job\"})"}
		]}]
	}`), 0o600))

	results, err = MigrateGrafana(context.Background(), []string{contradictoryPath}, GrafanaOptions{
		OutputDirectory: t.TempDir(), TargetURL: server.URL, APIKey: "key", HTTPClient: server.Client(),
		SourceNamespace: "grafana:test", Validate: true,
	})

	require.NoError(t, err)
	require.Len(t, results, 1)
	result = results[0]
	assert.Nil(t, result.Target)
	assert.Contains(t, result.TargetSkipped, "no executable widgets")
	assert.Zero(t, previewCalls.Load(), "contradictory source selection state must be rejected before target preview")
	assert.Zero(t, queryCalls.Load())
	assert.Zero(t, importCalls.Load())
	require.Len(t, result.Evidence.Panels, 1)
	require.Len(t, result.Evidence.Panels[0].Queries, 1)
	queryEvidence = result.Evidence.Panels[0].Queries[0]
	assert.Equal(t, "none", queryEvidence.EmittedKind)
	assert.Contains(t, queryEvidence.ReasonCodes, "MISSING_VARIABLE_VALUE")
	assert.Contains(t, queryEvidence.ReasonCodes, "MULTI_VARIABLE_VALUE_UNRESOLVED")

	lossyCustomPath := filepath.Join(t.TempDir(), "lossy-custom.json")
	require.NoError(t, os.WriteFile(lossyCustomPath, []byte(`{
		"title":"Lossy custom reload",
		"templating":{"list":[{
			"name":"environment","type":"custom","query":"001,prod",
			"current":{"value":"001"}
		}]},
		"panels":[{"title":"Availability","type":"timeseries","targets":[
			{"refId":"A","expr":"sum(up{environment=\"$environment\"})"}
		]}]
	}`), 0o600))

	results, err = MigrateGrafana(context.Background(), []string{lossyCustomPath}, GrafanaOptions{
		OutputDirectory: t.TempDir(), TargetURL: server.URL, APIKey: "key", HTTPClient: server.Client(),
		SourceNamespace: "grafana:test", Validate: true,
	})

	require.NoError(t, err)
	require.Len(t, results, 1)
	result = results[0]
	assert.Nil(t, result.Target)
	assert.Contains(t, result.TargetSkipped, "no executable widgets")
	assert.Zero(t, previewCalls.Load(), "lossy CUSTOM reload state must be rejected before target preview")
	assert.Zero(t, queryCalls.Load())
	assert.Zero(t, importCalls.Load())
	require.Len(t, result.Evidence.Variables, 1)
	assert.Equal(t, "none", result.Evidence.Variables[0].EmittedKind)
	assert.Contains(t, result.Evidence.Variables[0].ReasonCodes, "MISSING_VARIABLE_VALUE")
	assert.Contains(t, result.Evidence.Variables[0].ReasonCodes, "CUSTOM_VARIABLE_RELOAD_SEMANTICS")
	require.Len(t, result.Evidence.Panels, 1)
	require.Len(t, result.Evidence.Panels[0].Queries, 1)
	queryEvidence = result.Evidence.Panels[0].Queries[0]
	assert.Equal(t, "none", queryEvidence.EmittedKind)
	assert.Contains(t, queryEvidence.ReasonCodes, "MISSING_VARIABLE_VALUE")
	assert.Contains(t, queryEvidence.ReasonCodes, "CUSTOM_VARIABLE_RELOAD_SEMANTICS")
}

func TestMigrateGrafanaSkipsImportWhenEveryExecutableWidgetFailsValidation(t *testing.T) {
	t.Parallel()

	var importCalled atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v5/query_range/preview":
			writer.WriteHeader(http.StatusBadRequest)
			require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{"error": map[string]any{
				"code": "widget_rejected", "message": "bad widget query",
			}}))
		case "/api/v1/dashboards":
			importCalled.Store(true)
			writer.WriteHeader(http.StatusInternalServerError)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	input := filepath.Join(t.TempDir(), "all-rejected.json")
	require.NoError(t, os.WriteFile(input, []byte(`{
		"title":"All rejected",
		"panels":[{"title":"Broken","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]}]
	}`), 0o600))
	results, err := MigrateGrafana(context.Background(), []string{input}, GrafanaOptions{
		OutputDirectory: t.TempDir(), TargetURL: server.URL, APIKey: "key",
		HTTPClient: server.Client(), Validate: true, SourceNamespace: "grafana:test",
	})

	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Nil(t, results[0].Target)
	assert.False(t, importCalled.Load())
	assert.Contains(t, results[0].TargetSkipped, "all executable widgets failed")
	assert.Equal(t, []string{"/panels/0"}, results[0].ValidationRejected)
	assert.FileExists(t, results[0].CandidateDashboardPath)
	var safe signoz.DashboardV5
	decodeFile(t, results[0].DashboardPath, &safe)
	assert.Empty(t, safe.Widgets)
	assert.Empty(t, safe.Layout)
}

func TestMigrateGrafanaDoesNotModifyTargetAfterTransientWidgetFailure(t *testing.T) {
	t.Parallel()

	var importCalled atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v5/query_range/preview":
			writer.Header().Set("Retry-After", "0")
			writer.WriteHeader(http.StatusServiceUnavailable)
			require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{"error": map[string]any{
				"code": "backend_unavailable", "message": "temporary outage",
			}}))
		case "/api/v1/dashboards":
			importCalled.Store(true)
			writer.WriteHeader(http.StatusInternalServerError)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	input := filepath.Join(t.TempDir(), "transient-rejection.json")
	require.NoError(t, os.WriteFile(input, []byte(`{
		"title":"Transient rejection",
		"panels":[{"title":"Temporarily unavailable","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]}]
	}`), 0o600))
	results, err := MigrateGrafana(context.Background(), []string{input}, GrafanaOptions{
		OutputDirectory: t.TempDir(), TargetURL: server.URL, APIKey: "key",
		HTTPClient: server.Client(), Validate: true, SourceNamespace: "grafana:test",
	})

	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Nil(t, results[0].Target)
	assert.False(t, importCalled.Load())
	assert.Empty(t, results[0].ValidationRejected)
	assert.Equal(t, []string{"/panels/0"}, results[0].ValidationBlocked)
	assert.Contains(t, results[0].TargetSkipped, "could not be safely isolated")
	assert.Empty(t, results[0].CandidateDashboardPath)
	assert.Equal(t, http.StatusServiceUnavailable, results[0].Evidence.Panels[0].Queries[0].Validation.HTTPStatus)
	var retained signoz.DashboardV5
	decodeFile(t, results[0].DashboardPath, &retained)
	require.Len(t, retained.Widgets, 1, "transient failures must not delete candidate widgets")
}

func TestMigrateGrafanaPreservesBatchResultsAndFailedTargetEvidence(t *testing.T) {
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
			require.Equal(t, http.MethodPost, request.Method)
			if posts.Add(1) == 1 {
				writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"id": "first-dashboard"}})
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusBadRequest)
			require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{"error": map[string]any{
				"code": "invalid_dashboard", "message": "second dashboard rejected",
			}}))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	inputDirectory := t.TempDir()
	first := filepath.Join(inputDirectory, "first.json")
	second := filepath.Join(inputDirectory, "second.json")
	for path, title := range map[string]string{first: "First", second: "Second"} {
		require.NoError(t, os.WriteFile(path, fmt.Appendf(nil, `{
			"title":%q,
			"panels":[{"title":"Up","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]}]
		}`, title), 0o600))
	}

	results, err := MigrateGrafana(context.Background(), []string{first, second}, GrafanaOptions{
		OutputDirectory: t.TempDir(), TargetURL: server.URL, APIKey: "key", HTTPClient: server.Client(),
		SourceNamespace: "grafana:test",
	})

	require.ErrorContains(t, err, "second dashboard rejected")
	assert.Equal(t, ErrorTarget, KindOf(err))
	require.Len(t, results, 2, "the failed dashboard must not erase prior successful batch results or its own evidence")
	require.NotNil(t, results[0].Target)
	assert.Equal(t, "first-dashboard", results[0].Target.ID)
	assert.True(t, results[0].ImportRequested)
	assert.True(t, results[0].ImportAttempted)
	assert.True(t, results[0].ImportSucceeded)
	assert.Equal(t, "created", results[0].TargetAction)

	failed := results[1]
	assert.Nil(t, failed.Target)
	assert.True(t, failed.ImportRequested)
	assert.True(t, failed.ImportAttempted)
	assert.False(t, failed.ImportSucceeded)
	assert.Equal(t, "failed", failed.TargetAction)
	assert.Contains(t, failed.TargetSkipped, "target import failed")
	assert.Contains(t, failed.TargetError, "second dashboard rejected")
	assert.FileExists(t, failed.DashboardPath)
	assert.FileExists(t, failed.ReportPath)
	assert.FileExists(t, failed.HTMLPath)

	var persisted reporttypes.Report
	decodeFile(t, failed.ReportPath, &persisted)
	assert.Equal(t, true, persisted.Run.Flags["importRequested"])
	assert.Equal(t, true, persisted.Run.Flags["importAttempted"])
	assert.Equal(t, false, persisted.Run.Flags["importSucceeded"])
	assert.Equal(t, "failed", persisted.Run.Flags["targetAction"])
	assert.Contains(t, persisted.Run.Flags["targetError"], "second dashboard rejected")
	html, readErr := os.ReadFile(failed.HTMLPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(html), "Run outcome")
	assert.Contains(t, string(html), "importAttempted")
	assert.Contains(t, string(html), "importSucceeded")
	assert.Contains(t, string(html), "second dashboard rejected")
	assert.Equal(t, int32(2), posts.Load())
}

func TestMigrateGrafanaPersistsFatalValidationOutcome(t *testing.T) {
	t.Parallel()

	var importCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v5/query_range/preview":
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusUnauthorized)
			require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{"error": map[string]any{
				"code": "unauthorized", "message": "validation credentials rejected",
			}}))
		case "/api/v1/dashboards":
			importCalls.Add(1)
			writer.WriteHeader(http.StatusInternalServerError)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	input := filepath.Join(t.TempDir(), "validation-failure.json")
	require.NoError(t, os.WriteFile(input, []byte(`{
		"title":"Validation failure",
		"panels":[{"title":"Up","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]}]
	}`), 0o600))

	results, err := MigrateGrafana(context.Background(), []string{input}, GrafanaOptions{
		OutputDirectory: t.TempDir(), TargetURL: server.URL, APIKey: "bad-key", HTTPClient: server.Client(), Validate: true,
		SourceNamespace: "grafana:test",
	})

	require.ErrorContains(t, err, "validation credentials rejected")
	assert.Equal(t, ErrorTarget, KindOf(err))
	require.Len(t, results, 1, "fatal validation must still return its current evidence")
	result := results[0]
	assert.True(t, result.ImportRequested)
	assert.False(t, result.ImportAttempted)
	assert.False(t, result.ImportSucceeded)
	assert.Equal(t, "skipped", result.TargetAction)
	assert.Contains(t, result.TargetSkipped, "target validation failed")
	assert.Contains(t, result.TargetError, "validation credentials rejected")
	assert.Equal(t, int32(0), importCalls.Load())
	assert.FileExists(t, result.DashboardPath)
	assert.FileExists(t, result.ReportPath)
	assert.FileExists(t, result.HTMLPath)

	var persisted reporttypes.Report
	decodeFile(t, result.ReportPath, &persisted)
	assert.Equal(t, true, persisted.Run.Flags["importRequested"])
	assert.Equal(t, false, persisted.Run.Flags["importAttempted"])
	assert.Equal(t, false, persisted.Run.Flags["importSucceeded"])
	assert.Equal(t, "skipped", persisted.Run.Flags["targetAction"])
	assert.Contains(t, persisted.Run.Flags["targetError"], "validation credentials rejected")
}

func TestMigrateGrafanaDoesNotImportDisabledOnlyPayload(t *testing.T) {
	t.Parallel()

	var importCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v1/dashboards":
			importCalls.Add(1)
			writer.WriteHeader(http.StatusInternalServerError)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	input := filepath.Join(t.TempDir(), "disabled-only.json")
	require.NoError(t, os.WriteFile(input, []byte(`{
		"title":"Disabled only",
		"panels":[{"title":"Hidden","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)","hide":true}]}]
	}`), 0o600))

	results, err := MigrateGrafana(context.Background(), []string{input}, GrafanaOptions{
		OutputDirectory: t.TempDir(), TargetURL: server.URL, APIKey: "key", HTTPClient: server.Client(),
		SourceNamespace: "grafana:test",
	})

	require.NoError(t, err)
	require.Len(t, results, 1)
	result := results[0]
	assert.True(t, result.ImportRequested)
	assert.False(t, result.ImportAttempted)
	assert.False(t, result.ImportSucceeded)
	assert.Equal(t, "skipped", result.TargetAction)
	assert.Contains(t, result.TargetSkipped, "no executable widgets")
	assert.Equal(t, 0, result.Evidence.Run.Flags["candidateExecutableWidgets"])
	assert.Equal(t, 0, result.Evidence.Run.Flags["importableExecutableWidgets"])
	assert.Equal(t, int32(0), importCalls.Load())
}

func widgetTitles(dashboard signoz.DashboardV5) []string {
	titles := make([]string, 0, len(dashboard.Widgets))
	for _, widget := range dashboard.Widgets {
		titles = append(titles, widget.Title)
	}
	return titles
}

func assertDashboardLayoutsReferenceKeptWidgets(t *testing.T, dashboard signoz.DashboardV5) {
	t.Helper()
	widgets := make(map[string]signoz.Widget, len(dashboard.Widgets))
	for _, widget := range dashboard.Widgets {
		require.NotEmpty(t, widget.ID, "every kept widget must have an id")
		_, duplicate := widgets[widget.ID]
		require.Falsef(t, duplicate, "kept widget id %q is duplicated", widget.ID)
		widgets[widget.ID] = widget
	}
	placements := make(map[string]int, len(dashboard.Widgets))
	topLevel := make(map[string]bool, len(dashboard.Layout))
	for _, layout := range dashboard.Layout {
		_, kept := widgets[layout.I]
		assert.Truef(t, kept, "top-level layout references removed widget %q", layout.I)
		placements[layout.I]++
		topLevel[layout.I] = true
	}
	for rowID, group := range dashboard.PanelMap {
		row, kept := widgets[rowID]
		assert.Truef(t, kept, "panel map references removed row %q", rowID)
		assert.Equalf(t, "row", row.PanelTypes, "panel map key %q must identify a row widget", rowID)
		assert.Truef(t, topLevel[rowID], "panel map row %q must have a top-level layout", rowID)
		for _, layout := range group.Widgets {
			_, childKept := widgets[layout.I]
			assert.Truef(t, childKept, "row layout references removed widget %q", layout.I)
			assert.Falsef(t, topLevel[layout.I], "row child %q must not also have a top-level layout", layout.I)
			placements[layout.I]++
		}
	}
	for id := range widgets {
		assert.Equalf(t, 1, placements[id], "kept widget %q must occur in exactly one layout", id)
	}
}

func TestMigrateGrafanaUsesVariableOverrideAsGeneratedDefault(t *testing.T) {
	t.Parallel()

	output := t.TempDir()
	results, err := MigrateGrafana(
		context.Background(),
		[]string{"../source/grafana/testdata/modern.json"},
		GrafanaOptions{OutputDirectory: output, Variables: map[string]string{"job": "node-exporter"}},
	)
	require.NoError(t, err)
	require.Len(t, results, 1)

	var dashboard signoz.DashboardV5
	decodeFile(t, results[0].DashboardPath, &dashboard)
	for _, variable := range dashboard.Variables {
		if variable.Name == "job" {
			assert.Equal(t, []any{"node-exporter"}, variable.SelectedValue)
			assert.Equal(t, "node-exporter", variable.DefaultValue)
			return
		}
	}
	t.Fatal("job variable not emitted")
}

func TestMigrateGrafanaAppliesExplicitMetricNameMap(t *testing.T) {
	t.Parallel()

	results, err := MigrateGrafana(
		context.Background(),
		[]string{"../source/grafana/testdata/modern.json"},
		GrafanaOptions{OutputDirectory: t.TempDir(), MetricNameMap: map[string]string{"up": "service.check"}},
	)
	require.NoError(t, err)

	var dashboard signoz.DashboardV5
	decodeFile(t, results[0].DashboardPath, &dashboard)
	require.NotEmpty(t, dashboard.Widgets)
	require.NotEmpty(t, dashboard.Widgets[0].Query.PromQL)
	assert.Contains(t, dashboard.Widgets[0].Query.PromQL[0].Query, "service.check")

	var evidence reporttypes.Report
	decodeFile(t, results[0].ReportPath, &evidence)
	assert.Contains(t, evidence.Panels[0].Queries[0].ReasonCodes, "METRIC_NAME_REMAP")
}

func TestMigrateGrafanaFailsClosedForNameOnlyMappedBinaryShapes(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "name-only-binary.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"title":"Name-only binary",
		"panels":[{"title":"Binary","type":"timeseries","targets":[
			{"refId":"A","expr":"foo / bar"}
		]}]
	}`), 0o600))

	results, err := MigrateGrafana(context.Background(), []string{path}, GrafanaOptions{
		OutputDirectory: t.TempDir(),
		MetricNameMap:   map[string]string{"foo": "target.foo", "bar": "target.bar"},
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	query := results[0].Evidence.Panels[0].Queries[0]
	assert.Equal(t, "none", query.EmittedKind)
	assert.Contains(t, query.ReasonCodes, "TARGET_RESOURCE_VECTOR_MATCHING_UNRESOLVED")

	var dashboard signoz.DashboardV5
	decodeFile(t, results[0].DashboardPath, &dashboard)
	assert.Empty(t, dashboard.Widgets)
}

func TestMigrateGrafanaIsolatesMetricMetadataFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		metricName := request.URL.Query().Get("metricName")
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			if metricName == "bad_metric_sum" {
				writer.WriteHeader(http.StatusInternalServerError)
				writeJSONResponse(t, writer, map[string]any{"error": map[string]any{
					"code": "metadata_backend_failed", "message": "metadata backend failed",
				}})
				return
			}
			if strings.HasPrefix(metricName, "missing_metric") {
				writer.WriteHeader(http.StatusNotFound)
				writeJSONResponse(t, writer, map[string]any{"error": map[string]any{
					"code": "not_found", "message": "metric not found",
				}})
				return
			}
			if metricName == "foo_bucket" {
				writer.WriteHeader(http.StatusNotFound)
				writeJSONResponse(t, writer, map[string]any{"error": map[string]any{
					"code": "not_found", "message": "metric not found",
				}})
				return
			}
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			if metricName == "attributes_missing_metric" {
				writer.WriteHeader(http.StatusNotFound)
				writeJSONResponse(t, writer, map[string]any{"error": map[string]any{
					"code": "not_found", "message": "attributes not found",
				}})
				return
			}
			if metricName == "foo.bucket" {
				writer.WriteHeader(http.StatusInternalServerError)
				writeJSONResponse(t, writer, map[string]any{"error": map[string]any{
					"code": "attributes_backend_failed", "message": "attributes backend failed",
				}})
				return
			}
			require.Equal(t, "good_metric", metricName)
			start, err := strconv.ParseInt(request.URL.Query().Get("start"), 10, 64)
			require.NoError(t, err)
			end, err := strconv.ParseInt(request.URL.Query().Get("end"), 10, 64)
			require.NoError(t, err)
			assert.Equal(t, (2 * time.Hour).Milliseconds(), end-start)
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	path := filepath.Join(t.TempDir(), "metadata-isolation.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"title":"Metadata isolation",
		"panels":[
			{"title":"Bad","type":"timeseries","targets":[{"refId":"A","expr":"sum(bad_metric_sum)"}]},
			{"title":"Good","type":"timeseries","targets":[{"refId":"A","expr":"sum(good_metric)"}]},
			{"title":"Missing","type":"timeseries","targets":[{"refId":"A","expr":"sum(missing_metric_sum)"}]},
			{"title":"Attributes unavailable","type":"timeseries","targets":[{"refId":"A","expr":"sum(attributes_missing_metric)"}]},
			{"title":"Remapped attributes unavailable","type":"timeseries","targets":[{"refId":"A","expr":"sum(foo_bucket)"}]}
		]
	}`), 0o600))

	results, err := MigrateGrafana(context.Background(), []string{path}, GrafanaOptions{
		OutputDirectory: t.TempDir(), TargetURL: server.URL, APIKey: "key", HTTPClient: server.Client(),
		SourceNamespace: "grafana:test",
		Range:           2 * time.Hour, DryRun: true,
	})

	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Len(t, results[0].Evidence.Panels, 5)
	bad := results[0].Evidence.Panels[0].Queries[0]
	good := results[0].Evidence.Panels[1].Queries[0]
	missing := results[0].Evidence.Panels[2].Queries[0]
	attributesUnavailable := results[0].Evidence.Panels[3].Queries[0]
	remappedAttributesUnavailable := results[0].Evidence.Panels[4].Queries[0]
	assert.Equal(t, "promql", bad.EmittedKind)
	assert.Equal(t, "needs_review", bad.Verdict)
	assert.Contains(t, bad.ReasonCodes, "METRIC_METADATA_UNAVAILABLE")
	assert.False(t, bad.Validation.MetricChecked)
	assert.False(t, bad.Validation.MetricFound)
	assert.Equal(t, "promql", good.EmittedKind)
	assert.Equal(t, "needs_review", good.Verdict)
	assert.Equal(t, "builder", good.CandidateKind)
	assert.Contains(t, good.ReasonCodes, "BUILDER_LATEST_LOOKBACK_SEMANTICS")
	assert.True(t, good.Validation.MetricChecked)
	assert.True(t, good.Validation.MetricFound)
	assert.Equal(t, "promql", missing.EmittedKind)
	assert.Equal(t, "needs_review", missing.Verdict)
	assert.Contains(t, missing.ReasonCodes, "MISSING_METRIC_IN_TARGET")
	assert.NotContains(t, missing.ReasonCodes, "METRIC_METADATA_UNAVAILABLE")
	assert.True(t, missing.Validation.MetricChecked)
	assert.False(t, missing.Validation.MetricFound)
	assert.Equal(t, "promql", attributesUnavailable.EmittedKind)
	assert.Equal(t, "needs_review", attributesUnavailable.Verdict)
	assert.Contains(t, attributesUnavailable.ReasonCodes, "METRIC_METADATA_UNAVAILABLE")
	assert.NotContains(t, attributesUnavailable.ReasonCodes, "MISSING_METRIC_IN_TARGET")
	assert.False(t, attributesUnavailable.Validation.MetricChecked)
	assert.False(t, attributesUnavailable.Validation.MetricFound)
	assert.Equal(t, "promql", remappedAttributesUnavailable.EmittedKind)
	assert.Equal(t, "needs_review", remappedAttributesUnavailable.Verdict)
	assert.Contains(t, remappedAttributesUnavailable.ReasonCodes, "METRIC_NAME_REMAP")
	assert.Contains(t, remappedAttributesUnavailable.ReasonCodes, "METRIC_METADATA_UNAVAILABLE")
	assert.NotContains(t, remappedAttributesUnavailable.ReasonCodes, "MISSING_METRIC_IN_TARGET")
	assert.False(t, remappedAttributesUnavailable.Validation.MetricChecked)
	assert.False(t, remappedAttributesUnavailable.Validation.MetricFound)
	payload, readErr := os.ReadFile(results[0].DashboardPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(payload), "foo.bucket")
	assert.NotContains(t, string(payload), "foo_bucket")
}

func TestMigrateGrafanaResolvesHiddenMetadataIndependentlyOfBatchOrder(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	directory := t.TempDir()
	dashboardPath := filepath.Join(directory, "hidden.json")
	warmPath := filepath.Join(directory, "warm.json")
	require.NoError(t, os.WriteFile(dashboardPath, []byte(`{
		"title":"Hidden metadata",
		"panels":[{"title":"Panel","type":"timeseries","targets":[
			{"refId":"A","expr":"sum(up)"},
			{"refId":"B","expr":"sum(aux_metric)","hide":true}
		]}]
	}`), 0o600))
	require.NoError(t, os.WriteFile(warmPath, []byte(`{
		"title":"Warm metadata",
		"panels":[{"title":"Panel","type":"timeseries","targets":[
			{"refId":"A","expr":"sum(aux_metric)"}
		]}]
	}`), 0o600))

	options := func(output string) GrafanaOptions {
		return GrafanaOptions{
			OutputDirectory: output, TargetURL: server.URL, APIKey: "key", HTTPClient: server.Client(),
			SourceNamespace: "grafana:test", DryRun: true,
		}
	}
	alone, err := MigrateGrafana(context.Background(), []string{dashboardPath}, options(t.TempDir()))
	require.NoError(t, err)
	require.Len(t, alone, 1)
	warmed, err := MigrateGrafana(context.Background(), []string{warmPath, dashboardPath}, options(t.TempDir()))
	require.NoError(t, err)
	require.Len(t, warmed, 2)

	require.Len(t, alone[0].Evidence.Panels[0].Queries, 2)
	require.Len(t, warmed[1].Evidence.Panels[0].Queries, 2)
	assert.Equal(t, alone[0].Evidence.Panels[0].Queries[1], warmed[1].Evidence.Panels[0].Queries[1])
	hidden := alone[0].Evidence.Panels[0].Queries[1]
	assert.True(t, hidden.Disabled)
	assert.True(t, hidden.Validation.MetricChecked)
	assert.True(t, hidden.Validation.MetricFound)
	assert.NotContains(t, hidden.ReasonCodes, "METRIC_METADATA_UNAVAILABLE")
}

func TestMigrateGrafanaSkipsMetadataForGuaranteedOmittedPanels(t *testing.T) {
	t.Parallel()

	var metadataRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/api/v2/metrics/") {
			metadataRequests.Add(1)
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(`{"data":{}}`))
			return
		}
		writer.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	path := filepath.Join(t.TempDir(), "omitted-metadata.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"title":"Omitted metadata",
		"panels":[
			{"title":"Valid","type":"timeseries","targets":[{"refId":"A","expr":"vector(1)"}]},
			{"title":"Plugin","type":"vendor-unknown-panel","targets":[{"refId":"A","expr":"sum(aux_metric)"}]},
			{"title":"Hidden only","type":"timeseries","targets":[{"refId":"A","expr":"sum(hidden_metric)","hide":true}]},
			{"title":"Instant fatal","type":"timeseries","targets":[
				{"refId":"A","expr":"sum(instant_metric)","instant":true},
				{"refId":"B","expr":"sum(hidden_sibling)","hide":true}
			]},
			{"title":"Valid with dead hidden","type":"timeseries","targets":[
				{"refId":"A","expr":"vector(2)"},
				{"refId":"B","expr":"sum(hidden_instant_metric)","hide":true,"instant":true}
			]}
		]
	}`), 0o600))

	results, err := MigrateGrafana(context.Background(), []string{path}, GrafanaOptions{
		OutputDirectory: t.TempDir(), TargetURL: server.URL, APIKey: "key", HTTPClient: server.Client(),
		SourceNamespace: "grafana:test", DryRun: true,
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Zero(t, metadataRequests.Load())

	var dashboard signoz.DashboardV5
	decodeFile(t, results[0].DashboardPath, &dashboard)
	require.Len(t, dashboard.Widgets, 2)
	assert.Equal(t, "Valid", dashboard.Widgets[0].Title)
	assert.Equal(t, "Valid with dead hidden", dashboard.Widgets[1].Title)
	assert.Contains(t, results[0].Evidence.Panels[1].ReasonCodes, "PANEL_OMITTED")
	assert.Contains(t, results[0].Evidence.Panels[2].ReasonCodes, "PANEL_OMITTED")
	assert.Contains(t, results[0].Evidence.Panels[3].ReasonCodes, "PANEL_OMITTED")
	assert.NotContains(t, results[0].Evidence.Panels[4].ReasonCodes, "PANEL_OMITTED")
}

func TestMigrateGrafanaDoesNotCachePartialMetadataAfterFatalAttributeFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			writer.WriteHeader(http.StatusUnauthorized)
			writeJSONResponse(t, writer, map[string]any{"error": map[string]any{
				"code": "unauthorized", "message": "expired key",
			}})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	directory := t.TempDir()
	paths := make([]string, 0, 2)
	for index, title := range []string{"First", "Second"} {
		path := filepath.Join(directory, fmt.Sprintf("dashboard-%d.json", index))
		require.NoError(t, os.WriteFile(path, fmt.Appendf(nil, `{
			"title":%q,
			"panels":[{"title":"Panel","type":"timeseries","targets":[
				{"refId":"A","expr":"sum(partial_metric)"}
			]}]
		}`, title), 0o600))
		paths = append(paths, path)
	}

	results, err := MigrateGrafana(context.Background(), paths, GrafanaOptions{
		OutputDirectory: t.TempDir(), TargetURL: server.URL, APIKey: "key", HTTPClient: server.Client(),
		SourceNamespace: "grafana:test", DryRun: true,
	})
	require.Error(t, err)
	require.Len(t, results, 2)
	for _, result := range results {
		query := result.Evidence.Panels[0].Queries[0]
		assert.Equal(t, "promql", query.EmittedKind)
		assert.Contains(t, query.ReasonCodes, "METRIC_METADATA_UNAVAILABLE")
		assert.False(t, query.Validation.MetricChecked)
		assert.False(t, query.Validation.MetricFound)
	}
}

func TestMigrateGrafanaDoesNotPoisonUnattemptedMetricsAfterFatalLookup(t *testing.T) {
	t.Parallel()

	var fooMetadataRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		metricName := request.URL.Query().Get("metricName")
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			switch metricName {
			case "bad_metric":
				writeJSONResponse(t, writer, map[string]any{"data": map[string]any{}})
			case "foo_bucket":
				fooMetadataRequests.Add(1)
				writer.WriteHeader(http.StatusNotFound)
				writeJSONResponse(t, writer, map[string]any{"error": map[string]any{
					"code": "not_found", "message": "not found",
				}})
			case "foo.bucket":
				fooMetadataRequests.Add(1)
				writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
			default:
				writer.WriteHeader(http.StatusNotFound)
			}
		case "/api/v2/metrics/attributes":
			require.Equal(t, "foo.bucket", metricName)
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	directory := t.TempDir()
	first := filepath.Join(directory, "first.json")
	second := filepath.Join(directory, "second.json")
	require.NoError(t, os.WriteFile(first, []byte(`{
		"title":"Fatal then unattempted",
		"panels":[
			{"title":"Bad","type":"timeseries","targets":[{"refId":"A","expr":"sum(bad_metric)"}]},
			{"title":"Later","type":"timeseries","targets":[{"refId":"A","expr":"sum(foo_bucket)"}]}
		]
	}`), 0o600))
	require.NoError(t, os.WriteFile(second, []byte(`{
		"title":"Retry unattempted",
		"panels":[{"title":"Foo","type":"timeseries","targets":[{"refId":"A","expr":"sum(foo_bucket)"}]}]
	}`), 0o600))

	results, err := MigrateGrafana(context.Background(), []string{first, second}, GrafanaOptions{
		OutputDirectory: t.TempDir(), TargetURL: server.URL, APIKey: "key", HTTPClient: server.Client(),
		SourceNamespace: "grafana:test", DryRun: true,
	})
	require.Error(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, int32(2), fooMetadataRequests.Load(), "the later dashboard must resolve both name candidates")
	query := results[1].Evidence.Panels[0].Queries[0]
	assert.Contains(t, query.ReasonCodes, "METRIC_NAME_REMAP")
	assert.NotContains(t, query.ReasonCodes, "METRIC_METADATA_UNAVAILABLE")
	assert.True(t, query.Validation.MetricChecked)
	assert.True(t, query.Validation.MetricFound)
	payload, readErr := os.ReadFile(results[1].DashboardPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(payload), "foo.bucket")
}

func TestMigrateGrafanaTreatsMalformedMetadataSuccessAsFatal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		malformedPath string
		expected      string
	}{
		{name: "metadata missing type", malformedPath: "/api/v2/metrics/metadata", expected: "missing data.type"},
		{name: "attributes missing array", malformedPath: "/api/v2/metrics/attributes", expected: "missing data.attributes"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/api/v2/metrics/metadata":
					if test.malformedPath == request.URL.Path {
						writeJSONResponse(t, writer, map[string]any{"data": map[string]any{}})
						return
					}
					writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
				case "/api/v2/metrics/attributes":
					writeJSONResponse(t, writer, map[string]any{"data": map[string]any{}})
				default:
					writer.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)

			path := filepath.Join(t.TempDir(), "malformed-metadata.json")
			require.NoError(t, os.WriteFile(path, []byte(`{
				"title":"Malformed metadata",
				"panels":[{"title":"Metric","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]}]
			}`), 0o600))

			output := t.TempDir()
			results, err := MigrateGrafana(context.Background(), []string{path}, GrafanaOptions{
				OutputDirectory: output, TargetURL: server.URL, APIKey: "key", HTTPClient: server.Client(), DryRun: true,
				SourceNamespace: "grafana:test",
			})

			require.ErrorContains(t, err, test.expected)
			assert.Equal(t, ErrorTarget, KindOf(err))
			require.Len(t, results, 1, "fatal metadata failures must still return and persist evidence")
			result := results[0]
			assert.False(t, result.ImportAttempted)
			assert.False(t, result.ImportSucceeded)
			assert.Equal(t, "skipped", result.TargetAction)
			assert.Contains(t, result.TargetSkipped, "target validation failed")
			assert.Contains(t, result.TargetError, test.expected)
			assert.FileExists(t, result.DashboardPath)
			assert.FileExists(t, result.ReportPath)
			assert.FileExists(t, result.HTMLPath)

			var persisted reporttypes.Report
			decodeFile(t, result.ReportPath, &persisted)
			assert.Equal(t, false, persisted.Run.Flags["importAttempted"])
			assert.Equal(t, false, persisted.Run.Flags["importSucceeded"])
			assert.Equal(t, "skipped", persisted.Run.Flags["targetAction"])
			assert.Contains(t, persisted.Run.Flags["targetError"], test.expected)
		})
	}
}

func TestLocalMetricMetadataErrorClassification(t *testing.T) {
	t.Parallel()

	for _, statusCode := range []int{
		http.StatusBadRequest,
		http.StatusConflict,
		http.StatusRequestEntityTooLarge,
		http.StatusUnprocessableEntity,
		http.StatusInternalServerError,
		http.StatusBadGateway,
	} {
		assert.True(t, localMetricMetadataError(&signoz.APIError{StatusCode: statusCode}), "status %d", statusCode)
	}
	for _, statusCode := range []int{
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusMethodNotAllowed,
		http.StatusUnsupportedMediaType,
		http.StatusUpgradeRequired,
		http.StatusTooManyRequests,
	} {
		assert.False(t, localMetricMetadataError(&signoz.APIError{StatusCode: statusCode}), "status %d", statusCode)
	}
	assert.False(t, localMetricMetadataError(errors.New("transport failed")))
}

func decodeFile(t *testing.T, path string, destination any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean(path))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, destination))
}

func TestArtifactBasesDisambiguateDuplicateFilenames(t *testing.T) {
	t.Parallel()

	bases := artifactBases([]string{"loki/rules/alerts.yaml", "mimir/rules/alerts.yaml", "rules/unique.yaml"})
	require.Len(t, bases, 3)
	assert.NotEqual(t, bases[0], bases[1])
	assert.Contains(t, bases[0], "alerts-")
	assert.Contains(t, bases[1], "alerts-")
	assert.Equal(t, "unique", bases[2])
}

func TestArtifactBasesReserveEveryGeneratedArtifactName(t *testing.T) {
	t.Parallel()

	paths := []string{
		"first/foo.json",
		"second/foo.json",
		"third/foo.candidate.json",
		"first/foo.json",
	}
	bases := artifactBases(paths)
	require.Len(t, bases, len(paths))
	assert.Equal(t, bases, artifactBases(paths), "planning must be deterministic")

	seen := make(map[string]string, len(paths)*4)
	for index, base := range bases {
		for _, name := range artifactNames(base) {
			if previous, exists := seen[name]; exists {
				t.Fatalf("artifact %q collides for %q and %q", name, previous, paths[index])
			}
			seen[name] = paths[index]
		}
	}
}

func TestArtifactBasesAreIndependentOfDistinctInputOrder(t *testing.T) {
	t.Parallel()

	first := []string{"z/foo.json", "a/foo.json", "m/foo.candidate.json", "q/unique.json"}
	second := []string{"q/unique.json", "m/foo.candidate.json", "z/foo.json", "a/foo.json"}
	firstBases := artifactBases(first)
	secondBases := artifactBases(second)
	byPath := make(map[string]string, len(first))
	for index, path := range first {
		byPath[path] = firstBases[index]
	}
	for index, path := range second {
		assert.Equal(t, byPath[path], secondBases[index], "artifact base for %q changed with argument order", path)
	}
}

func TestArtifactBasesReserveCaseFoldedAndUnicodeEquivalentNames(t *testing.T) {
	t.Parallel()

	paths := []string{
		"one/Foo.json",
		"two/foo.json",
		"three/caf\u00e9.json",
		"four/cafe\u0301.json",
	}
	bases := artifactBases(paths)
	require.Len(t, bases, len(paths))
	seen := make(map[string]string, len(paths)*4)
	for index, base := range bases {
		for _, name := range artifactNames(base) {
			key := artifactKey(name)
			if previous, exists := seen[key]; exists {
				t.Fatalf("portable artifact %q collides for %q and %q", name, previous, paths[index])
			}
			seen[key] = paths[index]
		}
	}
	assert.NotEqual(t, bases[0], bases[1])
	assert.NotEqual(t, bases[2], bases[3])
}

func TestArtifactBasesEscapeWindowsInvalidAndReservedNames(t *testing.T) {
	t.Parallel()

	paths := []string{
		"CON.json",
		"nul.yaml",
		"foo:bar.json",
		"trailing .json",
		"trailing..json",
		strings.Repeat("x", 260) + ".json",
	}
	bases := artifactBases(paths)
	require.Len(t, bases, len(paths))
	for index, base := range bases {
		assert.True(t, portableArtifactBase(base), "%q produced non-portable base %q", paths[index], base)
		assert.LessOrEqual(t, len(base), maxArtifactBaseBytes)
	}
	assert.Equal(t, bases, artifactBases(paths), "portable escaping must be deterministic")
}

func TestMigrateGrafanaRejectsWholeBatchAfterInvalidInput(t *testing.T) {
	t.Parallel()

	invalid := filepath.Join(t.TempDir(), "invalid.json")
	require.NoError(t, os.WriteFile(invalid, []byte("{"), 0o600))
	output := filepath.Join(t.TempDir(), "not-created")
	results, err := MigrateGrafana(context.Background(), []string{
		invalid, "../source/grafana/testdata/modern.json",
	}, GrafanaOptions{OutputDirectory: output})

	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Nil(t, results)
	assert.NoDirExists(t, output)
}
