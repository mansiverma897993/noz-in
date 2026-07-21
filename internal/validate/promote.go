package validate

import (
	"context"
	"slices"
	"time"

	"github.com/mansiverma897993/signoz/internal/model"
	"github.com/mansiverma897993/signoz/internal/target/signoz"
)

// NativeExecutor runs v5 query_range requests and returns the series produced for
// every named query. *signoz.Client satisfies it.
type NativeExecutor interface {
	QueryRangeSeries(ctx context.Context, request signoz.QueryRangeRequest) (map[string][]signoz.MetricSeries, error)
}

// PromoteOptions parameterizes the live native-promotion differential.
type PromoteOptions struct {
	Now                time.Time
	Window             time.Duration
	Variables          map[string]signoz.VariableItem
	RelativeTolerance  float64
	AbsoluteTolerance  float64
	TimestampTolerance time.Duration
	MinimumPoints      int
}

// PromotionRecord is the per-query evidence for a native-promotion attempt.
type PromotionRecord struct {
	QuerySourcePath string  `json:"querySourcePath"`
	Promoted        bool    `json:"promoted"`
	Reason          string  `json:"reason"`
	MatchedSeries   int     `json:"matchedSeries"`
	MatchedPoints   int     `json:"matchedPoints"`
	WithinTolerance int     `json:"withinTolerance"`
	MaxRelative     float64 `json:"maxRelativeError"`
}

// PromoteNativeCandidates proves, against the live target, that each Builder or
// formula candidate is numerically equivalent to its own PromQL passthrough, and
// promotes the passing ones to a native verdict in place. A translation is only
// ever promoted after a passing differential, so the invariant "native implies a
// passing differential record" holds by construction. Candidates that fail to
// execute, return no data, or diverge are left as review/passthrough — never
// promoted on uncertainty.
func PromoteNativeCandidates(
	ctx context.Context,
	executor NativeExecutor,
	migration model.Migration,
	options PromoteOptions,
) []PromotionRecord {
	records := make([]PromotionRecord, 0)
	if executor == nil {
		return records
	}
	for path, translation := range migration.Translations {
		if !nativeCandidate(translation) {
			continue
		}
		record := PromotionRecord{QuerySourcePath: path}
		updated := promoteCandidate(ctx, executor, translation, options, &record)
		// promoteCandidate returns the translation to persist: the promoted
		// native on success, the phase-shift-annotated review translation when a
		// magnitude match was rejected for a temporal offset, or the original
		// otherwise. Writing it back unconditionally is safe — it is unchanged
		// except in those two cases.
		migration.Translations[path] = updated
		records = append(records, record)
	}
	return records
}

// nativeCandidate reports whether a translation is a structurally valid Builder or
// formula that is currently held at review pending live proof.
func nativeCandidate(translation model.Translation) bool {
	if translation.Kind != model.TranslationBuilder && translation.Kind != model.TranslationFormula {
		return false
	}
	if translation.Decision.Verdict == model.VerdictNative {
		return false
	}
	if translation.PromQL == "" {
		return false
	}
	return slices.ContainsFunc(translation.Decision.Reasons, model.IsBuilderCandidateSemanticReason)
}

// promoteCandidate runs the live differential for one candidate and returns the
// translation to persist. On a passing differential it returns the promoted
// native. When the Builder matched in magnitude but one step out of phase it
// returns the same review translation annotated with the phase-shift reason.
// Otherwise it returns the translation unchanged. record.Promoted reflects
// whether a native was emitted.
func promoteCandidate(
	ctx context.Context,
	executor NativeExecutor,
	translation model.Translation,
	options PromoteOptions,
	record *PromotionRecord,
) model.Translation {
	stats, reason, ok := probeAndCompare(ctx, executor, translation, translation.PromQL, options)
	record.Reason = reason
	if !ok {
		return translation
	}
	record.MatchedSeries = stats.matchedSeries
	record.MatchedPoints = stats.matchedPoints
	record.WithinTolerance = stats.withinTolerance
	record.MaxRelative = stats.maxRelative
	if stats.phaseShifted {
		// A magnitude-only match that holds one step out of phase is not
		// equivalence. Leave the verdict at review but record why, so the report
		// distinguishes a temporal offset from an ordinary divergence.
		record.Reason = "builder matched passthrough in magnitude but was offset one step in time; withheld from native promotion"
		annotated := translation
		annotated.Decision.Reasons = appendReason(annotated.Decision.Reasons, model.ReasonBuilderTemporalPhaseShift)
		return annotated
	}
	if !stats.equivalent {
		record.Reason = "builder and passthrough diverged beyond tolerance"
		return translation
	}
	record.Promoted = true
	record.Reason = "builder verified equivalent to passthrough on the live target"
	promoted := translation
	promoted.Decision.Verdict = model.VerdictNative
	promoted.Decision.Reasons = appendReason(promoted.Decision.Reasons, model.ReasonNativeDifferentialVerified)
	return promoted
}

// probeAndCompare executes a candidate translation's Builder envelope and its
// PromQL passthrough against the live target and compares the two series sets.
// It is the shared core of native promotion and the public verify command: the
// same live differential decides both. The returned ok is false only when the
// probe could not run or returned no data — divergence within data is reported
// through the comparison, not the ok flag.
func probeAndCompare(
	ctx context.Context,
	executor NativeExecutor,
	translation model.Translation,
	sourcePromQL string,
	options PromoteOptions,
) (signozComparison, string, bool) {
	builderReq, promqlReq, ok := signoz.NativeProbeRequests(
		translation, sourcePromQL, options.Variables, options.Now, options.Window,
	)
	if !ok {
		return signozComparison{}, "probe request could not be constructed", false
	}
	builderSeries, err := executor.QueryRangeSeries(ctx, builderReq)
	if err != nil {
		return signozComparison{}, "builder probe execution failed", false
	}
	promqlSeries, err := executor.QueryRangeSeries(ctx, promqlReq)
	if err != nil {
		return signozComparison{}, "passthrough probe execution failed", false
	}
	builder := firstSeries(builderSeries)
	passthrough := firstSeries(promqlSeries)
	if len(builder) == 0 || len(passthrough) == 0 {
		return signozComparison{}, "probe returned no data on the live target", false
	}
	return compareSignozSeries(builder, passthrough, options), "compared", true
}

func firstSeries(byQuery map[string][]signoz.MetricSeries) []signoz.MetricSeries {
	for _, series := range byQuery {
		if len(series) > 0 {
			return series
		}
	}
	return nil
}

func appendReason(reasons []model.ReasonCode, reason model.ReasonCode) []model.ReasonCode {
	if slices.Contains(reasons, reason) {
		return reasons
	}
	return append(reasons, reason)
}
