package validate

import (
	"context"
	"testing"

	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/mansiverma897993/noz-in/internal/target/signoz"
	"github.com/stretchr/testify/assert"
)

func verifyCandidateTranslation() model.Translation {
	return model.Translation{
		Kind:   model.TranslationBuilder,
		PromQL: `sum(node_memory_MemTotal_bytes)`,
		Builder: &model.BuilderQuery{
			Name: "A", MetricName: "node_memory_MemTotal_bytes",
			TimeAggregation: "latest", SpaceAggregation: "sum", StepSeconds: 60,
		},
		Decision: model.Decision{Verdict: model.VerdictNeedsReview},
	}
}

// seriesVals lets a test dial the builder vs passthrough values to land in a band.
func seriesVals(values ...float64) []signoz.MetricSeries {
	points := make([]signoz.MetricPoint, 0, len(values))
	for i, v := range values {
		points = append(points, signoz.MetricPoint{Timestamp: int64(1_000_000 + i*60_000), Value: v})
	}
	return []signoz.MetricSeries{{Labels: map[string]string{"service.name": "ne"}, Values: points}}
}

func TestVerifyCandidateBands(t *testing.T) {
	t.Parallel()
	cand := verifyCandidateTranslation()
	opts := promoteOptions()

	// Exact
	exact := VerifyCandidate(context.Background(),
		fakeExecutor{builder: seriesVals(1, 2, 3, 4, 5), passthrough: seriesVals(1, 2, 3, 4, 5)}, cand, cand.PromQL, opts)
	assert.Equal(t, FidelityExact, exact.Band)
	assert.True(t, exact.Pass)

	// Within 1% (builder 100.5 vs passthrough 100 -> 0.5%)
	near := VerifyCandidate(context.Background(),
		fakeExecutor{builder: seriesVals(100.5, 100.5, 100.5, 100.5, 100.5), passthrough: seriesVals(100, 100, 100, 100, 100)}, cand, cand.PromQL, opts)
	assert.Equal(t, FidelityWithin1Pct, near.Band)
	assert.True(t, near.Pass) // default threshold 0.05 >= 0.01

	// Diverged (100 vs 50 -> 50%)
	far := VerifyCandidate(context.Background(),
		fakeExecutor{builder: seriesVals(100, 100, 100, 100, 100), passthrough: seriesVals(50, 50, 50, 50, 50)}, cand, cand.PromQL, opts)
	assert.Equal(t, FidelityDiverged, far.Band)
	assert.False(t, far.Pass)

	// No data
	empty := VerifyCandidate(context.Background(),
		fakeExecutor{builder: nil, passthrough: seriesVals(1, 2, 3)}, cand, cand.PromQL, opts)
	assert.Equal(t, FidelityNoData, empty.Band)
	assert.False(t, empty.Pass)

	// Tight threshold rejects a within-5pct candidate
	tight := opts
	tight.RelativeTolerance = 0.01
	band5 := VerifyCandidate(context.Background(),
		fakeExecutor{builder: seriesVals(103, 103, 103, 103, 103), passthrough: seriesVals(100, 100, 100, 100, 100)}, cand, cand.PromQL, tight)
	assert.Equal(t, FidelityWithin5Pct, band5.Band)
	assert.False(t, band5.Pass) // 3% > 1% threshold
}
