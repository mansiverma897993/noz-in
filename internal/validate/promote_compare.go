package validate

import (
	"math"
	"sort"
	"strings"

	"github.com/mansiverma897993/noz-in/internal/target/signoz"
)

type signozComparison struct {
	equivalent      bool
	matchedSeries   int
	matchedPoints   int
	withinTolerance int
	maxRelative     float64
	// phaseShifted is set when the Builder result matched the passthrough in
	// magnitude but was offset by one step in time (e.g. SigNoz `latest` labels
	// its bucket at the start, so builder[t] == passthrough[t+step]). Such a
	// pair is not temporally equivalent and must not be promoted.
	phaseShifted bool
}

// compareSignozSeries checks whether a Builder result and a PromQL passthrough
// result — both produced by the same target over the same window — are the same
// series with values within tolerance. Both sides share the target's label scheme
// (service.name / service.instance.id), so series pair by exact label identity
// after dropping __name__. Equivalence requires every builder series to pair, at
// least one matched point, and every matched point within tolerance.
func compareSignozSeries(builder, passthrough []signoz.MetricSeries, options PromoteOptions) signozComparison {
	relative := options.RelativeTolerance
	if relative <= 0 {
		relative = 0.01
	}
	absolute := options.AbsoluteTolerance
	if absolute < 0 {
		absolute = 0
	}
	minimumPoints := options.MinimumPoints
	if minimumPoints <= 0 {
		minimumPoints = 1
	}
	toleranceMillis := options.TimestampTolerance.Milliseconds()
	if toleranceMillis <= 0 {
		toleranceMillis = 60_000
	}

	// Derive the dominant sample step so the point comparison can never silently
	// pair builder[t] with passthrough[t±one-step]: capping the fuzzy timestamp
	// tolerance to under half a step keeps only genuinely-aligned matches, and a
	// one-step offset is surfaced by the phase-shift check below instead of being
	// absorbed into an in-tolerance magnitude match.
	step := dominantStep(builder, passthrough)
	if step > 0 {
		if half := step/2 - 1; toleranceMillis > half {
			toleranceMillis = half
		}
		if toleranceMillis < 0 {
			toleranceMillis = 0
		}
	}

	passthroughByKey := make(map[string]signoz.MetricSeries, len(passthrough))
	for _, series := range passthrough {
		passthroughByKey[seriesKey(series.Labels)] = series
	}

	result := signozComparison{}
	for _, builderSeries := range builder {
		match, found := passthroughByKey[seriesKey(builderSeries.Labels)]
		if !found {
			// A builder series with no passthrough counterpart is a divergence.
			return signozComparison{equivalent: false, matchedSeries: result.matchedSeries}
		}
		result.matchedSeries++
		points, within, maxRelative := comparePoints(builderSeries.Values, match.Values, toleranceMillis, relative, absolute)
		result.matchedPoints += points
		result.withinTolerance += within
		result.maxRelative = math.Max(result.maxRelative, maxRelative)
		if seriesPhaseShifted(builderSeries.Values, match.Values, step, minimumPoints, relative) {
			result.phaseShifted = true
		}
		if points > 0 && within != points {
			return signozComparison{
				equivalent: false, matchedSeries: result.matchedSeries,
				matchedPoints: result.matchedPoints, withinTolerance: result.withinTolerance,
				maxRelative:  result.maxRelative,
				phaseShifted: result.phaseShifted,
			}
		}
	}
	// A magnitude match that only holds one step out of phase is not equivalence:
	// the migrated native would render every point offset in time from its PromQL
	// source. Withhold promotion and let the caller surface the phase-shift reason.
	result.equivalent = !result.phaseShifted &&
		result.matchedSeries == len(builder) &&
		result.matchedSeries > 0 &&
		result.matchedPoints >= minimumPoints &&
		result.withinTolerance == result.matchedPoints
	return result
}

// dominantStep returns the most common positive spacing between consecutive
// sample timestamps across the compared series, in milliseconds, or 0 when it
// cannot be determined. Both probes are issued at the same step over the same
// window, so a single dominant spacing is expected.
func dominantStep(seriesSets ...[]signoz.MetricSeries) int64 {
	counts := make(map[int64]int)
	for _, set := range seriesSets {
		for _, series := range set {
			for i := 1; i < len(series.Values); i++ {
				delta := series.Values[i].Timestamp - series.Values[i-1].Timestamp
				if delta > 0 {
					counts[delta]++
				}
			}
		}
	}
	var best int64
	var bestCount int
	for delta, count := range counts {
		if count > bestCount || (count == bestCount && delta < best) {
			best, bestCount = delta, count
		}
	}
	return best
}

// seriesPhaseShifted reports whether a builder series matches its passthrough
// counterpart markedly better one step over than at the same timestamp — the
// signature of a temporal offset such as SigNoz `latest` bucketing. A constant
// or already-aligned series has ~zero same-slot error and is never flagged; a
// genuinely divergent series is not improved by a shift and is likewise not
// flagged (its divergence is reported through the magnitude comparison).
func seriesPhaseShifted(builder, passthrough []signoz.MetricPoint, step int64, minimumPoints int, relative float64) bool {
	if step <= 0 {
		return false
	}
	if minimumPoints < 1 {
		minimumPoints = 1
	}
	byTimestamp := make(map[int64]float64, len(passthrough))
	for _, point := range passthrough {
		if point.Partial {
			continue
		}
		byTimestamp[point.Timestamp] = point.Value
	}
	// Same-slot error must be real (not floating-point noise) before a shift can
	// be judged an improvement; this exempts constant and exact series.
	const sameSlotFloor = 1e-6
	// The two probes align to different epochs (the Builder buckets to the wall
	// clock; the PromQL passthrough to the window start), so their grids are a
	// sub-step apart and never share exact timestamps. Match to the nearest point
	// within half a step: at offset 0 that is the aligned counterpart, at offset
	// ±step it is the neighbour one bucket over.
	tolerance := step / 2
	sameErr, sameN := offsetError(builder, byTimestamp, 0, tolerance)
	if sameN < minimumPoints || sameErr <= sameSlotFloor {
		return false
	}
	for _, offset := range []int64{step, -step} {
		shiftErr, shiftN := offsetError(builder, byTimestamp, offset, tolerance)
		// The shifted fit must have real support and be dramatically better than
		// the same-slot fit (an order of magnitude), i.e. the series truly lives
		// on the shifted grid.
		if shiftN >= minimumPoints && shiftErr*8 <= sameErr && shiftErr <= relative {
			return true
		}
	}
	return false
}

// offsetError returns the maximum relative error between each builder point and
// the passthrough point nearest to its timestamp plus offset (within tolerance),
// along with the number of points that found a counterpart.
func offsetError(builder []signoz.MetricPoint, passthrough map[int64]float64, offset, tolerance int64) (float64, int) {
	var maxRelative float64
	var matched int
	for _, point := range builder {
		if point.Partial {
			continue
		}
		other, found := nearestValue(passthrough, point.Timestamp+offset, tolerance)
		if !found {
			continue
		}
		matched++
		absoluteError := math.Abs(point.Value - other)
		denominator := math.Max(math.Abs(point.Value), math.Abs(other))
		if denominator > 0 {
			maxRelative = math.Max(maxRelative, absoluteError/denominator)
		}
	}
	return maxRelative, matched
}

// promoteTargetOnlyLabels are labels the SigNoz OpenTelemetry Prometheus receiver
// attaches to every scraped series as scrape-target or scope metadata. They are
// never logical query dimensions — a Grafana dashboard authored against
// Prometheus could not have selected on them because they do not exist in the
// Prometheus source; the receiver mints them at ingest. The promotion gate
// compares a Builder candidate against its own PromQL passthrough on the same
// target, and the two execution paths treat these labels asymmetrically: the
// passthrough returns them verbatim on every raw series while a Builder
// aggregation collapses them away. Excluding them from series identity is what
// lets a genuine equivalence pair instead of being misreported as a series
// mismatch. The set mirrors the diff harness's prometheusReceiverTargetOnlyLabels
// so both comparison paths agree on what is target-execution noise.
var promoteTargetOnlyLabels = map[string]struct{}{
	"__scope.name__":       {},
	"__scope.schema_url__": {},
	"__scope.version__":    {},
	"__temporality__":      {},
	"fingerprint":          {},
	"server.address":       {},
	"server.port":          {},
	"url.scheme":           {},
}

// seriesKey renders a label set as a stable identity string, dropping labels that
// are not part of a series' logical identity. __name__ is excluded because the
// two probes carry different metric names. Empty-valued labels are excluded
// because in the Prometheus data model an empty value is identical to the label
// being absent, yet the Builder result reports every groupBy dimension even when
// its value is empty while the passthrough drops it. Target-only receiver labels
// (promoteTargetOnlyLabels) are excluded because they are scrape metadata the
// passthrough keeps and the Builder aggregation discards. Without these three
// exclusions a candidate that is numerically identical to its passthrough never
// pairs and a real equivalence is misreported as divergence.
func seriesKey(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for key, value := range labels {
		if key == "__name__" || value == "" {
			continue
		}
		if _, targetOnly := promoteTargetOnlyLabels[key]; targetOnly {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(labels[key])
		builder.WriteByte('\x00')
	}
	return builder.String()
}

func comparePoints(
	builder, passthrough []signoz.MetricPoint,
	toleranceMillis int64,
	relative, absolute float64,
) (matched, within int, maxRelative float64) {
	byTimestamp := make(map[int64]float64, len(passthrough))
	for _, point := range passthrough {
		if point.Partial {
			continue
		}
		byTimestamp[point.Timestamp] = point.Value
	}
	for _, point := range builder {
		if point.Partial {
			continue
		}
		other, found := nearestValue(byTimestamp, point.Timestamp, toleranceMillis)
		if !found {
			continue
		}
		matched++
		absoluteError := math.Abs(point.Value - other)
		denominator := math.Max(math.Abs(point.Value), math.Abs(other))
		relativeError := 0.0
		if denominator > 0 {
			relativeError = absoluteError / denominator
		}
		maxRelative = math.Max(maxRelative, relativeError)
		if absoluteError <= absolute || relativeError <= relative {
			within++
		}
	}
	return matched, within, maxRelative
}

func nearestValue(byTimestamp map[int64]float64, timestamp, toleranceMillis int64) (float64, bool) {
	if value, found := byTimestamp[timestamp]; found {
		return value, true
	}
	bestDelta := toleranceMillis + 1
	var best float64
	found := false
	for candidate, value := range byTimestamp {
		delta := candidate - timestamp
		if delta < 0 {
			delta = -delta
		}
		if delta <= toleranceMillis && delta < bestDelta {
			bestDelta = delta
			best = value
			found = true
		}
	}
	return best, found
}
