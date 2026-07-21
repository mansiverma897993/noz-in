package validate

import (
	"context"

	"github.com/mansiverma897993/signoz/internal/model"
)

// FidelityBand classifies how closely a candidate Builder query reproduces its
// source PromQL on the live target, from an exact match to a rejection.
type FidelityBand string

const (
	// FidelityExact means every matched point was numerically identical.
	FidelityExact FidelityBand = "exact"
	// FidelityWithin1Pct means the maximum relative deviation was at most 1%.
	FidelityWithin1Pct FidelityBand = "within_1pct"
	// FidelityWithin5Pct means the maximum relative deviation was at most 5%.
	FidelityWithin5Pct FidelityBand = "within_5pct"
	// FidelityDiverged means the deviation exceeded 5% on at least one point.
	FidelityDiverged FidelityBand = "diverged"
	// FidelityPhaseShift means the candidate matched in magnitude but was offset
	// one step in time (not temporally equivalent).
	FidelityPhaseShift FidelityBand = "phase_shift"
	// FidelitySeriesMismatch means the two results were not the same set of series.
	FidelitySeriesMismatch FidelityBand = "series_mismatch"
	// FidelityNoData means one or both probes returned no data to compare.
	FidelityNoData FidelityBand = "no_data"
	// FidelityProbeFailed means the probe could not be constructed or executed.
	FidelityProbeFailed FidelityBand = "probe_failed"
)

// FidelityResult is the verifiable evidence that an agent-proposed Builder query
// is (or is not) a faithful replacement for a source PromQL query on the target.
type FidelityResult struct {
	Band            FidelityBand `json:"band"`
	Pass            bool         `json:"pass"`
	Detail          string       `json:"detail"`
	MatchedSeries   int          `json:"matchedSeries"`
	MatchedPoints   int          `json:"matchedPoints"`
	WithinTolerance int          `json:"withinTolerance"`
	MaxRelative     float64      `json:"maxRelativeError"`
	Threshold       float64      `json:"threshold"`
}

// VerifyCandidate runs the live differential between an arbitrary candidate
// Builder/formula query and a source PromQL query, and returns the measured
// fidelity band. This is the public gate the assist skill uses: an agent may
// propose any Builder query, but only what VerifyCandidate confirms within the
// operator's threshold is safe to adopt. Pass is true when the candidate is the
// same set of series and its maximum relative deviation is within options'
// RelativeTolerance (the fidelity threshold).
func VerifyCandidate(
	ctx context.Context,
	executor NativeExecutor,
	candidate model.Translation,
	sourcePromQL string,
	options PromoteOptions,
) FidelityResult {
	threshold := options.RelativeTolerance
	if threshold <= 0 {
		threshold = 0.05
	}
	if executor == nil {
		return FidelityResult{Band: FidelityProbeFailed, Detail: "no live target executor", Threshold: threshold}
	}
	stats, reason, ok := probeAndCompare(ctx, executor, candidate, sourcePromQL, options)
	if !ok {
		band := FidelityProbeFailed
		if reason == "probe returned no data on the live target" {
			band = FidelityNoData
		}
		return FidelityResult{Band: band, Detail: reason, Threshold: threshold}
	}
	result := FidelityResult{
		MatchedSeries:   stats.matchedSeries,
		MatchedPoints:   stats.matchedPoints,
		WithinTolerance: stats.withinTolerance,
		MaxRelative:     stats.maxRelative,
		Threshold:       threshold,
	}
	switch {
	case stats.matchedSeries == 0 || stats.matchedPoints == 0:
		result.Band = FidelitySeriesMismatch
		result.Detail = "candidate produced a different set of series than the source"
	case stats.phaseShifted:
		result.Band = FidelityPhaseShift
		result.Detail = "candidate matched in magnitude but was offset one step in time; not temporally equivalent"
	case stats.maxRelative <= 1e-9:
		result.Band, result.Pass = FidelityExact, true
		result.Detail = "every matched point was identical"
	case stats.maxRelative <= 0.01:
		result.Band = FidelityWithin1Pct
		result.Pass = threshold >= 0.01
		result.Detail = "maximum deviation within 1%"
	case stats.maxRelative <= 0.05:
		result.Band = FidelityWithin5Pct
		result.Pass = threshold >= stats.maxRelative
		result.Detail = "maximum deviation within 5%"
	default:
		result.Band = FidelityDiverged
		result.Detail = "maximum deviation exceeded 5%"
	}
	return result
}
