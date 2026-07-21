// Package diff compares Prometheus and SigNoz query results.
package diff

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	sourceprometheus "github.com/mansiverma897993/signoz/internal/source/prometheus"
	"github.com/mansiverma897993/signoz/internal/target/signoz"
)

// Status is the outcome of one query comparison.
type Status string

const (
	StatusEquivalent          Status = "equivalent"
	StatusValueMismatch       Status = "value_mismatch"
	StatusInsufficientOverlap Status = "insufficient_overlap"
	StatusNoSourceData        Status = "no_source_data"
	StatusNoTargetData        Status = "no_target_data"
	StatusBothEmpty           Status = "both_empty"
	StatusTargetOnlyData      Status = "target_only_data"
	StatusNoSeriesMatch       Status = "no_series_match"
	StatusError               Status = "error"
	StatusSkipped             Status = "skipped"
)

// TargetKind identifies the emitted SigNoz query representation being compared.
type TargetKind string

const (
	TargetKindBuilderQuery   TargetKind = "builder_query"
	TargetKindBuilderFormula TargetKind = "builder_formula"
	TargetKindPromQL         TargetKind = "promql"
)

// TargetProvenance identifies the ingestion path whose label behavior is being
// normalized. Receiver-added labels are never ignored without this explicit
// provenance binding.
type TargetProvenance string

const (
	TargetProvenanceOTelPrometheusReceiver TargetProvenance = "otel_prometheus_receiver"
)

// Options controls label normalization, point alignment, and numeric tolerance.
type Options struct {
	TimestampTolerance   time.Duration
	TargetTimestampShift time.Duration
	TargetKind           TargetKind
	TargetProvenance     TargetProvenance
	RelativeTolerance    float64
	AbsoluteTolerance    float64
	MinimumCoverage      float64
	MinimumMatchedPoints int
	LabelMap             map[string]string
	LabelValueAliases    map[string]map[string]string
}

// Stats summarizes one source/target result comparison.
type Stats struct {
	Status                     Status   `json:"status"`
	SourceSeries               int      `json:"sourceSeries"`
	TargetSeries               int      `json:"targetSeries"`
	MatchedSeries              int      `json:"matchedSeries"`
	UnmatchedSourceSeries      int      `json:"unmatchedSourceSeries,omitempty"`
	AmbiguousTargetSeries      int      `json:"ambiguousTargetSeries,omitempty"`
	UnmatchedTargetSeries      int      `json:"unmatchedTargetSeries,omitempty"`
	SourcePoints               int      `json:"sourcePoints"`
	TargetPoints               int      `json:"targetPoints"`
	ExcludedPartialPoints      int      `json:"excludedPartialTargetPoints,omitempty"`
	MatchedPoints              int      `json:"matchedPoints"`
	MinimumSeriesMatchedPoints int      `json:"minimumSeriesMatchedPoints"`
	WithinTolerance            int      `json:"withinTolerance"`
	NonFiniteMatches           int      `json:"nonFiniteMatches,omitempty"`
	Coverage                   float64  `json:"coverage"`
	MeanAbsoluteError          float64  `json:"meanAbsoluteError"`
	MaxAbsoluteError           float64  `json:"maxAbsoluteError"`
	MaxRelativeError           float64  `json:"maxRelativeError"`
	AlignmentError             string   `json:"alignmentError,omitempty"`
	IgnoredTargetLabels        []string `json:"ignoredTargetLabels,omitempty"`
}

// Compare aligns logical series and timestamp-compatible samples, then computes numeric drift.
func Compare(source []sourceprometheus.QuerySeries, target []signoz.MetricSeries, options Options) Stats {
	options = withDefaults(options)
	stats := Stats{SourceSeries: len(source), TargetSeries: len(target)}
	for _, series := range source {
		stats.SourcePoints += len(series.Values)
	}

	normalizedTarget, nonEmptyTargetSeries := normalizeTargetSeries(target, options, &stats)
	if comparisonComplete(&stats, nonEmptyTargetSeries) {
		return stats
	}

	candidatesBySource, targetCandidateSources, ambiguousTargets := matchSeries(source, normalizedTarget, options, &stats)
	absoluteErrorTotal, finiteMatches, usedTarget, err := compareMatchedSeries(
		source,
		normalizedTarget,
		candidatesBySource,
		targetCandidateSources,
		ambiguousTargets,
		options,
		&stats,
	)
	if err != nil {
		stats.Status = StatusError
		stats.AlignmentError = err.Error()
		return stats
	}
	stats.AmbiguousTargetSeries = len(ambiguousTargets)
	stats.UnmatchedTargetSeries = unmatchedTargetSeries(normalizedTarget, usedTarget)
	if finiteMatches > 0 {
		stats.MeanAbsoluteError = absoluteErrorTotal / float64(finiteMatches)
	}
	stats.Coverage = float64(stats.MatchedPoints) / float64(stats.SourcePoints)
	stats.Status = comparisonStatus(stats, options)
	return stats
}

func normalizeTargetSeries(
	target []signoz.MetricSeries,
	options Options,
	stats *Stats,
) ([]signoz.MetricSeries, int) {
	normalizedTarget := make([]signoz.MetricSeries, 0, len(target))
	nonEmptyTargetSeries := 0
	for _, series := range target {
		normalizedLabels := maps.Clone(series.Labels)
		// __name__ is the metric name, identical on both sides by construction, but
		// SigNoz and Prometheus can render it differently (dot vs underscore
		// notation). Pairing on it never adds information and produces spurious
		// "no series match" outcomes, so it is dropped for every target kind.
		delete(normalizedLabels, "__name__")
		normalized := signoz.MetricSeries{Labels: normalizedLabels}
		for _, point := range series.Values {
			if point.Partial {
				stats.ExcludedPartialPoints++
				continue
			}
			point.Timestamp += options.TargetTimestampShift.Milliseconds()
			normalized.Values = append(normalized.Values, point)
		}
		stats.TargetPoints += len(normalized.Values)
		if len(normalized.Values) > 0 {
			nonEmptyTargetSeries++
		}
		normalizedTarget = append(normalizedTarget, normalized)
	}
	return normalizedTarget, nonEmptyTargetSeries
}

func comparisonComplete(stats *Stats, nonEmptyTargetSeries int) bool {
	switch {
	case stats.TargetPoints == 0 && stats.ExcludedPartialPoints > 0:
		stats.Status = StatusInsufficientOverlap
	case stats.SourcePoints == 0 && stats.TargetPoints == 0:
		stats.Status = StatusBothEmpty
	case stats.SourcePoints == 0:
		stats.UnmatchedTargetSeries = nonEmptyTargetSeries
		stats.Status = StatusTargetOnlyData
	case stats.SourceSeries == 0:
		stats.Status = StatusNoSourceData
	case stats.TargetPoints == 0:
		stats.Status = StatusNoTargetData
	default:
		return false
	}
	return true
}

func matchSeries(
	source []sourceprometheus.QuerySeries,
	target []signoz.MetricSeries,
	options Options,
	stats *Stats,
) ([][]int, []int, map[int]bool) {
	candidatesBySource := make([][]int, len(source))
	targetCandidateSources := make([]int, len(target))
	ambiguousTargets := make(map[int]bool)
	for index, series := range source {
		sourceLabels := normalizeSourceLabels(series.Labels, options)
		candidates, ignoredLabels := matchingTargetSeries(sourceLabels, target, options)
		stats.IgnoredTargetLabels = append(stats.IgnoredTargetLabels, ignoredLabels...)
		candidatesBySource[index] = candidates
		if len(series.Values) > 0 && len(candidates) == 0 {
			stats.UnmatchedSourceSeries++
		}
		for _, targetIndex := range candidates {
			targetCandidateSources[targetIndex]++
			if len(candidates) > 1 {
				ambiguousTargets[targetIndex] = true
			}
		}
	}
	slices.Sort(stats.IgnoredTargetLabels)
	stats.IgnoredTargetLabels = slices.Compact(stats.IgnoredTargetLabels)
	return candidatesBySource, targetCandidateSources, ambiguousTargets
}

func compareMatchedSeries(
	source []sourceprometheus.QuerySeries,
	target []signoz.MetricSeries,
	candidatesBySource [][]int,
	targetCandidateSources []int,
	ambiguousTargets map[int]bool,
	options Options,
	stats *Stats,
) (float64, int, map[int]bool, error) {
	var absoluteErrorTotal float64
	var finiteMatches int
	usedTarget := make(map[int]bool, len(target))
	for sourceIndex, sourceSeries := range source {
		candidates := candidatesBySource[sourceIndex]
		if len(candidates) != 1 {
			continue
		}
		targetIndex := candidates[0]
		if targetCandidateSources[targetIndex] != 1 {
			ambiguousTargets[targetIndex] = true
			continue
		}
		usedTarget[targetIndex] = true
		stats.MatchedSeries++
		matches, err := alignPoints(sourceSeries.Values, target[targetIndex].Values, options.TimestampTolerance)
		if err != nil {
			return 0, 0, nil, err
		}
		if stats.MatchedSeries == 1 || len(matches) < stats.MinimumSeriesMatchedPoints {
			stats.MinimumSeriesMatchedPoints = len(matches)
		}
		seriesAbsoluteError, seriesFiniteMatches := accumulatePointMatches(matches, options, stats)
		absoluteErrorTotal += seriesAbsoluteError
		finiteMatches += seriesFiniteMatches
	}
	return absoluteErrorTotal, finiteMatches, usedTarget, nil
}

func accumulatePointMatches(matches []pointMatch, options Options, stats *Stats) (float64, int) {
	var absoluteErrorTotal float64
	var finiteMatches int
	for _, match := range matches {
		stats.MatchedPoints++
		if nonFiniteEqual(match.source, match.target) {
			stats.WithinTolerance++
			stats.NonFiniteMatches++
			continue
		}
		if !isFinite(match.source) || !isFinite(match.target) {
			continue
		}
		absoluteError := math.Abs(match.source - match.target)
		relativeError := relativeError(match.source, match.target, absoluteError)
		absoluteErrorTotal += absoluteError
		finiteMatches++
		stats.MaxAbsoluteError = max(stats.MaxAbsoluteError, absoluteError)
		stats.MaxRelativeError = max(stats.MaxRelativeError, relativeError)
		if absoluteError <= options.AbsoluteTolerance || relativeError <= options.RelativeTolerance {
			stats.WithinTolerance++
		}
	}
	return absoluteErrorTotal, finiteMatches
}

func unmatchedTargetSeries(target []signoz.MetricSeries, usedTarget map[int]bool) int {
	unmatched := 0
	for index, series := range target {
		if !usedTarget[index] && len(series.Values) > 0 {
			unmatched++
		}
	}
	return unmatched
}

func withDefaults(options Options) Options {
	if options.TimestampTolerance <= 0 {
		options.TimestampTolerance = time.Minute
	}
	if options.RelativeTolerance < 0 {
		options.RelativeTolerance = 0
	}
	if options.AbsoluteTolerance < 0 {
		options.AbsoluteTolerance = 0
	}
	if options.MinimumCoverage <= 0 || options.MinimumCoverage > 1 {
		options.MinimumCoverage = 0.8
	}
	if options.MinimumMatchedPoints <= 0 {
		options.MinimumMatchedPoints = 10
	}
	if options.LabelMap == nil {
		options.LabelMap = map[string]string{
			"instance": "service.instance.id",
			"job":      "service.name",
		}
	}
	return options
}

func normalizeTargetLabels(
	labels map[string]string,
	sourceLabels map[string]string,
	options Options,
) (map[string]string, bool) {
	normalized := make(map[string]string, len(labels))
	maps.Copy(normalized, labels)
	mappedFromLabels := make([]string, 0, len(options.LabelMap))
	for sourceLabel := range options.LabelMap {
		mappedFromLabels = append(mappedFromLabels, sourceLabel)
	}
	slices.Sort(mappedFromLabels)
	seenTargets := make(map[string]string, len(mappedFromLabels))
	conflict := false
	for _, sourceLabel := range mappedFromLabels {
		targetLabel := options.LabelMap[sourceLabel]
		if previousSource, duplicate := seenTargets[targetLabel]; duplicate && previousSource != sourceLabel {
			conflict = true
			continue
		}
		seenTargets[targetLabel] = sourceLabel
		_, sourceUsesMappedFrom := sourceLabels[sourceLabel]
		_, sourceUsesMappedTo := sourceLabels[targetLabel]
		if sourceLabel != targetLabel && sourceUsesMappedFrom && sourceUsesMappedTo {
			conflict = true
			continue
		}
		if !sourceUsesMappedFrom {
			// The source already uses the target-side name (or neither name), so
			// direct exact matching is the only sound interpretation.
			continue
		}
		value, ok := normalized[targetLabel]
		if !ok {
			continue
		}
		delete(normalized, targetLabel)
		// A target result that carries both sides of a configured remap has an
		// extra logical label. Even equal values are not evidence that the
		// duplicate is receiver-added or otherwise safe to discard.
		if _, found := normalized[sourceLabel]; found && sourceLabel != targetLabel {
			conflict = true
			continue
		}
		delete(normalized, targetLabel)
		normalized[sourceLabel] = value
	}
	return applyTargetValueAliases(normalized, sourceLabels, options.LabelValueAliases), conflict
}

func applyTargetValueAliases(
	labels map[string]string,
	sourceLabels map[string]string,
	aliasesByLabel map[string]map[string]string,
) map[string]string {
	normalized := maps.Clone(labels)
	for label, value := range normalized {
		if _, compared := sourceLabels[label]; !compared {
			continue
		}
		if sourceValue, found := aliasesByLabel[label][value]; found {
			normalized[label] = sourceValue
		}
	}
	return normalized
}

func normalizeSourceLabels(labels map[string]string, _ Options) map[string]string {
	normalized := make(map[string]string, len(labels))
	maps.Copy(normalized, labels)
	// Mirror the target-side normalization: __name__ is the metric name, always
	// identical for a paired query, so it is never a pairing label.
	delete(normalized, "__name__")
	return normalized
}

func matchingTargetSeries(
	sourceLabels map[string]string,
	target []signoz.MetricSeries,
	options Options,
) ([]int, []string) {
	candidates := make([]int, 0, 1)
	ignoredLabels := make([]string, 0)
	for index, series := range target {
		// Prefer an exact label interpretation. A source may legitimately carry
		// both the legacy and resource-style names; remapping such an already
		// exact target would turn valid evidence into an artificial conflict.
		directLabels := applyTargetValueAliases(series.Labels, sourceLabels, options.LabelValueAliases)
		if matches, ignored := labelsMatch(sourceLabels, directLabels, targetOnlyLabelAllowlist(options)); matches {
			candidates = append(candidates, index)
			ignoredLabels = append(ignoredLabels, ignored...)
			continue
		}

		targetLabels, labelConflict := normalizeTargetLabels(series.Labels, sourceLabels, options)
		if labelConflict {
			continue
		}
		matches, ignored := labelsMatch(sourceLabels, targetLabels, targetOnlyLabelAllowlist(options))
		if !matches {
			continue
		}
		candidates = append(candidates, index)
		ignoredLabels = append(ignoredLabels, ignored...)
	}
	return candidates, ignoredLabels
}

func labelsMatch(source, target map[string]string, allowedTargetOnly map[string]struct{}) (bool, []string) {
	for key, sourceValue := range source {
		targetValue, found := target[key]
		if !found || targetValue != sourceValue {
			return false, nil
		}
	}
	ignored := make([]string, 0)
	for key := range target {
		if _, found := source[key]; found {
			continue
		}
		if _, allowed := allowedTargetOnly[key]; !allowed {
			return false, nil
		}
		ignored = append(ignored, key)
	}
	slices.Sort(ignored)
	return true, ignored
}

var prometheusReceiverTargetOnlyLabels = map[string]struct{}{
	"__scope.name__":       {},
	"__scope.schema_url__": {},
	"__scope.version__":    {},
	"__temporality__":      {},
	"fingerprint":          {},
	"server.address":       {},
	"server.port":          {},
	"url.scheme":           {},
}

func targetOnlyLabelAllowlist(options Options) map[string]struct{} {
	if options.TargetProvenance != TargetProvenanceOTelPrometheusReceiver {
		return nil
	}
	switch options.TargetKind {
	case TargetKindBuilderQuery, TargetKindBuilderFormula, TargetKindPromQL:
		return prometheusReceiverTargetOnlyLabels
	default:
		return nil
	}
}

type pointMatch struct {
	source float64
	target float64
}

const (
	maxAlignmentPoints     = 1_000_000
	maxAlignmentCandidates = 1_000_000
)

type alignmentState struct {
	count       int
	skew        uint64
	parent      int
	sourceIndex int
	targetIndex int
}

type alignmentFenwick struct {
	best []int
}

// alignPoints treats every within-tolerance pair as an edge in an ordered
// bipartite graph. A Fenwick-tree dynamic program finds the longest increasing
// edge chain, then the lowest total timestamp skew among chains of that length.
// Complexity is O((n+m+E) log m) time and O(m+E) memory for E candidate pairs.
// The explicit point/candidate limits return an error instead of approximating
// when an unusually dense tolerance window would exceed the memory budget.
func alignPoints(source []sourceprometheus.QueryPoint, target []signoz.MetricPoint, tolerance time.Duration) ([]pointMatch, error) {
	if len(source) == 0 || len(target) == 0 {
		return nil, nil
	}
	if tolerance < 0 {
		return nil, fmt.Errorf("timestamp tolerance must not be negative")
	}
	if len(source) > maxAlignmentPoints || len(target) > maxAlignmentPoints {
		return nil, fmt.Errorf(
			"point alignment exceeds safety limit of %d points per series (source=%d target=%d)",
			maxAlignmentPoints, len(source), len(target),
		)
	}
	source = append([]sourceprometheus.QueryPoint(nil), source...)
	target = append([]signoz.MetricPoint(nil), target...)
	sort.SliceStable(source, func(left, right int) bool { return source[left].Timestamp < source[right].Timestamp })
	sort.SliceStable(target, func(left, right int) bool { return target[left].Timestamp < target[right].Timestamp })
	toleranceMillis := tolerance.Milliseconds()
	states := make([]alignmentState, 0, min(maxAlignmentCandidates, min(len(source), len(target))))
	tree := newAlignmentFenwick(len(target))
	left, right := 0, 0
	for sourceIndex, sourcePoint := range source {
		lower := saturatingSubtract(sourcePoint.Timestamp, toleranceMillis)
		upper := saturatingAdd(sourcePoint.Timestamp, toleranceMillis)
		for left < len(target) && target[left].Timestamp < lower {
			left++
		}
		if right < left {
			right = left
		}
		for right < len(target) && target[right].Timestamp <= upper {
			right++
		}
		candidateCount := right - left
		if candidateCount > maxAlignmentCandidates-len(states) {
			return nil, fmt.Errorf(
				"point alignment exceeds safety limit of %d candidate pairs",
				maxAlignmentCandidates,
			)
		}
		groupStart := len(states)
		for targetIndex := left; targetIndex < right; targetIndex++ {
			parent := tree.query(targetIndex, states)
			state := alignmentState{
				count:       1,
				skew:        timestampSkew(sourcePoint.Timestamp, target[targetIndex].Timestamp),
				parent:      parent,
				sourceIndex: sourceIndex,
				targetIndex: targetIndex,
			}
			if parent >= 0 {
				state.count += states[parent].count
				state.skew += states[parent].skew
			}
			states = append(states, state)
		}
		// Apply a source point's candidates only after the entire group is
		// evaluated so one source point cannot appear twice in a chain.
		for stateIndex := groupStart; stateIndex < len(states); stateIndex++ {
			tree.update(states[stateIndex].targetIndex, stateIndex, states)
		}
	}

	best := tree.query(len(target), states)
	if best < 0 {
		return nil, nil
	}
	matches := make([]pointMatch, 0, states[best].count)
	for stateIndex := best; stateIndex >= 0; stateIndex = states[stateIndex].parent {
		state := states[stateIndex]
		matches = append(matches, pointMatch{
			source: source[state.sourceIndex].Value,
			target: target[state.targetIndex].Value,
		})
	}
	slices.Reverse(matches)
	return matches, nil
}

func newAlignmentFenwick(size int) alignmentFenwick {
	best := make([]int, size+1)
	for index := range best {
		best[index] = -1
	}
	return alignmentFenwick{best: best}
}

func (tree alignmentFenwick) query(exclusiveTargetIndex int, states []alignmentState) int {
	best := -1
	for index := exclusiveTargetIndex; index > 0; index -= index & -index {
		best = betterAlignmentState(best, tree.best[index], states)
	}
	return best
}

func (tree alignmentFenwick) update(targetIndex, stateIndex int, states []alignmentState) {
	for index := targetIndex + 1; index < len(tree.best); index += index & -index {
		tree.best[index] = betterAlignmentState(tree.best[index], stateIndex, states)
	}
}

func betterAlignmentState(left, right int, states []alignmentState) int {
	if left < 0 {
		return right
	}
	if right < 0 {
		return left
	}
	if states[left].count != states[right].count {
		if states[left].count > states[right].count {
			return left
		}
		return right
	}
	if states[left].skew != states[right].skew {
		if states[left].skew < states[right].skew {
			return left
		}
		return right
	}
	// State indices follow stable source-timestamp/target-timestamp order, so
	// the earliest chain wins exact objective ties deterministically.
	return min(left, right)
}

func timestampSkew(left, right int64) uint64 {
	orderedLeft := uint64(left) ^ (uint64(1) << 63)
	orderedRight := uint64(right) ^ (uint64(1) << 63)
	if orderedLeft >= orderedRight {
		return orderedLeft - orderedRight
	}
	return orderedRight - orderedLeft
}

func saturatingSubtract(value, delta int64) int64 {
	if delta > 0 && value < math.MinInt64+delta {
		return math.MinInt64
	}
	return value - delta
}

func saturatingAdd(value, delta int64) int64 {
	if delta > 0 && value > math.MaxInt64-delta {
		return math.MaxInt64
	}
	return value + delta
}

func comparisonStatus(stats Stats, options Options) Status {
	if stats.MatchedSeries == 0 {
		return StatusNoSeriesMatch
	}
	if stats.UnmatchedSourceSeries > 0 {
		return StatusNoSeriesMatch
	}
	if stats.UnmatchedTargetSeries > 0 {
		return StatusTargetOnlyData
	}
	if stats.Coverage < options.MinimumCoverage {
		return StatusInsufficientOverlap
	}
	if stats.MinimumSeriesMatchedPoints < options.MinimumMatchedPoints {
		return StatusInsufficientOverlap
	}
	if stats.WithinTolerance != stats.MatchedPoints {
		return StatusValueMismatch
	}
	return StatusEquivalent
}

func relativeError(source, target, absolute float64) float64 {
	scale := max(math.Abs(source), math.Abs(target))
	if scale == 0 {
		return 0
	}
	return absolute / scale
}

func nonFiniteEqual(left, right float64) bool {
	return math.IsNaN(left) && math.IsNaN(right) || math.IsInf(left, 1) && math.IsInf(right, 1) || math.IsInf(left, -1) && math.IsInf(right, -1)
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

// AliasMap builds a one-to-one target-to-source label value mapping.
func AliasMap(sourceValue, targetValue string) map[string]string {
	if strings.TrimSpace(sourceValue) == "" || strings.TrimSpace(targetValue) == "" || sourceValue == targetValue {
		return nil
	}
	return map[string]string{targetValue: sourceValue}
}

// ValidateOptions rejects invalid user-provided tolerances.
func ValidateOptions(options Options) error {
	switch options.TargetKind {
	case "", TargetKindBuilderQuery, TargetKindBuilderFormula, TargetKindPromQL:
	default:
		return fmt.Errorf("unsupported target kind %q", options.TargetKind)
	}
	switch options.TargetProvenance {
	case "", TargetProvenanceOTelPrometheusReceiver:
	default:
		return fmt.Errorf("unsupported target provenance %q", options.TargetProvenance)
	}
	if options.TimestampTolerance < 0 {
		return fmt.Errorf("timestamp tolerance must not be negative")
	}
	if !isFinite(options.RelativeTolerance) {
		return fmt.Errorf("relative tolerance must be finite")
	}
	if options.RelativeTolerance < 0 {
		return fmt.Errorf("relative tolerance must not be negative")
	}
	if !isFinite(options.AbsoluteTolerance) {
		return fmt.Errorf("absolute tolerance must be finite")
	}
	if options.AbsoluteTolerance < 0 {
		return fmt.Errorf("absolute tolerance must not be negative")
	}
	if !isFinite(options.MinimumCoverage) {
		return fmt.Errorf("minimum coverage must be finite")
	}
	if options.MinimumCoverage < 0 || options.MinimumCoverage > 1 {
		return fmt.Errorf("minimum coverage must be between 0 and 1")
	}
	if options.MinimumMatchedPoints < 0 {
		return fmt.Errorf("minimum matched points must not be negative")
	}
	seenTargetLabels := make(map[string]string, len(options.LabelMap))
	for sourceLabel, targetLabel := range options.LabelMap {
		if strings.TrimSpace(sourceLabel) == "" || strings.TrimSpace(targetLabel) == "" {
			return fmt.Errorf("label map names must not be empty")
		}
		if previousSource, duplicate := seenTargetLabels[targetLabel]; duplicate && previousSource != sourceLabel {
			return fmt.Errorf("target label %q is mapped from both %q and %q", targetLabel, previousSource, sourceLabel)
		}
		seenTargetLabels[targetLabel] = sourceLabel
	}
	return nil
}

// ValidateIgnoredTargetLabels verifies that recorded label exceptions are a
// sorted, unique subset of the allowlist for the exact target context.
func ValidateIgnoredTargetLabels(labels []string, options Options) error {
	allowed := targetOnlyLabelAllowlist(options)
	for index, label := range labels {
		if strings.TrimSpace(label) == "" {
			return fmt.Errorf("ignored target label must not be empty")
		}
		if index > 0 && labels[index-1] >= label {
			return fmt.Errorf("ignored target labels must be sorted and unique")
		}
		if _, ok := allowed[label]; !ok {
			return fmt.Errorf(
				"target label %q is not allowed for target kind %q and provenance %q",
				label, options.TargetKind, options.TargetProvenance,
			)
		}
	}
	return nil
}
