package signoz

import "time"

const recommendedMetricPoints = 300

// MinimumMetricStep mirrors SigNoz v0.133.0's
// querybuilder.MinAllowedStepIntervalForMetric. The pinned release uses the
// same range policy for its recommended PromQL step and its minimum Builder
// metric step. Keep the boundary tests synchronized with the pinned target.
func MinimumMetricStep(window time.Duration) time.Duration {
	seconds := uint64(max(window/time.Second, 0))
	step := seconds / recommendedMetricPoints
	if step < 60 {
		return time.Minute
	}
	recommended := step - step%60
	switch {
	case window >= 7*24*time.Hour:
		recommended = roundToPositiveMultiple(recommended, 1800)
	case window >= 24*time.Hour:
		recommended = roundToPositiveMultiple(recommended, 300)
	}
	return time.Duration(recommended) * time.Second
}

// RecommendedPromQLStep returns the backend-selected step for dashboard
// PromQL, whose frontend request intentionally leaves step unset.
func RecommendedPromQLStep(window time.Duration) time.Duration {
	return MinimumMetricStep(window)
}

func roundToPositiveMultiple(value, multiple uint64) uint64 {
	return ((value + multiple/2) / multiple) * multiple
}
