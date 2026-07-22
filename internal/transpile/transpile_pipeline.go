package transpile

// This file implements the internal analysis pipeline that turns a parsed
// query into a Builder, Formula, PromQL passthrough, or review-only verdict.

import (
	"slices"
	"strings"
	"time"

	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/prometheus/prometheus/promql/parser"
)

type analysisContext struct {
	query    model.Query
	prepared preparedExpression
	expr     parser.Expr
	reasons  []model.ReasonCode
	review   bool
}

type builderCandidate func(parser.Expr, string) (model.BuilderQuery, bool)

func (analyzer *Analyzer) analyze(query model.Query) model.Translation {
	context, translation, complete := analyzer.startAnalysis(query)
	if complete {
		return translation
	}
	if translation, complete = analyzer.finishRiskAnalysis(context); complete {
		return translation
	}
	if context, translation, complete = analyzer.tryFormula(context); complete {
		return translation
	}
	if context, translation, complete = analyzer.tryBuilder(context, buildHistogram); complete {
		return translation
	}
	if context, translation, complete = analyzer.tryBuilder(context, buildAggregate); complete {
		return translation
	}
	if translation, complete = analyzer.tryMetadataQuery(context); complete {
		return translation
	}
	return analyzer.fallbackTranslation(context)
}

func (analyzer *Analyzer) startAnalysis(query model.Query) (analysisContext, model.Translation, bool) {
	if reason, excluded := analysisExclusion(query); excluded {
		return analysisContext{}, reviewOnlyTranslation(reason), true
	}
	prepared := analyzer.prepareExpression(query.Expression)
	if !prepared.executable {
		return analysisContext{}, model.Translation{
			Kind:     model.TranslationNone,
			Decision: model.Decision{Verdict: model.VerdictNeedsReview, Reasons: prepared.reasons},
		}, true
	}
	expr, err := analyzer.parser.ParseExpr(prepared.parse)
	if err != nil {
		return analysisContext{}, model.Translation{
			Kind:        model.TranslationNone,
			Decision:    model.Decision{Verdict: model.VerdictNeedsReview, Reasons: append(prepared.reasons, model.ReasonParseError)},
			ParseErrors: parseErrors(err),
		}, true
	}
	// Recording-rule inlining rewrites the sentinel-substituted parse AST, whose
	// scalar/range/offset variables have been replaced with placeholder literals.
	// Expanding that AST back into the passthrough string would bake those
	// placeholders into the emitted query (e.g. "* $factor" becoming "* 1").
	// Dynamic queries therefore keep their recording-rule metric intact; the
	// metric's ":" name routes it to a RECORDING_RULE_METRIC review outcome with
	// the pristine passthrough preserved.
	expr, inlined := expr, false
	if !prepared.dynamic {
		expr, inlined = analyzer.inlineRecordingRules(expr)
	}
	if analyzer.possibleNativeHistogramOutput(expr) {
		return analysisContext{}, model.Translation{
			Kind: model.TranslationNone,
			Decision: model.Decision{
				Verdict: model.VerdictNeedsReview,
				Reasons: uniqueReasons(append(prepared.reasons, model.ReasonTargetNativeHistogramDropped)),
			},
		}, true
	}
	if analyzer.hasTargetOnlySemanticLabelUse(expr) {
		return analysisContext{}, model.Translation{
			Kind: model.TranslationNone,
			Decision: model.Decision{
				Verdict: model.VerdictNeedsReview,
				Reasons: uniqueReasons(append(prepared.reasons, model.ReasonTargetOnlyLabelSemanticUse)),
			},
		}, true
	}
	if analyzer.hasRemappedMetricNameSemanticUse(expr) {
		return analysisContext{}, model.Translation{
			Kind: model.TranslationNone,
			Decision: model.Decision{
				Verdict: model.VerdictNeedsReview,
				Reasons: uniqueReasons(append(prepared.reasons, model.ReasonMetricNameSemanticUse)),
			},
		}, true
	}
	if len(analyzer.explicitStrippedOutputLabels(expr)) > 0 {
		return analysisContext{}, model.Translation{
			Kind: model.TranslationNone,
			Decision: model.Decision{
				Verdict: model.VerdictNeedsReview,
				Reasons: uniqueReasons(append(prepared.reasons, model.ReasonTargetResponseLabelStripped)),
			},
		}, true
	}
	dynamicLabel, dynamicMetric := analyzer.dynamicIdentifierRisks(expr, prepared.dynamicIdentifiers)
	if dynamicLabel || dynamicMetric {
		prepared.dynamic = true
		prepared.reasons = uniqueReasons(append(prepared.reasons, model.ReasonDynamicStructure))
	}
	reasons, review := inspectRisks(expr, analyzer.options.Interval)
	reasons = append(prepared.reasons, reasons...)
	if hasHistogramBucketFilter(expr) {
		reasons = append(reasons, model.ReasonHistogramBucketFilter)
		review = true
	}
	// Grafana computes interval globals from panel width, max data points,
	// datasource settings, and the selected time range, so a fixed replacement is
	// not an assumed native equivalence. It no longer forces review outright: the
	// candidate still ships as PromQL passthrough offline, and the live promotion
	// gate proves (or rejects) the Builder form against the resolved interval, so
	// the reason is informational rather than a blanket review gate.
	if inlined {
		reasons = append(reasons, model.ReasonRecordingRuleInlined)
		prepared.passthrough = expr.String()
	}
	if (dynamicLabel && analyzer.hasLabelRemappings()) || (dynamicMetric && analyzer.hasMetricRemappings()) {
		return analysisContext{}, model.Translation{
			Kind: model.TranslationNone,
			Decision: model.Decision{
				Verdict: model.VerdictNeedsReview,
				Reasons: uniqueReasons(append(reasons, model.ReasonDynamicRewriteConflict)),
			},
		}, true
	}
	if analyzer.hasLabelRemapCollision(expr) {
		return analysisContext{}, model.Translation{
			Kind: model.TranslationNone,
			Decision: model.Decision{
				Verdict: model.VerdictNeedsReview,
				Reasons: uniqueReasons(append(reasons, model.ReasonLabelRemapCollision)),
			},
		}, true
	}
	if analyzer.hasMetricRemapCollision(expr) {
		return analysisContext{}, model.Translation{
			Kind: model.TranslationNone,
			Decision: model.Decision{
				Verdict: model.VerdictNeedsReview,
				Reasons: uniqueReasons(append(reasons, model.ReasonMetricRemapCollision)),
			},
		}, true
	}
	if analyzer.hasUnsafeTargetVectorMatching(expr) {
		return analysisContext{}, model.Translation{
			Kind: model.TranslationNone,
			Decision: model.Decision{
				Verdict: model.VerdictNeedsReview,
				Reasons: uniqueReasons(append(reasons, model.ReasonTargetVectorMatchingUnresolved)),
			},
		}, true
	}
	return analysisContext{
		query:    query,
		prepared: prepared,
		expr:     expr,
		reasons:  uniqueReasons(reasons),
		review:   review,
	}, model.Translation{}, false
}

func analysisExclusion(query model.Query) (model.ReasonCode, bool) {
	switch {
	case isGrafanaExpression(query):
		return model.ReasonGrafanaExpression, true
	case !isPrometheusDatasource(query.Datasource):
		return model.ReasonNonPromDatasource, true
	case query.Instant:
		return model.ReasonInstantQuery, true
	case strings.TrimSpace(query.Expression) == "":
		return model.ReasonEmptyExpression, true
	default:
		return "", false
	}
}

func reviewOnlyTranslation(reason model.ReasonCode) model.Translation {
	return model.Translation{
		Kind:     model.TranslationNone,
		Decision: model.Decision{Verdict: model.VerdictNeedsReview, Reasons: []model.ReasonCode{reason}},
	}
}

func (analyzer *Analyzer) finishRiskAnalysis(context analysisContext) (model.Translation, bool) {
	if context.prepared.dynamic {
		if analyzer.requiredRewriteForDynamic(context.expr) {
			reasons := append(context.reasons, model.ReasonDynamicRewriteConflict)
			return model.Translation{
				Kind: model.TranslationNone,
				Decision: model.Decision{
					Verdict: model.VerdictNeedsReview,
					Reasons: uniqueReasons(reasons),
				},
			}, true
		}
	}
	missingMetric := analyzer.hasMissingMetric(context.expr)
	metadataUnavailable := analyzer.hasMetadataError(context.expr)
	if missingMetric || metadataUnavailable {
		passthrough, reasons := analyzer.rewriteAnalysis(context, context.prepared.dynamic)
		if missingMetric {
			reasons = append(reasons, model.ReasonMissingMetric)
		}
		if metadataUnavailable {
			reasons = append(reasons, model.ReasonMetricMetadataUnavailable)
		}
		return promQLTranslation(passthrough, model.VerdictNeedsReview, reasons), true
	}
	if context.review {
		passthrough, reasons := analyzer.rewriteAnalysis(context, context.prepared.dynamic)
		return promQLTranslation(passthrough, model.VerdictNeedsReview, reasons), true
	}
	if context.prepared.dynamic {
		passthrough, reasons := analyzer.rewriteAnalysis(context, true)
		return promQLTranslation(passthrough, model.VerdictPassthrough, reasons), true
	}
	return model.Translation{}, false
}

func (analyzer *Analyzer) rewriteAnalysis(context analysisContext, dynamic bool) (string, []model.ReasonCode) {
	passthrough, labelRemapped, metricRemapped, explicitMatching := analyzer.rewritePassthrough(
		context.expr,
		context.prepared.passthrough,
		dynamic,
	)
	return passthrough, appendRewriteReasons(context.reasons, labelRemapped, metricRemapped, explicitMatching)
}

func promQLTranslation(expression string, verdict model.Verdict, reasons []model.ReasonCode) model.Translation {
	return model.Translation{
		Kind:     model.TranslationPromQL,
		PromQL:   expression,
		Decision: model.Decision{Verdict: verdict, Reasons: uniqueReasons(reasons)},
	}
}

func (analyzer *Analyzer) tryFormula(context analysisContext) (analysisContext, model.Translation, bool) {
	formula, ok, labelMismatch := analyzer.buildFormula(context.expr, context.query)
	if !ok {
		if labelMismatch {
			context.reasons = append(context.reasons, model.ReasonFormulaLabelMismatch)
		}
		return context, model.Translation{}, false
	}
	analyzer.setFormulaStep(&formula)
	targetOnlyVectorMatchingRisk := formulaHasTargetOnlyVectorMatchingRisk(formula)
	compatible, metricKnown := analyzer.qualifyFormula(&formula)
	if !compatible || !metricKnown {
		context.reasons = appendMetricQualificationReason(context.reasons, compatible)
		return context, model.Translation{}, false
	}
	builderLabelsRemapped := analyzer.remapFormula(&formula)
	builderMetricsRemapped := analyzer.remapFormulaMetrics(&formula)
	passthrough, promQLLabelsRemapped, promQLMetricsRemapped, explicitMatching := analyzer.rewritePassthrough(
		context.expr,
		context.prepared.passthrough,
		false,
	)
	context.reasons = appendRewriteReasons(
		context.reasons,
		builderLabelsRemapped || promQLLabelsRemapped,
		builderMetricsRemapped || promQLMetricsRemapped,
		explicitMatching,
	)
	if targetOnlyVectorMatchingRisk {
		return context, promQLTranslation(passthrough, model.VerdictNeedsReview, context.reasons), true
	}
	context.reasons = append(context.reasons, formulaCandidateSemanticReasons(formula)...)
	return context, model.Translation{
		Kind:     model.TranslationFormula,
		Formula:  &formula,
		PromQL:   passthrough,
		Decision: model.Decision{Verdict: model.VerdictNeedsReview, Reasons: uniqueReasons(context.reasons)},
	}, true
}

func (analyzer *Analyzer) tryBuilder(
	context analysisContext,
	build builderCandidate,
) (analysisContext, model.Translation, bool) {
	builder, ok := build(context.expr, context.query.RefID)
	if !ok {
		return context, model.Translation{}, false
	}
	analyzer.setBuilderStep(&builder)
	compatible, metricKnown := analyzer.qualifyBuilder(&builder)
	if !compatible || !metricKnown {
		context.reasons = appendMetricQualificationReason(context.reasons, compatible)
		return context, model.Translation{}, false
	}
	return context, analyzer.builderTranslation(context, builder), true
}

func appendMetricQualificationReason(reasons []model.ReasonCode, compatible bool) []model.ReasonCode {
	if !compatible {
		return append(reasons, model.ReasonMetricTypeIncompatible)
	}
	return append(reasons, model.ReasonMetricTypeRequired)
}

func (analyzer *Analyzer) tryMetadataQuery(context analysisContext) (model.Translation, bool) {
	metric, ok := metadataMetricName(context.expr)
	if !ok {
		return model.Translation{}, false
	}
	metadata, found := analyzer.options.Metrics[metric]
	if !found {
		return model.Translation{}, false
	}
	builder, native := buildMetadataQuery(context.expr, context.query.RefID, metadata)
	if !native {
		return model.Translation{}, false
	}
	analyzer.setBuilderStep(&builder)
	return analyzer.builderTranslation(context, builder), true
}

func (analyzer *Analyzer) builderTranslation(context analysisContext, builder model.BuilderQuery) model.Translation {
	builderLabelsRemapped := analyzer.remapBuilder(&builder)
	builderMetricsRemapped := analyzer.remapBuilderMetric(&builder)
	passthrough, promQLLabelsRemapped, promQLMetricsRemapped, explicitMatching := analyzer.rewritePassthrough(
		context.expr,
		context.prepared.passthrough,
		false,
	)
	reasons := appendRewriteReasons(
		context.reasons,
		builderLabelsRemapped || promQLLabelsRemapped,
		builderMetricsRemapped || promQLMetricsRemapped,
		explicitMatching,
	)
	reasons = append(reasons, builderCandidateSemanticReasons(builder)...)
	return model.Translation{
		Kind:     model.TranslationBuilder,
		Builder:  &builder,
		PromQL:   passthrough,
		Decision: model.Decision{Verdict: model.VerdictNeedsReview, Reasons: uniqueReasons(reasons)},
	}
}

func (analyzer *Analyzer) fallbackTranslation(context analysisContext) model.Translation {
	if isMetadataDependent(context.expr) {
		context.reasons = append(context.reasons, model.ReasonMetricTypeRequired)
	}
	if len(context.reasons) == 0 {
		context.reasons = append(context.reasons, model.ReasonUnsupportedFunction)
	}
	passthrough, reasons := analyzer.rewriteAnalysis(context, false)
	return promQLTranslation(passthrough, model.VerdictPassthrough, reasons)
}

func builderCandidateSemanticReasons(builder model.BuilderQuery) []model.ReasonCode {
	if strings.HasPrefix(builder.SpaceAggregation, "p") {
		return []model.ReasonCode{model.ReasonBuilderHistogramPercentile}
	}
	switch builder.TimeAggregation {
	case "rate", "increase":
		return []model.ReasonCode{model.ReasonBuilderRateIncrease}
	case "latest":
		return []model.ReasonCode{model.ReasonBuilderLatestLookback}
	case "avg", "min", "max", "sum", "count":
		return []model.ReasonCode{model.ReasonBuilderOverTime}
	default:
		return nil
	}
}

func formulaCandidateSemanticReasons(formula model.Formula) []model.ReasonCode {
	reasons := []model.ReasonCode{model.ReasonBuilderFormulaEvaluation}
	for _, query := range formula.Queries {
		reasons = append(reasons, builderCandidateSemanticReasons(query)...)
	}
	return uniqueReasons(reasons)
}

func appendRewriteReasons(reasons []model.ReasonCode, labelRemapped, metricRemapped, explicitMatching bool) []model.ReasonCode {
	if labelRemapped {
		reasons = append(reasons, model.ReasonResourceLabelRemap)
	}
	if metricRemapped {
		reasons = append(reasons, model.ReasonMetricNameRemap)
	}
	if explicitMatching {
		reasons = append(reasons, model.ReasonTargetVectorMatching)
	}
	return reasons
}

func (analyzer *Analyzer) hasMissingMetric(expr parser.Expr) bool {
	missing := false
	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		selector, ok := node.(*parser.VectorSelector)
		if ok && analyzer.options.MissingMetrics[metricName(selector)] {
			missing = true
		}
		return nil
	})
	return missing
}

func (analyzer *Analyzer) hasMetadataError(expr parser.Expr) bool {
	unavailable := false
	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		selector, ok := node.(*parser.VectorSelector)
		if ok && analyzer.options.MetadataErrors[metricName(selector)] {
			unavailable = true
		}
		return nil
	})
	return unavailable
}

func (analyzer *Analyzer) setBuilderStep(builder *model.BuilderQuery) {
	// A rate/increase builder already carries a step aligned to its source range
	// (buildMetricExpression). Only gauge/instant builders, which have no range,
	// fall back to the panel interval.
	if builder.StepSeconds > 0 {
		return
	}
	builder.StepSeconds = max(int(analyzer.options.Interval/time.Second), 1)
}

func (analyzer *Analyzer) setFormulaStep(formula *model.Formula) {
	for index := range formula.Queries {
		analyzer.setBuilderStep(&formula.Queries[index])
	}
}

func isMetadataDependent(expr parser.Expr) bool {
	switch typed := unwrap(expr).(type) {
	case *parser.VectorSelector:
		return true
	case *parser.Call:
		return typed.Func.Name == "rate" || typed.Func.Name == "increase"
	default:
		return false
	}
}

func metadataMetricName(expr parser.Expr) (string, bool) {
	switch typed := unwrap(expr).(type) {
	case *parser.VectorSelector:
		name := metricName(typed)
		return name, name != ""
	case *parser.Call:
		if (typed.Func.Name != "rate" && typed.Func.Name != "increase") || len(typed.Args) != 1 {
			return "", false
		}
		matrix, ok := unwrap(typed.Args[0]).(*parser.MatrixSelector)
		if !ok {
			return "", false
		}
		selector, ok := unwrap(matrix.VectorSelector).(*parser.VectorSelector)
		if !ok {
			return "", false
		}
		name := metricName(selector)
		return name, name != ""
	default:
		return "", false
	}
}

func buildMetadataQuery(expr parser.Expr, name string, metadata model.TargetMetric) (model.BuilderQuery, bool) {
	builder, ok := buildMetricExpression(expr, name)
	if !ok {
		return model.BuilderQuery{}, false
	}
	if builder.TimeAggregation == "rate" || builder.TimeAggregation == "increase" {
		if metadata.Type != "sum" || !metadata.IsMonotonic || metadata.Temporality == "delta" {
			return model.BuilderQuery{}, false
		}
	}
	if builder.TimeAggregation == "latest" && metadata.Type != "gauge" && metadata.Type != "sum" {
		return model.BuilderQuery{}, false
	}
	builder.GroupBy = append([]string(nil), metadata.Attributes...)
	builder.Temporality = strings.ToLower(metadata.Temporality)
	return builder, true
}

func unwrap(expr parser.Expr) parser.Expr {
	for {
		paren, ok := expr.(*parser.ParenExpr)
		if !ok {
			return expr
		}
		expr = paren.Expr
	}
}

func uniqueReasons(reasons []model.ReasonCode) []model.ReasonCode {
	unique := make([]model.ReasonCode, 0, len(reasons))
	for _, reason := range reasons {
		if !slices.Contains(unique, reason) {
			unique = append(unique, reason)
		}
	}
	return unique
}
