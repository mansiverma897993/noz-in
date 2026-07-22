package signoz

import (
	"encoding/json"
	"testing"

	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmitV5BuilderPanel(t *testing.T) {
	t.Parallel()

	query := model.Query{RefID: "A", Legend: "CPU", SourcePath: "/panels/0/targets/0"}
	migration := model.Migration{
		Dashboard: model.Dashboard{
			Title: "Host",
			UID:   "host",
			Tags:  []string{"infra"},
			Panels: []model.Panel{{
				ID:         "1",
				Title:      "CPU",
				Kind:       model.PanelKindGraph,
				Grid:       model.Grid{X: 2, Y: 3, W: 11, H: 7},
				Queries:    []model.Query{query},
				SourcePath: "/panels/0",
			}},
		},
		Translations: map[string]model.Translation{
			query.SourcePath: {
				Kind: model.TranslationBuilder,
				Builder: &model.BuilderQuery{
					Name:             "A",
					MetricName:       "node_cpu_seconds_total",
					TimeAggregation:  "rate",
					SpaceAggregation: "sum",
					GroupBy:          []string{"instance"},
					Filters:          []model.Filter{{Label: "mode", Operator: "!=", Value: "idle"}},
				},
				Decision: model.Decision{Verdict: model.VerdictNative},
			},
		},
	}

	dashboard := EmitV5(migration)
	require.Len(t, dashboard.Widgets, 1)
	require.Len(t, dashboard.Layout, 1)
	assert.Equal(t, "v5", dashboard.Version)
	assert.True(t, dashboard.UploadedGrafana)
	assert.NotEmpty(t, dashboard.UUID)
	assert.Equal(t, Layout{X: 1, Y: 3, W: 6, H: 5, I: dashboard.Widgets[0].ID}, withoutLayoutFlags(dashboard.Layout[0]))
	assert.Equal(t, "builder", dashboard.Widgets[0].Query.QueryType)
	assert.False(t, dashboard.Widgets[0].SpanGaps)
	assert.Equal(t, "linear", dashboard.Widgets[0].LineInterpolation)
	assert.False(t, dashboard.Widgets[0].ShowPoints)
	require.Len(t, dashboard.Widgets[0].Query.Builder.QueryData, 1)
	assert.Equal(t, "mode != 'idle'", dashboard.Widgets[0].Query.Builder.QueryData[0].Filter.Expression)
	groupBy := dashboard.Widgets[0].Query.Builder.QueryData[0].GroupBy[0]
	assert.Equal(t, "instance", groupBy.Key)
	assert.Equal(t, "string", groupBy.DataType)
	assert.Equal(t, "tag", groupBy.Type)
	assert.Empty(t, dashboard.Widgets[0].Query.Builder.QueryData[0].Aggregations[0].ReduceTo)
}

func TestEmitV5PromQLVisualizationMapping(t *testing.T) {
	t.Parallel()

	// Value and pie panels keep their native SigNoz visualization on the
	// PromQL path; bar, table, and histogram remain graph-downgraded because
	// the pinned target renderings are not semantically usable for them.
	expected := map[model.PanelKind]string{
		model.PanelKindValue:     "value",
		model.PanelKindPie:       "pie",
		model.PanelKindBar:       "graph",
		model.PanelKindTable:     "graph",
		model.PanelKindHistogram: "graph",
	}
	for kind, want := range expected {
		query := model.Query{RefID: "A", Expression: "up", SourcePath: "/panels/0/targets/0"}
		migration := model.Migration{
			Dashboard: model.Dashboard{Panels: []model.Panel{{
				Title: "Panel", Kind: kind, Queries: []model.Query{query}, SourcePath: "/panels/0",
			}}},
			Translations: map[string]model.Translation{query.SourcePath: {
				Kind: model.TranslationPromQL, PromQL: "up",
				Decision: model.Decision{Verdict: model.VerdictPassthrough},
			}},
		}

		dashboard := EmitV5(migration)
		require.Len(t, dashboard.Widgets, 1, kind)
		assert.Equal(t, want, dashboard.Widgets[0].PanelTypes, kind)
	}
}

func TestEmitV5UsesCanonicalPromQLForBuilderSemanticCandidates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reason model.ReasonCode
		kind   model.TranslationKind
	}{
		{name: "rate or increase", reason: model.ReasonBuilderRateIncrease, kind: model.TranslationBuilder},
		{name: "latest lookback", reason: model.ReasonBuilderLatestLookback, kind: model.TranslationBuilder},
		{name: "histogram percentile", reason: model.ReasonBuilderHistogramPercentile, kind: model.TranslationBuilder},
		{name: "formula evaluation", reason: model.ReasonBuilderFormulaEvaluation, kind: model.TranslationFormula},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			query := model.Query{RefID: "A", Expression: "source_expression", SourcePath: "/panels/0/targets/0"}
			translation := model.Translation{
				Kind:   test.kind,
				PromQL: "canonical_promql",
				Decision: model.Decision{
					Verdict: model.VerdictNeedsReview,
					Reasons: []model.ReasonCode{test.reason},
				},
			}
			if test.kind == model.TranslationFormula {
				translation.Formula = &model.Formula{
					Name: "A", Expression: "A_1 / 2",
					Queries: []model.BuilderQuery{{Name: "A_1", MetricName: "metric", SpaceAggregation: "sum"}},
				}
			} else {
				translation.Builder = &model.BuilderQuery{Name: "A", MetricName: "metric", SpaceAggregation: "sum"}
			}
			migration := model.Migration{
				Dashboard: model.Dashboard{Title: "Candidate", Panels: []model.Panel{{
					Title: "Candidate", Kind: model.PanelKindGraph, Grid: model.Grid{W: 24, H: 8},
					Queries: []model.Query{query}, SourcePath: "/panels/0",
				}}},
				Translations: map[string]model.Translation{query.SourcePath: translation},
			}

			dashboard := EmitV5(migration)

			require.Len(t, dashboard.Widgets, 1)
			emitted := dashboard.Widgets[0].Query
			assert.Equal(t, "promql", emitted.QueryType)
			assert.Empty(t, emitted.Builder.QueryData)
			assert.Empty(t, emitted.Builder.QueryFormulas)
			require.Len(t, emitted.PromQL, 1)
			assert.Equal(t, "canonical_promql", emitted.PromQL[0].Query)
		})
	}
}

func TestEmitV5MixedPanelFallsBackAsAUnit(t *testing.T) {
	t.Parallel()

	native := model.Query{RefID: "A", Expression: "sum(up)", SourcePath: "/panels/0/targets/0"}
	passthrough := model.Query{RefID: "B", Expression: "up or vector(0)", SourcePath: "/panels/0/targets/1"}
	migration := model.Migration{
		Dashboard: model.Dashboard{
			Title: "Mixed",
			Panels: []model.Panel{{
				Title:      "Mixed queries",
				Kind:       model.PanelKindGraph,
				Grid:       model.Grid{W: 24, H: 8},
				Queries:    []model.Query{native, passthrough},
				SourcePath: "/panels/0",
			}},
		},
		Translations: map[string]model.Translation{
			native.SourcePath: {
				Kind:    model.TranslationBuilder,
				Builder: &model.BuilderQuery{Name: "A", MetricName: "up", SpaceAggregation: "sum"},
				PromQL:  `sum(up{"service.name"="$job"})`,
			},
			passthrough.SourcePath: {
				Kind:   model.TranslationPromQL,
				PromQL: "up or vector(0)",
			},
		},
	}

	dashboard := EmitV5(migration)
	query := dashboard.Widgets[0].Query
	assert.Equal(t, "promql", query.QueryType)
	assert.Empty(t, query.Builder.QueryData)
	require.Len(t, query.PromQL, 2)
	assert.Equal(t, `sum(up{"service.name"="$job"})`, query.PromQL[0].Query)
	assert.Equal(t, passthrough.Expression, query.PromQL[1].Query)
}

func TestEmitV5FormulaPanel(t *testing.T) {
	t.Parallel()

	query := model.Query{RefID: "A", Legend: "Errors", SourcePath: "/panels/0/targets/0"}
	migration := model.Migration{
		Dashboard: model.Dashboard{Title: "Formula", Panels: []model.Panel{{
			Kind: model.PanelKindValue, Grid: model.Grid{W: 24, H: 8}, Queries: []model.Query{query}, SourcePath: "/panels/0",
		}}},
		Translations: map[string]model.Translation{query.SourcePath: {
			Kind: model.TranslationFormula,
			Formula: &model.Formula{Name: "A", Expression: "(A_1 / A_2)", Queries: []model.BuilderQuery{
				{Name: "A_1", MetricName: "errors_total", TimeAggregation: "rate", SpaceAggregation: "sum"},
				{Name: "A_2", MetricName: "requests_total", TimeAggregation: "rate", SpaceAggregation: "sum"},
			}},
			Decision: model.Decision{Verdict: model.VerdictNative},
		}},
	}

	dashboard := EmitV5(migration)
	widgetQuery := dashboard.Widgets[0].Query
	assert.Equal(t, "builder", widgetQuery.QueryType)
	require.Len(t, widgetQuery.Builder.QueryData, 2)
	require.Len(t, widgetQuery.Builder.QueryFormulas, 1)
	assert.True(t, widgetQuery.Builder.QueryData[0].Disabled)
	assert.True(t, widgetQuery.Builder.QueryData[1].Disabled)
	assert.False(t, widgetQuery.Builder.QueryFormulas[0].Disabled)
	assert.Equal(t, "last", widgetQuery.Builder.QueryData[0].Aggregations[0].ReduceTo)
	assert.Equal(t, "last", widgetQuery.Builder.QueryData[1].Aggregations[0].ReduceTo)
	assert.Equal(t, "(A_1 / A_2)", widgetQuery.Builder.QueryFormulas[0].Expression)
	assert.Equal(t, "Errors", widgetQuery.Builder.QueryFormulas[0].Legend)
}

func TestEmitV5FormulaDependenciesNeverLeakAsVisibleSeries(t *testing.T) {
	t.Parallel()

	visible := model.Query{RefID: "A", SourcePath: "/panels/0/targets/0"}
	hidden := model.Query{RefID: "B", Hidden: true, SourcePath: "/panels/0/targets/1"}
	formula := func(name string) model.Translation {
		return model.Translation{
			Kind: model.TranslationFormula,
			Formula: &model.Formula{Name: name, Expression: name + "_1 / " + name + "_2", Queries: []model.BuilderQuery{
				{Name: name + "_1", MetricName: "left", SpaceAggregation: "sum"},
				{Name: name + "_2", MetricName: "right", SpaceAggregation: "sum"},
			}},
			Decision: model.Decision{Verdict: model.VerdictNative},
		}
	}
	migration := model.Migration{
		Dashboard: model.Dashboard{Title: "Formula visibility", Panels: []model.Panel{{
			Title: "Mixed", Kind: model.PanelKindGraph, Queries: []model.Query{visible, hidden}, SourcePath: "/panels/0",
		}}},
		Translations: map[string]model.Translation{
			visible.SourcePath: formula("A"),
			hidden.SourcePath:  formula("B"),
		},
	}

	dashboard := EmitV5(migration)
	require.Len(t, dashboard.Widgets, 1)
	builder := dashboard.Widgets[0].Query.Builder
	require.Len(t, builder.QueryData, 4)
	for _, dependency := range builder.QueryData {
		assert.True(t, dependency.Disabled, dependency.QueryName)
	}
	require.Len(t, builder.QueryFormulas, 2)
	assert.False(t, builder.QueryFormulas[0].Disabled)
	assert.True(t, builder.QueryFormulas[1].Disabled)
}

func TestEmitV5ValuePanelWithGroupedBuilderUsesPromQL(t *testing.T) {
	t.Parallel()

	query := model.Query{RefID: "A", Expression: "memory_bytes", SourcePath: "/panels/0/targets/0"}
	migration := model.Migration{
		Dashboard: model.Dashboard{Title: "Host", Panels: []model.Panel{{
			Title: "RAM", Kind: model.PanelKindValue, Grid: model.Grid{W: 6, H: 4}, Queries: []model.Query{query}, SourcePath: "/panels/0",
		}}},
		Translations: map[string]model.Translation{query.SourcePath: {
			Kind:    model.TranslationBuilder,
			Builder: &model.BuilderQuery{Name: "A", MetricName: "memory_bytes", GroupBy: []string{"service.name"}},
			PromQL:  `memory_bytes{"service.name"="$job"}`,
		}},
	}

	dashboard := EmitV5(migration)
	require.Len(t, dashboard.Widgets, 1)
	assert.Equal(t, "promql", dashboard.Widgets[0].Query.QueryType)
	require.Len(t, dashboard.Widgets[0].Query.PromQL, 1)
	assert.Equal(t, `memory_bytes{"service.name"="$job"}`, dashboard.Widgets[0].Query.PromQL[0].Query)
}

func TestEmitV5ValuePanelWithGroupedFormulaUsesPromQL(t *testing.T) {
	t.Parallel()

	query := model.Query{RefID: "A", Expression: "available / total", SourcePath: "/panels/0/targets/0"}
	migration := model.Migration{
		Dashboard: model.Dashboard{Title: "Host", Panels: []model.Panel{{
			Title: "RAM", Kind: model.PanelKindValue, Grid: model.Grid{W: 6, H: 4}, Queries: []model.Query{query}, SourcePath: "/panels/0",
		}}},
		Translations: map[string]model.Translation{query.SourcePath: {
			Kind: model.TranslationFormula,
			Formula: &model.Formula{Name: "A", Expression: "A_1 / A_2", Queries: []model.BuilderQuery{
				{Name: "A_1", MetricName: "available", GroupBy: []string{"service.name"}},
				{Name: "A_2", MetricName: "total", GroupBy: []string{"service.name"}},
			}},
			PromQL: `available / on("service.name") total`,
		}},
	}

	dashboard := EmitV5(migration)
	require.Len(t, dashboard.Widgets, 1)
	assert.Equal(t, "promql", dashboard.Widgets[0].Query.QueryType)
	assert.Equal(t, `available / on("service.name") total`, dashboard.Widgets[0].Query.PromQL[0].Query)
}

func TestEmitV5FallsBackWhenExpandedBuilderNamesCollide(t *testing.T) {
	t.Parallel()

	formulaQuery := model.Query{RefID: "A", Expression: "errors / requests", SourcePath: "/panels/0/targets/0"}
	collidingQuery := model.Query{RefID: "SM_collision_1", Expression: "up", SourcePath: "/panels/0/targets/1"}
	migration := model.Migration{
		Dashboard: model.Dashboard{Title: "Collision", Panels: []model.Panel{{
			Title: "Collision", Kind: model.PanelKindGraph, Grid: model.Grid{W: 24, H: 8},
			Queries: []model.Query{formulaQuery, collidingQuery}, SourcePath: "/panels/0",
		}}},
		Translations: map[string]model.Translation{
			formulaQuery.SourcePath: {
				Kind: model.TranslationFormula,
				Formula: &model.Formula{Name: "A", Expression: "SM_collision_1 / SM_collision_2", Queries: []model.BuilderQuery{
					{Name: "SM_collision_1", MetricName: "errors", SpaceAggregation: "sum"},
					{Name: "SM_collision_2", MetricName: "requests", SpaceAggregation: "sum"},
				}},
				PromQL: "errors / requests", Decision: model.Decision{Verdict: model.VerdictNative},
			},
			collidingQuery.SourcePath: {
				Kind: model.TranslationBuilder, Builder: &model.BuilderQuery{
					Name: "SM_collision_1", MetricName: "up", SpaceAggregation: "sum",
				},
				PromQL: "up", Decision: model.Decision{Verdict: model.VerdictNative},
			},
		},
	}

	assert.Equal(t, model.TranslationPromQL, migration.PanelMode(migration.Dashboard.Panels[0]))
	assert.Equal(t, model.ReasonQueryNameCollision, migration.PanelFallbackReason(migration.Dashboard.Panels[0]))
	dashboard := EmitV5(migration)
	require.Len(t, dashboard.Widgets, 1)
	assert.Equal(t, "promql", dashboard.Widgets[0].Query.QueryType)
	require.Len(t, dashboard.Widgets[0].Query.PromQL, 2)
	assert.Equal(t, []string{"A", "SM_collision_1"}, []string{
		dashboard.Widgets[0].Query.PromQL[0].Name,
		dashboard.Widgets[0].Query.PromQL[1].Name,
	})
}

func TestEmitV5SkipsHiddenUntranslatableTarget(t *testing.T) {
	t.Parallel()

	visible := model.Query{RefID: "A", Expression: "up", SourcePath: "/panels/0/targets/0"}
	hiddenExpression := model.Query{RefID: "B", Expression: "$A * 2", Hidden: true, SourcePath: "/panels/0/targets/1"}
	migration := model.Migration{
		Dashboard: model.Dashboard{Title: "Mixed", Panels: []model.Panel{{
			Title: "Mixed", Kind: model.PanelKindGraph, Grid: model.Grid{W: 24, H: 8},
			Queries: []model.Query{visible, hiddenExpression}, SourcePath: "/panels/0",
		}}},
		Translations: map[string]model.Translation{
			visible.SourcePath: {Kind: model.TranslationPromQL, PromQL: "up", Decision: model.Decision{Verdict: model.VerdictPassthrough}},
			hiddenExpression.SourcePath: {Kind: model.TranslationNone, Decision: model.Decision{
				Verdict: model.VerdictNeedsReview, Reasons: []model.ReasonCode{model.ReasonGrafanaExpression},
			}},
		},
	}

	dashboard := EmitV5(migration)
	require.Len(t, dashboard.Widgets, 1)
	require.Len(t, dashboard.Widgets[0].Query.PromQL, 1)
	assert.Equal(t, "A", dashboard.Widgets[0].Query.PromQL[0].Name)
	assert.NotContains(t, dashboard.Widgets[0].Query.PromQL[0].Query, "$A * 2")
}

func TestEmitV5OmitsPanelWhoseVisibleResultIsGrafanaExpression(t *testing.T) {
	t.Parallel()

	baseA := model.Query{RefID: "A", Expression: "errors", Hidden: true, SourcePath: "/panels/0/targets/0"}
	baseB := model.Query{RefID: "B", Expression: "requests", Hidden: true, SourcePath: "/panels/0/targets/1"}
	visible := model.Query{RefID: "C", Expression: "$A / $B", SourcePath: "/panels/0/targets/2"}
	migration := model.Migration{
		Dashboard: model.Dashboard{Title: "Expression", Panels: []model.Panel{{
			Title: "Ratio", Kind: model.PanelKindGraph, Queries: []model.Query{baseA, baseB, visible}, SourcePath: "/panels/0",
		}}},
		Translations: map[string]model.Translation{
			baseA.SourcePath: {Kind: model.TranslationPromQL, PromQL: "errors", Decision: model.Decision{Verdict: model.VerdictNeedsReview}},
			baseB.SourcePath: {Kind: model.TranslationPromQL, PromQL: "requests", Decision: model.Decision{Verdict: model.VerdictNeedsReview}},
			visible.SourcePath: {Kind: model.TranslationNone, Decision: model.Decision{
				Verdict: model.VerdictNeedsReview, Reasons: []model.ReasonCode{model.ReasonGrafanaExpression},
			}},
		},
	}

	assert.False(t, migration.PanelEmittable(migration.Dashboard.Panels[0]))
	assert.Empty(t, EmitV5(migration).Widgets)
}

func TestEmitV5LayoutNeverOverlapsOrExceedsGrid(t *testing.T) {
	t.Parallel()

	panels := []model.Panel{
		{Title: "A", Kind: model.PanelKindGraph, Grid: model.Grid{X: 0, Y: 0, W: 5, H: 4}, SourcePath: "/panels/0"},
		{Title: "B", Kind: model.PanelKindGraph, Grid: model.Grid{X: 5, Y: 0, W: 5, H: 4}, SourcePath: "/panels/1"},
		{Title: "C", Kind: model.PanelKindGraph, Grid: model.Grid{X: 10, Y: 0, W: 7, H: 4}, SourcePath: "/panels/2"},
		{Title: "D", Kind: model.PanelKindGraph, Grid: model.Grid{X: 17, Y: 0, W: 7, H: 4}, SourcePath: "/panels/3"},
	}
	translations := make(map[string]model.Translation)
	for index := range panels {
		query := model.Query{RefID: "A", Expression: "up", SourcePath: panels[index].SourcePath + "/targets/0"}
		panels[index].Queries = []model.Query{query}
		translations[query.SourcePath] = model.Translation{Kind: model.TranslationPromQL, PromQL: "up", Decision: model.Decision{Verdict: model.VerdictPassthrough}}
	}
	dashboard := EmitV5(model.Migration{Dashboard: model.Dashboard{Title: "Grid", Panels: panels}, Translations: translations})

	require.Len(t, dashboard.Layout, 4)
	for left := range dashboard.Layout {
		layout := dashboard.Layout[left]
		assert.GreaterOrEqual(t, layout.X, 0)
		assert.Greater(t, layout.W, 0)
		assert.LessOrEqual(t, layout.X+layout.W, 12)
		for right := left + 1; right < len(dashboard.Layout); right++ {
			assert.False(t, layoutsOverlap(layout, dashboard.Layout[right]), "%+v overlaps %+v", layout, dashboard.Layout[right])
		}
	}
}

func TestEmitV5PreservesRowContainmentAndCollapseState(t *testing.T) {
	t.Parallel()

	row := model.Panel{
		Title: "Database", Kind: model.PanelKindRow, Grid: model.Grid{X: 0, Y: 0, W: 24, H: 1},
		Collapsed: true, SourcePath: "/rows/0",
	}
	child := model.Panel{
		Title: "Queries", Kind: model.PanelKindGraph, Grid: model.Grid{X: 0, Y: 1, W: 12, H: 6},
		SourcePath: "/rows/0/panels/0",
	}
	query := model.Query{RefID: "A", Expression: "up", SourcePath: child.SourcePath + "/targets/0"}
	child.Queries = []model.Query{query}
	dashboard := EmitV5(model.Migration{
		Dashboard: model.Dashboard{Title: "Rows", UID: "rows", Panels: []model.Panel{row, child}},
		Translations: map[string]model.Translation{query.SourcePath: {
			Kind: model.TranslationPromQL, PromQL: "up", Decision: model.Decision{Verdict: model.VerdictPassthrough},
		}},
	})

	require.Len(t, dashboard.Layout, 1)
	require.Len(t, dashboard.Widgets, 2)
	rowID := dashboard.Layout[0].I
	require.Contains(t, dashboard.PanelMap, rowID)
	assert.True(t, dashboard.PanelMap[rowID].Collapsed)
	require.Len(t, dashboard.PanelMap[rowID].Widgets, 1)
	assert.Equal(t, dashboard.Widgets[1].ID, dashboard.PanelMap[rowID].Widgets[0].I)
}

func TestEmitV5ModernExpandedRowReassociatesFollowingSiblings(t *testing.T) {
	t.Parallel()

	// Grafana schemaVersion >= 16: an expanded row carries an empty panels[] and
	// its children follow it as top-level siblings until the next row. They must
	// land in the row's panelMap group so collapsing the row hides them.
	expandedRow := model.Panel{
		Title: "Expanded Row", Kind: model.PanelKindRow, Collapsed: false,
		Grid: model.Grid{X: 0, Y: 0, W: 24, H: 1}, SourcePath: "/panels/0",
	}
	child := model.Panel{
		Title: "Sibling Child", Kind: model.PanelKindGraph,
		Grid: model.Grid{X: 0, Y: 1, W: 12, H: 8}, SourcePath: "/panels/1",
		Queries: []model.Query{{RefID: "A", Expression: "up", SourcePath: "/panels/1/targets/0"}},
	}
	nextRow := model.Panel{
		Title: "Collapsed Row", Kind: model.PanelKindRow, Collapsed: true,
		Grid: model.Grid{X: 0, Y: 9, W: 24, H: 1}, SourcePath: "/panels/2",
	}
	nested := model.Panel{
		Title: "Nested Child", Kind: model.PanelKindGraph,
		Grid: model.Grid{X: 0, Y: 10, W: 12, H: 8}, SourcePath: "/panels/2/panels/0",
		Queries: []model.Query{{RefID: "A", Expression: "up", SourcePath: "/panels/2/panels/0/targets/0"}},
	}
	translations := map[string]model.Translation{
		"/panels/1/targets/0":          {Kind: model.TranslationPromQL, PromQL: "up", Decision: model.Decision{Verdict: model.VerdictPassthrough}},
		"/panels/2/panels/0/targets/0": {Kind: model.TranslationPromQL, PromQL: "up", Decision: model.Decision{Verdict: model.VerdictPassthrough}},
	}

	dashboard := EmitV5(model.Migration{
		Dashboard:    model.Dashboard{Title: "Modern rows", UID: "modern", Panels: []model.Panel{expandedRow, child, nextRow, nested}},
		Translations: translations,
	})

	expandedID := stableID("modern", "/panels/0")
	collapsedID := stableID("modern", "/panels/2")
	require.Contains(t, dashboard.PanelMap, expandedID)
	require.Contains(t, dashboard.PanelMap, collapsedID)
	assert.False(t, dashboard.PanelMap[expandedID].Collapsed)
	require.Len(t, dashboard.PanelMap[expandedID].Widgets, 1, "expanded row must own its following sibling")
	assert.True(t, dashboard.PanelMap[collapsedID].Collapsed)
	require.Len(t, dashboard.PanelMap[collapsedID].Widgets, 1, "collapsed row keeps its nested child")
}

func TestEmitV5ExpandedRowChildrenAreVisibleAndToggleReady(t *testing.T) {
	t.Parallel()

	row := model.Panel{
		Title: "Expanded", Kind: model.PanelKindRow, Grid: model.Grid{X: 0, Y: 0, W: 24, H: 1},
		SourcePath: "/rows/0",
	}
	left := model.Panel{
		Title: "Left", Kind: model.PanelKindGraph, Grid: model.Grid{X: 0, Y: 1, W: 12, H: 6},
		SourcePath: "/rows/0/panels/0",
	}
	right := model.Panel{
		Title: "Right", Kind: model.PanelKindGraph, Grid: model.Grid{X: 12, Y: 1, W: 12, H: 6},
		SourcePath: "/rows/0/panels/1",
	}
	following := model.Panel{
		Title: "Following", Kind: model.PanelKindGraph, Grid: model.Grid{X: 0, Y: 7, W: 24, H: 4},
		SourcePath: "/rows/1/panels/0",
	}
	panels := []model.Panel{row, left, right, following}
	translations := make(map[string]model.Translation)
	for index := 1; index < len(panels); index++ {
		query := model.Query{RefID: "A", Expression: "up", SourcePath: panels[index].SourcePath + "/targets/0"}
		panels[index].Queries = []model.Query{query}
		translations[query.SourcePath] = model.Translation{
			Kind: model.TranslationPromQL, PromQL: "up", Decision: model.Decision{Verdict: model.VerdictPassthrough},
		}
	}

	dashboard := EmitV5(model.Migration{
		Dashboard:    model.Dashboard{Title: "Expanded rows", UID: "expanded", Panels: panels},
		Translations: translations,
	})
	require.Len(t, dashboard.Layout, 4, "expanded children must be present in the rendered layout")
	rowID := dashboard.Widgets[0].ID
	require.Contains(t, dashboard.PanelMap, rowID)
	group := dashboard.PanelMap[rowID]
	assert.False(t, group.Collapsed)
	require.Len(t, group.Widgets, 2)
	assert.Equal(t, dashboard.Layout[1:3], group.Widgets, "panelMap must retain the same layouts used while expanded")
	for leftIndex := range dashboard.Layout {
		for rightIndex := leftIndex + 1; rightIndex < len(dashboard.Layout); rightIndex++ {
			assert.False(t, layoutsOverlap(dashboard.Layout[leftIndex], dashboard.Layout[rightIndex]))
		}
	}
}

func TestEmitV5CollapsedRowLayoutExpandsWithoutOverlap(t *testing.T) {
	t.Parallel()

	row := model.Panel{
		Title: "Collapsed", Kind: model.PanelKindRow, Grid: model.Grid{X: 0, Y: 0, W: 24, H: 1},
		Collapsed: true, SourcePath: "/rows/0",
	}
	child := model.Panel{
		Title: "Hidden child", Kind: model.PanelKindGraph, Grid: model.Grid{X: 0, Y: 1, W: 24, H: 6},
		SourcePath: "/rows/0/panels/0",
	}
	following := model.Panel{
		Title: "Following", Kind: model.PanelKindGraph, Grid: model.Grid{X: 0, Y: 1, W: 24, H: 4},
		SourcePath: "/rows/1/panels/0",
	}
	childQuery := model.Query{RefID: "A", Expression: "up", SourcePath: child.SourcePath + "/targets/0"}
	followingQuery := model.Query{RefID: "A", Expression: "up", SourcePath: following.SourcePath + "/targets/0"}
	child.Queries = []model.Query{childQuery}
	following.Queries = []model.Query{followingQuery}
	dashboard := EmitV5(model.Migration{
		Dashboard: model.Dashboard{Title: "Collapsed rows", UID: "collapsed", Panels: []model.Panel{row, child, following}},
		Translations: map[string]model.Translation{
			childQuery.SourcePath: {
				Kind: model.TranslationPromQL, PromQL: "up", Decision: model.Decision{Verdict: model.VerdictPassthrough},
			},
			followingQuery.SourcePath: {
				Kind: model.TranslationPromQL, PromQL: "up", Decision: model.Decision{Verdict: model.VerdictPassthrough},
			},
		},
	})

	require.Len(t, dashboard.Layout, 2, "collapsed child must not render in the top-level layout")
	rowLayout := dashboard.Layout[0]
	followingLayout := dashboard.Layout[1]
	assert.Equal(t, rowLayout.Y+rowLayout.H, followingLayout.Y, "collapsed rows must not reserve hidden child height twice")
	group := dashboard.PanelMap[rowLayout.I]
	require.True(t, group.Collapsed)
	require.Len(t, group.Widgets, 1)
	childLayout := group.Widgets[0]
	expansionShift := childLayout.Y + childLayout.H - (rowLayout.Y + rowLayout.H)
	expandedFollowing := followingLayout
	expandedFollowing.Y += expansionShift
	assert.False(t, layoutsOverlap(childLayout, expandedFollowing), "SigNoz's expansion shift must reveal children without overlap")
}

func TestEmitV5UsesExplicitSourceIdentityForDashboardWithoutGrafanaUID(t *testing.T) {
	t.Parallel()

	left := model.Migration{Dashboard: model.Dashboard{
		Title: "No UID", Source: model.Source{Path: "/tmp/run-one/source.json", Identity: "sha256:same-content"},
	}}
	right := model.Migration{Dashboard: model.Dashboard{
		Title: "No UID", Source: model.Source{Path: "/tmp/run-two/source.json", Identity: "sha256:same-content"},
	}}
	different := model.Migration{Dashboard: model.Dashboard{
		Title: "No UID", Source: model.Source{Path: "/tmp/run-three/source.json", Identity: "sha256:different-content"},
	}}
	renamed := left
	renamed.Dashboard.Title = "Renamed without UID"

	assert.Equal(t, EmitV5(left).UUID, EmitV5(right).UUID)
	assert.Equal(t, EmitV5(left).UUID, EmitV5(renamed).UUID)
	assert.NotEqual(t, EmitV5(left).UUID, EmitV5(different).UUID)
}

func TestEmitV5ScopesGrafanaUIDByStableSourceNamespace(t *testing.T) {
	t.Parallel()

	production := model.Migration{Dashboard: model.Dashboard{
		Title: "Service overview", UID: "shared-uid",
		Source: model.Source{Namespace: "grafana-org:production", Identity: "/exports/service.json"},
	}}
	renamed := production
	renamed.Dashboard.Title = "Service overview v2"
	renamed.Dashboard.Source.Identity = "/relocated/service.json"
	staging := production
	staging.Dashboard.Source.Namespace = "grafana-org:staging"

	assert.Equal(t, EmitV5(production).UUID, EmitV5(renamed).UUID,
		"a Grafana UID must remain stable across content and export-path changes inside one namespace")
	assert.NotEqual(t, EmitV5(production).UUID, EmitV5(staging).UUID,
		"the same Grafana UID in different organizations must not overwrite one target dashboard")
}

func TestEmitV5KeepsModernRowTargetsStructuralAndEmitsChildQuery(t *testing.T) {
	t.Parallel()

	rowQuery := model.Query{
		RefID: "A", Expression: "stale_row_metric", SourcePath: "/panels/0/targets/0",
	}
	childQuery := model.Query{
		RefID: "B", Expression: "sum(up)", SourcePath: "/panels/0/panels/0/targets/0",
	}
	row := model.Panel{
		Title: "Database", Kind: model.PanelKindRow, Grid: model.Grid{X: 0, Y: 0, W: 24, H: 1},
		Collapsed: true, SourcePath: "/panels/0", Queries: []model.Query{rowQuery},
	}
	child := model.Panel{
		Title: "Queries", Kind: model.PanelKindGraph, Grid: model.Grid{X: 0, Y: 1, W: 12, H: 6},
		SourcePath: "/panels/0/panels/0", Queries: []model.Query{childQuery},
	}
	migration := model.Migration{
		Dashboard: model.Dashboard{Title: "Rows", UID: "rows", Panels: []model.Panel{row, child}},
		Translations: map[string]model.Translation{
			rowQuery.SourcePath: {
				Kind: model.TranslationNone,
				Decision: model.Decision{
					Verdict: model.VerdictNeedsReview,
					Reasons: []model.ReasonCode{model.ReasonRowPanelTarget},
				},
			},
			childQuery.SourcePath: {
				Kind: model.TranslationBuilder,
				Builder: &model.BuilderQuery{
					Name: "B", MetricName: "up", SpaceAggregation: "sum",
				},
				Decision: model.Decision{Verdict: model.VerdictNative},
			},
		},
	}

	dashboard := EmitV5(migration)
	require.Len(t, dashboard.Widgets, 2)
	require.Len(t, dashboard.Layout, 1)
	rowWidget := dashboard.Widgets[0]
	childWidget := dashboard.Widgets[1]
	assert.Equal(t, "row", rowWidget.PanelTypes)
	assert.Equal(t, "builder", rowWidget.Query.QueryType)
	assert.Empty(t, rowWidget.Query.Builder.QueryData)
	assert.Empty(t, rowWidget.Query.Builder.QueryFormulas)
	require.Len(t, rowWidget.Query.PromQL, 1)
	assert.Empty(t, rowWidget.Query.PromQL[0].Query)
	require.Len(t, rowWidget.Query.ClickHouseSQL, 1)
	assert.Empty(t, rowWidget.Query.ClickHouseSQL[0].Query)

	assert.Equal(t, "builder", childWidget.Query.QueryType)
	require.Len(t, childWidget.Query.Builder.QueryData, 1)
	assert.Equal(t, "B", childWidget.Query.Builder.QueryData[0].QueryName)
	assert.Equal(t, "up", childWidget.Query.Builder.QueryData[0].Aggregations[0].MetricName)
	assert.Empty(t, childWidget.Query.Builder.QueryFormulas)

	assert.Equal(t, rowWidget.ID, dashboard.Layout[0].I)
	require.Contains(t, dashboard.PanelMap, rowWidget.ID)
	assert.True(t, dashboard.PanelMap[rowWidget.ID].Collapsed)
	require.Len(t, dashboard.PanelMap[rowWidget.ID].Widgets, 1)
	assert.Equal(t, childWidget.ID, dashboard.PanelMap[rowWidget.ID].Widgets[0].I)
}

func TestEmitV5IsDeterministic(t *testing.T) {
	t.Parallel()

	migration := model.Migration{Dashboard: model.Dashboard{Title: "Empty"}, Translations: map[string]model.Translation{}}
	first, err := json.Marshal(EmitV5(migration))
	require.NoError(t, err)
	second, err := json.Marshal(EmitV5(migration))
	require.NoError(t, err)
	assert.Equal(t, first, second)
}

func TestEmitV5PreservesVariableSelections(t *testing.T) {
	t.Parallel()

	migration := model.Migration{Dashboard: model.Dashboard{
		UID: "node",
		Variables: []model.Variable{
			{Name: "job", Label: "Prometheus job", Kind: model.VariableKindQuery, Query: "label_values(up, job)", Current: []string{"node-exporter"}, SourcePath: "/templating/list/0"},
			{Name: "hosts", Kind: model.VariableKindQuery, Query: "label_values(up, instance)", Current: []string{"a", "b"}, Multi: true, SourcePath: "/templating/list/1"},
			{Name: "all_hosts", Kind: model.VariableKindQuery, Query: "label_values(up, instance)", Current: []string{"$__all"}, Multi: true, IncludeAll: true, SourcePath: "/templating/list/2"},
		},
	}, VariableTranslations: map[string]model.VariableTranslation{
		"/templating/list/0": {Kind: "dynamic", Attribute: "job", Decision: model.Decision{Verdict: model.VerdictNative}},
		"/templating/list/1": {Kind: "dynamic", Attribute: "instance", Decision: model.Decision{Verdict: model.VerdictNative}},
		"/templating/list/2": {Kind: "dynamic", Attribute: "instance", Decision: model.Decision{Verdict: model.VerdictNative}},
	}}

	dashboard := EmitV5(migration)
	require.Len(t, dashboard.Variables, 3)
	variablesByName := make(map[string]VariableV5, len(dashboard.Variables))
	for _, variable := range dashboard.Variables {
		variablesByName[variable.Name] = variable
	}
	assert.Equal(t, "node-exporter", variablesByName["job"].SelectedValue)
	assert.Equal(t, "node-exporter", variablesByName["job"].DefaultValue)
	assert.Equal(t, "Prometheus job", variablesByName["job"].Description)
	assert.Equal(t, []string{"a", "b"}, variablesByName["hosts"].SelectedValue)
	assert.Equal(t, "a", variablesByName["hosts"].DefaultValue)
	assert.True(t, variablesByName["all_hosts"].AllSelected)
	assert.Nil(t, variablesByName["all_hosts"].SelectedValue)
}

func TestEmitV5OmitsNonDynamicAllWithoutProvenSelection(t *testing.T) {
	t.Parallel()

	for _, current := range []string{"All", "$__all", "__all__"} {
		t.Run(current, func(t *testing.T) {
			t.Parallel()

			migration := model.Migration{Dashboard: model.Dashboard{
				UID: "node",
				Variables: []model.Variable{{
					Name: "environment", Kind: model.VariableKindCustom, Query: "prod,stage",
					Current: []string{current}, Multi: true, IncludeAll: true,
					SourcePath: "/templating/list/0",
				}},
			}, VariableTranslations: map[string]model.VariableTranslation{
				// Exercise the target-side safety boundary independently from the
				// migration classifier: even a stale/hand-built translation must
				// not serialize a non-dynamic All sentinel.
				"/templating/list/0": {
					Kind: "custom", Decision: model.Decision{Verdict: model.VerdictNative},
				},
			}}

			dashboard := EmitV5(migration)
			assert.Empty(t, dashboard.Variables)
			encoded, err := json.Marshal(dashboard)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), current)
		})
	}
}

func TestEmitV5PreservesExplicitNonDynamicSelectionList(t *testing.T) {
	t.Parallel()

	migration := model.Migration{Dashboard: model.Dashboard{
		UID: "node",
		Variables: []model.Variable{{
			Name: "environment", Kind: model.VariableKindCustom, Query: "prod,stage",
			Current: []string{"prod", "stage"}, Multi: true, IncludeAll: true,
			SourcePath: "/templating/list/0",
		}},
	}, VariableTranslations: map[string]model.VariableTranslation{
		"/templating/list/0": {Kind: "custom", Decision: model.Decision{Verdict: model.VerdictNative}},
	}}

	dashboard := EmitV5(migration)
	require.Len(t, dashboard.Variables, 1)
	for _, variable := range dashboard.Variables {
		assert.Equal(t, "prod,stage", variable.CustomValue)
		assert.Equal(t, []string{"prod", "stage"}, variable.SelectedValue)
		assert.Equal(t, "prod", variable.DefaultValue)
		assert.False(t, variable.AllSelected)
	}
}

func TestEmitV5PinsCustomOptionsToCurrentSelection(t *testing.T) {
	t.Parallel()

	migration := model.Migration{Dashboard: model.Dashboard{
		UID: "node",
		Variables: []model.Variable{{
			Name: "environment", Kind: model.VariableKindCustom, Query: "prod,stage",
			Current: []string{"prod"}, Multi: true, SourcePath: "/templating/list/0",
		}},
	}, VariableTranslations: map[string]model.VariableTranslation{
		"/templating/list/0": {
			Kind: "custom", CustomValue: "prod",
			Decision: model.Decision{Verdict: model.VerdictNeedsReview},
		},
	}}

	dashboard := EmitV5(migration)
	require.Len(t, dashboard.Variables, 1)
	for _, variable := range dashboard.Variables {
		assert.Equal(t, "prod", variable.CustomValue)
		assert.Equal(t, []string{"prod"}, variable.SelectedValue)
		assert.Equal(t, "prod", variable.DefaultValue)
	}
}

func TestEmitV5RejectsStaleOrLossyCustomTranslation(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		current     []string
		customValue string
	}{
		"stale translation": {current: []string{"prod"}, customValue: "stage"},
		"lossy current":     {current: []string{"001"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			migration := model.Migration{Dashboard: model.Dashboard{
				UID: "node", Variables: []model.Variable{{
					Name: "environment", Kind: model.VariableKindCustom, Query: "prod,stage",
					Current: test.current, SourcePath: "/templating/list/0",
				}},
			}, VariableTranslations: map[string]model.VariableTranslation{
				"/templating/list/0": {
					Kind: "custom", CustomValue: test.customValue,
					Decision: model.Decision{Verdict: model.VerdictNative},
				},
			}}

			assert.Empty(t, EmitV5(migration).Variables)
		})
	}
}

func TestEmitV5NeverTruncatesContradictorySelectionArray(t *testing.T) {
	t.Parallel()

	migration := model.Migration{Dashboard: model.Dashboard{
		UID: "node",
		Variables: []model.Variable{{
			Name: "job", Kind: model.VariableKindQuery, Query: "label_values(up, job)",
			Current: []string{"api", "worker"}, Multi: false,
			SourcePath: "/templating/list/0",
		}},
	}, VariableTranslations: map[string]model.VariableTranslation{
		// A normal migration rejects this contradictory source shape before
		// emission. This hand-built translation exercises the emitter's final
		// defensive boundary: it may normalize to multi-select, but must never
		// silently validate one array and persist only its first value.
		"/templating/list/0": {Kind: "dynamic", Attribute: "job", Decision: model.Decision{Verdict: model.VerdictNative}},
	}}

	dashboard := EmitV5(migration)
	require.Len(t, dashboard.Variables, 1)
	for _, variable := range dashboard.Variables {
		assert.True(t, variable.MultiSelect)
		assert.Equal(t, []string{"api", "worker"}, variable.SelectedValue)
		assert.Equal(t, "api", variable.DefaultValue)
	}
}

func TestEmitV5PersistsEveryRecognizedDynamicAllSpelling(t *testing.T) {
	t.Parallel()

	for _, current := range []string{"All", "$__all", "__all__"} {
		t.Run(current, func(t *testing.T) {
			t.Parallel()

			migration := model.Migration{Dashboard: model.Dashboard{
				UID: "node",
				Variables: []model.Variable{{
					Name: "instance", Kind: model.VariableKindQuery,
					Current: []string{current}, IncludeAll: true,
					SourcePath: "/templating/list/0",
				}},
			}, VariableTranslations: map[string]model.VariableTranslation{
				"/templating/list/0": {
					Kind: "dynamic", Attribute: "instance",
					Decision: model.Decision{Verdict: model.VerdictNative},
				},
			}}

			dashboard := EmitV5(migration)
			require.Len(t, dashboard.Variables, 1)
			for _, variable := range dashboard.Variables {
				assert.True(t, variable.AllSelected)
				assert.True(t, variable.MultiSelect)
				assert.True(t, variable.ShowAllOption)
				assert.Nil(t, variable.SelectedValue)
				assert.Empty(t, variable.DefaultValue)
			}
		})
	}
}

func withoutLayoutFlags(layout Layout) Layout {
	layout.Moved = false
	layout.Static = false
	return layout
}
