package diff

import (
	"fmt"
	"maps"
	"math"
	"math/bits"
	"slices"
	"testing"
	"time"

	sourceprometheus "github.com/mansiverma897993/signoz/internal/source/prometheus"
	"github.com/mansiverma897993/signoz/internal/target/signoz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompareNormalizesResourceLabelsAndScrapeTimes(t *testing.T) {
	t.Parallel()

	source := []sourceprometheus.QuerySeries{{
		Labels: map[string]string{"__name__": "up", "job": "node-exporter", "instance": "source:9100"},
		Values: []sourceprometheus.QueryPoint{{Timestamp: 10_000, Value: 1}, {Timestamp: 70_000, Value: 2}},
	}}
	target := []signoz.MetricSeries{{
		Labels: map[string]string{
			"__name__": "up", "service.name": "node-exporter", "service.instance.id": "source-node", "server.address": "source-node",
		},
		Values: []signoz.MetricPoint{{Timestamp: 15_000, Value: 1.01}, {Timestamp: 75_000, Value: 2.01}},
	}}

	stats := Compare(source, target, Options{
		TimestampTolerance:   10 * time.Second,
		TargetKind:           TargetKindPromQL,
		TargetProvenance:     TargetProvenanceOTelPrometheusReceiver,
		RelativeTolerance:    0.02,
		MinimumCoverage:      1,
		MinimumMatchedPoints: 1,
		LabelValueAliases:    map[string]map[string]string{"instance": {"source-node": "source:9100"}},
	})
	assert.Equal(t, StatusEquivalent, stats.Status)
	assert.Equal(t, 1, stats.MatchedSeries)
	assert.Equal(t, 2, stats.MatchedPoints)
	assert.Equal(t, 1.0, stats.Coverage)
	assert.Equal(t, []string{"server.address"}, stats.IgnoredTargetLabels)
}

func TestCompareRejectsArbitraryTargetLabelSupersets(t *testing.T) {
	t.Parallel()

	source := []sourceprometheus.QuerySeries{{
		Labels: map[string]string{"job": "api"},
		Values: []sourceprometheus.QueryPoint{{Timestamp: 1_000, Value: 1}},
	}}
	target := []signoz.MetricSeries{{
		Labels: map[string]string{
			"service.name": "api",
			"tenant":       "other-tenant",
			"cluster":      "other-cluster",
		},
		Values: []signoz.MetricPoint{{Timestamp: 1_000, Value: 1}},
	}}

	stats := Compare(source, target, Options{
		TargetKind:           TargetKindPromQL,
		TargetProvenance:     TargetProvenanceOTelPrometheusReceiver,
		MinimumCoverage:      1,
		MinimumMatchedPoints: 1,
	})

	assert.Equal(t, StatusNoSeriesMatch, stats.Status)
	assert.Equal(t, 1, stats.UnmatchedSourceSeries)
	assert.Equal(t, 1, stats.UnmatchedTargetSeries)
	assert.Empty(t, stats.IgnoredTargetLabels)
}

func TestCompareRejectsTargetExtrasWhenSourceLabelsAreEmpty(t *testing.T) {
	t.Parallel()

	source := []sourceprometheus.QuerySeries{{
		Labels: map[string]string{},
		Values: []sourceprometheus.QueryPoint{{Timestamp: 1_000, Value: 1}},
	}}
	target := []signoz.MetricSeries{{
		Labels: map[string]string{"tenant": "other-tenant"},
		Values: []signoz.MetricPoint{{Timestamp: 1_000, Value: 1}},
	}}

	stats := Compare(source, target, Options{
		TargetKind:           TargetKindBuilderQuery,
		TargetProvenance:     TargetProvenanceOTelPrometheusReceiver,
		MinimumCoverage:      1,
		MinimumMatchedPoints: 1,
	})

	assert.Equal(t, StatusNoSeriesMatch, stats.Status)
	assert.Equal(t, 1, stats.UnmatchedSourceSeries)
}

func TestCompareIgnoresOnlyProvenanceBoundReceiverLabels(t *testing.T) {
	t.Parallel()

	source := []sourceprometheus.QuerySeries{{
		Labels: map[string]string{"job": "api"},
		Values: []sourceprometheus.QueryPoint{{Timestamp: 1_000, Value: 1}},
	}}
	target := []signoz.MetricSeries{{
		Labels: map[string]string{
			"service.name":         "api",
			"__scope.name__":       "otelcol/prometheusreceiver",
			"__scope.schema_url__": "https://opentelemetry.io/schemas/1.26.0",
			"__scope.version__":    "0.144.0",
			"__temporality__":      "Cumulative",
			"fingerprint":          "abc123",
			"server.address":       "source-node",
			"server.port":          "9100",
			"url.scheme":           "http",
		},
		Values: []signoz.MetricPoint{{Timestamp: 1_000, Value: 1}},
	}}

	for _, test := range []struct {
		name    string
		options Options
		want    Status
	}{
		{
			name: "explicit receiver provenance and query kind",
			options: Options{
				TargetKind:       TargetKindPromQL,
				TargetProvenance: TargetProvenanceOTelPrometheusReceiver,
			},
			want: StatusEquivalent,
		},
		{
			name:    "provenance alone",
			options: Options{TargetProvenance: TargetProvenanceOTelPrometheusReceiver},
			want:    StatusNoSeriesMatch,
		},
		{
			name:    "query kind alone",
			options: Options{TargetKind: TargetKindPromQL},
			want:    StatusNoSeriesMatch,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			test.options.MinimumCoverage = 1
			test.options.MinimumMatchedPoints = 1
			stats := Compare(source, target, test.options)
			assert.Equal(t, test.want, stats.Status)
			if test.want == StatusEquivalent {
				assert.Equal(t, []string{
					"__scope.name__", "__scope.schema_url__", "__scope.version__", "__temporality__", "fingerprint",
					"server.address", "server.port", "url.scheme",
				}, stats.IgnoredTargetLabels)
			} else {
				assert.Empty(t, stats.IgnoredTargetLabels)
			}
		})
	}
}

func TestCompareRejectsReceiverLabelCardinalitySplits(t *testing.T) {
	t.Parallel()

	source := []sourceprometheus.QuerySeries{{
		Labels: map[string]string{},
		Values: []sourceprometheus.QueryPoint{{Timestamp: 1_000, Value: 1}},
	}}
	target := []signoz.MetricSeries{
		{
			Labels: map[string]string{"server.address": "node-a"},
			Values: []signoz.MetricPoint{{Timestamp: 1_000, Value: 1}},
		},
		{
			Labels: map[string]string{"server.address": "node-b"},
			Values: []signoz.MetricPoint{{Timestamp: 1_000, Value: 1}},
		},
	}

	stats := Compare(source, target, Options{
		TargetKind:           TargetKindBuilderFormula,
		TargetProvenance:     TargetProvenanceOTelPrometheusReceiver,
		MinimumCoverage:      1,
		MinimumMatchedPoints: 1,
	})

	assert.Equal(t, StatusNoSeriesMatch, stats.Status)
	assert.Zero(t, stats.MatchedSeries)
	assert.Equal(t, 2, stats.AmbiguousTargetSeries)
	assert.Equal(t, 2, stats.UnmatchedTargetSeries)
	assert.Equal(t, []string{"server.address"}, stats.IgnoredTargetLabels)
}

func TestCompareReportsValueMismatch(t *testing.T) {
	t.Parallel()

	source := []sourceprometheus.QuerySeries{{Labels: map[string]string{}, Values: []sourceprometheus.QueryPoint{{Timestamp: 1_000, Value: 1}}}}
	target := []signoz.MetricSeries{{Labels: map[string]string{}, Values: []signoz.MetricPoint{{Timestamp: 1_000, Value: 5}}}}
	stats := Compare(source, target, Options{RelativeTolerance: 0.1, MinimumCoverage: 1, MinimumMatchedPoints: 1})
	assert.Equal(t, StatusValueMismatch, stats.Status)
	assert.Equal(t, 0.8, stats.MaxRelativeError)
}

func TestCompareRejectsMissingSourceSeriesEvenWhenPointCoveragePasses(t *testing.T) {
	t.Parallel()

	primarySource := sourceprometheus.QuerySeries{Labels: map[string]string{"series": "primary"}}
	primaryTarget := signoz.MetricSeries{Labels: map[string]string{"series": "primary"}}
	for index := range 10 {
		primarySource.Values = append(primarySource.Values, sourceprometheus.QueryPoint{Timestamp: int64(index), Value: 1})
		primaryTarget.Values = append(primaryTarget.Values, signoz.MetricPoint{Timestamp: int64(index), Value: 1})
	}
	missingSource := sourceprometheus.QuerySeries{
		Labels: map[string]string{"series": "missing"},
		Values: []sourceprometheus.QueryPoint{{Timestamp: 0, Value: 1}},
	}

	stats := Compare(
		[]sourceprometheus.QuerySeries{primarySource, missingSource},
		[]signoz.MetricSeries{primaryTarget},
		Options{MinimumCoverage: 0.8, MinimumMatchedPoints: 1, TimestampTolerance: time.Millisecond},
	)

	assert.Greater(t, stats.Coverage, 0.8, "the regression must exercise the former aggregate-coverage false positive")
	assert.Equal(t, 1, stats.UnmatchedSourceSeries)
	assert.Equal(t, StatusNoSeriesMatch, stats.Status)
}

func TestCompareRejectsSimultaneousSourceAndMappedTargetLabels(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		sourceLabel string
		targetLabel string
	}{
		{name: "job", sourceLabel: "job", targetLabel: "service.name"},
		{name: "instance", sourceLabel: "instance", targetLabel: "service.instance.id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := []sourceprometheus.QuerySeries{{
				Labels: map[string]string{test.sourceLabel: "api"},
				Values: []sourceprometheus.QueryPoint{{Timestamp: 1, Value: 1}},
			}}
			options := Options{MinimumCoverage: 1, MinimumMatchedPoints: 1, TimestampTolerance: time.Millisecond}
			for _, target := range []signoz.MetricSeries{
				{
					Labels: map[string]string{test.sourceLabel: "worker", test.targetLabel: "api"},
					Values: []signoz.MetricPoint{{Timestamp: 1, Value: 1}},
				},
				{
					Labels: map[string]string{test.sourceLabel: "api", test.targetLabel: "api"},
					Values: []signoz.MetricPoint{{Timestamp: 1, Value: 1}},
				},
			} {
				stats := Compare(source, []signoz.MetricSeries{target}, options)
				assert.Equal(t, StatusNoSeriesMatch, stats.Status)
				assert.Equal(t, 1, stats.UnmatchedSourceSeries)
			}
		})
	}
}

func TestCompareKeepsNativeTargetSideLabelNamesExact(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		label string
	}{
		{name: "service name", label: "service.name"},
		{name: "service instance id", label: "service.instance.id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := []sourceprometheus.QuerySeries{{
				Labels: map[string]string{test.label: "api"},
				Values: []sourceprometheus.QueryPoint{{Timestamp: 1, Value: 1}},
			}}
			target := []signoz.MetricSeries{{
				Labels: map[string]string{test.label: "api"},
				Values: []signoz.MetricPoint{{Timestamp: 1, Value: 1}},
			}}

			stats := Compare(source, target, Options{
				MinimumCoverage: 1, MinimumMatchedPoints: 1, TimestampTolerance: time.Millisecond,
			})
			assert.Equal(t, StatusEquivalent, stats.Status)
		})
	}
}

func TestCompareAcceptsExactDualLabelMapNamespaces(t *testing.T) {
	t.Parallel()

	source := []sourceprometheus.QuerySeries{{
		Labels: map[string]string{"job": "legacy", "service.name": "native"},
		Values: []sourceprometheus.QueryPoint{{Timestamp: 1, Value: 1}},
	}}
	target := []signoz.MetricSeries{{
		Labels: map[string]string{"job": "legacy", "service.name": "native"},
		Values: []signoz.MetricPoint{{Timestamp: 1, Value: 1}},
	}}

	stats := Compare(source, target, Options{
		MinimumCoverage: 1, MinimumMatchedPoints: 1, TimestampTolerance: time.Millisecond,
	})
	assert.Equal(t, StatusEquivalent, stats.Status)
	assert.Equal(t, 1, stats.MatchedSeries)
}

func TestCompareRejectsNonExactDualLabelMapNamespaces(t *testing.T) {
	t.Parallel()

	source := []sourceprometheus.QuerySeries{{
		Labels: map[string]string{"job": "legacy", "service.name": "native"},
		Values: []sourceprometheus.QueryPoint{{Timestamp: 1, Value: 1}},
	}}
	target := []signoz.MetricSeries{{
		Labels: map[string]string{"service.name": "legacy"},
		Values: []signoz.MetricPoint{{Timestamp: 1, Value: 1}},
	}}

	stats := Compare(source, target, Options{
		MinimumCoverage: 1, MinimumMatchedPoints: 1, TimestampTolerance: time.Millisecond,
	})
	assert.Equal(t, StatusNoSeriesMatch, stats.Status)
	assert.Equal(t, 1, stats.UnmatchedSourceSeries)
}

func TestCompareAppliesValueAliasesToEveryComparedLabel(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		label string
	}{
		{name: "ordinary label", label: "cluster"},
		{name: "native resource label", label: "service.name"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := []sourceprometheus.QuerySeries{{
				Labels: map[string]string{test.label: "prod"},
				Values: []sourceprometheus.QueryPoint{{Timestamp: 1, Value: 1}},
			}}
			target := []signoz.MetricSeries{{
				Labels: map[string]string{test.label: "production"},
				Values: []signoz.MetricPoint{{Timestamp: 1, Value: 1}},
			}}

			stats := Compare(source, target, Options{
				MinimumCoverage: 1, MinimumMatchedPoints: 1, TimestampTolerance: time.Millisecond,
				LabelValueAliases: map[string]map[string]string{
					test.label: {"production": "prod"},
				},
			})
			assert.Equal(t, StatusEquivalent, stats.Status)
			assert.Equal(t, 1, stats.MatchedSeries)
		})
	}
}

func TestCompareRejectsAliasInducedTargetSeriesAmbiguity(t *testing.T) {
	t.Parallel()

	source := []sourceprometheus.QuerySeries{{
		Labels: map[string]string{"cluster": "prod"},
		Values: []sourceprometheus.QueryPoint{{Timestamp: 1, Value: 1}},
	}}
	target := []signoz.MetricSeries{
		{
			Labels: map[string]string{"cluster": "prod"},
			Values: []signoz.MetricPoint{{Timestamp: 1, Value: 1}},
		},
		{
			Labels: map[string]string{"cluster": "production"},
			Values: []signoz.MetricPoint{{Timestamp: 1, Value: 1}},
		},
	}

	stats := Compare(source, target, Options{
		MinimumCoverage: 1, MinimumMatchedPoints: 1, TimestampTolerance: time.Millisecond,
		LabelValueAliases: map[string]map[string]string{
			"cluster": {"production": "prod"},
		},
	})
	assert.Equal(t, StatusNoSeriesMatch, stats.Status)
	assert.Zero(t, stats.MatchedSeries)
	assert.Equal(t, 2, stats.AmbiguousTargetSeries)
}

func TestCompareAppliesBuilderTimestampShiftOnlyWhenRequested(t *testing.T) {
	t.Parallel()

	source := []sourceprometheus.QuerySeries{{Values: []sourceprometheus.QueryPoint{{Timestamp: 60_000, Value: 1}}}}
	target := []signoz.MetricSeries{{Values: []signoz.MetricPoint{{Timestamp: 0, Value: 1}}}}

	builder := Compare(source, target, Options{
		TargetKind:           TargetKindBuilderQuery,
		TargetTimestampShift: time.Minute,
		TimestampTolerance:   time.Second,
		MinimumCoverage:      1,
		MinimumMatchedPoints: 1,
	})
	assert.Equal(t, StatusEquivalent, builder.Status)

	promQL := Compare(source, target, Options{
		TargetKind:           TargetKindPromQL,
		TimestampTolerance:   time.Second,
		MinimumCoverage:      1,
		MinimumMatchedPoints: 1,
	})
	assert.Equal(t, StatusInsufficientOverlap, promQL.Status)
}

func TestCompareExcludesPartialTargetPoints(t *testing.T) {
	t.Parallel()

	source := []sourceprometheus.QuerySeries{{Values: []sourceprometheus.QueryPoint{
		{Timestamp: 1_000, Value: 1},
		{Timestamp: 2_000, Value: 2},
	}}}
	target := []signoz.MetricSeries{{Values: []signoz.MetricPoint{
		{Timestamp: 1_000, Value: 999, Partial: true},
		{Timestamp: 2_000, Value: 2},
	}}}
	stats := Compare(source, target, Options{MinimumCoverage: 0.5, MinimumMatchedPoints: 1, TimestampTolerance: time.Millisecond})
	assert.Equal(t, StatusEquivalent, stats.Status)
	assert.Equal(t, 1, stats.TargetPoints)
	assert.Equal(t, 1, stats.ExcludedPartialPoints)
	assert.Equal(t, 1, stats.MatchedPoints)
}

func TestCompareTreatsAllPartialTargetDataAsInconclusive(t *testing.T) {
	t.Parallel()

	source := []sourceprometheus.QuerySeries{{Values: []sourceprometheus.QueryPoint{{Timestamp: 1_000, Value: 1}}}}
	target := []signoz.MetricSeries{{Values: []signoz.MetricPoint{{Timestamp: 1_000, Value: 1, Partial: true}}}}
	stats := Compare(source, target, Options{MinimumCoverage: 1})
	assert.Equal(t, StatusInsufficientOverlap, stats.Status)
	assert.Zero(t, stats.TargetPoints)
	assert.Equal(t, 1, stats.ExcludedPartialPoints)
}

func TestCompareDropsMetricNameForEveryTargetKind(t *testing.T) {
	t.Parallel()

	source := []sourceprometheus.QuerySeries{{
		Labels: map[string]string{"__name__": "up", "job": "api"},
		Values: []sourceprometheus.QueryPoint{{Timestamp: 1_000, Value: 1}},
	}}
	target := []signoz.MetricSeries{{
		Labels: map[string]string{"service.name": "api"},
		Values: []signoz.MetricPoint{{Timestamp: 1_000, Value: 1}},
	}}

	builder := Compare(source, target, Options{TargetKind: TargetKindBuilderQuery, MinimumCoverage: 1, MinimumMatchedPoints: 1})
	assert.Equal(t, StatusEquivalent, builder.Status)
	assert.Equal(t, 1, builder.MatchedSeries)

	// __name__ is the metric name and never a pairing label. A PromQL passthrough
	// target reconciles against the source exactly like a Builder target instead
	// of reporting a spurious no-series-match.
	promQL := Compare(source, target, Options{TargetKind: TargetKindPromQL, MinimumCoverage: 1, MinimumMatchedPoints: 1})
	assert.Equal(t, StatusEquivalent, promQL.Status)
	assert.Equal(t, 1, promQL.MatchedSeries)

	ambiguous := Compare(source, []signoz.MetricSeries{
		{Labels: map[string]string{"service.name": "api", "server.address": "one"}, Values: []signoz.MetricPoint{{Timestamp: 1_000, Value: 1}}},
		{Labels: map[string]string{"service.name": "api", "server.address": "two"}, Values: []signoz.MetricPoint{{Timestamp: 1_000, Value: 1}}},
	}, Options{
		TargetKind:           TargetKindBuilderFormula,
		TargetProvenance:     TargetProvenanceOTelPrometheusReceiver,
		MinimumCoverage:      1,
		MinimumMatchedPoints: 1,
	})
	assert.Equal(t, StatusNoSeriesMatch, ambiguous.Status)
	assert.Equal(t, 2, ambiguous.AmbiguousTargetSeries)
}

func TestCompareTreatsMatchingNaNAsEquivalent(t *testing.T) {
	t.Parallel()

	source := []sourceprometheus.QuerySeries{{Values: []sourceprometheus.QueryPoint{{Timestamp: 1_000, Value: math.NaN()}}}}
	target := []signoz.MetricSeries{{Values: []signoz.MetricPoint{{Timestamp: 1_000, Value: math.NaN()}}}}
	stats := Compare(source, target, Options{MinimumCoverage: 1, MinimumMatchedPoints: 1})
	assert.Equal(t, StatusEquivalent, stats.Status)
	assert.Equal(t, 1, stats.NonFiniteMatches)
}

func TestCompareDistinguishesBothEmptyFromTargetOnlyData(t *testing.T) {
	t.Parallel()

	assert.Equal(t, StatusBothEmpty, Compare(nil, nil, Options{}).Status)
	target := []signoz.MetricSeries{{Values: []signoz.MetricPoint{{Timestamp: 1, Value: 1}}}}
	stats := Compare(nil, target, Options{})
	assert.Equal(t, StatusTargetOnlyData, stats.Status)
	assert.Equal(t, 1, stats.UnmatchedTargetSeries)
}

func TestCompareReportsAdditionalNonEmptyTargetSeries(t *testing.T) {
	t.Parallel()

	source := []sourceprometheus.QuerySeries{{
		Labels: map[string]string{"job": "api"},
		Values: []sourceprometheus.QueryPoint{{Timestamp: 1_000, Value: 1}},
	}}
	target := []signoz.MetricSeries{
		{
			Labels: map[string]string{"service.name": "api"},
			Values: []signoz.MetricPoint{{Timestamp: 1_000, Value: 1}},
		},
		{
			Labels: map[string]string{"service.name": "worker"},
			Values: []signoz.MetricPoint{{Timestamp: 1_000, Value: 1}},
		},
	}

	stats := Compare(source, target, Options{MinimumCoverage: 1, MinimumMatchedPoints: 1})
	assert.Equal(t, StatusTargetOnlyData, stats.Status)
	assert.Equal(t, 1, stats.MatchedSeries)
	assert.Equal(t, 1, stats.UnmatchedTargetSeries)
}

func TestCompareIgnoresAdditionalEmptyTargetSeries(t *testing.T) {
	t.Parallel()

	source := []sourceprometheus.QuerySeries{{
		Labels: map[string]string{"job": "api"},
		Values: []sourceprometheus.QueryPoint{{Timestamp: 1_000, Value: 1}},
	}}
	target := []signoz.MetricSeries{
		{
			Labels: map[string]string{"service.name": "api"},
			Values: []signoz.MetricPoint{{Timestamp: 1_000, Value: 1}},
		},
		{Labels: map[string]string{"service.name": "worker"}},
	}

	stats := Compare(source, target, Options{MinimumCoverage: 1, MinimumMatchedPoints: 1})
	assert.Equal(t, StatusEquivalent, stats.Status)
	assert.Zero(t, stats.UnmatchedTargetSeries)
}

func TestAlignPointsMaximizesCardinalityBeforeTimestampSkew(t *testing.T) {
	t.Parallel()

	matches, err := alignPoints(
		[]sourceprometheus.QueryPoint{{Timestamp: 4, Value: 40}, {Timestamp: 6, Value: 60}},
		[]signoz.MetricPoint{{Timestamp: 0, Value: 0}, {Timestamp: 5, Value: 50}},
		5*time.Millisecond,
	)

	require.NoError(t, err)
	assert.Equal(t, []pointMatch{{source: 40, target: 0}, {source: 60, target: 50}}, matches)
}

func TestAlignPointsMinimizesSkewAfterCardinality(t *testing.T) {
	t.Parallel()

	matches, err := alignPoints(
		[]sourceprometheus.QueryPoint{{Timestamp: 4, Value: 40}, {Timestamp: 10, Value: 100}},
		[]signoz.MetricPoint{{Timestamp: 0, Value: 0}, {Timestamp: 5, Value: 50}, {Timestamp: 10, Value: 100}},
		5*time.Millisecond,
	)

	require.NoError(t, err)
	assert.Equal(t, []pointMatch{{source: 40, target: 50}, {source: 100, target: 100}}, matches)
}

func TestAlignPointsUsesStableTieBreakAndTimestampOrder(t *testing.T) {
	t.Parallel()

	tied, err := alignPoints(
		[]sourceprometheus.QueryPoint{{Timestamp: 5, Value: 50}},
		[]signoz.MetricPoint{{Timestamp: 10, Value: 100}, {Timestamp: 0, Value: 0}},
		5*time.Millisecond,
	)
	require.NoError(t, err)
	assert.Equal(t, []pointMatch{{source: 50, target: 0}}, tied)

	source := []sourceprometheus.QueryPoint{{Timestamp: 10, Value: 100}, {Timestamp: 5, Value: 50}}
	target := []signoz.MetricPoint{{Timestamp: 10, Value: 100}, {Timestamp: 0, Value: 0}}
	matches, err := alignPoints(source, target, 5*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, []pointMatch{{source: 50, target: 0}, {source: 100, target: 100}}, matches)

	for range 5 {
		repeated, repeatErr := alignPoints(source, target, 5*time.Millisecond)
		require.NoError(t, repeatErr)
		assert.Equal(t, matches, repeated)
	}
}

func TestAlignPointsMatchesExhaustiveObjectiveForSmallSeries(t *testing.T) {
	t.Parallel()

	timestampSets := [][]int64{nil}
	for mask := 1; mask < 1<<6; mask++ {
		if bits.OnesCount(uint(mask)) > 3 {
			continue
		}
		var timestamps []int64
		for timestamp := range 6 {
			if mask&(1<<timestamp) != 0 {
				timestamps = append(timestamps, int64(timestamp))
			}
		}
		timestampSets = append(timestampSets, timestamps)
	}

	for _, sourceTimestamps := range timestampSets {
		for _, targetTimestamps := range timestampSets {
			for toleranceMillis := int64(0); toleranceMillis <= 3; toleranceMillis++ {
				source := make([]sourceprometheus.QueryPoint, 0, len(sourceTimestamps))
				for _, timestamp := range slices.Backward(sourceTimestamps) {
					source = append(source, sourceprometheus.QueryPoint{Timestamp: timestamp, Value: float64(timestamp)})
				}
				target := make([]signoz.MetricPoint, 0, len(targetTimestamps))
				for _, timestamp := range slices.Backward(targetTimestamps) {
					target = append(target, signoz.MetricPoint{Timestamp: timestamp, Value: float64(timestamp)})
				}

				matches, err := alignPoints(source, target, time.Duration(toleranceMillis)*time.Millisecond)
				require.NoError(t, err)
				actual := alignmentObjective{count: len(matches)}
				for index, match := range matches {
					actual.skew += timestampSkew(int64(match.source), int64(match.target))
					if index > 0 {
						assert.Less(t, matches[index-1].source, match.source)
						assert.Less(t, matches[index-1].target, match.target)
					}
				}
				expected := exhaustiveAlignmentObjective(sourceTimestamps, targetTimestamps, toleranceMillis)
				assert.Equalf(t, expected, actual, "source=%v target=%v tolerance=%dms", sourceTimestamps, targetTimestamps, toleranceMillis)
			}
		}
	}
}

type alignmentObjective struct {
	count int
	skew  uint64
}

func exhaustiveAlignmentObjective(source, target []int64, toleranceMillis int64) alignmentObjective {
	memo := make(map[[2]int]alignmentObjective)
	var visit func(int, int) alignmentObjective
	visit = func(sourceIndex, targetIndex int) alignmentObjective {
		if sourceIndex == len(source) || targetIndex == len(target) {
			return alignmentObjective{}
		}
		key := [2]int{sourceIndex, targetIndex}
		if cached, found := memo[key]; found {
			return cached
		}
		best := betterAlignmentObjective(visit(sourceIndex+1, targetIndex), visit(sourceIndex, targetIndex+1))
		skew := timestampSkew(source[sourceIndex], target[targetIndex])
		if skew <= uint64(toleranceMillis) {
			matched := visit(sourceIndex+1, targetIndex+1)
			matched.count++
			matched.skew += skew
			best = betterAlignmentObjective(best, matched)
		}
		memo[key] = best
		return best
	}
	return visit(0, 0)
}

func betterAlignmentObjective(left, right alignmentObjective) alignmentObjective {
	if left.count != right.count {
		if left.count > right.count {
			return left
		}
		return right
	}
	if left.skew <= right.skew {
		return left
	}
	return right
}

func TestCompareUsesMaximumCardinalityAlignment(t *testing.T) {
	t.Parallel()

	source := []sourceprometheus.QuerySeries{{Values: []sourceprometheus.QueryPoint{
		{Timestamp: 4, Value: 1}, {Timestamp: 6, Value: 2},
	}}}
	target := []signoz.MetricSeries{{Values: []signoz.MetricPoint{
		{Timestamp: 0, Value: 1}, {Timestamp: 5, Value: 2},
	}}}
	stats := Compare(source, target, Options{
		TimestampTolerance:   5 * time.Millisecond,
		MinimumCoverage:      1,
		MinimumMatchedPoints: 2,
	})

	assert.Equal(t, StatusEquivalent, stats.Status)
	assert.Equal(t, 2, stats.MatchedPoints)
	assert.Equal(t, 2, stats.MinimumSeriesMatchedPoints)
}

func TestCompareRequiresDefaultMinimumPointsPerSeries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		matchedPoints int
		expected      Status
	}{
		{name: "zero", matchedPoints: 0, expected: StatusInsufficientOverlap},
		{name: "one", matchedPoints: 1, expected: StatusInsufficientOverlap},
		{name: "nine", matchedPoints: 9, expected: StatusInsufficientOverlap},
		{name: "ten", matchedPoints: 10, expected: StatusEquivalent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pointCount := max(test.matchedPoints, 10)
			sourcePoints := make([]sourceprometheus.QueryPoint, pointCount)
			targetPoints := make([]signoz.MetricPoint, pointCount)
			for index := range pointCount {
				sourcePoints[index] = sourceprometheus.QueryPoint{Timestamp: int64(index), Value: float64(index)}
				targetTimestamp := int64(index)
				if index >= test.matchedPoints {
					targetTimestamp += 10_000
				}
				targetPoints[index] = signoz.MetricPoint{Timestamp: targetTimestamp, Value: float64(index)}
			}
			stats := Compare(
				[]sourceprometheus.QuerySeries{{Values: sourcePoints}},
				[]signoz.MetricSeries{{Values: targetPoints}},
				Options{TimestampTolerance: time.Millisecond, MinimumCoverage: float64(test.matchedPoints) / float64(pointCount)},
			)
			assert.Equal(t, test.expected, stats.Status)
			assert.Equal(t, test.matchedPoints, stats.MinimumSeriesMatchedPoints)
		})
	}
}

func TestCompareMinimumPointsAppliesPerSeries(t *testing.T) {
	t.Parallel()

	for _, pointsPerSeries := range []int{1, 10} {
		source := make([]sourceprometheus.QuerySeries, 10)
		target := make([]signoz.MetricSeries, 10)
		for seriesIndex := range 10 {
			labels := map[string]string{"series": fmt.Sprintf("%d", seriesIndex)}
			source[seriesIndex].Labels = labels
			target[seriesIndex].Labels = maps.Clone(labels)
			for pointIndex := range pointsPerSeries {
				source[seriesIndex].Values = append(source[seriesIndex].Values, sourceprometheus.QueryPoint{
					Timestamp: int64(pointIndex), Value: float64(pointIndex),
				})
				target[seriesIndex].Values = append(target[seriesIndex].Values, signoz.MetricPoint{
					Timestamp: int64(pointIndex), Value: float64(pointIndex),
				})
			}
		}
		stats := Compare(source, target, Options{TimestampTolerance: time.Millisecond, MinimumCoverage: 1})
		assert.Equal(t, pointsPerSeries, stats.MinimumSeriesMatchedPoints)
		assert.Equal(t, 10*pointsPerSeries, stats.MatchedPoints)
		assert.Equal(t, 1.0, stats.Coverage)
		if pointsPerSeries < 10 {
			assert.Equal(t, StatusInsufficientOverlap, stats.Status)
		} else {
			assert.Equal(t, StatusEquivalent, stats.Status)
		}
	}
}

func TestComparePartialPointsDoNotSatisfyMinimum(t *testing.T) {
	t.Parallel()

	sourcePoints := make([]sourceprometheus.QueryPoint, 10)
	targetPoints := make([]signoz.MetricPoint, 10)
	for index := range 10 {
		sourcePoints[index] = sourceprometheus.QueryPoint{Timestamp: int64(index), Value: 1}
		targetPoints[index] = signoz.MetricPoint{Timestamp: int64(index), Value: 1, Partial: index == 9}
	}
	stats := Compare(
		[]sourceprometheus.QuerySeries{{Values: sourcePoints}},
		[]signoz.MetricSeries{{Values: targetPoints}},
		Options{TimestampTolerance: time.Millisecond, MinimumCoverage: 0.9},
	)
	assert.Equal(t, StatusInsufficientOverlap, stats.Status)
	assert.Equal(t, 9, stats.MinimumSeriesMatchedPoints)
	assert.Equal(t, 1, stats.ExcludedPartialPoints)
}

func TestValidateOptions(t *testing.T) {
	t.Parallel()

	assert.Error(t, ValidateOptions(Options{RelativeTolerance: -1}))
	assert.Error(t, ValidateOptions(Options{MinimumCoverage: 1.1}))
	assert.Error(t, ValidateOptions(Options{MinimumMatchedPoints: -1}))
	assert.Error(t, ValidateOptions(Options{TargetKind: "unknown"}))
	assert.Error(t, ValidateOptions(Options{TargetProvenance: "unknown"}))
	assert.Error(t, ValidateOptions(Options{LabelMap: map[string]string{"job": "service.name", "instance": "service.name"}}))
	assert.Error(t, ValidateOptions(Options{LabelMap: map[string]string{"": "service.name"}}))
	assert.NoError(t, ValidateOptions(Options{MinimumCoverage: 0.8}))
}

func TestValidateIgnoredTargetLabelsRequiresExactContextAndCanonicalOrder(t *testing.T) {
	t.Parallel()

	options := Options{
		TargetKind:       TargetKindPromQL,
		TargetProvenance: TargetProvenanceOTelPrometheusReceiver,
	}
	assert.NoError(t, ValidateIgnoredTargetLabels([]string{"server.address", "url.scheme"}, options))
	assert.Error(t, ValidateIgnoredTargetLabels([]string{"tenant"}, options))
	assert.Error(t, ValidateIgnoredTargetLabels([]string{"url.scheme", "server.address"}, options))
	assert.Error(t, ValidateIgnoredTargetLabels([]string{"server.address", "server.address"}, options))
	assert.Error(t, ValidateIgnoredTargetLabels([]string{"server.address"}, Options{TargetKind: TargetKindPromQL}))
}

func TestValidateOptionsRejectsNonFiniteTolerances(t *testing.T) {
	t.Parallel()

	tests := map[string]Options{
		"NaN relative tolerance":      {RelativeTolerance: math.NaN()},
		"infinite relative tolerance": {RelativeTolerance: math.Inf(1)},
		"NaN absolute tolerance":      {AbsoluteTolerance: math.NaN()},
		"infinite absolute tolerance": {AbsoluteTolerance: math.Inf(1)},
		"NaN minimum coverage":        {MinimumCoverage: math.NaN()},
		"infinite minimum coverage":   {MinimumCoverage: math.Inf(1)},
		"negative infinite tolerance": {AbsoluteTolerance: math.Inf(-1)},
		"negative infinite coverage":  {MinimumCoverage: math.Inf(-1)},
	}
	for name, options := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.ErrorContains(t, ValidateOptions(options), "finite")
		})
	}
}
