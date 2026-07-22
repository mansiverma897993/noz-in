package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mansiverma897993/noz-in/internal/diff"
	"github.com/mansiverma897993/noz-in/internal/model"
	sourceprometheus "github.com/mansiverma897993/noz-in/internal/source/prometheus"
	"github.com/mansiverma897993/noz-in/internal/target/signoz"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeDifferentialOptionsRequiresKnownExplicitProvenance(t *testing.T) {
	t.Parallel()

	base := DifferentialOptions{
		SourceURL: "http://prometheus.example",
		TargetURL: "http://signoz.example",
	}
	strict, compareOptions, err := normalizeDifferentialOptions(base)
	require.NoError(t, err)
	assert.Empty(t, strict.TargetProvenance)
	assert.Empty(t, compareOptions.TargetProvenance)

	base.TargetProvenance = "invented_receiver"
	_, _, err = normalizeDifferentialOptions(base)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported target provenance")

	base.TargetProvenance = "  " + string(diff.TargetProvenanceOTelPrometheusReceiver) + "  "
	explicit, compareOptions, err := normalizeDifferentialOptions(base)
	require.NoError(t, err)
	assert.Equal(t, string(diff.TargetProvenanceOTelPrometheusReceiver), explicit.TargetProvenance)
	assert.Equal(t, diff.TargetProvenanceOTelPrometheusReceiver, compareOptions.TargetProvenance)
}

func TestValidateGrafanaDifferentialSkipsUnresolvedVariablesBeforeLiveRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		variable          string
		sourceOverrides   map[string]string
		targetOverrides   map[string]string
		expectedSource    []string
		expectedTarget    []string
		expectedExtraCode model.ReasonCode
		expectedSkip      string
	}{
		{
			name:              "All without explicit source expansion",
			variable:          `{"name":"job","type":"query","query":"label_values(up, job)","includeAll":true,"current":{"value":["All"]}}`,
			expectedExtraCode: model.ReasonVariableAllValue,
			expectedSkip:      "query was not emitted",
		},
		{
			name:              "feature-toggle-dependent multi current values",
			variable:          `{"name":"job","type":"query","multi":true,"current":{"value":["api's","worker"]}}`,
			expectedExtraCode: model.ReasonVariableValueEscaping,
			expectedSkip:      "query was not emitted",
		},
		{
			name:              "feature-toggle-dependent scalar escaping",
			variable:          `{"name":"job","type":"query","current":{"value":"api\"west"}}`,
			expectedExtraCode: model.ReasonVariableValueEscaping,
			expectedSkip:      "query was not emitted",
		},
		{
			name:              "scalar backslash is escaped by Grafana but raw in target",
			variable:          `{"name":"job","type":"query","current":{"value":"api\\west"}}`,
			expectedExtraCode: model.ReasonVariableValueEscaping,
			expectedSkip:      "query was not emitted",
		},
		{
			name:              "multi regex metacharacter is escaped by Grafana but raw in target",
			variable:          `{"name":"job","type":"query","multi":true,"current":{"value":["api.prod","worker"]}}`,
			expectedExtraCode: model.ReasonVariableValueEscaping,
			expectedSkip:      "query was not emitted",
		},
		{
			name:            "one-sided override does not resolve the other side",
			variable:        `{"name":"job","type":"query","multi":true,"current":{"value":["api's","worker"]}}`,
			targetOverrides: map[string]string{"job": "api"}, expectedSource: []string{"job"},
			expectedExtraCode: model.ReasonMultiVariableValue,
		},
		{
			name:            "target override does not mutate source All selection",
			variable:        `{"name":"job","type":"custom","query":"prod,stage","multi":true,"includeAll":true,"current":{"value":["All"]}}`,
			targetOverrides: map[string]string{"job": "prod"}, expectedSource: []string{"job"},
			expectedExtraCode: model.ReasonVariableAllValue,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var sourceQueries atomic.Int32
			var targetQueries atomic.Int32
			source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				sourceQueries.Add(1)
				writer.WriteHeader(http.StatusInternalServerError)
			}))
			t.Cleanup(source.Close)
			target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/api/v2/metrics/metadata":
					writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
				case "/api/v2/metrics/attributes":
					writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
				case "/api/v5/query_range":
					targetQueries.Add(1)
					writer.WriteHeader(http.StatusInternalServerError)
				default:
					writer.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(target.Close)

			path := filepath.Join(t.TempDir(), "variables.json")
			dashboard := `{"title":"Variables","templating":{"list":[` + test.variable + `]},"panels":[{"title":"Availability","type":"timeseries","targets":[{"refId":"A","expr":"sum(up{job=~\"$job\"})"}]}]}`
			require.NoError(t, os.WriteFile(path, []byte(dashboard), 0o600))

			report, err := ValidateGrafanaDifferential(context.Background(), path, DifferentialOptions{
				SourceURL: source.URL, TargetURL: target.URL, TargetAPIKey: "key", HTTPClient: source.Client(),
				SourceVariables: test.sourceOverrides, TargetVariables: test.targetOverrides,
				Now: time.Unix(180, 0), Range: 2 * time.Minute, Step: time.Minute, Workers: 1,
			})

			require.NoError(t, err)
			require.Len(t, report.Comparisons, 1)
			comparison := report.Comparisons[0]
			assert.Equal(t, diff.StatusSkipped, comparison.Stats.Status)
			expectedSkip := test.expectedSkip
			if expectedSkip == "" {
				expectedSkip = "missing dashboard variable value"
			}
			assert.Equal(t, expectedSkip, comparison.SkippedReason)
			assert.Equal(t, model.VerdictNeedsReview, comparison.Verdict)
			assert.ElementsMatch(t, test.expectedSource, comparison.MissingSource)
			assert.ElementsMatch(t, test.expectedTarget, comparison.MissingTarget)
			assert.Contains(t, comparison.Reasons, model.ReasonMissingVariableValue)
			assert.Contains(t, comparison.Reasons, test.expectedExtraCode)
			assert.Zero(t, sourceQueries.Load())
			assert.Zero(t, targetQueries.Load())
			assert.Empty(t, comparison.TargetArtifact, "no target request artifact should exist for an unresolved side")
		})
	}
}

func TestValidateGrafanaDifferentialUsesExplicitSourceAllAndTargetAllSentinel(t *testing.T) {
	t.Parallel()

	var sourceExpression string
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		sourceExpression = request.URL.Query().Get("query")
		writeJSONResponse(t, writer, map[string]any{
			"status": "success", "data": map[string]any{"resultType": "matrix", "result": []any{map[string]any{
				"metric": map[string]any{}, "values": []any{[]any{60.0, "1"}, []any{120.0, "2"}},
			}}},
		})
	}))
	t.Cleanup(source.Close)
	var targetRequest signoz.QueryRangeRequest
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v5/query_range":
			require.NoError(t, json.NewDecoder(request.Body).Decode(&targetRequest))
			writeJSONResponse(t, writer, map[string]any{
				"status": "success", "data": map[string]any{"data": map[string]any{"results": []any{map[string]any{
					"queryName": "A", "series": []any{map[string]any{"values": []any{
						map[string]any{"timestamp": 60_000, "value": 1}, map[string]any{"timestamp": 120_000, "value": 2},
					}}},
				}}}},
			})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(target.Close)

	path := filepath.Join(t.TempDir(), "all.json")
	dashboard := `{"title":"All","templating":{"list":[{"name":"job","type":"query","query":"label_values(up, job)","includeAll":true,"allValue":".*","current":{"value":["All"]}}]},"panels":[{"title":"Availability","type":"timeseries","targets":[{"refId":"A","expr":"sum(up{job=~\"$job\"})"}]}]}`
	require.NoError(t, os.WriteFile(path, []byte(dashboard), 0o600))

	report, err := ValidateGrafanaDifferential(context.Background(), path, DifferentialOptions{
		SourceURL: source.URL, TargetURL: target.URL, TargetAPIKey: "key", HTTPClient: source.Client(),
		Now: time.Unix(180, 0), Range: 2 * time.Minute, Step: time.Minute,
		MinimumCoverage: 1, MinimumMatchedPoints: 1, Workers: 1,
	})

	require.NoError(t, err)
	require.Len(t, report.Comparisons, 1)
	assert.Equal(t, `sum(up{job=~".*"})`, sourceExpression)
	assert.Equal(t, "__all__", targetRequest.Variables["job"].Value)
	assert.Equal(t, diff.StatusEquivalent, report.Comparisons[0].Stats.Status)
}

func TestValidateGrafanaDifferentialUsesExactVariableSelectionsOnBothSides(t *testing.T) {
	t.Parallel()

	var sourceExpression string
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		sourceExpression = request.URL.Query().Get("query")
		writeJSONResponse(t, writer, map[string]any{
			"status": "success", "data": map[string]any{"resultType": "matrix", "result": []any{map[string]any{
				"metric": map[string]any{}, "values": []any{[]any{60.0, "1"}, []any{120.0, "2"}},
			}}},
		})
	}))
	t.Cleanup(source.Close)

	var targetRequest signoz.QueryRangeRequest
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v5/query_range":
			require.NoError(t, json.NewDecoder(request.Body).Decode(&targetRequest))
			writeJSONResponse(t, writer, map[string]any{
				"status": "success", "data": map[string]any{"data": map[string]any{"results": []any{map[string]any{
					"queryName": "A", "series": []any{map[string]any{"values": []any{
						map[string]any{"timestamp": 60_000, "value": 1}, map[string]any{"timestamp": 120_000, "value": 2},
					}}},
				}}}},
			})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(target.Close)

	tests := []struct {
		name            string
		variable        string
		reference       string
		sourceOverrides map[string]string
		targetOverrides map[string]string
		wantSource      string
		wantTarget      any
	}{
		{
			name: "safe multi selection",
			variable: `{
				"name":"job","type":"query","query":"label_values(up, job)",
				"multi":true,"current":{"value":["api","worker"]}
			}`,
			wantSource: `sum(up{job=~"(api|worker)"})`,
			wantTarget: []any{"api", "worker"},
		},
		{
			name: "explicit raw pipe preserves regex bytes",
			variable: `{
				"name":"job","type":"query","query":"label_values(up, job)",
				"multi":true,"current":{"value":["api.prod","worker|canary"]}
			}`,
			reference:  `${job:pipe}`,
			wantSource: `sum(up{job=~"api.prod|worker|canary"})`,
			wantTarget: []any{"api.prod", "worker|canary"},
		},
		{
			name: "one-element target multi override",
			variable: `{
				"name":"job","type":"query","query":"label_values(up, job)",
				"multi":true,"current":{"value":["api","worker"]}
			}`,
			targetOverrides: map[string]string{"job": "api"},
			wantSource:      `sum(up{job=~"(api|worker)"})`,
			wantTarget:      []any{"api"},
		},
		{
			name: "target override resolves non-dynamic All before translation",
			variable: `{
				"name":"job","type":"custom","query":"prod,stage",
				"multi":true,"includeAll":true,"current":{"value":["All"]}
			}`,
			sourceOverrides: map[string]string{"job": "prod|stage"},
			targetOverrides: map[string]string{"job": "prod"},
			wantSource:      `sum(up{job=~"prod|stage"})`,
			wantTarget:      []any{"prod"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sourceExpression = ""
			targetRequest = signoz.QueryRangeRequest{}
			path := filepath.Join(t.TempDir(), "variables.json")
			reference := test.reference
			if reference == "" {
				reference = "$job"
			}
			dashboard := `{
				"title":"Variables","templating":{"list":[` + test.variable + `]},
				"panels":[{"title":"Availability","type":"timeseries","targets":[{
					"refId":"A","expr":"sum(up{job=~\"` + reference + `\"})"
				}]}]
			}`
			require.NoError(t, os.WriteFile(path, []byte(dashboard), 0o600))

			report, err := ValidateGrafanaDifferential(context.Background(), path, DifferentialOptions{
				SourceURL: source.URL, TargetURL: target.URL, TargetAPIKey: "key", HTTPClient: source.Client(),
				SourceVariables: test.sourceOverrides, TargetVariables: test.targetOverrides,
				Now: time.Unix(180, 0), Range: 2 * time.Minute, Step: time.Minute,
				MinimumCoverage: 1, MinimumMatchedPoints: 1, Workers: 1,
			})

			require.NoError(t, err)
			require.Len(t, report.Comparisons, 1)
			assert.Equal(t, test.wantSource, sourceExpression)
			assert.Equal(t, test.wantTarget, targetRequest.Variables["job"].Value)
			assert.Equal(t, diff.StatusEquivalent, report.Comparisons[0].Stats.Status)
			assert.Empty(t, report.Comparisons[0].MissingSource)
			assert.Empty(t, report.Comparisons[0].MissingTarget)
		})
	}
}

func TestValidateGrafanaDifferentialDoesNotAliasTargetAllSentinel(t *testing.T) {
	t.Parallel()

	var sourceExpression string
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		sourceExpression = request.URL.Query().Get("query")
		writeJSONResponse(t, writer, map[string]any{
			"status": "success",
			"data": map[string]any{"resultType": "matrix", "result": []any{map[string]any{
				"metric": map[string]any{"cluster": "prod"},
				"values": []any{[]any{60.0, "1"}, []any{120.0, "2"}},
			}}},
		})
	}))
	t.Cleanup(source.Close)

	var targetRequest signoz.QueryRangeRequest
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{
				map[string]any{"key": "cluster"},
			}}})
		case "/api/v5/query_range":
			require.NoError(t, json.NewDecoder(request.Body).Decode(&targetRequest))
			writeJSONResponse(t, writer, map[string]any{
				"status": "success",
				"data": map[string]any{"data": map[string]any{"results": []any{map[string]any{
					"queryName": "A",
					"series": []any{map[string]any{
						"labels": []any{map[string]any{
							"key": map[string]any{"name": "cluster"}, "value": "__all__",
						}},
						"values": []any{
							map[string]any{"timestamp": 60_000, "value": 1},
							map[string]any{"timestamp": 120_000, "value": 2},
						},
					}},
				}}}},
			})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(target.Close)

	path := filepath.Join(t.TempDir(), "custom-all.json")
	dashboard := `{
		"title":"Custom All alias",
		"templating":{"list":[{
			"name":"clusterVar","type":"query","query":"label_values(up, cluster)",
			"includeAll":true,"allValue":".*","current":{"value":["All"]}
		}]},
		"panels":[{"title":"Availability","type":"timeseries","targets":[{
			"refId":"A","expr":"up{cluster=~\"$clusterVar\"} offset 1m"
		}]}]
	}`
	require.NoError(t, os.WriteFile(path, []byte(dashboard), 0o600))

	report, err := ValidateGrafanaDifferential(context.Background(), path, DifferentialOptions{
		SourceURL: source.URL, TargetURL: target.URL, TargetAPIKey: "key", HTTPClient: source.Client(),
		Now: time.Unix(180, 0), Range: 2 * time.Minute, Step: time.Minute,
		MinimumCoverage: 1, MinimumMatchedPoints: 1, Workers: 1,
	})

	require.NoError(t, err)
	require.Len(t, report.Comparisons, 1)
	assert.Equal(t, `up{cluster=~".*"} offset 1m`, sourceExpression)
	assert.Equal(t, "__all__", targetRequest.Variables["clusterVar"].Value)
	assert.Empty(t, report.Comparisons[0].LabelValueAliases)
	assert.Empty(t, report.Comparisons[0].LabelValueAliasBindings)
	assert.Equal(t, diff.StatusNoSeriesMatch, report.Comparisons[0].Stats.Status)
}

func TestValidateGrafanaDifferentialScopesLabelAliasesToTheExactQuery(t *testing.T) {
	t.Parallel()

	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, writer, map[string]any{
			"status": "success",
			"data": map[string]any{"resultType": "matrix", "result": []any{map[string]any{
				"metric": map[string]any{"cluster": "prod", "environment": "test", "job": "api"},
				"values": []any{[]any{120.0, "1"}, []any{180.0, "2"}},
			}}},
		})
	}))
	t.Cleanup(source.Close)

	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{
				map[string]any{"key": "cluster"},
				map[string]any{"key": "environment"},
				map[string]any{"key": "job"},
			}}})
		case "/api/v5/query_range":
			writeJSONResponse(t, writer, map[string]any{
				"status": "success",
				"data": map[string]any{"data": map[string]any{"results": []any{map[string]any{
					"queryName": "A",
					"series": []any{map[string]any{
						"labels": []any{
							map[string]any{"key": map[string]any{"name": "cluster"}, "value": "production"},
							map[string]any{"key": map[string]any{"name": "environment"}, "value": "testing"},
							map[string]any{"key": map[string]any{"name": "job"}, "value": "api"},
						},
						"values": []any{
							map[string]any{"timestamp": 120_000, "value": 1.0},
							map[string]any{"timestamp": 180_000, "value": 2.0},
						},
					}},
				}}}},
			})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(target.Close)

	path := filepath.Join(t.TempDir(), "scoped-aliases.json")
	dashboard := `{
		"title":"Scoped aliases",
		"templating":{"list":[
			{"name":"clusterVar","type":"query","query":"label_values(up, cluster)","current":{"value":["prod"]}},
			{"name":"environmentVar","type":"query","query":"label_values(up, environment)","current":{"value":["test"]}}
		]},
		"panels":[
			{"title":"Uses variable","type":"timeseries","targets":[{"refId":"A","expr":"up{cluster=\"$clusterVar\",environment=\"$environmentVar\"} offset 1m"}]},
			{"title":"Variable on another label","type":"timeseries","targets":[{"refId":"A","expr":"up{environment=\"$clusterVar\"} offset 1m"}]},
			{"title":"Unrelated","type":"timeseries","targets":[{"refId":"A","expr":"up{job=\"api\"} offset 1m"}]}
		]
	}`
	require.NoError(t, os.WriteFile(path, []byte(dashboard), 0o600))
	migrated, err := MigrateGrafana(context.Background(), []string{path}, GrafanaOptions{
		OutputDirectory: t.TempDir(),
		RateInterval:    5 * time.Minute,
		Interval:        time.Minute,
		Range:           2 * time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, migrated, 1)

	report, err := ValidateGrafanaDifferential(context.Background(), path, DifferentialOptions{
		SourceURL: source.URL, TargetURL: target.URL, TargetAPIKey: "key", HTTPClient: source.Client(),
		SourceVariables: map[string]string{"clusterVar": "prod", "environmentVar": "test"},
		TargetVariables: map[string]string{"clusterVar": "production", "environmentVar": "testing"},
		Now:             time.Unix(180, 0), Range: 2 * time.Minute, Step: time.Minute,
		MinimumCoverage: 1, MinimumMatchedPoints: 1, Workers: 1,
		MigrationReportPath: migrated[0].ReportPath,
	})

	require.NoError(t, err)
	require.Len(t, report.Comparisons, 3)
	byPanel := make(map[string]DifferentialQuery, len(report.Comparisons))
	for _, comparison := range report.Comparisons {
		byPanel[comparison.PanelTitle] = comparison
	}
	assert.Equal(t, diff.StatusEquivalent, byPanel["Uses variable"].Stats.Status)
	assert.Equal(t, map[string]map[string]string{
		"cluster":     {"production": "prod"},
		"environment": {"testing": "test"},
	}, byPanel["Uses variable"].LabelValueAliases)
	assert.ElementsMatch(t, []DifferentialLabelValueAliasBinding{
		{
			VariableName: "clusterVar",
			SourceLabel:  "cluster",
			TargetLabel:  "cluster",
			SourceValue:  "prod",
			TargetValue:  "production",
		},
		{
			VariableName: "environmentVar",
			SourceLabel:  "environment",
			TargetLabel:  "environment",
			SourceValue:  "test",
			TargetValue:  "testing",
		},
	}, byPanel["Uses variable"].LabelValueAliasBindings)
	assert.Equal(t, diff.StatusNoSeriesMatch, byPanel["Variable on another label"].Stats.Status)
	assert.Empty(t, byPanel["Variable on another label"].LabelValueAliases)
	assert.Equal(t, diff.StatusNoSeriesMatch, byPanel["Unrelated"].Stats.Status)
	assert.Empty(t, byPanel["Unrelated"].LabelValueAliases)

	cloneReport := func() DifferentialReport {
		encoded, cloneErr := json.Marshal(report)
		require.NoError(t, cloneErr)
		var clone DifferentialReport
		require.NoError(t, json.Unmarshal(encoded, &clone))
		return clone
	}
	aliasComparison := func(candidate *DifferentialReport) *DifferentialQuery {
		for index := range candidate.Comparisons {
			if candidate.Comparisons[index].PanelTitle == "Uses variable" {
				return &candidate.Comparisons[index]
			}
		}
		require.FailNow(t, "alias comparison is missing")
		return nil
	}

	tampered := cloneReport()
	for index := range tampered.Comparisons {
		if tampered.Comparisons[index].PanelTitle == "Uses variable" {
			tampered.Comparisons[index].LabelValueAliases = map[string]map[string]string{
				"cluster": {"staging": "development"},
			}
		}
	}
	err = AttachDifferentialEvidence(migrated[0].ReportPath, tampered)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "do not match their exact persisted bindings")

	tampered = cloneReport()
	comparison := aliasComparison(&tampered)
	comparison.LabelValueAliasBindings[0].SourceValue = "staging"
	comparison.LabelValueAliases["cluster"] = map[string]string{"production": "staging"}
	err = AttachDifferentialEvidence(migrated[0].ReportPath, tampered)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exact materialized source expression does not match")

	tampered = cloneReport()
	comparison = aliasComparison(&tampered)
	var request signoz.QueryRangeRequest
	require.NoError(t, json.Unmarshal(comparison.TargetArtifact, &request))
	variable := request.Variables["clusterVar"]
	variable.Value = "staging"
	request.Variables["clusterVar"] = variable
	comparison.TargetArtifact, err = json.Marshal(request)
	require.NoError(t, err)
	comparison.TargetArtifactSHA256, err = canonicalJSONSHA256(comparison.TargetArtifact)
	require.NoError(t, err)
	err = AttachDifferentialEvidence(migrated[0].ReportPath, tampered)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match the exact target request variable")

	tampered = cloneReport()
	comparison = aliasComparison(&tampered)
	comparison.LabelValueAliasBindings[0].SourceLabel = "region"
	comparison.LabelValueAliasBindings[0].TargetLabel = "region"
	delete(comparison.LabelValueAliases, "cluster")
	comparison.LabelValueAliases["region"] = map[string]string{"production": "prod"}
	err = AttachDifferentialEvidence(migrated[0].ReportPath, tampered)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match its migration variable evidence")

	require.NoError(t, AttachDifferentialEvidence(migrated[0].ReportPath, report))
}

func TestDifferentialAliasBindingsRequireExactFullSourceMaterialization(t *testing.T) {
	t.Parallel()

	request := signoz.QueryRangeRequest{
		SchemaVersion: "v1",
		RequestType:   "time_series",
		Variables: map[string]signoz.VariableItem{
			"clusterVar": {Type: "dynamic", Value: "development"},
		},
		CompositeQuery: signoz.CompositeQuery{Queries: []signoz.QueryEnvelope{{
			Type: "promql",
			Spec: signoz.PromQLSpec{
				Name: "A", Query: `up{cluster="$clusterVar"} or up{cluster="prod"}`,
			},
		}}},
	}
	artifact, err := json.Marshal(request)
	require.NoError(t, err)
	evidence := reporttypes.Report{Run: reporttypes.Run{Flags: map[string]any{
		"rateInterval": "5m", "intervalDefault": "1m", "range": "1h",
	}}}
	query := reporttypes.QueryRecord{
		RefID: "A", SourcePath: "/panels/0/targets/0",
		Original: `up{cluster="$clusterVar"} or up{cluster="prod"}`,
	}
	comparison := DifferentialQuery{
		SourcePath:       query.SourcePath,
		SourceExpression: `up{cluster="dev"} or up{cluster="prod"}`,
		TargetKind:       string(diff.TargetKindPromQL),
		TargetQueryName:  "A",
		TargetExpression: `up{cluster="$clusterVar"} or up{cluster="prod"}`,
		TargetArtifact:   artifact,
		LabelValueAliases: map[string]map[string]string{
			"cluster": {"development": "prod"},
		},
		LabelValueAliasBindings: []DifferentialLabelValueAliasBinding{{
			VariableName: "clusterVar",
			SourceLabel:  "cluster",
			TargetLabel:  "cluster",
			SourceValue:  "prod",
			TargetValue:  "development",
		}},
	}

	err = validateDifferentialLabelValueAliasBindings(
		evidence, signoz.DashboardV5{}, query, comparison,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exact materialized source expression does not match")
}

func TestDifferentialAliasBindingRejectsTargetAllSentinel(t *testing.T) {
	t.Parallel()

	err := validateDifferentialLabelValueAliasBindingShape(DifferentialLabelValueAliasBinding{
		VariableName: "clusterVar",
		SourceLabel:  "cluster",
		TargetLabel:  "cluster",
		SourceValue:  "prod",
		TargetValue:  "__all__",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "matcher-removal syntax")
	err = validateDifferentialLabelValueAliases(map[string]map[string]string{
		"cluster": {"__all__": "prod"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "matcher-removal syntax")
}

func TestPromQLAliasProofRequiresSameMatcherAndSurvivingLabel(t *testing.T) {
	t.Parallel()

	const sentinel = "__promcast_alias_probe_test__"
	for _, test := range []struct {
		name       string
		expression string
		value      string
		want       bool
	}{
		{name: "direct equality", expression: `up{cluster="` + sentinel + `"}`, value: "prod", want: true},
		{name: "literal regex", expression: `sum by (cluster) (rate(up{cluster=~"` + sentinel + `"}[5m]))`, value: "prod", want: true},
		{name: "selection aggregate", expression: `topk(1, up{cluster="` + sentinel + `"})`, value: "prod", want: true},
		{name: "wrong matcher label", expression: `up{environment="` + sentinel + `"}`, value: "prod"},
		{name: "aggregation drops label", expression: `sum(up{cluster="` + sentinel + `"})`, value: "prod"},
		{name: "label rewrite is not proven", expression: `label_replace(up{cluster="` + sentinel + `"}, "cluster", "fixed", "job", ".+")`, value: "prod"},
		{name: "regex override selects several values", expression: `up{cluster=~"` + sentinel + `"}`, value: "prod|stage"},
		{name: "embedded matcher value", expression: `up{cluster="prefix-` + sentinel + `"}`, value: "prod"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, test.want, promQLAliasLabelProof(test.expression, "cluster", sentinel, test.value))
		})
	}
}

func TestBuilderAliasProofRequiresExactFilterAndGroupByProvenance(t *testing.T) {
	t.Parallel()

	const sentinel = "__promcast_alias_probe_test__"
	base := signoz.Widget{Query: signoz.WidgetQuery{Builder: signoz.BuilderContainer{QueryData: []signoz.BuilderQueryData{{
		QueryName: "A",
		Filter:    signoz.Expression{Expression: "cluster = '$clusterVar'"},
		GroupBy:   []signoz.DashboardGroupBy{{Key: "cluster"}},
	}}}}}
	values := map[string]string{"clusterVar": sentinel}
	assert.True(t, builderAliasLabelProof(base, "A", "cluster", sentinel, "production", values))

	withoutGroup := base
	withoutGroup.Query.Builder.QueryData = append([]signoz.BuilderQueryData(nil), base.Query.Builder.QueryData...)
	withoutGroup.Query.Builder.QueryData[0].GroupBy = nil
	assert.False(t, builderAliasLabelProof(withoutGroup, "A", "cluster", sentinel, "production", values))

	wrongMatcher := base
	wrongMatcher.Query.Builder.QueryData = append([]signoz.BuilderQueryData(nil), base.Query.Builder.QueryData...)
	wrongMatcher.Query.Builder.QueryData[0].Filter.Expression = "environment = '$clusterVar'"
	assert.False(t, builderAliasLabelProof(wrongMatcher, "A", "cluster", sentinel, "production", values))

	usedElsewhere := base
	usedElsewhere.Query.Builder.QueryData = append([]signoz.BuilderQueryData(nil), base.Query.Builder.QueryData...)
	usedElsewhere.Query.Builder.QueryData[0].Legend = "$clusterVar"
	assert.False(t, builderAliasLabelProof(usedElsewhere, "A", "cluster", sentinel, "production", values))
}

func TestValidateGrafanaDifferentialExecutesCanonicalPromQLForBuilderCandidates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		expression string
	}{
		{name: "builder candidate", expression: "sum(up)"},
		{name: "formula candidate", expression: "sum(up) / sum(process_start_time_seconds)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writeJSONResponse(t, writer, map[string]any{
					"status": "success",
					"data": map[string]any{"resultType": "matrix", "result": []any{map[string]any{
						"metric": map[string]any{},
						"values": []any{[]any{120.0, "1"}, []any{180.0, "2"}},
					}}},
				})
			}))
			t.Cleanup(source.Close)

			var requestTypes []string
			target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/api/v2/metrics/metadata":
					writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
					return
				case "/api/v2/metrics/attributes":
					assert.Equal(t, "60999", request.URL.Query().Get("start"))
					assert.Equal(t, "180999", request.URL.Query().Get("end"))
					writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
					return
				}
				require.Equal(t, "/api/v5/query_range", request.URL.Path)
				var body struct {
					Start          uint64 `json:"start"`
					End            uint64 `json:"end"`
					CompositeQuery struct {
						Queries []struct {
							Type string `json:"type"`
						} `json:"queries"`
					} `json:"compositeQuery"`
				}
				require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
				assert.Equal(t, uint64(60_000), body.Start)
				assert.Equal(t, uint64(180_000), body.End)
				requestTypes = requestTypes[:0]
				for _, query := range body.CompositeQuery.Queries {
					requestTypes = append(requestTypes, query.Type)
				}
				response := map[string]any{
					"status": "success",
					"data": map[string]any{
						"data": map[string]any{
							"results": []any{
								map[string]any{
									"queryName": "A",
									"aggregations": []any{
										map[string]any{"series": []any{
											map[string]any{
												"labels": []any{},
												"values": []any{
													map[string]any{"timestamp": 60_000, "value": 1.0},
													map[string]any{"timestamp": 120_000, "value": 2.0},
												},
											},
										}},
									},
								},
							},
						},
					},
				}
				writeJSONResponse(t, writer, response)
			}))
			t.Cleanup(target.Close)

			path := filepath.Join(t.TempDir(), "candidate.json")
			dashboard := fmt.Sprintf(`{"title":"Candidate","panels":[{"title":"Value","type":"timeseries","targets":[{"refId":"A","expr":%q}]}]}`, test.expression)
			require.NoError(t, os.WriteFile(path, []byte(dashboard), 0o600))

			report, err := ValidateGrafanaDifferential(context.Background(), path, DifferentialOptions{
				SourceURL: source.URL, TargetURL: target.URL, TargetAPIKey: "test-key",
				HTTPClient: source.Client(), Now: time.UnixMilli(180_999), Range: 2 * time.Minute,
				Step: time.Minute, MinimumCoverage: 1, MinimumMatchedPoints: 1, Workers: 1,
			})
			require.NoError(t, err)
			require.Len(t, report.Comparisons, 1)
			comparison := report.Comparisons[0]
			assert.Equal(t, "promql", comparison.TargetKind)
			assert.Equal(t, model.VerdictNeedsReview, comparison.Verdict)
			assert.Equal(t, time.UnixMilli(60_000), report.Window.Start)
			assert.Equal(t, time.UnixMilli(180_000), report.Window.End)
			assert.Equal(t, 1, report.Tolerances.MinimumMatchedPoints)
			assert.Equal(t, []string{"promql"}, requestTypes)
			assert.NotEmpty(t, comparison.TargetArtifact)
			require.NotNil(t, comparison.Window)
			assert.Equal(t, report.Window, *comparison.Window)
			var artifactRequest signoz.QueryRangeRequest
			require.NoError(t, json.Unmarshal(comparison.TargetArtifact, &artifactRequest))
			assert.Equal(t, uint64(comparison.Window.Start.UnixMilli()), artifactRequest.Start)
			assert.Equal(t, uint64(comparison.Window.End.UnixMilli()), artifactRequest.End)
			assert.False(t, artifactRequest.NoCache)
			require.NotNil(t, artifactRequest.FormatOptions)
			digest := sha256.Sum256(comparison.TargetArtifact)
			assert.Equal(t, fmt.Sprintf("%x", digest[:]), comparison.TargetArtifactSHA256)
			assert.Equal(t, diff.StatusEquivalent, comparison.Stats.Status)
		})
	}
}

func TestExecuteDifferentialTaskUsesTargetKindTimestampSemantics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		targetKind      diff.TargetKind
		targetTimestamp int64
	}{
		{name: "builder query shifts one step", targetKind: diff.TargetKindBuilderQuery, targetTimestamp: 0},
		{name: "builder formula shifts one step", targetKind: diff.TargetKindBuilderFormula, targetTimestamp: 0},
		{name: "promql keeps timestamp", targetKind: diff.TargetKindPromQL, targetTimestamp: 60_000},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sourceServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writeJSONResponse(t, writer, map[string]any{
					"status": "success",
					"data": map[string]any{"resultType": "matrix", "result": []any{map[string]any{
						"metric": map[string]any{}, "values": []any{[]any{60.0, "1"}},
					}}},
				})
			}))
			t.Cleanup(sourceServer.Close)
			targetServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writeJSONResponse(t, writer, map[string]any{
					"status": "success",
					"data": map[string]any{"data": map[string]any{"results": []any{map[string]any{
						"queryName": "A",
						"series": []any{map[string]any{
							"labels": []any{},
							"values": []any{map[string]any{"timestamp": test.targetTimestamp, "value": 1}},
						}},
					}}}},
				})
			}))
			t.Cleanup(targetServer.Close)

			sourceClient, err := sourceprometheus.NewClient(sourceServer.URL, "", sourceServer.Client())
			require.NoError(t, err)
			targetClient, err := signoz.NewClient(targetServer.URL, "key", targetServer.Client())
			require.NoError(t, err)
			record := executeDifferentialTask(
				context.Background(),
				DifferentialQuery{},
				differentialTask{
					sourceExpression: "up",
					targetRequest:    signoz.QueryRangeRequest{},
					targetQueryName:  "A",
					targetKind:       test.targetKind,
					step:             time.Minute,
					window:           DifferentialWindow{Start: time.Unix(0, 0), End: time.Unix(120, 0), StepMillis: 60_000},
				},
				sourceClient,
				targetClient,
				diff.Options{TimestampTolerance: time.Millisecond, MinimumCoverage: 1, MinimumMatchedPoints: 1},
			)
			assert.Equal(t, diff.StatusEquivalent, record.Stats.Status)
		})
	}
}

func TestAlignedDifferentialWindowUsesEpochStepBoundaries(t *testing.T) {
	t.Parallel()

	location := time.FixedZone("test", 5*60*60+30*60)
	window := alignedDifferentialWindow(time.UnixMilli(180_999).In(location), 2*time.Minute, time.Minute)
	assert.Equal(t, int64(60_000), window.Start.UnixMilli())
	assert.Equal(t, int64(180_000), window.End.UnixMilli())
	assert.Equal(t, int64(60_000), window.StepMillis)
	assert.Equal(t, location, window.Start.Location())
}

func TestValidateGrafanaDifferentialUsesPerQueryAlignedWindows(t *testing.T) {
	t.Parallel()

	type sourceRequest struct {
		startMillis int64
		endMillis   int64
		stepMillis  int64
	}
	sourceRequests := make(map[int64]sourceRequest)
	sourceServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		startSeconds, err := strconv.ParseFloat(request.URL.Query().Get("start"), 64)
		require.NoError(t, err)
		endSeconds, err := strconv.ParseFloat(request.URL.Query().Get("end"), 64)
		require.NoError(t, err)
		stepSeconds, err := strconv.ParseFloat(request.URL.Query().Get("step"), 64)
		require.NoError(t, err)
		stepMillis := int64(stepSeconds * 1000)
		sourceRequests[stepMillis] = sourceRequest{
			startMillis: int64(startSeconds * 1000),
			endMillis:   int64(endSeconds * 1000),
			stepMillis:  stepMillis,
		}
		timestamp := startSeconds
		if stepMillis == int64((30 * time.Minute).Milliseconds()) {
			timestamp += stepSeconds
		}
		writeJSONResponse(t, writer, map[string]any{
			"status": "success",
			"data": map[string]any{"resultType": "matrix", "result": []any{map[string]any{
				"metric": map[string]any{}, "values": []any{[]any{timestamp, "1"}},
			}}},
		})
	}))
	t.Cleanup(sourceServer.Close)

	targetQueryRequests := 0
	targetServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
			return
		case "/api/v2/metrics/attributes":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
			return
		}
		var captured signoz.QueryRangeRequest
		require.NoError(t, json.NewDecoder(request.Body).Decode(&captured))
		require.NotEmpty(t, captured.CompositeQuery.Queries)
		targetQueryRequests++
		writeJSONResponse(t, writer, map[string]any{
			"status": "success",
			"data": map[string]any{"data": map[string]any{"results": []any{map[string]any{
				"queryName": "A",
				"series": []any{map[string]any{
					"labels": []any{},
					"values": []any{map[string]any{"timestamp": int64(captured.Start), "value": 1}},
				}},
			}}}},
		})
	}))
	t.Cleanup(targetServer.Close)

	path := filepath.Join(t.TempDir(), "heterogeneous.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"title":"Heterogeneous steps",
		"panels":[
			{"title":"Builder","type":"timeseries","targets":[{"refId":"A","expr":"sum(up)"}]},
			{"title":"PromQL","type":"timeseries","targets":[{"refId":"A","expr":"sum(up) ^ 2"}]}
		]
	}`), 0o600))

	report, err := ValidateGrafanaDifferential(context.Background(), path, DifferentialOptions{
		SourceURL: sourceServer.URL, TargetURL: targetServer.URL, TargetAPIKey: "key",
		HTTPClient: sourceServer.Client(), Interval: 30 * time.Minute, Range: 2 * time.Hour,
		Step: time.Minute, TimestampTolerance: time.Second, MinimumCoverage: 1, MinimumMatchedPoints: 1,
		Now: time.Unix(10_901, 0), Workers: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, report.Summary.Equivalent)
	assert.Equal(t, 2, targetQueryRequests)
	for _, comparison := range report.Comparisons {
		assert.Equal(t, "promql", comparison.TargetKind)
		require.NotNil(t, comparison.Window)
		assert.Equal(t, int64(3_660_000), comparison.Window.Start.UnixMilli())
		assert.Equal(t, int64(10_860_000), comparison.Window.End.UnixMilli())
		assert.Equal(t, int64(time.Minute.Milliseconds()), comparison.Window.StepMillis)
		assert.Equal(t, int64(time.Minute.Milliseconds()), comparison.EvaluationStepMillis)

		var artifactRequest signoz.QueryRangeRequest
		require.NoError(t, json.Unmarshal(comparison.TargetArtifact, &artifactRequest))
		assert.Equal(t, uint64(3_660_000), artifactRequest.Start)
		assert.Equal(t, uint64(10_860_000), artifactRequest.End)
		assert.False(t, artifactRequest.NoCache)
		require.NotNil(t, artifactRequest.FormatOptions)
		require.Len(t, artifactRequest.CompositeQuery.Queries, 1)
		assert.Equal(t, "promql", artifactRequest.CompositeQuery.Queries[0].Type)
		digest := sha256.Sum256(comparison.TargetArtifact)
		assert.Equal(t, fmt.Sprintf("%x", digest[:]), comparison.TargetArtifactSHA256)
	}
	capturedSource, found := sourceRequests[int64(time.Minute.Milliseconds())]
	require.True(t, found)
	assert.Equal(t, sourceRequest{
		startMillis: 3_660_000,
		endMillis:   10_860_000,
		stepMillis:  int64(time.Minute.Milliseconds()),
	}, capturedSource)
}

func TestValidateGrafanaDifferentialComparesLiveResults(t *testing.T) {
	t.Parallel()

	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, writer, map[string]any{
			"status": "success",
			"data": map[string]any{"resultType": "matrix", "result": []any{map[string]any{
				"metric": map[string]any{"job": "api", "instance": "source:9100"},
				"values": []any{[]any{120.0, "1"}, []any{180.0, "2"}},
			}}},
		})
	}))
	t.Cleanup(source.Close)
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "test-key", request.Header.Get("SIGNOZ-API-KEY"))
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
			return
		case "/api/v2/metrics/attributes":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{
				map[string]any{"key": "service.name"},
				map[string]any{"key": "service.instance.id"},
			}}})
			return
		}
		assert.Equal(t, "/api/v5/query_range", request.URL.Path)
		writeJSONResponse(t, writer, map[string]any{
			"status": "success",
			"data": map[string]any{
				"data": map[string]any{
					"results": []any{map[string]any{
						"queryName": "A",
						"aggregations": []any{map[string]any{
							"series": []any{map[string]any{
								"labels": []any{
									map[string]any{"key": map[string]any{"name": "service.name"}, "value": "api"},
									map[string]any{"key": map[string]any{"name": "service.instance.id"}, "value": "source:9100"},
									map[string]any{"key": map[string]any{"name": "server.address"}, "value": "source-node"},
								},
								"values": []any{
									map[string]any{"timestamp": 120_000, "value": 1.0},
									map[string]any{"timestamp": 180_000, "value": 2.0},
								},
							}},
						}},
					}},
				},
			},
		})
	}))
	t.Cleanup(target.Close)

	options := DifferentialOptions{
		SourceURL:            source.URL,
		TargetURL:            target.URL,
		TargetAPIKey:         "test-key",
		HTTPClient:           source.Client(),
		SourceVariables:      map[string]string{"job": "api", "node": "source:9100"},
		TargetVariables:      map[string]string{"job": "api", "node": "source-node"},
		Now:                  time.Unix(180, 0),
		Range:                2 * time.Minute,
		Step:                 time.Minute,
		MinimumCoverage:      1,
		MinimumMatchedPoints: 1,
		MaxQueries:           1,
	}

	strictReport, err := ValidateGrafanaDifferential(
		context.Background(),
		"../source/grafana/testdata/modern.json",
		options,
	)
	require.NoError(t, err)
	assert.Empty(t, strictReport.TargetProvenance)
	assert.Equal(t, 1, strictReport.Summary.NoSeriesMatch)
	assert.Equal(t, diff.StatusNoSeriesMatch, strictReport.Comparisons[0].Stats.Status)
	assert.Empty(t, strictReport.Comparisons[0].Stats.IgnoredTargetLabels)

	options.TargetProvenance = string(diff.TargetProvenanceOTelPrometheusReceiver)
	report, err := ValidateGrafanaDifferential(
		context.Background(),
		"../source/grafana/testdata/modern.json",
		options,
	)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Summary.Compared)
	assert.Equal(t, 1, report.Summary.Equivalent)
	assert.Equal(t, diff.TargetProvenanceOTelPrometheusReceiver, report.TargetProvenance)
	assert.Equal(t, diff.StatusEquivalent, report.Comparisons[0].Stats.Status)
	assert.Equal(t, []string{"server.address"}, report.Comparisons[0].Stats.IgnoredTargetLabels)
}

func TestValidateGrafanaDifferentialDowngradesPromQLValuePanelToTimeSeries(t *testing.T) {
	t.Parallel()

	sourceQueries := 0
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		sourceQueries++
		writeJSONResponse(t, writer, map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": []any{}}})
	}))
	t.Cleanup(source.Close)
	targetQueries := 0
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		default:
			targetQueries++
			writeJSONResponse(t, writer, map[string]any{
				"status": "success", "data": map[string]any{"data": map[string]any{"results": []any{}}},
			})
		}
	}))
	t.Cleanup(target.Close)

	path := filepath.Join(t.TempDir(), "scalar.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"title":"Scalar","panels":[{"title":"Up","type":"stat","targets":[{"refId":"A","expr":"sum(up)"}]}]
	}`), 0o600))
	report, err := ValidateGrafanaDifferential(context.Background(), path, DifferentialOptions{
		SourceURL: source.URL, TargetURL: target.URL, TargetAPIKey: "key", HTTPClient: source.Client(),
		Now: time.Unix(180, 0), Range: 2 * time.Minute, Step: time.Minute,
	})

	require.NoError(t, err)
	require.Len(t, report.Comparisons, 1)
	assert.Equal(t, diff.StatusBothEmpty, report.Comparisons[0].Stats.Status)
	assert.Empty(t, report.Comparisons[0].SkippedReason)
	assert.Equal(t, "promql", report.Comparisons[0].TargetKind)
	assert.NotEmpty(t, report.Comparisons[0].TargetArtifactSHA256)
	assert.Equal(t, 1, sourceQueries)
	assert.Equal(t, 1, targetQueries)
	assert.Equal(t, 1, report.Summary.BothEmpty)
	assert.Equal(t, 10, report.Tolerances.MinimumMatchedPoints)
}

func TestValidateGrafanaDifferentialDowngradesHeatmapToWorkingTimeSeriesEnvelope(t *testing.T) {
	t.Parallel()

	var sourceQueries atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		sourceQueries.Add(1)
		writeJSONResponse(t, writer, map[string]any{
			"status": "success",
			"data":   map[string]any{"resultType": "matrix", "result": []any{}},
		})
	}))
	t.Cleanup(source.Close)

	var targetQueries atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"type": "gauge"}})
		case "/api/v2/metrics/attributes":
			writeJSONResponse(t, writer, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v5/query_range":
			targetQueries.Add(1)
			writeJSONResponse(t, writer, map[string]any{
				"status": "success",
				"data":   map[string]any{"data": map[string]any{"results": []any{}}},
			})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(target.Close)

	path := filepath.Join(t.TempDir(), "histogram.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"title":"Histogram","panels":[{"title":"Up distribution","type":"heatmap","targets":[{"refId":"A","expr":"up"}]}]
	}`), 0o600))
	report, err := ValidateGrafanaDifferential(context.Background(), path, DifferentialOptions{
		SourceURL: source.URL, TargetURL: target.URL, TargetAPIKey: "key", HTTPClient: source.Client(),
		Now: time.Unix(180, 0), Range: 2 * time.Minute, Step: time.Minute,
	})

	require.NoError(t, err)
	require.Len(t, report.Comparisons, 1)
	comparison := report.Comparisons[0]
	assert.Empty(t, comparison.SkippedReason)
	assert.Equal(t, diff.StatusBothEmpty, comparison.Stats.Status)
	var targetRequest signoz.QueryRangeRequest
	require.NoError(t, json.Unmarshal(comparison.TargetArtifact, &targetRequest))
	assert.Equal(t, "time_series", targetRequest.RequestType)
	assert.Equal(t, int32(1), sourceQueries.Load())
	assert.Equal(t, int32(1), targetQueries.Load())
	assert.NotEmpty(t, report.Comparisons[0].TargetArtifactSHA256)
}

func TestWriteDifferentialReportPersistsMinimumMatchedPoints(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "differential.json")
	report := DifferentialReport{
		Tolerances: DifferentialTolerances{MinimumMatchedPoints: 7},
		Comparisons: []DifferentialQuery{{
			Stats: diff.Stats{MinimumSeriesMatchedPoints: 6},
		}},
	}
	require.NoError(t, WriteDifferentialReport(path, report))

	encoded, err := os.ReadFile(path)
	require.NoError(t, err)
	var artifact struct {
		Tolerances struct {
			MinimumMatchedPoints int `json:"minimumMatchedPoints"`
		} `json:"tolerances"`
		Comparisons []struct {
			Stats struct {
				MinimumSeriesMatchedPoints int `json:"minimumSeriesMatchedPoints"`
			} `json:"stats"`
		} `json:"comparisons"`
	}
	require.NoError(t, json.Unmarshal(encoded, &artifact))
	assert.Equal(t, 7, artifact.Tolerances.MinimumMatchedPoints)
	require.Len(t, artifact.Comparisons, 1)
	assert.Equal(t, 6, artifact.Comparisons[0].Stats.MinimumSeriesMatchedPoints)
}

func TestAttachDifferentialEvidenceUpdatesJSONAndHTML(t *testing.T) {
	t.Parallel()

	results, err := MigrateGrafana(context.Background(), []string{"../source/grafana/testdata/modern.json"}, GrafanaOptions{
		OutputDirectory: t.TempDir(),
	})
	require.NoError(t, err)
	require.Len(t, results, 1)

	differential := differentialFromEvidence(t, results[0].Evidence)
	differential.TargetProvenance = ""

	require.NoError(t, AttachDifferentialEvidence(results[0].ReportPath, differential))
	var evidence reporttypes.Report
	decodeFile(t, results[0].ReportPath, &evidence)
	require.NotNil(t, evidence.Differential)
	assert.Empty(t, evidence.Differential.TargetProvenance)
	for _, panel := range evidence.Panels {
		for _, query := range panel.Queries {
			require.NotEmpty(t, query.Comparison)
			var comparison DifferentialQuery
			require.NoError(t, json.Unmarshal(query.Comparison, &comparison))
			assert.Equal(t, query.SourcePath, comparison.SourcePath)
			assert.Equal(t, diff.StatusSkipped, comparison.Stats.Status)
		}
	}
	html, err := os.ReadFile(filepath.Clean(results[0].HTMLPath))
	require.NoError(t, err)
	assert.Contains(t, string(html), "Differential comparison")
	assert.Contains(t, string(html), string(diff.StatusSkipped))
}

func TestAttachDifferentialEvidenceRejectsUnapprovedIgnoredTargetLabels(t *testing.T) {
	t.Parallel()

	results, err := MigrateGrafana(context.Background(), []string{"../source/grafana/testdata/modern.json"}, GrafanaOptions{
		OutputDirectory: t.TempDir(),
	})
	require.NoError(t, err)
	require.Len(t, results, 1)

	differential := differentialFromEvidence(t, results[0].Evidence)
	require.NotEmpty(t, differential.Comparisons)
	differential.Comparisons[0].Stats.IgnoredTargetLabels = []string{"tenant"}

	err = AttachDifferentialEvidence(results[0].ReportPath, differential)
	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "invalid ignored target labels")
	assert.Contains(t, err.Error(), "tenant")
}

func TestAttachDifferentialEvidenceRejectsMismatchedSource(t *testing.T) {
	t.Parallel()

	results, err := MigrateGrafana(context.Background(), []string{"../source/grafana/testdata/modern.json"}, GrafanaOptions{
		OutputDirectory: t.TempDir(),
	})
	require.NoError(t, err)

	differential := differentialFromEvidence(t, results[0].Evidence)
	differential.Source.Path = "different.json"
	err = AttachDifferentialEvidence(results[0].ReportPath, differential)
	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "does not match")
}

func TestAttachDifferentialEvidenceRejectsDuplicateQueryPaths(t *testing.T) {
	t.Parallel()

	results, err := MigrateGrafana(context.Background(), []string{"../source/grafana/testdata/modern.json"}, GrafanaOptions{
		OutputDirectory: t.TempDir(),
	})
	require.NoError(t, err)
	differential := differentialFromEvidence(t, results[0].Evidence)
	require.NotEmpty(t, differential.Comparisons)
	differential.Comparisons = append(differential.Comparisons, differential.Comparisons[0])

	err = AttachDifferentialEvidence(results[0].ReportPath, differential)
	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "duplicate source path")
}

func TestEmittedQuerySpecHashIsStableAndCoversFormulaDependencies(t *testing.T) {
	t.Parallel()

	widget := signoz.Widget{
		Title:      "Ratio",
		PanelTypes: "graph",
		Query: signoz.WidgetQuery{
			QueryType: "builder",
			Builder: signoz.BuilderContainer{
				QueryData: []signoz.BuilderQueryData{{
					QueryName: "A_1", StepInterval: 60,
					Aggregations: []signoz.MetricAggregation{{MetricName: "requests_total", SpaceAggregation: "sum"}},
				}},
				QueryFormulas: []signoz.BuilderFormula{{QueryName: "A", Expression: "A_1 / 100"}},
			},
		},
	}
	first, found, err := emittedQuerySpec(widget, "A")
	require.NoError(t, err)
	require.True(t, found)
	second, found, err := emittedQuerySpec(widget, "A")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, first.SHA256, second.SHA256)

	changed := widget
	changed.Query.Builder.QueryData = append([]signoz.BuilderQueryData(nil), widget.Query.Builder.QueryData...)
	changed.Query.Builder.QueryData[0].StepInterval = 300
	changedIdentity, found, err := emittedQuerySpec(changed, "A")
	require.NoError(t, err)
	require.True(t, found)
	assert.NotEqual(t, first.SHA256, changedIdentity.SHA256)
}

func TestAttachDifferentialEvidenceRejectsLegacyMissingBindings(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*reporttypes.Report, *DifferentialReport)
		expected string
	}{
		{
			name: "migration source hash",
			mutate: func(evidence *reporttypes.Report, _ *DifferentialReport) {
				evidence.Source.SHA256 = ""
			},
			expected: "no valid source SHA-256; rerun migration",
		},
		{
			name: "migration query spec hash",
			mutate: func(evidence *reporttypes.Report, _ *DifferentialReport) {
				evidence.Panels[0].Queries[0].EmittedSpecSHA256 = ""
			},
			expected: "no valid emitted specification SHA-256; rerun migration",
		},
		{
			name: "migration emitted kind",
			mutate: func(evidence *reporttypes.Report, _ *DifferentialReport) {
				evidence.Panels[0].Queries[0].EmittedKind = ""
			},
			expected: "missing or unsupported emitted kind",
		},
		{
			name: "differential source hash",
			mutate: func(_ *reporttypes.Report, differential *DifferentialReport) {
				differential.Source.SHA256 = ""
			},
			expected: "no valid source SHA-256; rerun differential validation",
		},
		{
			name: "differential target spec hash",
			mutate: func(_ *reporttypes.Report, differential *DifferentialReport) {
				differential.Comparisons[0].TargetSpecSHA256 = ""
			},
			expected: "no valid target specification SHA-256; rerun differential validation",
		},
		{
			name: "differential target kind",
			mutate: func(_ *reporttypes.Report, differential *DifferentialReport) {
				differential.Comparisons[0].TargetKind = ""
			},
			expected: "has no target kind; rerun differential validation",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			results, err := MigrateGrafana(context.Background(), []string{"../source/grafana/testdata/modern.json"}, GrafanaOptions{
				OutputDirectory: t.TempDir(),
			})
			require.NoError(t, err)
			require.Len(t, results, 1)
			evidence := results[0].Evidence
			differential := differentialFromEvidence(t, evidence)
			test.mutate(&evidence, &differential)
			require.NoError(t, updateDashboardReportArtifactSet(results[0].ReportPath, &evidence))

			err = AttachDifferentialEvidence(results[0].ReportPath, differential)
			require.Error(t, err)
			assert.Equal(t, ErrorInput, KindOf(err))
			assert.Contains(t, err.Error(), test.expected)
		})
	}
}

func TestAttachDifferentialEvidenceRejectsMismatchedEmittedKind(t *testing.T) {
	results, err := MigrateGrafana(context.Background(), []string{"../source/grafana/testdata/modern.json"}, GrafanaOptions{
		OutputDirectory: t.TempDir(),
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	differential := differentialFromEvidence(t, results[0].Evidence)
	require.NotEmpty(t, differential.Comparisons)
	if differential.Comparisons[0].TargetKind == "promql" {
		differential.Comparisons[0].TargetKind = "builder_query"
	} else {
		differential.Comparisons[0].TargetKind = "promql"
	}

	err = AttachDifferentialEvidence(results[0].ReportPath, differential)
	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "target kind")
	assert.Contains(t, err.Error(), "does not match the effective primary artifact kind")
}

func TestAttachDifferentialEvidenceRejectsMismatchedTargetQueryName(t *testing.T) {
	results, err := MigrateGrafana(context.Background(), []string{"../source/grafana/testdata/modern.json"}, GrafanaOptions{
		OutputDirectory: t.TempDir(),
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	differential := differentialFromEvidence(t, results[0].Evidence)
	require.NotEmpty(t, differential.Comparisons)
	differential.Comparisons[0].TargetQueryName += "_stale"

	err = AttachDifferentialEvidence(results[0].ReportPath, differential)
	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "target query name")
	assert.Contains(t, err.Error(), "does not match migration target query name")
}

func TestAttachDifferentialEvidenceRejectsMismatchedTargetExpression(t *testing.T) {
	results, err := MigrateGrafana(context.Background(), []string{"../source/grafana/testdata/modern.json"}, GrafanaOptions{
		OutputDirectory: t.TempDir(),
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	differential := differentialFromEvidence(t, results[0].Evidence)
	require.NotEmpty(t, differential.Comparisons)
	differential.Comparisons[0].TargetExpression += " stale"

	err = AttachDifferentialEvidence(results[0].ReportPath, differential)
	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "target expression")
	assert.Contains(t, err.Error(), "does not match the migration's emitted expression")
}

func TestAttachDifferentialEvidenceRejectsSamePathMutatedSourceBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dashboard.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"title":"Binding","schemaVersion":41,
		"panels":[{"title":"Up","type":"timeseries","targets":[{"refId":"A","expr":"sum(up) ^ 2"}]}]
	}`), 0o600))
	first, err := MigrateGrafana(context.Background(), []string{path}, GrafanaOptions{OutputDirectory: t.TempDir()})
	require.NoError(t, err)
	require.Len(t, first, 1)

	require.NoError(t, os.WriteFile(path, []byte(`{
		"title":"Binding","schemaVersion":41,
		"panels":[{"title":"Up","type":"timeseries","targets":[{"refId":"A","expr":"sum(up) ^ 3"}]}]
	}`), 0o600))
	second, err := MigrateGrafana(context.Background(), []string{path}, GrafanaOptions{OutputDirectory: t.TempDir()})
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.NotEqual(t, first[0].Evidence.Source.SHA256, second[0].Evidence.Source.SHA256)

	err = AttachDifferentialEvidence(first[0].ReportPath, differentialFromEvidence(t, second[0].Evidence))
	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "source SHA-256")
	assert.Contains(t, err.Error(), "does not match")
}

func TestEmittedQuerySpecHashIsStableAndChangedOptionsCannotAttach(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dashboard.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"title":"Rate binding","schemaVersion":41,
		"panels":[{"title":"Rate","type":"timeseries","targets":[{
			"refId":"A","expr":"sum(rate(http_requests_total[$__rate_interval])) ^ 2"
		}]}]
	}`), 0o600))
	migrate := func(rateInterval time.Duration) GrafanaResult {
		results, err := MigrateGrafana(context.Background(), []string{path}, GrafanaOptions{
			OutputDirectory: t.TempDir(),
			RateInterval:    rateInterval,
		})
		require.NoError(t, err)
		require.Len(t, results, 1)
		return results[0]
	}

	first := migrate(5 * time.Minute)
	same := migrate(5 * time.Minute)
	changed := migrate(10 * time.Minute)
	firstHash := first.Evidence.Panels[0].Queries[0].EmittedSpecSHA256
	require.True(t, validSHA256(firstHash))
	assert.Equal(t, first.Evidence.Source.SHA256, same.Evidence.Source.SHA256)
	assert.Equal(t, firstHash, same.Evidence.Panels[0].Queries[0].EmittedSpecSHA256)
	assert.NotEqual(t, firstHash, changed.Evidence.Panels[0].Queries[0].EmittedSpecSHA256)

	differential := differentialFromEvidence(t, changed.Evidence)
	require.Len(t, differential.Comparisons, 1)
	differential.Comparisons[0].TargetExpression = first.Evidence.Panels[0].Queries[0].PromQL
	err := AttachDifferentialEvidence(first.ReportPath, differential)
	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "materialization settings do not match migration evidence")
}

func TestAttachDifferentialEvidenceRequiresExactQueryBijection(t *testing.T) {
	results, err := MigrateGrafana(context.Background(), []string{"../source/grafana/testdata/modern.json"}, GrafanaOptions{
		OutputDirectory: t.TempDir(),
	})
	require.NoError(t, err)
	require.Len(t, results, 1)

	t.Run("missing comparison", func(t *testing.T) {
		differential := differentialFromEvidence(t, results[0].Evidence)
		require.Greater(t, len(differential.Comparisons), 1)
		differential.Comparisons = differential.Comparisons[:len(differential.Comparisons)-1]
		err := AttachDifferentialEvidence(results[0].ReportPath, differential)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing migration query")
	})

	t.Run("unmapped comparison", func(t *testing.T) {
		differential := differentialFromEvidence(t, results[0].Evidence)
		differential.Comparisons[0].SourcePath = "/panels/999/targets/999"
		err := AttachDifferentialEvidence(results[0].ReportPath, differential)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is not mapped to a migration query")
	})
}

func TestAttachDifferentialEvidenceRejectsDuplicateMigrationQueryPaths(t *testing.T) {
	results, err := MigrateGrafana(context.Background(), []string{"../source/grafana/testdata/modern.json"}, GrafanaOptions{
		OutputDirectory: t.TempDir(),
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	evidence := results[0].Evidence
	require.GreaterOrEqual(t, len(evidence.Panels[0].Queries), 2)
	evidence.Panels[0].Queries[1].SourcePath = evidence.Panels[0].Queries[0].SourcePath
	require.NoError(t, updateDashboardReportArtifactSet(results[0].ReportPath, &evidence))

	err = AttachDifferentialEvidence(results[0].ReportPath, differentialFromEvidence(t, results[0].Evidence))
	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "duplicate primary query source path")
}

func TestAttachDifferentialEvidenceVerifiesTargetArtifactHashAndEnvelope(t *testing.T) {
	results, err := MigrateGrafana(context.Background(), []string{"../source/grafana/testdata/modern.json"}, GrafanaOptions{
		OutputDirectory: t.TempDir(),
	})
	require.NoError(t, err)
	require.Len(t, results, 1)

	var dashboard signoz.DashboardV5
	decodeFile(t, results[0].DashboardPath, &dashboard)
	require.NotEmpty(t, dashboard.Widgets)
	window := DifferentialWindow{Start: time.Unix(60, 0), End: time.Unix(180, 0), StepMillis: 60_000}
	request, err := signoz.DashboardRequestForWidgetWindowWithVariableTypes(
		dashboard.Widgets[0], nil, signoz.DashboardVariableTypes(dashboard),
		window.End, window.End.Sub(window.Start),
	)
	require.NoError(t, err)
	artifact, err := json.MarshalIndent(request, "", "    ")
	require.NoError(t, err)
	artifactHash, err := canonicalJSONSHA256(artifact)
	require.NoError(t, err)

	withArtifact := func() DifferentialReport {
		differential := differentialFromEvidence(t, results[0].Evidence)
		differential.Comparisons[0].TargetArtifact = append(json.RawMessage(nil), artifact...)
		differential.Comparisons[0].TargetArtifactSHA256 = artifactHash
		differential.Comparisons[0].Window = &window
		return differential
	}
	freshRequest := func() signoz.QueryRangeRequest {
		decoded, decodeErr := decodeTargetArtifact(artifact)
		require.NoError(t, decodeErr)
		return decoded
	}

	t.Run("formatted artifact is accepted using whitespace-canonical hash", func(t *testing.T) {
		require.NoError(t, AttachDifferentialEvidence(results[0].ReportPath, withArtifact()))
	})

	t.Run("artifact without hash", func(t *testing.T) {
		differential := withArtifact()
		differential.Comparisons[0].TargetArtifactSHA256 = ""
		err := AttachDifferentialEvidence(results[0].ReportPath, differential)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "both target artifact and target artifact SHA-256")
	})

	t.Run("hash without artifact", func(t *testing.T) {
		differential := withArtifact()
		differential.Comparisons[0].TargetArtifact = nil
		err := AttachDifferentialEvidence(results[0].ReportPath, differential)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "both target artifact and target artifact SHA-256")
	})

	t.Run("tampered artifact bytes", func(t *testing.T) {
		differential := withArtifact()
		tampered := freshRequest()
		tampered.End++
		differential.Comparisons[0].TargetArtifact, err = json.MarshalIndent(tampered, "", "    ")
		require.NoError(t, err)
		err = AttachDifferentialEvidence(results[0].ReportPath, differential)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "target artifact SHA-256 does not match its artifact")
	})

	t.Run("rehashed different static envelope", func(t *testing.T) {
		differential := withArtifact()
		tampered := freshRequest()
		require.NotEmpty(t, tampered.CompositeQuery.Queries)
		switch spec := tampered.CompositeQuery.Queries[0].Spec.(type) {
		case signoz.PromQLSpec:
			spec.Query += " + vector(1)"
			tampered.CompositeQuery.Queries[0].Spec = spec
		case signoz.BuilderQuerySpec:
			spec.Legend += " stale"
			tampered.CompositeQuery.Queries[0].Spec = spec
		case signoz.FormulaSpec:
			spec.Expression += " + 1"
			tampered.CompositeQuery.Queries[0].Spec = spec
		default:
			t.Fatalf("unexpected target spec type %T", spec)
		}
		differential.Comparisons[0].TargetArtifact, err = json.MarshalIndent(tampered, "", "    ")
		require.NoError(t, err)
		differential.Comparisons[0].TargetArtifactSHA256, err = canonicalJSONSHA256(differential.Comparisons[0].TargetArtifact)
		require.NoError(t, err)
		err = AttachDifferentialEvidence(results[0].ReportPath, differential)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "static envelope does not match its bound target specification")
	})

	t.Run("rehashed different dynamic window", func(t *testing.T) {
		differential := withArtifact()
		tampered := freshRequest()
		tampered.End++
		differential.Comparisons[0].TargetArtifact, err = json.MarshalIndent(tampered, "", "    ")
		require.NoError(t, err)
		differential.Comparisons[0].TargetArtifactSHA256, err = canonicalJSONSHA256(differential.Comparisons[0].TargetArtifact)
		require.NoError(t, err)
		err = AttachDifferentialEvidence(results[0].ReportPath, differential)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "target artifact window does not match its comparison window")
	})
}

func differentialFromEvidence(t *testing.T, evidence reporttypes.Report) DifferentialReport {
	t.Helper()

	materialization, _, err := differentialMaterializationFromEvidence(evidence)
	require.NoError(t, err)
	targetURL := evidence.Run.Target
	if targetURL == "" {
		targetURL = "http://signoz.example"
	}
	result := DifferentialReport{
		Source: model.Source{
			Kind:          evidence.Source.Kind,
			SchemaVersion: evidence.Source.SchemaVersion,
			Path:          evidence.Source.Path,
			Identity:      evidence.Source.Identity,
			SHA256:        evidence.Source.SHA256,
		},
		SourceURL:        "http://prometheus.example",
		TargetURL:        targetURL,
		TargetProvenance: diff.TargetProvenanceOTelPrometheusReceiver,
		PrimaryArtifact:  evidence.PrimaryArtifact,
		Materialization:  materialization,
		Window: DifferentialWindow{
			Start:      time.Unix(60, 0).UTC(),
			End:        time.Unix(180, 0).UTC(),
			StepMillis: 60_000,
		},
		Tolerances: DifferentialTolerances{
			TimestampMillis:      60_000,
			Relative:             0.15,
			Absolute:             1e-9,
			Coverage:             0.8,
			MinimumMatchedPoints: 1,
		},
	}
	for _, panel := range evidence.Panels {
		for _, query := range panel.Queries {
			identity, err := effectiveRecordedQueryIdentity(panel, query)
			require.NoError(t, err)
			result.Comparisons = append(result.Comparisons, DifferentialQuery{
				PanelTitle:       panel.Title,
				RefID:            query.RefID,
				SourcePath:       query.SourcePath,
				TargetExpression: identity.TargetExpression,
				TargetKind:       identity.TargetKind,
				TargetQueryName:  identity.TargetQueryName,
				TargetSpecSHA256: identity.SHA256,
				Stats:            diff.Stats{Status: diff.StatusSkipped},
				SkippedReason:    "test fixture did not execute live queries",
			})
		}
	}
	result.Summary = summarizeDifferential(result.Comparisons)
	return result
}

func writeJSONResponse(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(writer).Encode(value))
}
