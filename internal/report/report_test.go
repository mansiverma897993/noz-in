package report

import (
	"encoding/json"
	"testing"

	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/mansiverma897993/noz-in/internal/target/signoz"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildUsesEffectivePanelMode(t *testing.T) {
	t.Parallel()

	native := model.Query{RefID: "A", Expression: "sum(up)", Format: "table", SourcePath: "/panels/0/targets/0"}
	passthrough := model.Query{RefID: "B", Expression: "up or vector(0)", SourcePath: "/panels/0/targets/1"}
	migration := model.Migration{
		Dashboard: model.Dashboard{
			Title: "Mixed",
			Panels: []model.Panel{{
				ID:         "1",
				Title:      "Mixed",
				Kind:       model.PanelKindGraph,
				SourcePath: "/panels/0",
				Queries:    []model.Query{native, passthrough},
			}},
		},
		Translations: map[string]model.Translation{
			native.SourcePath: {
				Kind:     model.TranslationBuilder,
				Builder:  &model.BuilderQuery{Name: "A", MetricName: "up", SpaceAggregation: "sum"},
				PromQL:   `sum(up{"service.name"="$job"})`,
				Decision: model.Decision{Verdict: model.VerdictNative},
			},
			passthrough.SourcePath: {
				Kind:     model.TranslationPromQL,
				PromQL:   passthrough.Expression,
				Decision: model.Decision{Verdict: model.VerdictPassthrough},
			},
		},
	}

	report := Build(migration)
	assert.Equal(t, 2, report.Summary.Queries)
	assert.Zero(t, report.Summary.Native)
	assert.Equal(t, 2, report.Summary.Passthrough)
	require.Len(t, report.Panels, 1)
	assert.Equal(t, string(model.TranslationPromQL), report.Panels[0].EmittedMode)
	assert.Contains(t, report.Panels[0].Queries[0].ReasonCodes, string(model.ReasonMixedPanelQueries))
	assert.Equal(t, string(model.TranslationBuilder), report.Panels[0].Queries[0].CandidateKind)
	assert.Equal(t, string(model.TranslationPromQL), report.Panels[0].Queries[0].EmittedKind)
	assert.Equal(t, `sum(up{"service.name"="$job"})`, report.Panels[0].Queries[0].PromQL)
	assert.Equal(t, "table", report.Panels[0].Queries[0].Format)
	encoded, err := json.Marshal(report.Panels[0].Queries[0])
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"format":"table"`)
}

func TestEmittedPanelKindMirrorsV5GraphDowngrades(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		kind model.PanelKind
		mode model.TranslationKind
		want string
	}{
		{name: "bar", kind: model.PanelKindBar, mode: model.TranslationBuilder, want: "graph"},
		{name: "histogram", kind: model.PanelKindHistogram, mode: model.TranslationPromQL, want: "graph"},
		{name: "PromQL table", kind: model.PanelKindTable, mode: model.TranslationPromQL, want: "graph"},
		{name: "PromQL value", kind: model.PanelKindValue, mode: model.TranslationPromQL, want: "graph"},
		{name: "PromQL pie", kind: model.PanelKindPie, mode: model.TranslationPromQL, want: "graph"},
		{name: "Builder value", kind: model.PanelKindValue, mode: model.TranslationBuilder, want: "value"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			recorded := emittedPanelKind(model.Panel{Kind: test.kind}, test.mode)
			assert.Equal(t, test.want, recorded)
			// The evidence record must name the visualization the emitter
			// actually writes, so the two can never drift apart again.
			assert.Equal(t, signoz.EmittedPanelType(test.kind, test.mode), recorded)
		})
	}
}

func TestBuildNeverCountsBuilderSemanticCandidatesAsNative(t *testing.T) {
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
					Title: "Candidate", Kind: model.PanelKindGraph, Queries: []model.Query{query}, SourcePath: "/panels/0",
				}}},
				Translations: map[string]model.Translation{query.SourcePath: translation},
			}

			evidence := Build(migration)

			assert.Zero(t, evidence.Summary.Native)
			assert.Zero(t, evidence.Summary.Builder)
			assert.Zero(t, evidence.Summary.Formula)
			assert.Equal(t, 1, evidence.Summary.NeedsReview)
			assert.Equal(t, 1, evidence.Summary.PromQLPanels)
			require.Len(t, evidence.Panels, 1)
			require.Len(t, evidence.Panels[0].Queries, 1)
			record := evidence.Panels[0].Queries[0]
			assert.Equal(t, string(test.kind), record.CandidateKind)
			assert.Equal(t, string(model.TranslationPromQL), record.EmittedKind)
			assert.Equal(t, string(model.VerdictNeedsReview), record.Verdict)
			assert.Equal(t, "canonical_promql", record.PromQL)
			assert.Contains(t, record.ReasonCodes, string(test.reason))
			if test.kind == model.TranslationFormula {
				assert.NotNil(t, record.Formula)
			} else {
				assert.NotNil(t, record.Builder)
			}
		})
	}
}

func TestBuildIncludesEveryReasonDescription(t *testing.T) {
	t.Parallel()

	report := Build(model.Migration{Dashboard: model.Dashboard{Title: "Empty"}})
	assert.Len(t, report.ReasonCodes, len(model.ReasonCodes()))
}

func TestBuildUsesArraysForEmptyCollections(t *testing.T) {
	t.Parallel()

	evidence := Build(model.Migration{Dashboard: model.Dashboard{
		Title:  "Empty",
		Panels: []model.Panel{{Title: "Runbook", Kind: model.PanelKindText}},
	}})
	require.NotNil(t, evidence.Panels)
	require.NotNil(t, evidence.Variables)
	require.NotNil(t, evidence.Panels[0].Queries)

	encoded, err := json.Marshal(evidence)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"queries":[]`)
}

func TestRefreshSummaryUsesDataPresenceVocabulary(t *testing.T) {
	t.Parallel()

	evidence := reporttypes.Report{
		Summary: reporttypes.Summary{ValidationEligible: 2, DataPresent: 1},
		Panels: []reporttypes.PanelRecord{{
			Verdict: string(model.VerdictPassthrough),
			Queries: []reporttypes.QueryRecord{{
				Validation: reporttypes.Validation{Executed: true, DataPresent: true},
			}},
		}},
	}

	RefreshSummary(&evidence)

	assert.Equal(t, 50.0, evidence.Summary.DataPresentPercent)
	assert.Equal(t, "passthrough-and-data-present", evidence.Panels[0].State)
	assert.Equal(t, "passthrough-without-data-evidence", panelState(reporttypes.PanelRecord{
		Verdict: string(model.VerdictPassthrough), Queries: []reporttypes.QueryRecord{{}},
	}))
	encoded, err := json.Marshal(evidence)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"dataPresentPercent":50`)
	assert.NotContains(t, string(encoded), "dataVerified")
}

func TestPanelStateUsesOnlyValidationEligibleQueries(t *testing.T) {
	t.Parallel()

	panel := reporttypes.PanelRecord{
		Verdict: string(model.VerdictPassthrough),
		Queries: []reporttypes.QueryRecord{
			{EmittedKind: string(model.TranslationPromQL), Validation: reporttypes.Validation{Executed: true, DataPresent: true}},
			{Disabled: true, EmittedKind: string(model.TranslationPromQL)},
			{EmittedKind: string(model.TranslationNone)},
		},
	}
	assert.Equal(t, "passthrough-and-data-present", panelState(panel))

	panel.Queries = panel.Queries[1:]
	assert.Equal(t, "passthrough-without-data-evidence", panelState(panel))
}

func TestBuildReportsValuePanelBuilderFallback(t *testing.T) {
	t.Parallel()

	query := model.Query{RefID: "A", Expression: "memory_bytes", SourcePath: "/panels/0/targets/0"}
	migration := model.Migration{
		Dashboard: model.Dashboard{Title: "Host", Panels: []model.Panel{{
			Title: "RAM", Kind: model.PanelKindValue, Queries: []model.Query{query}, SourcePath: "/panels/0",
		}}},
		Translations: map[string]model.Translation{query.SourcePath: {
			Kind:     model.TranslationBuilder,
			Builder:  &model.BuilderQuery{Name: "A", MetricName: "memory_bytes", GroupBy: []string{"service.name"}},
			PromQL:   `memory_bytes{"service.name"="$job"}`,
			Decision: model.Decision{Verdict: model.VerdictNative},
		}},
	}

	report := Build(migration)
	require.Len(t, report.Panels, 1)
	assert.Equal(t, string(model.TranslationPromQL), report.Panels[0].EmittedMode)
	assert.Equal(t, string(model.VerdictPassthrough), report.Panels[0].Queries[0].Verdict)
	assert.Contains(t, report.Panels[0].Queries[0].ReasonCodes, string(model.ReasonBuilderValueGroupBy))
}

func TestBuildAccountsForPanelAndVariableLevelCompatibility(t *testing.T) {
	t.Parallel()

	query := model.Query{RefID: "A", Expression: "up", SourcePath: "/panels/0/targets/0"}
	migration := model.Migration{
		Dashboard: model.Dashboard{
			Title: "Compatibility",
			Panels: []model.Panel{
				{Title: "Joined", Kind: model.PanelKindTable, SourcePath: "/panels/0", Queries: []model.Query{query}, Transforms: []string{"joinByField"}},
				{Title: "Runbook", Kind: model.PanelKindText, SourcePath: "/panels/1", Content: "Read the runbook."},
			},
			Variables: []model.Variable{
				{Name: "source", Kind: model.VariableKindDatasource, SourcePath: "/templating/list/0"},
				{Name: "instance", Kind: model.VariableKindQuery, Current: []string{"$__all"}, AllValue: ".+", SourcePath: "/templating/list/1"},
			},
		},
		Translations: map[string]model.Translation{query.SourcePath: {
			Kind: model.TranslationPromQL, PromQL: "up", Decision: model.Decision{Verdict: model.VerdictPassthrough},
		}},
		VariableTranslations: map[string]model.VariableTranslation{
			"/templating/list/0": {Kind: "none", Decision: model.Decision{Verdict: model.VerdictNeedsReview, Reasons: []model.ReasonCode{model.ReasonDatasourceVariable}}},
			"/templating/list/1": {Kind: "dynamic", Attribute: "instance", Decision: model.Decision{
				Verdict: model.VerdictNeedsReview,
				Reasons: []model.ReasonCode{model.ReasonVariableRegex, model.ReasonVariableAllValue},
				Notes:   []string{`Grafana allValue=".+" differs from target All matcher removal; current=["$__all"].`},
			}},
		},
	}

	evidence := Build(migration)
	assert.Equal(t, 2, evidence.Summary.PanelsAccounted)
	assert.Equal(t, 1, evidence.Summary.QueriesAccounted)
	assert.Equal(t, 2, evidence.Summary.VariablesAccounted)
	assert.True(t, evidence.Summary.ReconciliationComplete)
	assert.Equal(t, 2, evidence.Summary.PanelsNeedsReview)
	assert.Equal(t, 2, evidence.Summary.Variables)
	assert.Equal(t, 2, evidence.Summary.VariablesNeedsReview)
	assert.Equal(t, "graph", evidence.Panels[0].EmittedKind)
	assert.Contains(t, evidence.Panels[0].ReasonCodes, string(model.ReasonPanelTypeDowngrade))
	assert.Contains(t, evidence.Panels[0].ReasonCodes, string(model.ReasonGrafanaTransformation))
	assert.Equal(t, "Read the runbook.", evidence.Panels[1].Content)
	assert.Contains(t, evidence.Panels[1].ReasonCodes, string(model.ReasonTextPanel))
	assert.Len(t, evidence.Variables, 2)
	assert.Equal(t, []string{"$__all"}, evidence.Variables[1].Current)
	assert.Equal(t, ".+", evidence.Variables[1].AllValue)
	assert.Contains(t, evidence.Variables[1].ReasonCodes, string(model.ReasonVariableAllValue))
	assert.Equal(t, []string{`Grafana allValue=".+" differs from target All matcher removal; current=["$__all"].`}, evidence.Variables[1].Notes)
}

func TestBuildAccountsForQuerySourceFeatures(t *testing.T) {
	t.Parallel()

	query := model.Query{
		RefID: "A", Expression: "sum(up)", Step: 30, SourcePath: "/panels/0/targets/0",
		SourceFeatures: []model.SourceFeature{
			{Kind: "query_step", SourcePath: "/panels/0/targets/0/step", Detail: `"30"`, Reason: model.ReasonGrafanaIntervalControl},
			{Kind: "query_range", SourcePath: "/panels/0/targets/0/range", Detail: "false", Reason: model.ReasonUnmappedQueryConfig},
			{Kind: "query_exemplar", SourcePath: "/panels/0/targets/0/exemplar", Detail: "true", Reason: model.ReasonUnmappedQueryConfig},
		},
	}
	migration := model.Migration{
		Dashboard: model.Dashboard{
			Title: "Query features",
			SourceInventory: model.SourceInventory{
				Captured: true, Panels: 1, Queries: 1, SourceFeatures: 3,
			},
			Panels: []model.Panel{{
				Title: "Requests", Kind: model.PanelKindGraph, SourcePath: "/panels/0", Queries: []model.Query{query},
			}},
		},
		Translations: map[string]model.Translation{query.SourcePath: {
			Kind: model.TranslationBuilder,
			Builder: &model.BuilderQuery{
				Name: "A", MetricName: "up", SpaceAggregation: "sum", StepSeconds: 60,
			},
			Decision: model.Decision{Verdict: model.VerdictNative},
		}},
	}

	evidence := Build(migration)
	require.Len(t, evidence.Panels, 1)
	require.Len(t, evidence.Panels[0].Queries, 1)
	record := evidence.Panels[0].Queries[0]
	assert.Equal(t, "needs_review", evidence.Panels[0].Verdict)
	assert.Contains(t, evidence.Panels[0].ReasonCodes, string(model.ReasonUnmappedQueryConfig))
	assert.Contains(t, evidence.Panels[0].ReasonCodes, string(model.ReasonGrafanaIntervalControl))
	assert.Equal(t, "needs_review", record.Verdict)
	assert.Contains(t, record.ReasonCodes, string(model.ReasonUnmappedQueryConfig))
	assert.Contains(t, record.ReasonCodes, string(model.ReasonGrafanaIntervalControl))
	assert.Equal(t, 30, record.Step)
	require.Len(t, record.SourceFeatures, 3)
	assert.Equal(t, `"30"`, record.SourceFeatures[0].Detail)
	assert.Equal(t, "false", record.SourceFeatures[1].Detail)
	assert.Equal(t, "/panels/0/targets/0/exemplar", record.SourceFeatures[2].SourcePath)
	assert.Equal(t, 3, evidence.Summary.SourceFeatures)
	assert.Equal(t, 3, evidence.Summary.SourceFeaturesAccounted)
	assert.Equal(t, 3, evidence.Summary.SourceFeaturesNeedsReview)
	assert.True(t, evidence.Summary.ReconciliationComplete)

	encoded, err := json.Marshal(record)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"step":30`)
	assert.Contains(t, string(encoded), `"sourceFeatures"`)

	fallback := queryRecord(query, migration.Translations[query.SourcePath], model.TranslationPromQL, model.ReasonMixedPanelQueries)
	assert.Equal(t, string(model.TranslationPromQL), fallback.EmittedKind)
	assert.Equal(t, string(model.VerdictNeedsReview), fallback.Verdict)
	assert.Contains(t, fallback.ReasonCodes, string(model.ReasonUnmappedQueryConfig))
	assert.Contains(t, fallback.ReasonCodes, string(model.ReasonMixedPanelQueries))
}

func TestBuildPreservesVariableLabelAndRawConfigurationEvidence(t *testing.T) {
	t.Parallel()

	variable := model.Variable{
		Name: "cluster", Label: "Kubernetes cluster", Kind: model.VariableKindQuery,
		SourcePath: "/templating/list/0",
		SourceFeatures: []model.SourceFeature{{
			Kind: "variable_property", SourcePath: "/templating/list/0/hide", Detail: "0",
			Reason: model.ReasonUnmappedVariableConfig,
		}},
	}
	migration := model.Migration{
		Dashboard: model.Dashboard{
			Title: "Variables",
			SourceInventory: model.SourceInventory{
				Captured: true, Variables: 1, SourceFeatures: 1,
			},
			Variables: []model.Variable{variable},
		},
		VariableTranslations: map[string]model.VariableTranslation{
			variable.SourcePath: {
				Kind: "dynamic", Attribute: "k8s.cluster.name",
				Decision: model.Decision{
					Verdict: model.VerdictNeedsReview,
					Reasons: []model.ReasonCode{model.ReasonUnmappedVariableConfig},
				},
			},
		},
	}

	evidence := Build(migration)
	require.Len(t, evidence.Variables, 1)
	record := evidence.Variables[0]
	assert.Equal(t, "Kubernetes cluster", record.Label)
	assert.Equal(t, "needs_review", record.Verdict)
	assert.Equal(t, []string{string(model.ReasonUnmappedVariableConfig)}, record.ReasonCodes)
	require.Len(t, record.SourceFeatures, 1)
	assert.Equal(t, "/templating/list/0/hide", record.SourceFeatures[0].SourcePath)
	assert.Equal(t, "0", record.SourceFeatures[0].Detail)
	assert.Equal(t, 1, evidence.Summary.SourceFeatures)
	assert.Equal(t, 1, evidence.Summary.SourceFeaturesAccounted)
	assert.Equal(t, 1, evidence.Summary.SourceFeaturesNeedsReview)
	assert.Equal(t, 1, evidence.Summary.VariablesNeedsReview)
	assert.True(t, evidence.Summary.ReconciliationComplete)
}

func TestBuildReconcilesModernRowTargetAsNonEmittedReview(t *testing.T) {
	t.Parallel()

	rowQuery := model.Query{
		RefID: "A", Expression: "stale_row_metric", SourcePath: "/panels/0/targets/0",
	}
	childQuery := model.Query{
		RefID: "B", Expression: "sum(up)", SourcePath: "/panels/0/panels/0/targets/0",
	}
	migration := model.Migration{
		Dashboard: model.Dashboard{
			Title: "Rows",
			SourceInventory: model.SourceInventory{
				Captured: true, Panels: 2, Queries: 2,
			},
			Panels: []model.Panel{
				{
					Title: "Database", Kind: model.PanelKindRow, SourcePath: "/panels/0",
					Queries: []model.Query{rowQuery},
				},
				{
					Title: "Queries", Kind: model.PanelKindGraph, SourcePath: "/panels/0/panels/0",
					Queries: []model.Query{childQuery},
				},
			},
		},
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

	evidence := Build(migration)
	assert.Equal(t, 2, evidence.Summary.Panels)
	assert.Equal(t, 2, evidence.Summary.PanelsAccounted)
	assert.Equal(t, 2, evidence.Summary.Queries)
	assert.Equal(t, 2, evidence.Summary.QueriesAccounted)
	assert.Equal(t, 1, evidence.Summary.NeedsReview)
	assert.Equal(t, 1, evidence.Summary.Native)
	assert.Equal(t, 1, evidence.Summary.Builder)
	assert.True(t, evidence.Summary.ReconciliationComplete)

	require.Len(t, evidence.Panels, 2)
	rowRecord := evidence.Panels[0]
	assert.Equal(t, string(model.TranslationBuilder), rowRecord.EmittedMode)
	assert.Equal(t, string(model.VerdictNeedsReview), rowRecord.Verdict)
	assert.Contains(t, rowRecord.ReasonCodes, string(model.ReasonRowPanelTarget))
	require.Len(t, rowRecord.Queries, 1)
	assert.Equal(t, string(model.TranslationNone), rowRecord.Queries[0].CandidateKind)
	assert.Equal(t, string(model.TranslationNone), rowRecord.Queries[0].EmittedKind)
	assert.Equal(t, string(model.VerdictNeedsReview), rowRecord.Queries[0].Verdict)
	assert.Contains(t, rowRecord.Queries[0].ReasonCodes, string(model.ReasonRowPanelTarget))
	assert.NotContains(t, rowRecord.Queries[0].ReasonCodes, string(model.ReasonPanelOmitted))
	assert.Empty(t, rowRecord.Queries[0].PromQL)

	childRecord := evidence.Panels[1]
	assert.Equal(t, string(model.TranslationBuilder), childRecord.EmittedMode)
	assert.Equal(t, string(model.VerdictNative), childRecord.Verdict)
	require.Len(t, childRecord.Queries, 1)
	assert.Equal(t, string(model.TranslationBuilder), childRecord.Queries[0].EmittedKind)
	assert.Equal(t, string(model.VerdictNative), childRecord.Queries[0].Verdict)
	require.NotNil(t, childRecord.Queries[0].Builder)
	assert.Equal(t, "up", childRecord.Queries[0].Builder.MetricName)
}

func TestBuildDetectsSourceReconciliationMismatch(t *testing.T) {
	t.Parallel()

	evidence := Build(model.Migration{Dashboard: model.Dashboard{
		Title: "Incomplete",
		SourceInventory: model.SourceInventory{
			Captured: true, Panels: 2, Queries: 1, Variables: 1, SourceFeatures: 1,
		},
		Panels: []model.Panel{{Title: "Only one", Kind: model.PanelKindText, SourcePath: "/panels/0"}},
	}})

	assert.Equal(t, reporttypes.SourceInventory{Panels: 2, Queries: 1, Variables: 1, SourceFeatures: 1}, evidence.SourceInventory)
	assert.Equal(t, 1, evidence.Summary.PanelsAccounted)
	assert.Zero(t, evidence.Summary.QueriesAccounted)
	assert.False(t, evidence.Summary.ReconciliationComplete)
}
