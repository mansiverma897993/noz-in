package migrate

import (
	"testing"

	"github.com/mansiverma897993/signoz/internal/model"
	"github.com/mansiverma897993/signoz/internal/transpile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDashboardAccountsForQueriesAndVariables(t *testing.T) {
	t.Parallel()

	dashboard := model.Dashboard{
		Panels: []model.Panel{{Queries: []model.Query{{SourcePath: "panels[0].targets[0]", RefID: "A", Expression: "sum(up)"}}}},
		Variables: []model.Variable{
			{SourcePath: "templating.list[0]", Name: "instance", Kind: model.VariableKindQuery, Query: "label_values(up, instance)"},
			{SourcePath: "templating.list[1]", Name: "result", Kind: model.VariableKindQuery, Query: "query_result(up)"},
			{SourcePath: "templating.list[2]", Name: "source", Kind: model.VariableKindDatasource},
		},
	}
	migration := Dashboard(dashboard, transpile.NewAnalyzer(transpile.Options{Metrics: map[string]model.TargetMetric{
		"up": {Type: "gauge"},
	}}))

	require.Len(t, migration.Translations, 1)
	assert.Equal(t, model.VerdictNeedsReview, migration.Translations["panels[0].targets[0]"].Decision.Verdict)
	assert.Contains(t, migration.Translations["panels[0].targets[0]"].Decision.Reasons, model.ReasonBuilderLatestLookback)
	assert.Equal(t, "none", migration.VariableTranslations["templating.list[0]"].Kind)
	assert.Contains(t, migration.VariableTranslations["templating.list[0]"].Decision.Reasons, model.ReasonMissingVariableValue)
	assert.Equal(t, model.VerdictNeedsReview, migration.VariableTranslations["templating.list[1]"].Decision.Verdict)
	assert.Contains(t, migration.VariableTranslations["templating.list[1]"].Decision.Reasons, model.ReasonQueryResultVariable)
	assert.Equal(t, "none", migration.VariableTranslations["templating.list[2]"].Kind)
}

func TestDashboardClassifiesRowTargetsAsStructuralOnly(t *testing.T) {
	t.Parallel()

	rowQuery := model.Query{RefID: "A", Expression: "up", SourcePath: "/panels/0/targets/0"}
	childQuery := model.Query{RefID: "A", Expression: "up", SourcePath: "/panels/0/panels/0/targets/0"}
	dashboard := model.Dashboard{Panels: []model.Panel{
		{ID: "row", Title: "Section", Kind: model.PanelKindRow, Queries: []model.Query{rowQuery}, SourcePath: "/panels/0"},
		{ID: "child", Title: "Child", Kind: model.PanelKindGraph, Queries: []model.Query{childQuery}, SourcePath: "/panels/0/panels/0"},
	}}
	migration := Dashboard(dashboard, transpile.NewAnalyzer(transpile.Options{Metrics: map[string]model.TargetMetric{
		"up": {Type: "gauge"},
	}}))

	row, ok := migration.TranslationFor(rowQuery)
	require.True(t, ok)
	assert.Equal(t, model.TranslationNone, row.Kind)
	assert.Equal(t, model.VerdictNeedsReview, row.Decision.Verdict)
	assert.Contains(t, row.Decision.Reasons, model.ReasonRowPanelTarget)
	assert.Empty(t, row.PromQL)
	assert.Nil(t, row.Builder)
	assert.Equal(t, model.TranslationBuilder, migration.PanelMode(dashboard.Panels[0]))
	child, ok := migration.TranslationFor(childQuery)
	require.True(t, ok)
	assert.Equal(t, model.TranslationBuilder, child.Kind)
}

func TestVariableTranslationPreservesMultipleReviewReasons(t *testing.T) {
	t.Parallel()

	translation := translateVariable(model.Variable{
		Kind: model.VariableKindQuery, Query: "label_values(up{job=\"$job\"}, instance)", Regex: "/node-.*/",
		IncludeAll: true, AllValue: ".+", Current: []string{"$__all"},
	}, transpile.NewAnalyzer(transpile.Options{}))
	assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
	assert.ElementsMatch(t, []model.ReasonCode{
		model.ReasonVariableSelectorScope, model.ReasonVariableRegex, model.ReasonChainedVariable,
		model.ReasonVariableAllValue,
	}, translation.Decision.Reasons)
	assert.Contains(t, translation.Decision.Notes, `Grafana allValue=".+" differs from target All matcher removal; current=["$__all"].`)
}

func TestVariableTranslationClassifiesCustomAllValueSemantics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		includeAll bool
		allValue   string
		wantReview bool
	}{
		{name: "unset", includeAll: true},
		{name: "empty", includeAll: true, allValue: "   "},
		{name: "default wildcard", includeAll: true, allValue: ".*"},
		{name: "trimmed default wildcard", includeAll: true, allValue: "  .*  "},
		{name: "one or more", includeAll: true, allValue: ".+", wantReview: true},
		{name: "other custom value", includeAll: true, allValue: "prod|stage", wantReview: true},
		{name: "inert while All disabled", allValue: ".+"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			translation := translateVariable(model.Variable{
				Kind: model.VariableKindQuery, Query: "label_values(instance)",
				IncludeAll: test.includeAll, AllValue: test.allValue, Current: []string{"$__all"},
			}, transpile.NewAnalyzer(transpile.Options{}))

			if test.wantReview {
				assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
				assert.Contains(t, translation.Decision.Reasons, model.ReasonVariableAllValue)
				require.Len(t, translation.Decision.Notes, 1)
				assert.Contains(t, translation.Decision.Notes[0], `current=["$__all"]`)
				return
			}
			assert.Equal(t, model.VerdictNative, translation.Decision.Verdict)
			assert.NotContains(t, translation.Decision.Reasons, model.ReasonVariableAllValue)
			assert.Empty(t, translation.Decision.Notes)
		})
	}
}

func TestVariableTranslationDefaultAllValuePreservesUnrelatedReviewReasons(t *testing.T) {
	t.Parallel()

	translation := translateVariable(model.Variable{
		Kind: model.VariableKindQuery, Query: "label_values(instance)", Regex: "/node-.*/",
		IncludeAll: true, AllValue: ".*", Current: []string{"$__all"},
	}, transpile.NewAnalyzer(transpile.Options{}))

	assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
	assert.Equal(t, []model.ReasonCode{model.ReasonVariableRegex}, translation.Decision.Reasons)
	assert.NotContains(t, translation.Decision.Reasons, model.ReasonVariableAllValue)
	assert.Empty(t, translation.Decision.Notes)
}

func TestVariableTranslationAccountsForSelectorScopeAndLabelRemap(t *testing.T) {
	t.Parallel()

	analyzer := transpile.NewAnalyzer(transpile.Options{})
	for name, query := range map[string]string{
		"bare metric":     "label_values(up, instance)",
		"static matcher":  `label_values(up{job="node"}, instance)`,
		"chained matcher": `label_values(up{job="$job"}, instance)`,
	} {
		t.Run(name, func(t *testing.T) {
			translation := translateVariable(model.Variable{Kind: model.VariableKindQuery, Query: query}, analyzer)

			assert.Equal(t, "dynamic", translation.Kind)
			assert.Equal(t, "service.instance.id", translation.Attribute)
			assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
			assert.Contains(t, translation.Decision.Reasons, model.ReasonVariableSelectorScope)
			if name == "chained matcher" {
				assert.Contains(t, translation.Decision.Reasons, model.ReasonChainedVariable)
			}
		})
	}

	global := translateVariable(model.Variable{Kind: model.VariableKindQuery, Query: "label_values(job)"}, analyzer)
	assert.Equal(t, model.VerdictNative, global.Decision.Verdict)
	assert.Equal(t, "service.name", global.Attribute)
}

func TestVariableTranslationMarksConstantTextboxMutability(t *testing.T) {
	t.Parallel()

	translation := translateVariable(model.Variable{
		Kind: model.VariableKindConstant, Query: "prod", Current: []string{"prod"},
	}, transpile.NewAnalyzer(transpile.Options{}))

	assert.Equal(t, "textbox", translation.Kind)
	assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
	assert.Contains(t, translation.Decision.Reasons, model.ReasonGrafanaConstantVariable)
}

func TestDashboardOmitsUnresolvedNonDynamicAllAndDependentQueries(t *testing.T) {
	t.Parallel()

	dashboard := model.Dashboard{
		Variables: []model.Variable{{
			Name: "environment", Kind: model.VariableKindCustom, Query: "prod,stage",
			Current: []string{"$__all"}, Multi: true, IncludeAll: true,
			SourcePath: "/templating/list/0",
		}},
		Panels: []model.Panel{{Queries: []model.Query{
			{RefID: "A", Expression: `up{environment=~"$environment"}`, SourcePath: "/panels/0/targets/0"},
			{RefID: "B", Expression: `up{environment=~"${environment}"}`, SourcePath: "/panels/0/targets/1"},
			{RefID: "C", Expression: `up{environment=~"[[environment]]"}`, SourcePath: "/panels/0/targets/2"},
			{RefID: "D", Expression: `up{environment=~"{{environment}}"}`, SourcePath: "/panels/0/targets/3"},
			{RefID: "E", Expression: `up{environment=~"{{.environment}}"}`, SourcePath: "/panels/0/targets/4"},
			{RefID: "F", Expression: `up{job="api"}`, SourcePath: "/panels/0/targets/5"},
		}}},
	}
	migration := Dashboard(dashboard, transpile.NewAnalyzer(transpile.Options{Metrics: map[string]model.TargetMetric{
		"up": {Type: "gauge"},
	}}))

	variable := migration.VariableTranslations["/templating/list/0"]
	assert.Equal(t, "none", variable.Kind)
	assert.Equal(t, model.VerdictNeedsReview, variable.Decision.Verdict)
	assert.Contains(t, variable.Decision.Reasons, model.ReasonMissingVariableValue)
	assert.Contains(t, variable.Decision.Reasons, model.ReasonVariableAllValue)
	assert.Contains(t, variable.Decision.Notes, `Grafana All is selected for non-dynamic variable "environment", but the normalized export has no proven complete option list; the target variable and dependent queries were omitted.`)

	for _, path := range []string{
		"/panels/0/targets/0", "/panels/0/targets/1", "/panels/0/targets/2",
		"/panels/0/targets/3", "/panels/0/targets/4",
	} {
		translation := migration.Translations[path]
		assert.Equal(t, model.TranslationNone, translation.Kind, path)
		assert.Nil(t, translation.Builder, path)
		assert.Nil(t, translation.Formula, path)
		assert.Empty(t, translation.PromQL, path)
		assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict, path)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonMissingVariableValue, path)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonVariableAllValue, path)
		assert.Contains(t, translation.Decision.Notes, `Query omitted because non-dynamic variable "environment" has Grafana All selected without a proven complete target value list.`, path)
	}
	for _, path := range []string{"/panels/0/targets/0", "/panels/0/targets/1", "/panels/0/targets/2"} {
		assert.Contains(t, migration.Translations[path].Decision.Reasons, model.ReasonRegexVariable, path)
	}

	unrelated := migration.Translations["/panels/0/targets/5"]
	assert.NotEqual(t, model.TranslationNone, unrelated.Kind)
	assert.NotContains(t, unrelated.Decision.Reasons, model.ReasonMissingVariableValue)
	assert.NotContains(t, unrelated.Decision.Reasons, model.ReasonVariableAllValue)
}

func TestDashboardOmitsContradictorySingleSelectArrayAndDependentQueries(t *testing.T) {
	t.Parallel()

	dashboard := model.Dashboard{
		Variables: []model.Variable{{
			Name: "job", Kind: model.VariableKindQuery, Query: "label_values(up, job)",
			Current: []string{"api", "worker"}, Multi: false,
			SourcePath: "/templating/list/0",
		}},
		Panels: []model.Panel{{Queries: []model.Query{
			{RefID: "A", Expression: `up{job=~"$job"}`, SourcePath: "/panels/0/targets/0"},
			{RefID: "B", Expression: `up{job=~"${job}"}`, SourcePath: "/panels/0/targets/1"},
			{RefID: "C", Expression: `up{job=~"[[job]]"}`, SourcePath: "/panels/0/targets/2"},
			{RefID: "D", Expression: `up{job=~"{{job}}"}`, SourcePath: "/panels/0/targets/3"},
			{RefID: "E", Expression: `up{job=~"{{.job}}"}`, SourcePath: "/panels/0/targets/4"},
			{RefID: "F", Expression: `up{environment="prod"}`, SourcePath: "/panels/0/targets/5"},
		}}},
	}
	migration := Dashboard(dashboard, transpile.NewAnalyzer(transpile.Options{Metrics: map[string]model.TargetMetric{
		"up": {Type: "gauge"},
	}}))

	variable := migration.VariableTranslations["/templating/list/0"]
	assert.Equal(t, "none", variable.Kind)
	assert.Equal(t, model.VerdictNeedsReview, variable.Decision.Verdict)
	assert.Contains(t, variable.Decision.Reasons, model.ReasonMissingVariableValue)
	assert.Contains(t, variable.Decision.Reasons, model.ReasonMultiVariableValue)
	assert.Contains(t, variable.Decision.Notes, `Grafana variable "job" has 2 current values while multi is disabled; the contradictory target variable and dependent queries were omitted.`)

	for _, path := range []string{
		"/panels/0/targets/0", "/panels/0/targets/1", "/panels/0/targets/2",
		"/panels/0/targets/3", "/panels/0/targets/4",
	} {
		translation := migration.Translations[path]
		assert.Equal(t, model.TranslationNone, translation.Kind, path)
		assert.Nil(t, translation.Builder, path)
		assert.Nil(t, translation.Formula, path)
		assert.Empty(t, translation.PromQL, path)
		assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict, path)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonMissingVariableValue, path)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonMultiVariableValue, path)
		assert.Contains(t, translation.Decision.Notes, `Query omitted because variable "job" has multiple current values while Grafana multi is disabled.`, path)
	}

	unrelated := migration.Translations["/panels/0/targets/5"]
	assert.NotEqual(t, model.TranslationNone, unrelated.Kind)
	assert.NotContains(t, unrelated.Decision.Reasons, model.ReasonMissingVariableValue)
	assert.NotContains(t, unrelated.Decision.Reasons, model.ReasonMultiVariableValue)
}

func TestDashboardPinsCustomVariableReloadToValidatedSelection(t *testing.T) {
	t.Parallel()

	dashboard := model.Dashboard{
		Variables: []model.Variable{{
			Name: "environment", Kind: model.VariableKindCustom, Query: "prod,stage",
			Current: []string{"prod"}, Multi: true, SourcePath: "/templating/list/0",
		}},
		Panels: []model.Panel{{Queries: []model.Query{{
			RefID: "A", Expression: `up{environment=~"$environment"}`,
			SourcePath: "/panels/0/targets/0",
		}}}},
	}
	migration := Dashboard(dashboard, transpile.NewAnalyzer(transpile.Options{
		Metrics: map[string]model.TargetMetric{"up": {Type: "gauge"}},
	}))

	variable := migration.VariableTranslations["/templating/list/0"]
	assert.Equal(t, "custom", variable.Kind)
	assert.Equal(t, "prod", variable.CustomValue)
	assert.Equal(t, model.VerdictNeedsReview, variable.Decision.Verdict)
	assert.Contains(t, variable.Decision.Reasons, model.ReasonCustomVariableReload)
	assert.Contains(t, variable.Decision.Notes,
		`Custom variable "environment" options were reduced to the proven current selection so target reload executes the value that was validated.`)
	assert.NotEqual(t, model.TranslationNone, migration.Translations["/panels/0/targets/0"].Kind)
}

func TestDashboardPreservesExactCompleteCustomSelection(t *testing.T) {
	t.Parallel()

	dashboard := model.Dashboard{Variables: []model.Variable{{
		Name: "environment", Kind: model.VariableKindCustom, Query: `prod,stage`,
		Current: []string{"prod", "stage"}, Multi: true, SourcePath: "/templating/list/0",
	}}}
	migration := Dashboard(dashboard, transpile.NewAnalyzer(transpile.Options{}))

	variable := migration.VariableTranslations["/templating/list/0"]
	assert.Equal(t, "custom", variable.Kind)
	assert.Equal(t, "prod,stage", variable.CustomValue)
	assert.Equal(t, model.VerdictNative, variable.Decision.Verdict)
	assert.NotContains(t, variable.Decision.Reasons, model.ReasonCustomVariableReload)
}

func TestDashboardOmitsLossyCustomReloadAndDependentQueries(t *testing.T) {
	t.Parallel()

	for _, current := range []string{"001", "display : prod", "\uFEFFprod"} {
		t.Run(current, func(t *testing.T) {
			t.Parallel()
			dashboard := model.Dashboard{
				Variables: []model.Variable{{
					Name: "environment", Kind: model.VariableKindCustom, Query: "prod,stage",
					Current: []string{current}, SourcePath: "/templating/list/0",
				}},
				Panels: []model.Panel{{Queries: []model.Query{{
					RefID: "A", Expression: `up{environment="$environment"}`,
					SourcePath: "/panels/0/targets/0",
				}}}},
			}
			migration := Dashboard(dashboard, transpile.NewAnalyzer(transpile.Options{
				Metrics: map[string]model.TargetMetric{"up": {Type: "gauge"}},
			}))

			variable := migration.VariableTranslations["/templating/list/0"]
			assert.Equal(t, "none", variable.Kind)
			assert.Empty(t, variable.CustomValue)
			assert.Contains(t, variable.Decision.Reasons, model.ReasonMissingVariableValue)
			assert.Contains(t, variable.Decision.Reasons, model.ReasonCustomVariableReload)

			query := migration.Translations["/panels/0/targets/0"]
			assert.Equal(t, model.TranslationNone, query.Kind)
			assert.Contains(t, query.Decision.Reasons, model.ReasonMissingVariableValue)
			assert.Contains(t, query.Decision.Reasons, model.ReasonCustomVariableReload)
			assert.Contains(t, query.Decision.Notes,
				`Query omitted because custom variable "environment" cannot reproduce its selected value after target dashboard reload.`)
		})
	}
}

func TestDashboardOmitsOnlyVariableReferencesWhosePinnedEscapingDiverges(t *testing.T) {
	t.Parallel()

	dashboard := model.Dashboard{
		Variables: []model.Variable{
			{
				Name: "host", Kind: model.VariableKindQuery, Query: "label_values(up, host)",
				Current: []string{"api.prod", "worker"}, Multi: true, SourcePath: "/templating/list/0",
			},
			{
				Name: "path", Kind: model.VariableKindQuery, Query: "label_values(up, path)",
				Current: []string{`api\west`}, SourcePath: "/templating/list/1",
			},
			{
				Name: "plain", Kind: model.VariableKindQuery, Query: "label_values(up, plain)",
				Current: []string{"api.prod"}, SourcePath: "/templating/list/2",
			},
		},
		Panels: []model.Panel{
			{Queries: []model.Query{{RefID: "A", Expression: `up{host=~"$host"}`, SourcePath: "/panels/0/targets/0"}}},
			{Queries: []model.Query{{RefID: "A", Expression: `up{host=~"${host:regex}"}`, SourcePath: "/panels/1/targets/0"}}},
			{Queries: []model.Query{{RefID: "A", Expression: `up{host=~"${host:pipe}"}`, SourcePath: "/panels/2/targets/0"}}},
			{Queries: []model.Query{{RefID: "A", Expression: `up{path=~"$path"}`, SourcePath: "/panels/3/targets/0"}}},
			{Queries: []model.Query{{RefID: "A", Expression: `up{plain=~"$plain"}`, SourcePath: "/panels/4/targets/0"}}},
			{Queries: []model.Query{{RefID: "A", Expression: `up{host="$host"}`, SourcePath: "/panels/5/targets/0"}}},
		},
	}
	migration := Dashboard(dashboard, transpile.NewAnalyzer(transpile.Options{
		Metrics: map[string]model.TargetMetric{"up": {Type: "gauge"}},
	}))

	for _, path := range []string{
		"/panels/0/targets/0", "/panels/1/targets/0", "/panels/3/targets/0", "/panels/5/targets/0",
	} {
		translation := migration.Translations[path]
		assert.Equal(t, model.TranslationNone, translation.Kind, path)
		assert.Empty(t, translation.PromQL, path)
		assert.Nil(t, translation.Builder, path)
		assert.Nil(t, translation.Formula, path)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonMissingVariableValue, path)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonVariableValueEscaping, path)
	}
	assert.NotEqual(t, model.TranslationNone, migration.Translations["/panels/2/targets/0"].Kind,
		"explicit pipe is the exact raw-join path in both pinned runtimes")
	assert.NotEqual(t, model.TranslationNone, migration.Translations["/panels/4/targets/0"].Kind,
		"regular scalar interpolation does not regex-escape a dot")

	for _, path := range []string{"/templating/list/0", "/templating/list/1"} {
		variable := migration.VariableTranslations[path]
		assert.Equal(t, model.VerdictNeedsReview, variable.Decision.Verdict, path)
		assert.Contains(t, variable.Decision.Reasons, model.ReasonVariableValueEscaping, path)
	}
	assert.NotContains(t, migration.VariableTranslations["/templating/list/2"].Decision.Reasons,
		model.ReasonVariableValueEscaping)
}

func TestDashboardOmitsValuesReinterpretedByPinnedSigNozRenderer(t *testing.T) {
	t.Parallel()

	dashboard := model.Dashboard{
		Variables: []model.Variable{
			{Name: "env", Kind: model.VariableKindQuery, Query: "label_values(up, env)", Current: []string{`{{.SIGNOZ_START_TIME}}`}, SourcePath: "/templating/list/0"},
			{Name: "longer", Kind: model.VariableKindQuery, Query: "label_values(up, longer)", Current: []string{"$x"}, SourcePath: "/templating/list/1"},
			{Name: "x", Kind: model.VariableKindQuery, Query: "label_values(up, x)", Current: []string{"prod"}, SourcePath: "/templating/list/2"},
		},
		Panels: []model.Panel{{Queries: []model.Query{
			{RefID: "A", Expression: `up{env="$env"}`, SourcePath: "/panels/0/targets/0"},
			{RefID: "B", Expression: `up{longer="$longer"}`, SourcePath: "/panels/0/targets/1"},
		}}},
	}
	migration := Dashboard(dashboard, transpile.NewAnalyzer(transpile.Options{
		Metrics: map[string]model.TargetMetric{"up": {Type: "gauge"}},
	}))

	for _, path := range []string{"/panels/0/targets/0", "/panels/0/targets/1"} {
		translation := migration.Translations[path]
		assert.Equal(t, model.TranslationNone, translation.Kind, path)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonVariableValueEscaping, path)
	}
}

func TestDashboardOmitsLiteralDynamicAllSentinelWithoutGrafanaAll(t *testing.T) {
	t.Parallel()

	dashboard := model.Dashboard{
		Variables: []model.Variable{{
			Name: "job", Kind: model.VariableKindQuery, Query: "label_values(up, job)",
			Current: []string{"__all__"}, SourcePath: "/templating/list/0",
		}},
		Panels: []model.Panel{{Queries: []model.Query{{
			RefID: "A", Expression: `up{job="$job"}`, SourcePath: "/panels/0/targets/0",
		}}}},
	}
	migration := Dashboard(dashboard, transpile.NewAnalyzer(transpile.Options{
		Metrics: map[string]model.TargetMetric{"up": {Type: "gauge"}},
	}))

	translation := migration.Translations["/panels/0/targets/0"]
	assert.Equal(t, model.TranslationNone, translation.Kind)
	assert.Contains(t, translation.Decision.Reasons, model.ReasonVariableValueEscaping)
	assert.Contains(t, translation.Decision.Reasons, model.ReasonVariableAllValue)
}

func TestDashboardOmitsReferencedVariableWithoutNonblankCurrentSelection(t *testing.T) {
	t.Parallel()

	for _, current := range [][]string{nil, {"   "}} {
		dashboard := model.Dashboard{
			Variables: []model.Variable{{
				Name: "job", Kind: model.VariableKindQuery, Query: "label_values(up, job)",
				Current: current, SourcePath: "/templating/list/0",
			}},
			Panels: []model.Panel{{Queries: []model.Query{{
				RefID: "A", Expression: `up{job="$job"}`, SourcePath: "/panels/0/targets/0",
			}}}},
		}
		migration := Dashboard(dashboard, transpile.NewAnalyzer(transpile.Options{
			Metrics: map[string]model.TargetMetric{"up": {Type: "gauge"}},
		}))

		assert.Equal(t, "none", migration.VariableTranslations["/templating/list/0"].Kind)
		translation := migration.Translations["/panels/0/targets/0"]
		assert.Equal(t, model.TranslationNone, translation.Kind)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonMissingVariableValue)
	}
}

func TestDashboardOmitsPromQLChangedOnlyByPinnedSigNozRenderer(t *testing.T) {
	t.Parallel()

	dashboard := model.Dashboard{
		Variables: []model.Variable{{
			Name: "env", Kind: model.VariableKindQuery, Query: "label_values(up, env)",
			Current: []string{"prod"}, SourcePath: "/templating/list/0",
		}},
		Panels: []model.Panel{{Queries: []model.Query{
			{RefID: "A", Expression: `up{label="$environment"} or up`, SourcePath: "/panels/0/targets/0"},
			{RefID: "B", Expression: `up{label="{{literal}}"} or up`, SourcePath: "/panels/0/targets/1"},
			{RefID: "C", Expression: `label_replace(up,"dst","$start_timestamp","src","(.*)")`, SourcePath: "/panels/0/targets/2"},
			{RefID: "D", Expression: `vector($__from)`, SourcePath: "/panels/0/targets/3"},
		}}},
	}
	migration := Dashboard(dashboard, transpile.NewAnalyzer(transpile.Options{
		Metrics: map[string]model.TargetMetric{"up": {Type: "gauge", Attributes: []string{"label"}}},
	}))

	for _, path := range []string{
		"/panels/0/targets/0", "/panels/0/targets/1", "/panels/0/targets/2",
	} {
		translation := migration.Translations[path]
		assert.Equal(t, model.TranslationNone, translation.Kind, path)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonVariableValueEscaping, path)
		assert.Contains(t, translation.Decision.Notes,
			"Query omitted because pinned SigNoz runtime-variable or Go-template rendering would change bytes Grafana sends unchanged to Prometheus.", path)
	}
	assert.Equal(t, model.TranslationPromQL, migration.Translations["/panels/0/targets/3"].Kind)
}

func TestDashboardChecksActualPromQLFallbackForBuilderAndFormulaCandidates(t *testing.T) {
	t.Parallel()

	dashboard := model.Dashboard{Panels: []model.Panel{
		{SourcePath: "/panels/0", Queries: []model.Query{{
			RefID: "A", Expression: `sum(up{label="{{literal}}"})`, SourcePath: "/panels/0/targets/0",
		}}},
		{SourcePath: "/panels/1", Queries: []model.Query{{
			RefID: "A", Expression: `sum(up{label="$start_timestamp"}) + sum(up)`, SourcePath: "/panels/1/targets/0",
		}}},
		{SourcePath: "/panels/2", Queries: []model.Query{{
			RefID: "A", Expression: `up{label="[[missing]]"} or up`, SourcePath: "/panels/2/targets/0",
		}}},
	}}
	migration := Dashboard(dashboard, transpile.NewAnalyzer(transpile.Options{
		Metrics: map[string]model.TargetMetric{"up": {Type: "gauge"}},
	}))

	for _, path := range []string{"/panels/0/targets/0", "/panels/1/targets/0", "/panels/2/targets/0"} {
		translation := migration.Translations[path]
		assert.Equal(t, model.TranslationNone, translation.Kind, path)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonVariableValueEscaping, path)
	}
}

func TestDashboardPreservesResolvedAllSelections(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		variable model.Variable
		wantKind string
	}{
		{
			name: "dynamic All remains representable",
			variable: model.Variable{
				Name: "instance", Kind: model.VariableKindQuery, Query: "label_values(instance)",
				Current: []string{"__all__"}, Multi: true, IncludeAll: true, AllValue: ".*",
				SourcePath: "/templating/list/0",
			},
			wantKind: "dynamic",
		},
		{
			name: "explicit non-dynamic scalar override",
			variable: model.Variable{
				Name: "environment", Kind: model.VariableKindCustom, Query: "prod,stage",
				Current: []string{"prod"}, Multi: true, IncludeAll: true,
				SourcePath: "/templating/list/0",
			},
			wantKind: "custom",
		},
		{
			name: "explicit non-dynamic scalar list",
			variable: model.Variable{
				Name: "environment", Kind: model.VariableKindCustom, Query: "prod,stage",
				Current: []string{"prod", "stage"}, Multi: true, IncludeAll: true,
				SourcePath: "/templating/list/0",
			},
			wantKind: "custom",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			dashboard := model.Dashboard{
				Variables: []model.Variable{test.variable},
				Panels: []model.Panel{{Queries: []model.Query{{
					RefID: "A", Expression: `up{instance=~"$` + test.variable.Name + `"}`,
					SourcePath: "/panels/0/targets/0",
				}}}},
			}
			migration := Dashboard(dashboard, transpile.NewAnalyzer(transpile.Options{Metrics: map[string]model.TargetMetric{
				"up": {Type: "gauge"},
			}}))

			translation := migration.VariableTranslations[test.variable.SourcePath]
			assert.Equal(t, test.wantKind, translation.Kind)
			assert.NotContains(t, translation.Decision.Reasons, model.ReasonMissingVariableValue)
			assert.NotEqual(t, model.TranslationNone, migration.Translations["/panels/0/targets/0"].Kind)
		})
	}
}

func TestDashboardDynamicAllRequiresExplicitMatchAllInPositiveRegexMatchers(t *testing.T) {
	t.Parallel()

	dashboard := model.Dashboard{
		Variables: []model.Variable{{
			Name: "job", Kind: model.VariableKindQuery, Query: "label_values(up, job)",
			Current: []string{"__all__"}, Multi: true, IncludeAll: true, AllValue: ".*",
			SourcePath: "/templating/list/0",
		}, {
			Name: "default_all", Kind: model.VariableKindQuery, Query: "label_values(up, instance)",
			Current: []string{"__all__"}, Multi: true, IncludeAll: true,
			SourcePath: "/templating/list/1",
		}},
		Panels: []model.Panel{{Queries: []model.Query{
			{RefID: "A", Expression: `up{job=~"$job"}`, SourcePath: "/panels/0/targets/0"},
			{RefID: "B", Expression: `up{job!~"$job"}`, SourcePath: "/panels/0/targets/1"},
			{RefID: "C", Expression: `up{job="$job"}`, SourcePath: "/panels/0/targets/2"},
			{RefID: "D", Expression: `up{job=~"prefix-$job"}`, SourcePath: "/panels/0/targets/3"},
			{RefID: "E", Expression: `up{instance=~"$default_all"}`, SourcePath: "/panels/0/targets/4"},
		}}},
	}
	migration := Dashboard(dashboard, transpile.NewAnalyzer(transpile.Options{
		Metrics: map[string]model.TargetMetric{"up": {Type: "gauge"}},
	}))

	assert.NotEqual(t, model.TranslationNone, migration.Translations["/panels/0/targets/0"].Kind)
	for _, path := range []string{
		"/panels/0/targets/1", "/panels/0/targets/2", "/panels/0/targets/3", "/panels/0/targets/4",
	} {
		translation := migration.Translations[path]
		assert.Equal(t, model.TranslationNone, translation.Kind, path)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonVariableValueEscaping, path)
	}
}
