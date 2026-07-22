package integration_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mansiverma897993/noz-in/internal/migrate"
	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/mansiverma897993/noz-in/internal/report"
	"github.com/mansiverma897993/noz-in/internal/source/grafana"
	"github.com/mansiverma897993/noz-in/internal/target/signoz"
	"github.com/mansiverma897993/noz-in/internal/transpile"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGrafanaV11HostileDashboardEndToEnd(t *testing.T) {
	t.Parallel()

	dashboard, err := grafana.ParseFile("testdata/grafana-v11-hostile.json")
	require.NoError(t, err)
	require.Equal(t, 41, dashboard.Source.SchemaVersion)
	require.Equal(t, model.SourceInventory{
		Captured: true, Panels: 12, Queries: 28, Variables: 8, SourceFeatures: dashboard.SourceInventory.SourceFeatures,
	}, dashboard.SourceInventory)
	require.Greater(t, dashboard.SourceInventory.SourceFeatures, 20)

	assertFeature(t, dashboard.SourceFeatures, "/time", model.ReasonUnmappedDashboardConfig)
	assertFeature(t, dashboard.SourceFeatures, "/timezone", model.ReasonUnmappedDashboardConfig)
	assertFeature(t, dashboard.SourceFeatures, "/refresh", model.ReasonUnmappedDashboardConfig)
	assertFeature(t, dashboard.SourceFeatures, "/timepicker", model.ReasonUnmappedDashboardConfig)
	assertFeature(t, dashboard.SourceFeatures, "/annotations/list/0", model.ReasonAnnotationQuery)
	assertFeature(t, dashboard.SourceFeatures, "/links/0", model.ReasonDashboardLink)

	job := variableByName(t, dashboard, "job")
	assertFeature(t, job.SourceFeatures, "/templating/list/1/hide", model.ReasonUnmappedVariableConfig)
	assertFeature(t, job.SourceFeatures, "/templating/list/1/options", model.ReasonUnmappedVariableConfig)
	assertFeature(t, job.SourceFeatures, "/templating/list/1/refresh", model.ReasonUnmappedVariableConfig)
	assertFeature(t, job.SourceFeatures, "/templating/list/1/skipUrlSync", model.ReasonUnmappedVariableConfig)
	assertFeature(t, job.SourceFeatures, "/templating/list/1/sort", model.ReasonUnmappedVariableConfig)
	assert.Equal(t, []string{"host-a", "host-b"}, variableByName(t, dashboard, "hosts").Current)
	assert.Equal(t, []string{"$__all"}, variableByName(t, dashboard, "all_hosts").Current)
	assert.Empty(t, variableByName(t, dashboard, "unset_all").Current)
	assert.Empty(t, variableByName(t, dashboard, "missing_current").Current)

	modifierPanel := panelByPath(t, dashboard, "/panels/0")
	assertFeature(t, modifierPanel.SourceFeatures, "/panels/0/transparent", model.ReasonUnmappedVisualization)
	assertFeature(t, modifierPanel.SourceFeatures, "/panels/0/fieldConfig/defaults/thresholds", model.ReasonFieldThresholds)
	assertFeature(t, modifierPanel.SourceFeatures, "/panels/0/fieldConfig/overrides/0", model.ReasonFieldOverrides)
	knobQuery := panelByPath(t, dashboard, "/panels/4").Queries[1]
	assertFeature(t, knobQuery.SourceFeatures, "/panels/4/targets/1/step", model.ReasonGrafanaIntervalControl)
	assertFeature(t, knobQuery.SourceFeatures, "/panels/4/targets/1/range", model.ReasonUnmappedQueryConfig)
	assertFeature(t, knobQuery.SourceFeatures, "/panels/4/targets/1/exemplar", model.ReasonUnmappedQueryConfig)
	assertFeature(t, knobQuery.SourceFeatures, "/panels/4/targets/1/editorMode", model.ReasonUnmappedQueryConfig)

	analyzer := transpile.NewAnalyzer(transpile.Options{
		RateInterval: 5 * time.Minute,
		Interval:     5 * time.Minute,
		Range:        6 * time.Hour,
		Metrics: map[string]model.TargetMetric{
			"up":                  {Type: "gauge", Temporality: "unspecified", Attributes: []string{"job", "instance"}},
			"http_requests_total": {Type: "sum", Temporality: "cumulative", IsMonotonic: true, Attributes: []string{"job", "instance"}},
			"errors_total":        {Type: "sum", Temporality: "cumulative", IsMonotonic: true},
			"requests_total":      {Type: "sum", Temporality: "cumulative", IsMonotonic: true},
		},
	})
	migration := migrate.Dashboard(dashboard, analyzer)
	payload := signoz.EmitV5(migration)
	evidence := report.Build(migration)

	assertTranslation(t, migration, "/panels/0/targets/0", model.TranslationPromQL, model.ReasonUnsupportedModifier)
	assertTranslation(t, migration, "/panels/0/targets/1", model.TranslationPromQL, model.ReasonUnsupportedModifier)
	// A resolved rate-interval on a known cumulative metric is now a Builder
	// candidate (offline it still ships PromQL passthrough via panel mode, and it
	// is never claimed native without a live promotion).
	assertTranslation(t, migration, "/panels/0/targets/2", model.TranslationBuilder, model.ReasonRateIntervalRewrite)
	assertTranslation(t, migration, "/panels/0/targets/2", model.TranslationBuilder, model.ReasonGrafanaIntervalControl)
	assertTranslation(t, migration, "/panels/0/targets/2", model.TranslationBuilder, model.ReasonUnmappedQueryConfig)
	assert.Equal(t, model.VerdictNeedsReview, migration.Translations["/panels/0/targets/2"].Decision.Verdict)
	assertTranslation(t, migration, "/panels/0/targets/3", model.TranslationPromQL, model.ReasonDynamicStructure)
	assertTranslation(t, migration, "/panels/1/targets/0", model.TranslationPromQL, model.ReasonUnsupportedOperator)
	assertTranslation(t, migration, "/panels/1/targets/2", model.TranslationPromQL, model.ReasonNonExactMetricSelector)
	assertTranslation(t, migration, "/panels/1/targets/4", model.TranslationPromQL, model.ReasonRegexVariable)
	assertTranslation(t, migration, "/panels/2/targets/4", model.TranslationNone, model.ReasonGrafanaVariableFormat)
	assertTranslation(t, migration, "/panels/3/targets/2", model.TranslationNone, model.ReasonGrafanaExpression)
	assertTranslation(t, migration, "/panels/4/targets/0", model.TranslationPromQL, model.ReasonInstantQuery)
	assertTranslation(t, migration, "/panels/4/targets/1", model.TranslationBuilder, model.ReasonGrafanaQueryFormat)
	assertTranslation(t, migration, "/panels/4/targets/2", model.TranslationNone, model.ReasonEmptyExpression)
	anchor := migration.Translations["/panels/1/targets/3"]
	assert.NotContains(t, anchor.Decision.Reasons, model.ReasonRegexVariable)
	assert.Contains(t, anchor.PromQL, `api$`)
	assert.Contains(t, migration.Translations["/panels/1/targets/0"].PromQL, `^ 2`)
	assert.NotContains(t, migration.Translations["/panels/0/targets/2"].PromQL, `$__rate_interval`)
	assert.Contains(t, migration.Translations["/panels/0/targets/2"].PromQL, `[5m]`)

	selectors := panelByPath(t, dashboard, "/panels/1")
	require.Len(t, selectors.Queries, 5)
	assert.Equal(t, []string{"A", "B", "C", "D", "E"}, queryRefs(selectors.Queries))
	assert.Equal(t, "A", selectors.Queries[1].OriginalRefID)
	assert.True(t, selectors.Queries[1].RefIDNormalized)
	assert.True(t, selectors.Queries[2].RefIDNormalized)
	assertTranslation(t, migration, selectors.Queries[1].SourcePath, model.TranslationBuilder, model.ReasonRefIDNormalized)
	assertTranslation(t, migration, selectors.Queries[2].SourcePath, model.TranslationPromQL, model.ReasonRefIDNormalized)

	require.Equal(t, 12, evidence.Summary.Panels)
	require.Equal(t, 28, evidence.Summary.Queries)
	require.Equal(t, 8, evidence.Summary.Variables)
	assert.Equal(t, evidence.Summary.Panels, evidence.Summary.PanelsAccounted)
	assert.Equal(t, evidence.Summary.Queries, evidence.Summary.QueriesAccounted)
	assert.Equal(t, evidence.Summary.Variables, evidence.Summary.VariablesAccounted)
	assert.Equal(t, evidence.Summary.SourceFeatures, evidence.Summary.SourceFeaturesAccounted)
	assert.Equal(t, dashboard.SourceInventory.SourceFeatures, evidence.Summary.SourceFeatures)
	assert.True(t, evidence.Summary.ReconciliationComplete)
	assert.Equal(t, 5, evidence.Summary.PanelsOmitted)

	assertPanelReason(t, evidence, "/panels/3", model.ReasonGrafanaExpression)
	assertPanelReason(t, evidence, "/panels/5", model.ReasonNoQueryTargets)
	assertPanelReason(t, evidence, "/panels/6", model.ReasonLibraryPanel)
	assertPanelReason(t, evidence, "/panels/7", model.ReasonTextPanel)
	assertPanelReason(t, evidence, "/panels/8", model.ReasonUnsupportedPanel)
	for _, path := range []string{"/panels/3", "/panels/5", "/panels/6", "/panels/7", "/panels/8"} {
		assert.Equal(t, string(model.TranslationNone), reportPanelByPath(t, evidence, path).EmittedMode, path)
	}

	require.Len(t, payload.Widgets, 7)
	require.Len(t, payload.Layout, 5)
	require.Len(t, payload.PanelMap, 1)
	assert.Equal(t, []string{
		"/panels/0", "/panels/1", "/panels/2", "/panels/4", "/panels/9",
		"/panels/9/panels/0", "/panels/9/panels/1",
	}, emittedWidgetPaths(payload))
	assertLayoutSet(t, payload.Layout)
	for _, group := range payload.PanelMap {
		assert.True(t, group.Collapsed)
		require.Len(t, group.Widgets, 2)
		assertLayoutSet(t, group.Widgets)
	}
	assertUniqueWidgetAndQueryIDs(t, payload)

	variablesByName := emittedVariablesByName(payload)
	require.Len(t, variablesByName, 6)
	assert.Equal(t, []string{"host-a", "host-b"}, variablesByName["hosts"].SelectedValue)
	assert.Equal(t, "host-a", variablesByName["hosts"].DefaultValue)
	assert.True(t, variablesByName["all_hosts"].AllSelected)
	assert.True(t, variablesByName["custom_all"].AllSelected)
	assert.Nil(t, variablesByName["all_hosts"].SelectedValue)
	_, hasUnsetAll := variablesByName["unset_all"]
	_, hasMissingCurrent := variablesByName["missing_current"]
	assert.False(t, hasUnsetAll)
	assert.False(t, hasMissingCurrent)
	assertVariableReason(t, evidence, "unset_all", model.ReasonMissingVariableValue)
	assertVariableReason(t, evidence, "missing_current", model.ReasonMissingVariableValue)
	assertPanelReason(t, evidence, "/panels/2", model.ReasonGrafanaVariableFormat)
	assertVariableReason(t, evidence, "custom_all", model.ReasonVariableAllValue)
	assertVariableReason(t, evidence, "job", model.ReasonVariableRegex)
	assertVariableReason(t, evidence, "job", model.ReasonUnmappedVariableConfig)

	payloadJSON, err := json.Marshal(payload)
	require.NoError(t, err)
	emitted := string(payloadJSON)
	assert.Contains(t, emitted, "offset 1h")
	assert.Contains(t, emitted, "@ 1700000000")
	assert.Contains(t, emitted, "api$")
	assert.Contains(t, emitted, `${1}`)
	assert.NotContains(t, emitted, `${job}`)
	assert.NotContains(t, emitted, `[[hosts]]`)
	assert.NotContains(t, emitted, `${job:regex}`)
	assert.NotContains(t, emitted, `${job:pipe}`)
	assert.NotContains(t, emitted, `${job:csv}`)
	assert.NotContains(t, emitted, `$A / $B`)
	assert.NotContains(t, emitted, `$missing_current`)
	assert.NotContains(t, emitted, `$unset_all`)

	evidenceJSON, err := json.Marshal(evidence)
	require.NoError(t, err)
	assert.Contains(t, string(evidenceJSON), `${job:csv}`)
	assert.Contains(t, string(evidenceJSON), `$A / $B`)
	assert.Contains(t, string(evidenceJSON), string(model.ReasonGrafanaExpression))
	assert.Contains(t, string(evidenceJSON), string(model.ReasonUnmappedDashboardConfig))
}

func variableByName(t *testing.T, dashboard model.Dashboard, name string) model.Variable {
	t.Helper()
	for _, variable := range dashboard.Variables {
		if variable.Name == name {
			return variable
		}
	}
	require.FailNow(t, "variable not found", name)
	return model.Variable{}
}

func panelByPath(t *testing.T, dashboard model.Dashboard, path string) model.Panel {
	t.Helper()
	for _, panel := range dashboard.Panels {
		if panel.SourcePath == path {
			return panel
		}
	}
	require.FailNow(t, "panel not found", path)
	return model.Panel{}
}

func reportPanelByPath(t *testing.T, evidence reporttypes.Report, path string) reporttypes.PanelRecord {
	t.Helper()
	for _, panel := range evidence.Panels {
		if panel.SourcePath == path {
			return panel
		}
	}
	require.FailNow(t, "report panel not found", path)
	return reporttypes.PanelRecord{}
}

func assertFeature(t *testing.T, features []model.SourceFeature, path string, reason model.ReasonCode) {
	t.Helper()
	for _, feature := range features {
		if feature.SourcePath == path && feature.Reason == reason {
			return
		}
	}
	assert.Fail(t, "source feature not found", "path=%s reason=%s features=%+v", path, reason, features)
}

func assertTranslation(t *testing.T, migration model.Migration, path string, kind model.TranslationKind, reason model.ReasonCode) {
	t.Helper()
	translation, ok := migration.Translations[path]
	require.True(t, ok, path)
	assert.Equal(t, kind, translation.Kind, path)
	assert.Contains(t, translation.Decision.Reasons, reason, path)
}

func assertPanelReason(t *testing.T, evidence reporttypes.Report, path string, reason model.ReasonCode) {
	t.Helper()
	panel := reportPanelByPath(t, evidence, path)
	assert.Contains(t, panel.ReasonCodes, string(reason), path)
}

func assertVariableReason(t *testing.T, evidence reporttypes.Report, name string, reason model.ReasonCode) {
	t.Helper()
	for _, variable := range evidence.Variables {
		if variable.Name == name {
			assert.Contains(t, variable.ReasonCodes, string(reason), name)
			return
		}
	}
	require.FailNow(t, "report variable not found", name)
}

func queryRefs(queries []model.Query) []string {
	refs := make([]string, 0, len(queries))
	for _, query := range queries {
		refs = append(refs, query.RefID)
	}
	return refs
}

func assertLayoutSet(t *testing.T, layouts []signoz.Layout) {
	t.Helper()
	for left, layout := range layouts {
		assert.GreaterOrEqual(t, layout.X, 0)
		assert.Greater(t, layout.W, 0)
		assert.LessOrEqual(t, layout.X+layout.W, 12)
		assert.Greater(t, layout.H, 0)
		for right := left + 1; right < len(layouts); right++ {
			assert.False(t, layoutsOverlap(layout, layouts[right]), "%+v overlaps %+v", layout, layouts[right])
		}
	}
}

func layoutsOverlap(left, right signoz.Layout) bool {
	return left.X < right.X+right.W && right.X < left.X+left.W &&
		left.Y < right.Y+right.H && right.Y < left.Y+left.H
}

func assertUniqueWidgetAndQueryIDs(t *testing.T, dashboard signoz.DashboardV5) {
	t.Helper()
	widgetIDs := make([]string, 0, len(dashboard.Widgets))
	for _, widget := range dashboard.Widgets {
		widgetIDs = append(widgetIDs, widget.ID)
		var names []string
		switch widget.Query.QueryType {
		case "builder":
			for _, query := range widget.Query.Builder.QueryData {
				names = append(names, query.QueryName)
			}
			for _, formula := range widget.Query.Builder.QueryFormulas {
				names = append(names, formula.QueryName)
			}
		case "promql":
			for _, query := range widget.Query.PromQL {
				names = append(names, query.Name)
			}
		default:
			assert.Fail(t, "unknown emitted query type", widget.Query.QueryType)
		}
		for _, name := range names {
			assert.NotEmpty(t, strings.TrimSpace(name), widget.Title)
		}
		unique := append([]string(nil), names...)
		slices.Sort(unique)
		unique = slices.Compact(unique)
		assert.Len(t, unique, len(names), widget.Title)
	}
	uniqueWidgets := append([]string(nil), widgetIDs...)
	slices.Sort(uniqueWidgets)
	uniqueWidgets = slices.Compact(uniqueWidgets)
	assert.Len(t, uniqueWidgets, len(widgetIDs))
}

func emittedVariablesByName(dashboard signoz.DashboardV5) map[string]signoz.VariableV5 {
	variables := make(map[string]signoz.VariableV5, len(dashboard.Variables))
	for _, variable := range dashboard.Variables {
		variables[variable.Name] = variable
	}
	return variables
}

func emittedWidgetPaths(dashboard signoz.DashboardV5) []string {
	paths := make([]string, 0, len(dashboard.Widgets))
	for _, widget := range dashboard.Widgets {
		paths = append(paths, widget.SourcePath)
	}
	slices.Sort(paths)
	return paths
}
