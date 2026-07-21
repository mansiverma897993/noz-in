package transpile

import (
	"testing"
	"time"

	"github.com/mansiverma897993/signoz/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOverTimeAggregationBecomesBuilderCandidate confirms the distilled rule:
// an aggregation over an *_over_time range function on a gauge becomes a Builder
// candidate (step-aligned), recognized for native promotion, but never claimed
// native offline.
func TestOverTimeAggregationBecomesBuilderCandidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		expr    string
		wantAgg string
	}{
		{"sum(avg_over_time(node_load1[5m]))", "avg"},
		{"max(max_over_time(node_load1[5m]))", "max"},
		{"min(min_over_time(node_load1[5m]))", "min"},
		{"sum(sum_over_time(node_load1[5m]))", "sum"},
		{"count(count_over_time(node_load1[5m]))", "count"},
	}
	for _, testCase := range cases {
		translation := NewAnalyzer(Options{Interval: time.Minute, Metrics: map[string]model.TargetMetric{
			"node_load1": {Type: "gauge"},
		}}).Analyze(model.Query{RefID: "A", Expression: testCase.expr})
		require.Equal(t, model.TranslationBuilder, translation.Kind, testCase.expr)
		require.NotNil(t, translation.Builder, testCase.expr)
		assert.Equal(t, testCase.wantAgg, translation.Builder.TimeAggregation, testCase.expr)
		assert.Equal(t, 300, translation.Builder.StepSeconds, testCase.expr)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonBuilderOverTime, testCase.expr)
		assert.True(t, model.IsBuilderCandidateSemanticReason(model.ReasonBuilderOverTime))
		// Honest: offline can never prove equivalence, so never native.
		assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict, testCase.expr)
	}

	// last_over_time maps to the latest lookback semantic (still a candidate).
	last := NewAnalyzer(Options{Interval: time.Minute, Metrics: map[string]model.TargetMetric{
		"node_load1": {Type: "gauge"},
	}}).Analyze(model.Query{RefID: "A", Expression: "sum(last_over_time(node_load1[5m]))"})
	require.Equal(t, model.TranslationBuilder, last.Kind)
	assert.Equal(t, "latest", last.Builder.TimeAggregation)

	// A bare per-series over_time (no outer aggregation) is not a Builder shape.
	bare := NewAnalyzer(Options{Interval: time.Minute}).Analyze(model.Query{RefID: "A", Expression: "avg_over_time(node_load1[5m])"})
	assert.NotEqual(t, model.TranslationBuilder, bare.Kind)
}
