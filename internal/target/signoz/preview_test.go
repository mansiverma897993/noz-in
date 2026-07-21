package signoz

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreviewRequestForFormulaWidget(t *testing.T) {
	t.Parallel()

	widget := Widget{
		Title:      "CPU Busy",
		PanelTypes: "value",
		Query: WidgetQuery{QueryType: "builder", Builder: BuilderContainer{
			QueryData: []BuilderQueryData{{
				QueryName: "A_1", StepInterval: 60,
				GroupBy: []DashboardGroupBy{
					{Key: "service.name", DataType: "string", Type: "resource"},
					{Key: "cpu", DataType: "string", Type: "tag"},
				},
				Aggregations: []MetricAggregation{{
					MetricName: "node_cpu_seconds_total", TimeAggregation: "rate", SpaceAggregation: "avg",
				}},
				Filter: Expression{Expression: "mode = 'idle' AND instance = '$node'"},
			}},
			QueryFormulas: []BuilderFormula{{QueryName: "A", Expression: "(100 * (1 - A_1))"}},
		}},
	}
	now := time.UnixMilli(1_700_000_000_000)
	request, err := PreviewRequestForWidget(widget, map[string]string{"node": "source-node"}, now)
	require.NoError(t, err)
	assert.Equal(t, "scalar", request.RequestType)
	assert.Equal(t, uint64(1_700_000_000_000), request.End)
	assert.Equal(t, "source-node", request.Variables["node"].Value)
	require.Len(t, request.CompositeQuery.Queries, 2)
	assert.Equal(t, "builder_query", request.CompositeQuery.Queries[0].Type)
	assert.Equal(t, "builder_formula", request.CompositeQuery.Queries[1].Type)
	spec, ok := request.CompositeQuery.Queries[0].Spec.(BuilderQuerySpec)
	require.True(t, ok)
	require.Len(t, spec.GroupBy, 2)
	assert.Equal(t, GroupBy{Name: "service.name", FieldDataType: "string", FieldContext: "resource"}, spec.GroupBy[0])
	assert.Equal(t, GroupBy{Name: "cpu", FieldDataType: "string", FieldContext: "tag"}, spec.GroupBy[1])

	encoded, err := json.Marshal(request.CompositeQuery.Queries[0].Spec)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"having":{"expression":""}`)
	assert.Contains(t, string(encoded), `"disabled":false`)
	assert.Contains(t, string(encoded), `"source":""`)
	assert.NotContains(t, string(encoded), `"temporality"`)
	assert.NotContains(t, string(encoded), `"orderBy"`)
	assert.NotContains(t, string(encoded), `"key"`)
}

func TestDashboardBuilderRequestMirrorsPinnedFrontendWire(t *testing.T) {
	t.Parallel()

	widget := Widget{
		Title: "Up", PanelTypes: "graph",
		Query: WidgetQuery{QueryType: "builder", Builder: BuilderContainer{
			QueryData: []BuilderQueryData{{
				DataSource: "metrics", QueryName: "A_1", StepInterval: 60,
				Aggregations: []MetricAggregation{{
					MetricName: "up", TimeAggregation: "latest", SpaceAggregation: "sum",
				}},
				Filter: Expression{}, Having: Expression{},
			}},
			QueryFormulas: []BuilderFormula{{QueryName: "A", Expression: "A_1", Disabled: false}},
		}},
	}
	request, err := DashboardRequestForWidgetWindow(widget, nil, time.Unix(3600, 0), time.Hour)
	require.NoError(t, err)
	require.Len(t, request.CompositeQuery.Queries, 2)

	query, err := json.Marshal(request.CompositeQuery.Queries[0].Spec)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"name":"A_1",
		"signal":"metrics",
		"source":"",
		"stepInterval":60,
		"disabled":false,
		"filter":{"expression":""},
		"having":{"expression":""},
		"aggregations":[{
			"metricName":"up",
			"timeAggregation":"latest",
			"spaceAggregation":"sum"
		}]
	}`, string(query))

	formula, err := json.Marshal(request.CompositeQuery.Queries[1].Spec)
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"A","expression":"A_1","disabled":false}`, string(formula))
}

func TestDashboardRequestMirrorsPinnedFrontendPanelRequestTypes(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		panelType   string
		requestType string
	}{
		{panelType: "graph", requestType: "time_series"},
		{panelType: "bar", requestType: "time_series"},
		{panelType: "histogram", requestType: "distribution"},
		{panelType: "value", requestType: "scalar"},
		{panelType: "table", requestType: "scalar"},
		{panelType: "pie", requestType: "scalar"},
	} {
		t.Run(test.panelType, func(t *testing.T) {
			t.Parallel()
			widget := Widget{
				PanelTypes: test.panelType,
				Query:      WidgetQuery{QueryType: "promql", PromQL: []PromQLQuery{{Name: "A", Query: "up"}}},
			}
			request, err := DashboardRequestForWidgetWindow(widget, nil, time.Unix(300, 0), time.Minute)
			require.NoError(t, err)
			assert.Equal(t, test.requestType, request.RequestType)
		})
	}
}

func TestDashboardRequestPreservesPinnedScalarListVariableValues(t *testing.T) {
	t.Parallel()

	widget := Widget{
		PanelTypes: "graph",
		Query:      WidgetQuery{QueryType: "promql", PromQL: []PromQLQuery{{Name: "A", Query: `up{job=~"$job"}`}}},
	}
	request, err := DashboardRequestForWidgetWindowWithVariableTypes(
		widget,
		map[string]any{
			"job":     []string{"api", "worker"},
			"enabled": false,
			"limit":   json.Number("2"),
		},
		map[string]string{"job": "dynamic", "enabled": "custom", "limit": "query"},
		time.Unix(300, 0),
		time.Minute,
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"api", "worker"}, request.Variables["job"].Value)
	assert.Equal(t, false, request.Variables["enabled"].Value)
	assert.Equal(t, json.Number("2"), request.Variables["limit"].Value)

	encoded, err := json.Marshal(request)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"job":{"type":"dynamic","value":["api","worker"]}`)

	_, err = DashboardRequestForWidgetWindowWithVariableTypes(
		widget,
		map[string]any{"job": []any{"api", map[string]string{"hostile": "shape"}}},
		nil,
		time.Unix(300, 0),
		time.Minute,
	)
	require.ErrorContains(t, err, `dashboard variable "job": value 1`)
}

func TestDashboardBuilderRequestUsesNullForUnsetStepAndOmitsEmptyTemporality(t *testing.T) {
	t.Parallel()

	empty := ""
	widget := Widget{PanelTypes: "graph", Query: WidgetQuery{
		QueryType: "builder",
		Builder: BuilderContainer{QueryData: []BuilderQueryData{{
			QueryName:    "A",
			Aggregations: []MetricAggregation{{MetricName: "up", Temporality: &empty}},
		}}},
	}}
	request, err := DashboardRequestForWidgetWindow(widget, nil, time.Unix(3600, 0), time.Hour)
	require.NoError(t, err)
	encoded, err := json.Marshal(request.CompositeQuery.Queries[0].Spec)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"stepInterval":null`)
	assert.NotContains(t, string(encoded), `"temporality"`)
}

func TestDashboardGroupByUsesFrontendStorageSchema(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(DashboardGroupBy{Key: "cpu", DataType: "string", Type: "tag"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"key":"cpu","dataType":"string","type":"tag"}`, string(encoded))
	assert.NotContains(t, string(encoded), `"name"`)
}

func TestPreviewRequestRejectsUnknownQueryType(t *testing.T) {
	t.Parallel()

	_, err := PreviewRequestForWidget(Widget{Title: "Unknown", Query: WidgetQuery{QueryType: "clickhouse_sql"}}, nil, time.Now())
	require.ErrorContains(t, err, "unsupported query type")
}

func TestPreviewRequestRejectsWindowBeforeUnixEpoch(t *testing.T) {
	t.Parallel()

	widget := Widget{
		Title:      "Historical",
		PanelTypes: "graph",
		Query: WidgetQuery{QueryType: "promql", PromQL: []PromQLQuery{{
			Name: "A", Query: "up",
		}}},
	}

	_, err := PreviewRequestForWidgetWindow(widget, nil, time.Unix(30, 0), time.Minute)
	require.ErrorContains(t, err, "must not precede the Unix epoch")
}

func TestPreviewRequestCarriesNormalizedVariableSyntaxAndAllSentinel(t *testing.T) {
	t.Parallel()

	widget := Widget{
		Title: "Availability", PanelTypes: "graph",
		Query: WidgetQuery{QueryType: "promql", PromQL: []PromQLQuery{{
			Name: "A", Query: `sum(up{job=~"$job"})`,
		}}},
	}
	request, err := PreviewRequestForWidgetWindow(
		widget, map[string]string{"job": "__all__"}, time.Unix(300, 0), time.Minute,
	)
	require.NoError(t, err)

	encoded, err := json.Marshal(request)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"query":"sum(up{job=~\"$job\"})"`)
	assert.Contains(t, string(encoded), `"variables":{"job":{"type":"query","value":"__all__"}}`)
}

func TestDashboardRequestMirrorsFrontendPromQLEnvelope(t *testing.T) {
	t.Parallel()

	widget := Widget{
		Title: "CPU", PanelTypes: "graph",
		Query: WidgetQuery{QueryType: "promql", PromQL: []PromQLQuery{{
			Name: "A", Query: "rate(cpu_total[5m])", Disabled: false,
		}}},
	}
	request, err := DashboardRequestForWidgetWindow(widget, nil, time.Unix(90_000, 0), 24*time.Hour)
	require.NoError(t, err)
	encoded, err := json.Marshal(request)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), `"step"`)
	assert.NotContains(t, string(encoded), `"noCache"`)
	assert.Contains(t, string(encoded), `"formatOptions":{"formatTableResultForUI":false,"fillGaps":false}`)
	require.Len(t, request.CompositeQuery.Queries, 1)
	spec, ok := request.CompositeQuery.Queries[0].Spec.(PromQLSpec)
	require.True(t, ok)
	assert.Zero(t, spec.Step)

	preview, err := PreviewRequestForWidgetWindow(widget, nil, time.Unix(90_000, 0), 24*time.Hour)
	require.NoError(t, err)
	assert.True(t, preview.NoCache)
}
