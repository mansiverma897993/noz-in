package validate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mansiverma897993/signoz/internal/model"
	migrationreport "github.com/mansiverma897993/signoz/internal/report"
	"github.com/mansiverma897993/signoz/internal/target/signoz"
	"github.com/mansiverma897993/signoz/pkg/reporttypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMissingWidgetVariablesScansEveryEmittedExpression(t *testing.T) {
	t.Parallel()

	builder := signoz.Widget{Query: signoz.WidgetQuery{QueryType: "builder", Builder: signoz.BuilderContainer{
		QueryData:     []signoz.BuilderQueryData{{Filter: signoz.Expression{Expression: `service = '${filter}' AND capture = '${1}'`}}},
		QueryFormulas: []signoz.BuilderFormula{{Expression: `$formula + ${known}`}},
	}}}
	promQL := signoz.Widget{Query: signoz.WidgetQuery{QueryType: "promql", PromQL: []signoz.PromQLQuery{{
		Query: `sum(up{job="$prom"})`,
	}}}}

	assert.Equal(t, []string{"filter", "formula"}, missingWidgetVariables(builder, map[string]any{"known": "1"}))
	assert.Equal(t, []string{"prom"}, missingWidgetVariables(promQL, nil))
	assert.Empty(t, missingWidgetVariables(promQL, map[string]any{"prom": []string{"api", "worker"}}))
}

func TestDashboardRejectsExecutableWidgetWithUnknownSourcePath(t *testing.T) {
	t.Parallel()

	dashboard := signoz.DashboardV5{Widgets: []signoz.Widget{
		validationWidget("/panels/missing", "Unknown", "A"),
	}}
	evidence := reporttypes.Report{Panels: []reporttypes.PanelRecord{{
		SourcePath: "/panels/0",
		Queries:    []reporttypes.QueryRecord{{RefID: "A", EmittedKind: "promql"}},
	}}}

	valid, err := Dashboard(context.Background(), nil, dashboard, &evidence, nil, Options{})

	require.ErrorContains(t, err, `source path "/panels/missing" has no evidence panel`)
	assert.False(t, valid)
}

func TestDashboardRejectsDuplicateExecutableWidgetMapping(t *testing.T) {
	t.Parallel()

	dashboard := signoz.DashboardV5{Widgets: []signoz.Widget{
		validationWidget("/panels/0", "First", "A"),
		validationWidget("/panels/0", "Duplicate", "B"),
	}}
	evidence := reporttypes.Report{Panels: []reporttypes.PanelRecord{{
		SourcePath: "/panels/0",
		Queries:    []reporttypes.QueryRecord{{RefID: "A", EmittedKind: "promql"}},
	}}}

	valid, err := Dashboard(context.Background(), nil, dashboard, &evidence, nil, Options{})

	require.ErrorContains(t, err, `more than one executable widget maps to evidence panel "/panels/0"`)
	assert.False(t, valid)
}

func TestDashboardRejectsEvidenceQueryWithoutEmittedWidget(t *testing.T) {
	t.Parallel()

	evidence := reporttypes.Report{Panels: []reporttypes.PanelRecord{{
		SourcePath: "/panels/0",
		Queries:    []reporttypes.QueryRecord{{RefID: "A", EmittedKind: "promql"}},
	}}}

	valid, err := Dashboard(context.Background(), nil, signoz.DashboardV5{}, &evidence, nil, Options{})

	require.ErrorContains(t, err, `evidence panel "/panels/0" has executable queries but no emitted widget`)
	assert.False(t, valid)
}

func TestDashboardWithNoValidationJobsDoesNotPass(t *testing.T) {
	t.Parallel()

	dashboard := signoz.DashboardV5{Widgets: []signoz.Widget{{
		SourcePath: "/panels/0", Title: "Structural row", PanelTypes: "row",
	}}}
	evidence := reporttypes.Report{Panels: []reporttypes.PanelRecord{{SourcePath: "/panels/0"}}}

	valid, err := Dashboard(context.Background(), nil, dashboard, &evidence, nil, Options{})

	require.NoError(t, err)
	assert.False(t, valid, "an empty validation plan must never be interpreted as target validation success")
}

func TestDashboardSkipsMissingVariableWidgetAndContinuesSibling(t *testing.T) {
	t.Parallel()

	var previewCalls atomic.Int32
	var queryCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			CompositeQuery struct {
				Queries []struct {
					Spec struct {
						Name string `json:"name"`
					} `json:"spec"`
				} `json:"queries"`
			} `json:"compositeQuery"`
		}
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		require.Len(t, body.CompositeQuery.Queries, 1)
		name := body.CompositeQuery.Queries[0].Spec.Name
		require.Equal(t, "GOOD", name, "the missing-variable widget must not issue a request")
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v5/query_range/preview":
			previewCalls.Add(1)
			require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{"data": map[string]any{
				"compositeQuery": map[string]any{name: map[string]any{"valid": true}},
			}}))
		case "/api/v5/query_range":
			queryCalls.Add(1)
			require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{
				"status": "success", "data": map[string]any{"data": map[string]any{"results": []any{map[string]any{
					"queryName": name, "series": []any{map[string]any{"values": []any{map[string]any{"timestamp": 1, "value": 1}}}},
				}}}},
			}))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client, err := signoz.NewClient(server.URL, "key", server.Client())
	require.NoError(t, err)

	dashboard := signoz.DashboardV5{Widgets: []signoz.Widget{
		validationWidget("/panels/0", "Missing", "BAD"),
		validationWidget("/panels/1", "Sibling", "GOOD"),
	}}
	dashboard.Widgets[0].Query.PromQL[0].Query = `sum(up{job="$job",node="${unknown}"})`
	evidence := reporttypes.Report{
		Summary: reporttypes.Summary{Native: 2, PanelsNative: 2},
		Panels: []reporttypes.PanelRecord{
			{SourcePath: "/panels/0", Verdict: string(model.VerdictNative), Queries: []reporttypes.QueryRecord{{
				RefID: "BAD", EmittedKind: "promql", Verdict: string(model.VerdictNative),
			}}},
			{SourcePath: "/panels/1", Verdict: string(model.VerdictNative), Queries: []reporttypes.QueryRecord{{
				RefID: "GOOD", EmittedKind: "promql", Verdict: string(model.VerdictNative),
			}}},
		},
	}

	valid, err := Dashboard(context.Background(), client, dashboard, &evidence, nil, Options{
		Workers: 2,
		Now:     func() time.Time { return time.Unix(1_700_000_000, 0) },
		VariableIssues: map[string]VariableIssue{"job": {
			Reasons: []model.ReasonCode{model.ReasonMissingVariableValue, model.ReasonMultiVariableValue},
			Detail:  "multiple current values are scalar-unsafe",
		}},
	})

	require.NoError(t, err)
	assert.False(t, valid)
	assert.Equal(t, int32(1), previewCalls.Load())
	assert.Equal(t, int32(1), queryCalls.Load())
	missing := evidence.Panels[0].Queries[0]
	assert.False(t, missing.Validation.Previewed)
	assert.False(t, missing.Validation.Executed)
	assert.Equal(t, "MISSING_VARIABLE_VALUE", missing.Validation.ErrorCode)
	assert.Equal(t, []string{"job", "unknown"}, missing.Validation.MissingVariables)
	assert.Contains(t, missing.Validation.ReasonCodes, "MULTI_VARIABLE_VALUE_UNRESOLVED")
	assert.Equal(t, string(model.VerdictNeedsReview), missing.Verdict)
	assert.Contains(t, missing.ReasonCodes, "MISSING_VARIABLE_VALUE")
	assert.Contains(t, missing.ReasonCodes, "MULTI_VARIABLE_VALUE_UNRESOLVED")
	assert.Equal(t, string(model.VerdictNeedsReview), evidence.Panels[0].Verdict)
	assert.Equal(t, "needs-review", evidence.Panels[0].State)
	assert.True(t, evidence.Panels[1].Queries[0].Validation.Executed)
	assert.Equal(t, 1, evidence.Summary.Native)
	assert.Equal(t, 1, evidence.Summary.NeedsReview)
	assert.Equal(t, 1, evidence.Summary.PanelsNative)
	assert.Equal(t, 1, evidence.Summary.PanelsNeedsReview)
}

func TestDashboardValidatesQueriesWithBoundedWorkers(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v5/query_range/preview":
			require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{"data": map[string]any{
				"compositeQuery": map[string]any{"A": map[string]any{"valid": true}},
			}}))
		case "/api/v5/query_range":
			require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{"data": map[string]any{"results": []any{map[string]any{
					"queryName": "A",
					"series":    []any{map[string]any{"values": []any{map[string]any{"timestamp": 1, "value": 1}}}},
				}}}},
			}))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client, err := signoz.NewClient(server.URL, "key", server.Client())
	require.NoError(t, err)

	widget := signoz.Widget{
		Title:      "CPU",
		PanelTypes: "graph",
		Query: signoz.WidgetQuery{
			QueryType: "promql",
			PromQL:    []signoz.PromQLQuery{{Name: "A", Query: "up"}},
		},
	}
	dashboard := signoz.DashboardV5{Widgets: []signoz.Widget{widget, widget}}
	evidence := reporttypes.Report{Panels: []reporttypes.PanelRecord{
		{Queries: []reporttypes.QueryRecord{{RefID: "A"}}},
		{Queries: []reporttypes.QueryRecord{{RefID: "A"}}},
	}}

	valid, err := Dashboard(context.Background(), client, dashboard, &evidence, nil, Options{
		Workers: 2,
		Now:     func() time.Time { return time.Unix(1_700_000_000, 0) },
	})

	require.NoError(t, err)
	assert.True(t, valid)
	assert.Equal(t, 2, evidence.Summary.PreviewValid)
	assert.Equal(t, 2, evidence.Summary.Executed)
	assert.Equal(t, 2, evidence.Summary.DataPresent)
	assert.Equal(t, "2023-11-14T22:13:20Z", evidence.Panels[0].Queries[0].Validation.CheckedAt)
}

func TestDashboardIsolatesWidgetAPIErrorAndContinues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		failedPath      string
		statusCode      int
		structured      bool
		expectedCode    string
		expectedMessage string
	}{
		{
			name: "preview 400", failedPath: "/api/v5/query_range/preview", statusCode: http.StatusBadRequest,
			structured: true, expectedCode: "widget_rejected", expectedMessage: "bad widget query",
		},
		{
			name: "preview 500", failedPath: "/api/v5/query_range/preview", statusCode: http.StatusInternalServerError,
			expectedCode: "PREVIEW_API_ERROR", expectedMessage: "backend unavailable",
		},
		{
			name: "query 400", failedPath: "/api/v5/query_range", statusCode: http.StatusBadRequest,
			structured: true, expectedCode: "widget_rejected", expectedMessage: "bad widget query",
		},
		{
			name: "query 500", failedPath: "/api/v5/query_range", statusCode: http.StatusInternalServerError,
			expectedCode: "EXECUTION_API_ERROR", expectedMessage: "backend unavailable",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				var body struct {
					CompositeQuery struct {
						Queries []struct {
							Spec struct {
								Name string `json:"name"`
							} `json:"spec"`
						} `json:"queries"`
					} `json:"compositeQuery"`
				}
				require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
				require.Len(t, body.CompositeQuery.Queries, 1)
				name := body.CompositeQuery.Queries[0].Spec.Name
				if name == "FAIL" && request.URL.Path == test.failedPath {
					if test.structured {
						writer.Header().Set("Content-Type", "application/json")
						writer.WriteHeader(test.statusCode)
						require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{"error": map[string]any{
							"code": "widget_rejected", "message": "bad widget query",
						}}))
					} else {
						http.Error(writer, "backend unavailable", test.statusCode)
					}
					return
				}

				writer.Header().Set("Content-Type", "application/json")
				switch request.URL.Path {
				case "/api/v5/query_range/preview":
					require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{"data": map[string]any{
						"compositeQuery": map[string]any{name: map[string]any{"valid": true}},
					}}))
				case "/api/v5/query_range":
					require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{
						"status": "success",
						"data": map[string]any{"data": map[string]any{"results": []any{map[string]any{
							"queryName": name,
							"series":    []any{map[string]any{"values": []any{map[string]any{"timestamp": 1, "value": 1}}}},
						}}}},
					}))
				default:
					writer.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)
			client, err := signoz.NewClient(server.URL, "key", server.Client())
			require.NoError(t, err)

			dashboard := signoz.DashboardV5{Widgets: []signoz.Widget{
				validationWidget("/panels/0", "Rejected", "FAIL"),
				validationWidget("/panels/1", "Valid", "OK"),
			}}
			evidence := reporttypes.Report{Panels: []reporttypes.PanelRecord{
				{SourcePath: "/panels/0", Queries: []reporttypes.QueryRecord{
					{RefID: "FAIL", Validation: reporttypes.Validation{MetricChecked: true, MetricFound: true}},
					{
						RefID: "IGNORED", Disabled: true, EmittedKind: "none",
						Validation: reporttypes.Validation{MetricChecked: true},
					},
				}},
				{SourcePath: "/panels/1", Queries: []reporttypes.QueryRecord{{RefID: "OK"}}},
			}}

			valid, err := Dashboard(context.Background(), client, dashboard, &evidence, nil, Options{
				Workers: 2,
				Now:     func() time.Time { return time.Unix(1_700_000_000, 0) },
			})

			require.NoError(t, err)
			assert.False(t, valid)
			failed := evidence.Panels[0].Queries[0].Validation
			assert.Equal(t, test.expectedCode, failed.ErrorCode)
			assert.Equal(t, test.statusCode, failed.HTTPStatus)
			assert.Contains(t, failed.Error, test.expectedMessage)
			assert.Equal(t, "2023-11-14T22:13:20Z", failed.CheckedAt)
			assert.False(t, failed.Executed)
			assert.True(t, failed.MetricChecked)
			assert.True(t, failed.MetricFound)
			assert.True(t, evidence.Panels[0].Queries[1].Validation.MetricChecked)
			if test.failedPath == "/api/v5/query_range/preview" {
				assert.True(t, failed.Previewed)
				assert.False(t, failed.PreviewOK)
			} else {
				assert.True(t, failed.Previewed)
				assert.True(t, failed.PreviewOK)
			}

			passed := evidence.Panels[1].Queries[0].Validation
			assert.True(t, passed.PreviewOK)
			assert.True(t, passed.Executed)
			assert.True(t, passed.DataPresent)
			assert.Equal(t, 1, evidence.Summary.Executed)
			assert.Equal(t, 1, evidence.Summary.DataPresent)
		})
	}
}

func TestDashboardKeepsNonWidgetAPIErrorsFatal(t *testing.T) {
	t.Parallel()

	for _, statusCode := range []int{
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusMethodNotAllowed,
		http.StatusUnsupportedMediaType,
		http.StatusUpgradeRequired,
		http.StatusTooManyRequests,
	} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				writer.Header().Set("Retry-After", "0")
				writer.WriteHeader(statusCode)
				require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{"error": map[string]any{
					"code": "request_rejected", "message": http.StatusText(statusCode),
				}}))
			}))
			t.Cleanup(server.Close)
			client, err := signoz.NewClient(server.URL, "key", server.Client())
			require.NoError(t, err)

			dashboard := signoz.DashboardV5{Widgets: []signoz.Widget{validationWidget("/panels/0", "CPU", "A")}}
			evidence := reporttypes.Report{Panels: []reporttypes.PanelRecord{
				{SourcePath: "/panels/0", Queries: []reporttypes.QueryRecord{{RefID: "A"}}},
			}}

			valid, err := Dashboard(context.Background(), client, dashboard, &evidence, nil, Options{Workers: 1})

			require.Error(t, err)
			assert.False(t, valid)
			assert.Contains(t, err.Error(), fmt.Sprintf("HTTP %d", statusCode))
			assert.False(t, evidence.Panels[0].Queries[0].Validation.Previewed)
		})
	}
}

func TestDashboardRecordsWidgetAPIErrorForDisabledEmittedQuery(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{"error": map[string]any{
			"code": "widget_rejected", "message": "disabled query is invalid",
		}}))
	}))
	t.Cleanup(server.Close)
	client, err := signoz.NewClient(server.URL, "key", server.Client())
	require.NoError(t, err)

	widget := validationWidget("/panels/0", "CPU", "A")
	widget.Query.PromQL[0].Disabled = true
	dashboard := signoz.DashboardV5{Widgets: []signoz.Widget{widget}}
	evidence := reporttypes.Report{Panels: []reporttypes.PanelRecord{{
		SourcePath: "/panels/0",
		Queries:    []reporttypes.QueryRecord{{RefID: "A", Disabled: true, EmittedKind: "promql"}},
	}}}

	valid, err := Dashboard(context.Background(), client, dashboard, &evidence, nil, Options{Workers: 1})

	require.NoError(t, err)
	assert.False(t, valid)
	validation := evidence.Panels[0].Queries[0].Validation
	assert.True(t, validation.Previewed)
	assert.False(t, validation.PreviewOK)
	assert.Equal(t, "widget_rejected", validation.ErrorCode)
	assert.Contains(t, validation.Error, "disabled query is invalid")
}

func validationWidget(sourcePath, title, name string) signoz.Widget {
	return signoz.Widget{
		SourcePath: sourcePath,
		Title:      title,
		PanelTypes: "graph",
		Query: signoz.WidgetQuery{
			QueryType: "promql",
			PromQL:    []signoz.PromQLQuery{{Name: name, Query: "up"}},
		},
	}
}

func TestPreviewFailurePreservesStructuredCode(t *testing.T) {
	t.Parallel()

	code, message := previewFailure(json.RawMessage(`{"code":"invalid_query","message":"unknown metric"}`))
	assert.Equal(t, "invalid_query", code)
	assert.Equal(t, "unknown metric", message)
}

func TestApplyPreviewResultsRejectsValidResponseWithError(t *testing.T) {
	t.Parallel()

	validations := make([]reporttypes.Validation, 1)
	panel := reporttypes.PanelRecord{Queries: []reporttypes.QueryRecord{{
		RefID: "A", EmittedKind: "promql",
	}}}
	ready := applyPreviewResults(validations, panel, map[string]signoz.PreviewResult{
		"A": {Valid: true, Error: json.RawMessage(`{"code":"contradiction"}`)},
	}, time.Unix(1_700_000_000, 0))

	assert.False(t, ready)
	assert.False(t, validations[0].PreviewOK)
	assert.Equal(t, "PREVIEW_RESPONSE_INCONSISTENT", validations[0].ErrorCode)
	assert.Contains(t, validations[0].Error, "valid while also returning an error")
}

func TestDashboardAttributesRejectedFormulaDependencyToSourceQuery(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		require.Equal(t, "/api/v5/query_range/preview", request.URL.Path)
		require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{"data": map[string]any{
			"compositeQuery": map[string]any{
				"A_1": map[string]any{"valid": false, "error": map[string]any{
					"code": "invalid_dependency", "message": "metric type is incompatible",
				}},
				"A_2": map[string]any{"valid": true},
				"A":   map[string]any{"valid": true},
			},
		}}))
	}))
	t.Cleanup(server.Close)
	client, err := signoz.NewClient(server.URL, "key", server.Client())
	require.NoError(t, err)

	widget := signoz.Widget{
		SourcePath: "/panels/0", Title: "Ratio", PanelTypes: "graph",
		Query: signoz.WidgetQuery{QueryType: "builder", Builder: signoz.BuilderContainer{
			QueryData:     []signoz.BuilderQueryData{{QueryName: "A_1"}, {QueryName: "A_2"}},
			QueryFormulas: []signoz.BuilderFormula{{QueryName: "A", Expression: "A_1 / A_2"}},
		}},
	}
	evidence := reporttypes.Report{Panels: []reporttypes.PanelRecord{{
		SourcePath: "/panels/0",
		Queries: []reporttypes.QueryRecord{{
			RefID: "A", EmittedKind: "formula",
			Formula: &reporttypes.Formula{Name: "A", Queries: []reporttypes.BuilderQuery{{Name: "A_1"}, {Name: "A_2"}}},
		}},
	}}}

	valid, err := Dashboard(context.Background(), client, signoz.DashboardV5{Widgets: []signoz.Widget{widget}}, &evidence, nil, Options{Workers: 1})

	require.NoError(t, err)
	assert.False(t, valid)
	validation := evidence.Panels[0].Queries[0].Validation
	assert.True(t, validation.Previewed)
	assert.False(t, validation.PreviewOK)
	assert.Equal(t, "DEPENDENCY_invalid_dependency", validation.ErrorCode)
	assert.Contains(t, validation.Error, `emitted dependency "A_1"`)
	assert.False(t, validation.Executed)
	assert.Equal(t, 1, evidence.Summary.ValidationEligible)
	assert.Equal(t, 1, evidence.Summary.ValidationFailed)
}

func TestDataPresentPercentUsesEveryValidationEligibleQuery(t *testing.T) {
	t.Parallel()

	evidence := reporttypes.Report{Summary: reporttypes.Summary{
		ValidationEligible: 2,
		ValidationFailed:   1,
		Executed:           1,
		DataPresent:        1,
	}}

	migrationreport.RefreshSummary(&evidence)

	assert.Equal(t, 50.0, evidence.Summary.DataPresentPercent)
}
