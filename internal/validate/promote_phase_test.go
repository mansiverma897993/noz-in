package validate

import (
	"testing"

	"github.com/mansiverma897993/signoz/internal/target/signoz"
)

// step and window used by the phase-shift fixtures.
const phaseStepMillis = 60_000

func pointsAt(values ...float64) []signoz.MetricPoint {
	points := make([]signoz.MetricPoint, len(values))
	for i, value := range values {
		points[i] = signoz.MetricPoint{Timestamp: int64(i) * phaseStepMillis, Value: value}
	}
	return points
}

// Real values recorded live from SigNoz: the builder `latest` result is exactly
// the PromQL passthrough advanced by one step (builder[t] == passthrough[t+60s]),
// the one-bucket temporal offset the strict gate must reject despite a
// sub-tolerance same-slot magnitude difference.
var (
	memAvailablePassthrough = []float64{
		15683272704, 15684308992, 15684354048, 15684288512, 15683543040,
		15683649536, 15683444736, 15683809280, 15682973696, 15652872192, 15653339136,
	}
	memAvailableBuilder = []float64{
		15684308992, 15684354048, 15684288512, 15683543040, 15683649536,
		15683444736, 15683809280, 15682973696, 15652872192, 15653339136, 15653210112,
	}
)

func TestCompareRejectsOneStepPhaseShift(t *testing.T) {
	builder := []signoz.MetricSeries{{Labels: map[string]string{"__name__": "m"}, Values: pointsAt(memAvailableBuilder...)}}
	passthrough := []signoz.MetricSeries{{Labels: map[string]string{"__name__": "m"}, Values: pointsAt(memAvailablePassthrough...)}}

	stats := compareSignozSeries(builder, passthrough, PromoteOptions{RelativeTolerance: 0.05, MinimumPoints: 3})
	if !stats.phaseShifted {
		t.Fatalf("expected phaseShifted=true for a one-step-offset latest series")
	}
	if stats.equivalent {
		t.Fatalf("a phase-shifted series must not be equivalent (would promote to native)")
	}
}

func TestCompareAcceptsAlignedConstantSeries(t *testing.T) {
	// A constant gauge (e.g. MemTotal) has zero same-slot error and no phase to
	// shift: it must remain equivalent and eligible for native promotion.
	const c = 16466128896.0
	builder := []signoz.MetricSeries{{Labels: map[string]string{"__name__": "m"}, Values: pointsAt(c, c, c, c, c)}}
	passthrough := []signoz.MetricSeries{{Labels: map[string]string{"__name__": "m"}, Values: pointsAt(c, c, c, c, c)}}

	stats := compareSignozSeries(builder, passthrough, PromoteOptions{RelativeTolerance: 0.05, MinimumPoints: 3})
	if stats.phaseShifted {
		t.Fatalf("a constant series must not be flagged as phase-shifted")
	}
	if !stats.equivalent {
		t.Fatalf("an exact constant match must be equivalent")
	}
}

func TestCompareAcceptsAlignedMovingSeries(t *testing.T) {
	// A correctly-aligned moving series (same value at the same timestamp) must
	// stay equivalent: shifting it makes the fit worse, so it is not flagged.
	aligned := pointsAt(memAvailablePassthrough...)
	builder := []signoz.MetricSeries{{Labels: map[string]string{"__name__": "m"}, Values: aligned}}
	passthrough := []signoz.MetricSeries{{Labels: map[string]string{"__name__": "m"}, Values: aligned}}

	stats := compareSignozSeries(builder, passthrough, PromoteOptions{RelativeTolerance: 0.05, MinimumPoints: 3})
	if stats.phaseShifted {
		t.Fatalf("an aligned moving series must not be flagged as phase-shifted")
	}
	if !stats.equivalent {
		t.Fatalf("an aligned moving series must be equivalent")
	}
}

// TestCompareEmptyGroupByLabelPairsWithAbsent reproduces a live-audit finding:
// a Builder candidate grouped by a target-only receiver attribute (server.port)
// whose value is empty reports that dimension as an empty-valued label, while
// the PromQL passthrough omits the label entirely. The two series are the same
// series and must pair; before the fix the key mismatch misreported every such
// candidate as "diverged" (160 of 161 Node Exporter candidates in the live
// run).
func TestCompareEmptyGroupByLabelPairsWithAbsent(t *testing.T) {
	const c = 16466128896.0
	builder := []signoz.MetricSeries{{
		Labels: map[string]string{
			"service.name": "node-exporter", "service.instance.id": "source-node",
			"cpu": "", // empty groupBy dimension reported by the Builder
		},
		Values: pointsAt(c, c, c, c),
	}}
	passthrough := []signoz.MetricSeries{{
		Labels: map[string]string{
			"__name__":     "node_memory_MemTotal_bytes",
			"service.name": "node-exporter", "service.instance.id": "source-node",
		},
		Values: pointsAt(c, c, c, c),
	}}

	stats := compareSignozSeries(builder, passthrough, PromoteOptions{RelativeTolerance: 0.05, MinimumPoints: 3})
	if stats.matchedSeries != 1 {
		t.Fatalf("empty-valued builder label must pair with the label-absent passthrough series, matchedSeries=%d", stats.matchedSeries)
	}
	if !stats.equivalent {
		t.Fatalf("identical series must be equivalent once paired")
	}
}

// TestCompareValuedLabelStillSeparates confirms the empty-label and target-only
// rules do not weaken identity: a genuinely valued logical dimension on one side
// with no counterpart on the other still refuses to pair. The label must be a
// real dimension (cpu), not a receiver target-only label, which is dropped by
// design.
func TestCompareValuedLabelStillSeparates(t *testing.T) {
	const c = 42.0
	builder := []signoz.MetricSeries{{
		Labels: map[string]string{"service.name": "a", "cpu": "3"},
		Values: pointsAt(c, c, c, c),
	}}
	passthrough := []signoz.MetricSeries{{
		Labels: map[string]string{"service.name": "a"},
		Values: pointsAt(c, c, c, c),
	}}

	stats := compareSignozSeries(builder, passthrough, PromoteOptions{RelativeTolerance: 0.05, MinimumPoints: 3})
	if stats.equivalent {
		t.Fatalf("a valued label with no passthrough counterpart must not pair as equivalent")
	}
}

// TestCompareDropsReceiverTargetOnlyLabels reproduces the live OTel Prometheus
// receiver condition: the PromQL passthrough returns scrape-target metadata
// (server.address, url.scheme) on every raw series while the Builder aggregation
// collapses them away. The two must still pair on their shared logical dimensions
// so a numerically identical candidate is promoted instead of being misreported
// as a series mismatch.
func TestCompareDropsReceiverTargetOnlyLabels(t *testing.T) {
	const c = 16466128896.0
	builder := []signoz.MetricSeries{{
		Labels: map[string]string{"service.name": "node-exporter", "service.instance.id": "local-node"},
		Values: pointsAt(c, c, c, c),
	}}
	passthrough := []signoz.MetricSeries{{
		Labels: map[string]string{
			"__name__":     "node_memory_MemTotal_bytes",
			"service.name": "node-exporter", "service.instance.id": "local-node",
			"server.address": "local-node", "url.scheme": "http",
		},
		Values: pointsAt(c, c, c, c),
	}}

	stats := compareSignozSeries(builder, passthrough, PromoteOptions{RelativeTolerance: 0.05, MinimumPoints: 3})
	if stats.matchedSeries != 1 {
		t.Fatalf("receiver target-only labels must not block pairing, matchedSeries=%d", stats.matchedSeries)
	}
	if !stats.equivalent {
		t.Fatalf("identical series must be equivalent once the target-only labels are dropped")
	}
}

// TestCompareDistinctInstancesStillSeparate guards the receiver-label rule against
// over-merging: two series that share every dropped label but differ on a real
// dimension (service.instance.id, i.e. two hosts) must not collapse into one.
func TestCompareDistinctInstancesStillSeparate(t *testing.T) {
	const c = 100.0
	builder := []signoz.MetricSeries{{
		Labels: map[string]string{"service.instance.id": "host-a"},
		Values: pointsAt(c, c, c, c),
	}}
	passthrough := []signoz.MetricSeries{{
		Labels: map[string]string{
			"service.instance.id": "host-b",
			"server.address":      "host-b", "url.scheme": "http",
		},
		Values: pointsAt(c, c, c, c),
	}}

	stats := compareSignozSeries(builder, passthrough, PromoteOptions{RelativeTolerance: 0.05, MinimumPoints: 3})
	if stats.equivalent {
		t.Fatalf("series on different instances must not pair merely because target-only labels were dropped")
	}
}

// TestCompareRejectsPhaseShiftAcrossSubStepGrids reproduces the real-target
// condition that a naive exact-timestamp phase check missed: the Builder buckets
// to the wall clock while the PromQL passthrough aligns to the window start, so
// the two grids are a fraction of a step apart and never share an exact
// timestamp. The phase detection must still pair points by nearest-within-half-a-
// step and flag the one-step offset.
func TestCompareRejectsPhaseShiftAcrossSubStepGrids(t *testing.T) {
	const skew = 17_000 // builder grid offset from the passthrough grid, < step/2
	builderPoints := make([]signoz.MetricPoint, len(memAvailableBuilder))
	for i, v := range memAvailableBuilder {
		builderPoints[i] = signoz.MetricPoint{Timestamp: skew + int64(i)*phaseStepMillis, Value: v}
	}
	passthroughPoints := pointsAt(memAvailablePassthrough...)
	builder := []signoz.MetricSeries{{Labels: map[string]string{"__name__": "m"}, Values: builderPoints}}
	passthrough := []signoz.MetricSeries{{Labels: map[string]string{"__name__": "m"}, Values: passthroughPoints}}

	stats := compareSignozSeries(builder, passthrough, PromoteOptions{RelativeTolerance: 0.05, MinimumPoints: 3})
	if !stats.phaseShifted {
		t.Fatalf("expected phaseShifted=true even when the two probe grids are sub-step offset")
	}
	if stats.equivalent {
		t.Fatalf("a sub-step-offset phase shift must not be promoted to native")
	}
}
