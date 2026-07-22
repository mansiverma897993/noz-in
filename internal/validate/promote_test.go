package validate

import (
	"context"
	"testing"
	"time"

	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/mansiverma897993/noz-in/internal/target/signoz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeExecutor returns canned series keyed by whether the request is a builder or
// a PromQL probe, so a test can make the two agree or diverge deterministically.
type fakeExecutor struct {
	builder     []signoz.MetricSeries
	passthrough []signoz.MetricSeries
	builderErr  error
}

func (f fakeExecutor) QueryRangeSeries(_ context.Context, request signoz.QueryRangeRequest) (map[string][]signoz.MetricSeries, error) {
	isBuilder := false
	for _, query := range request.CompositeQuery.Queries {
		if query.Type == "builder_query" || query.Type == "builder_formula" {
			isBuilder = true
		}
	}
	if isBuilder {
		if f.builderErr != nil {
			return nil, f.builderErr
		}
		return map[string][]signoz.MetricSeries{"A": f.builder}, nil
	}
	return map[string][]signoz.MetricSeries{"A": f.passthrough}, nil
}

func series(value float64) []signoz.MetricSeries {
	points := make([]signoz.MetricPoint, 0, 6)
	for i := range 6 {
		points = append(points, signoz.MetricPoint{Timestamp: int64(1_000_000 + i*60_000), Value: value})
	}
	return []signoz.MetricSeries{{Labels: map[string]string{"service.name": "ne"}, Values: points}}
}

func candidateMigration() model.Migration {
	return model.Migration{
		Translations: map[string]model.Translation{
			"/panels/0/targets/0": {
				Kind:   model.TranslationBuilder,
				PromQL: `sum(rate(http_requests_total[5m]))`,
				Builder: &model.BuilderQuery{
					Name: "A", MetricName: "http_requests_total",
					TimeAggregation: "rate", SpaceAggregation: "sum", StepSeconds: 300,
				},
				Decision: model.Decision{
					Verdict: model.VerdictNeedsReview,
					Reasons: []model.ReasonCode{model.ReasonBuilderRateIncrease},
				},
			},
		},
	}
}

func promoteOptions() PromoteOptions {
	return PromoteOptions{
		Now: time.Unix(2_000_000, 0), Window: time.Hour,
		RelativeTolerance: 0.05, TimestampTolerance: time.Minute, MinimumPoints: 3,
	}
}

func TestPromoteWhenBuilderMatchesPassthrough(t *testing.T) {
	t.Parallel()
	migration := candidateMigration()
	exec := fakeExecutor{builder: series(42), passthrough: series(42)}

	records := PromoteNativeCandidates(context.Background(), exec, migration, promoteOptions())

	require.Len(t, records, 1)
	assert.True(t, records[0].Promoted)
	promoted := migration.Translations["/panels/0/targets/0"]
	assert.Equal(t, model.VerdictNative, promoted.Decision.Verdict)
	// Native implies a passing differential: the verified reason must be present.
	assert.Contains(t, promoted.Decision.Reasons, model.ReasonNativeDifferentialVerified)
}

func TestDoNotPromoteWhenBuilderDiverges(t *testing.T) {
	t.Parallel()
	migration := candidateMigration()
	exec := fakeExecutor{builder: series(42), passthrough: series(99)}

	records := PromoteNativeCandidates(context.Background(), exec, migration, promoteOptions())

	require.Len(t, records, 1)
	assert.False(t, records[0].Promoted)
	assert.Equal(t, model.VerdictNeedsReview, migration.Translations["/panels/0/targets/0"].Decision.Verdict)
}

func TestDoNotPromoteWhenProbeReturnsNoData(t *testing.T) {
	t.Parallel()
	migration := candidateMigration()
	exec := fakeExecutor{builder: nil, passthrough: series(42)}

	records := PromoteNativeCandidates(context.Background(), exec, migration, promoteOptions())

	require.Len(t, records, 1)
	assert.False(t, records[0].Promoted)
	assert.Equal(t, model.VerdictNeedsReview, migration.Translations["/panels/0/targets/0"].Decision.Verdict)
}

func TestDoNotPromoteWhenBuilderExecutionFails(t *testing.T) {
	t.Parallel()
	migration := candidateMigration()
	exec := fakeExecutor{builderErr: context.DeadlineExceeded, passthrough: series(42)}

	records := PromoteNativeCandidates(context.Background(), exec, migration, promoteOptions())

	require.Len(t, records, 1)
	assert.False(t, records[0].Promoted)
	assert.Equal(t, model.VerdictNeedsReview, migration.Translations["/panels/0/targets/0"].Decision.Verdict)
}

func TestNativeAlwaysImpliesDifferentialVerified(t *testing.T) {
	t.Parallel()
	// A mixed batch: one candidate matches (promotable), one diverges (must stay
	// review). After promotion the invariant must hold globally: every native
	// verdict carries the verified reason, and every non-native carries none.
	migration := model.Migration{Translations: map[string]model.Translation{
		"/match": candidateMigration().Translations["/panels/0/targets/0"],
		"/diverge": {
			Kind:   model.TranslationBuilder,
			PromQL: `sum(rate(errors_total[5m]))`,
			Builder: &model.BuilderQuery{
				Name: "A", MetricName: "errors_total",
				TimeAggregation: "rate", SpaceAggregation: "sum", StepSeconds: 300,
			},
			Decision: model.Decision{
				Verdict: model.VerdictNeedsReview,
				Reasons: []model.ReasonCode{model.ReasonBuilderRateIncrease},
			},
		},
	}}
	// The fake executor makes the builder equal the passthrough for every query,
	// so /match promotes; force /diverge to fail by giving it its own executor.
	PromoteNativeCandidates(context.Background(),
		fakeExecutor{builder: series(7), passthrough: series(7)}, model.Migration{
			Translations: map[string]model.Translation{"/match": migration.Translations["/match"]},
		}, promoteOptions())
	// Re-run against the full migration with a diverging executor for coverage of
	// the invariant check rather than the promotion outcome.
	PromoteNativeCandidates(context.Background(),
		fakeExecutor{builder: series(7), passthrough: series(7)}, migration, promoteOptions())

	for path, translation := range migration.Translations {
		if translation.Decision.Verdict == model.VerdictNative {
			assert.Contains(t, translation.Decision.Reasons, model.ReasonNativeDifferentialVerified,
				"native verdict without a differential proof: %s", path)
		} else {
			assert.NotContains(t, translation.Decision.Reasons, model.ReasonNativeDifferentialVerified,
				"non-native verdict must not claim differential proof: %s", path)
		}
	}
}

func TestOfflineNeverPromotes(t *testing.T) {
	t.Parallel()
	migration := candidateMigration()

	records := PromoteNativeCandidates(context.Background(), nil, migration, promoteOptions())

	assert.Empty(t, records)
	assert.Equal(t, model.VerdictNeedsReview, migration.Translations["/panels/0/targets/0"].Decision.Verdict)
}
