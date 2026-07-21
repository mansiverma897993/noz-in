package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPanelModeForcesPromQLForBuilderSemanticCandidates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reason ReasonCode
		kind   TranslationKind
	}{
		{name: "rate or increase", reason: ReasonBuilderRateIncrease, kind: TranslationBuilder},
		{name: "latest lookback", reason: ReasonBuilderLatestLookback, kind: TranslationBuilder},
		{name: "histogram percentile", reason: ReasonBuilderHistogramPercentile, kind: TranslationBuilder},
		{name: "formula evaluation", reason: ReasonBuilderFormulaEvaluation, kind: TranslationFormula},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			query := Query{RefID: "A", Expression: "canonical_promql", SourcePath: "/panels/0/targets/0"}
			translation := Translation{
				Kind:   test.kind,
				PromQL: query.Expression,
				Decision: Decision{
					Verdict: VerdictNeedsReview,
					Reasons: []ReasonCode{test.reason},
				},
			}
			if test.kind == TranslationFormula {
				translation.Formula = &Formula{
					Name: "A", Expression: "A_1 / 2",
					Queries: []BuilderQuery{{Name: "A_1", MetricName: "metric", SpaceAggregation: "sum"}},
				}
			} else {
				translation.Builder = &BuilderQuery{Name: "A", MetricName: "metric", SpaceAggregation: "sum"}
			}
			panel := Panel{Kind: PanelKindGraph, Queries: []Query{query}, SourcePath: "/panels/0"}
			migration := Migration{
				Dashboard:    Dashboard{Panels: []Panel{panel}},
				Translations: map[string]Translation{query.SourcePath: translation},
			}

			assert.True(t, IsBuilderCandidateSemanticReason(test.reason))
			assert.Equal(t, TranslationPromQL, migration.PanelMode(panel))
			assert.Equal(t, test.reason, migration.PanelFallbackReason(panel))
			assert.Equal(t, VerdictNeedsReview, migration.PanelDecision(panel).Verdict)
		})
	}
}

func TestBuilderSemanticReasonClassifierIsClosed(t *testing.T) {
	t.Parallel()

	assert.False(t, IsBuilderCandidateSemanticReason(ReasonMixedPanelQueries))
	assert.False(t, IsBuilderCandidateSemanticReason(ReasonUnsupportedModifier))
	assert.False(t, IsBuilderCandidateSemanticReason(ReasonCode("FUTURE_REASON")))
}
