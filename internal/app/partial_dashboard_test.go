package app

import (
	"net/http"
	"testing"

	"github.com/mansiverma897993/signoz/internal/target/signoz"
	"github.com/mansiverma897993/signoz/pkg/reporttypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidationSafeDashboardPrunesRejectedWidgetsAndLayouts(t *testing.T) {
	t.Parallel()

	candidate := signoz.DashboardV5{
		Widgets: []signoz.Widget{
			{ID: "good", SourcePath: "panels/0", Query: promQLWidgetQuery("up")},
			{ID: "row", SourcePath: "panels/1", PanelTypes: "row"},
			{ID: "child-good", SourcePath: "panels/1/panels/0", Query: promQLWidgetQuery("up")},
			{ID: "bad", SourcePath: "panels/1/panels/1", Query: promQLWidgetQuery("bad")},
		},
		Layout: []signoz.Layout{{I: "good"}, {I: "row"}},
		PanelMap: map[string]signoz.PanelGroup{
			"row": {Collapsed: true, Widgets: []signoz.Layout{{I: "child-good"}, {I: "bad"}}},
		},
	}
	evidence := reporttypes.Report{Panels: []reporttypes.PanelRecord{
		{SourcePath: "panels/0", Queries: []reporttypes.QueryRecord{{
			RefID: "A", EmittedKind: "promql", Validation: successfulValidation(),
		}}},
		{SourcePath: "panels/1"},
		{SourcePath: "panels/1/panels/0", Queries: []reporttypes.QueryRecord{{
			RefID: "A", EmittedKind: "promql", Validation: successfulValidation(),
		}}},
		{SourcePath: "panels/1/panels/1", Queries: []reporttypes.QueryRecord{{
			RefID: "A", EmittedKind: "promql", Validation: reporttypes.Validation{
				Previewed: true, ErrorCode: "WIDGET_REJECTED",
			},
		}}},
	}}

	safe, rejected, blocked := validationSafeDashboard(candidate, evidence)

	assert.Equal(t, []string{"panels/1/panels/1"}, rejected)
	assert.Empty(t, blocked)
	require.Len(t, safe.Widgets, 3)
	assert.Equal(t, []string{"good", "row", "child-good"}, []string{safe.Widgets[0].ID, safe.Widgets[1].ID, safe.Widgets[2].ID})
	assert.Equal(t, []signoz.Layout{{I: "good"}, {I: "row"}}, safe.Layout)
	require.Contains(t, safe.PanelMap, "row")
	assert.Equal(t, []signoz.Layout{{I: "child-good"}}, safe.PanelMap["row"].Widgets)
	assert.Len(t, candidate.Widgets, 4, "candidate must remain unchanged")
	assert.Len(t, candidate.PanelMap["row"].Widgets, 2, "candidate row layout must remain unchanged")
	assert.Equal(t, 2, enabledExecutableWidgetCount(safe))
	assertDashboardLayoutsReferenceKeptWidgets(t, safe)
}

func TestEnabledExecutableWidgetCountIgnoresDisabledEnvelopes(t *testing.T) {
	t.Parallel()

	dashboard := signoz.DashboardV5{Widgets: []signoz.Widget{
		{ID: "disabled-promql", Query: signoz.WidgetQuery{QueryType: "promql", PromQL: []signoz.PromQLQuery{{Disabled: true}}}},
		{ID: "disabled-builder", Query: signoz.WidgetQuery{QueryType: "builder", Builder: signoz.BuilderContainer{
			QueryData: []signoz.BuilderQueryData{{Disabled: true}}, QueryFormulas: []signoz.BuilderFormula{{Disabled: true}},
		}}},
		{ID: "enabled-promql", Query: signoz.WidgetQuery{QueryType: "promql", PromQL: []signoz.PromQLQuery{{Disabled: true}, {}}}},
		{ID: "enabled-formula", Query: signoz.WidgetQuery{QueryType: "builder", Builder: signoz.BuilderContainer{
			QueryData: []signoz.BuilderQueryData{{Disabled: true}}, QueryFormulas: []signoz.BuilderFormula{{}},
		}}},
		{ID: "enabled-clickhouse", Query: signoz.WidgetQuery{QueryType: "clickhouse_sql", ClickHouseSQL: []signoz.PromQLQuery{{}}}},
		{ID: "empty"},
	}}

	assert.Equal(t, 3, enabledExecutableWidgetCount(dashboard))
}

func TestValidationSafeDashboardKeepsDisabledAndUnemittedQueriesOutOfFailureDecision(t *testing.T) {
	t.Parallel()

	candidate := signoz.DashboardV5{Widgets: []signoz.Widget{{
		ID: "widget", SourcePath: "panels/0", Query: promQLWidgetQuery("up"),
	}}}
	evidence := reporttypes.Report{Panels: []reporttypes.PanelRecord{{
		SourcePath: "panels/0",
		Queries: []reporttypes.QueryRecord{
			{RefID: "A", EmittedKind: "promql", Validation: successfulValidation()},
			{RefID: "B", EmittedKind: "promql", Disabled: true},
			{RefID: "C", EmittedKind: "none"},
		},
	}}}

	safe, rejected, blocked := validationSafeDashboard(candidate, evidence)

	assert.Empty(t, rejected)
	assert.Empty(t, blocked)
	assert.Equal(t, candidate, safe)
}

func TestValidationSafeDashboardDoesNotPruneTransientTargetFailures(t *testing.T) {
	t.Parallel()

	for _, status := range []int{409, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			candidate := signoz.DashboardV5{Widgets: []signoz.Widget{{
				ID: "widget", SourcePath: "panels/0", Query: promQLWidgetQuery("up"),
			}}}
			evidence := reporttypes.Report{Panels: []reporttypes.PanelRecord{{
				SourcePath: "panels/0", Queries: []reporttypes.QueryRecord{{
					RefID: "A", EmittedKind: "promql", Validation: reporttypes.Validation{
						Previewed: true, ErrorCode: "PREVIEW_API_ERROR", HTTPStatus: status,
					},
				}},
			}}}

			safe, rejected, blocked := validationSafeDashboard(candidate, evidence)

			assert.Empty(t, rejected)
			assert.Equal(t, []string{"panels/0"}, blocked)
			assert.Equal(t, candidate, safe)
		})
	}
}

func TestValidationSafeDashboardBlocksRejectedRowContainer(t *testing.T) {
	t.Parallel()

	candidate := signoz.DashboardV5{Widgets: []signoz.Widget{
		{ID: "row", SourcePath: "panels/0", PanelTypes: "row", Query: promQLWidgetQuery("bad")},
		{ID: "child", SourcePath: "panels/0/panels/0", Query: promQLWidgetQuery("up")},
	}, PanelMap: map[string]signoz.PanelGroup{"row": {Widgets: []signoz.Layout{{I: "child"}}}}}
	evidence := reporttypes.Report{Panels: []reporttypes.PanelRecord{
		{Kind: "row", SourcePath: "panels/0", Queries: []reporttypes.QueryRecord{{
			RefID: "A", EmittedKind: "promql", Validation: reporttypes.Validation{Previewed: true, ErrorCode: "BAD_QUERY", HTTPStatus: 400},
		}}},
		{SourcePath: "panels/0/panels/0", Queries: []reporttypes.QueryRecord{{
			RefID: "A", EmittedKind: "promql", Validation: successfulValidation(),
		}}},
	}}

	safe, rejected, blocked := validationSafeDashboard(candidate, evidence)

	assert.Empty(t, rejected)
	assert.Equal(t, []string{"panels/0"}, blocked)
	assert.Equal(t, candidate, safe, "a structural row failure must block import rather than orphan its child")
}

func promQLWidgetQuery(query string) signoz.WidgetQuery {
	return signoz.WidgetQuery{
		QueryType: "promql",
		PromQL:    []signoz.PromQLQuery{{Name: "A", Query: query}},
	}
}

func successfulValidation() reporttypes.Validation {
	return reporttypes.Validation{Previewed: true, PreviewOK: true, Executed: true}
}
