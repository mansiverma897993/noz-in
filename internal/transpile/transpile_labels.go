package transpile

// This file detects label-semantics hazards: native-histogram outputs,
// target-only or remapped label reads, and explicitly stripped output labels.

import (
	"slices"
	"strings"

	"github.com/prometheus/prometheus/promql/parser"
)

func (analyzer *Analyzer) possibleNativeHistogramOutput(expr parser.Expr) bool {
	switch typed := expr.(type) {
	case *parser.VectorSelector:
		metadata, found := analyzer.options.Metrics[metricName(typed)]
		if !found {
			return false
		}
		metricType := strings.ToLower(strings.TrimSpace(metadata.Type))
		return metricType == "histogram" || metricType == "exponential_histogram"
	case *parser.MatrixSelector:
		return analyzer.possibleNativeHistogramOutput(typed.VectorSelector)
	case *parser.SubqueryExpr:
		return analyzer.possibleNativeHistogramOutput(typed.Expr)
	case *parser.ParenExpr:
		return analyzer.possibleNativeHistogramOutput(typed.Expr)
	case *parser.UnaryExpr:
		return analyzer.possibleNativeHistogramOutput(typed.Expr)
	case *parser.StepInvariantExpr:
		return analyzer.possibleNativeHistogramOutput(typed.Expr)
	case *parser.AggregateExpr:
		switch typed.Op {
		case parser.COUNT, parser.COUNT_VALUES, parser.GROUP:
			return false
		default:
			return analyzer.possibleNativeHistogramOutput(typed.Expr)
		}
	case *parser.Call:
		switch typed.Func.Name {
		case "histogram_count", "histogram_sum", "histogram_avg", "histogram_stddev", "histogram_stdvar",
			"histogram_fraction", "histogram_quantile", "count_over_time", "present_over_time", "scalar":
			return false
		default:
			return slices.ContainsFunc(typed.Args, analyzer.possibleNativeHistogramOutput)
		}
	case *parser.BinaryExpr:
		return analyzer.possibleNativeHistogramOutput(typed.LHS) || analyzer.possibleNativeHistogramOutput(typed.RHS)
	default:
		return false
	}
}

func (analyzer *Analyzer) explicitStrippedOutputLabels(expr parser.Expr) map[string]bool {
	labels := make(map[string]bool)
	add := func(name string) {
		name = analyzer.TargetLabel(name)
		if name == "fingerprint" || strings.HasPrefix(name, "__") && name != "__name__" {
			labels[name] = true
		}
	}
	merge := func(other map[string]bool) {
		for name := range other {
			labels[name] = true
		}
	}

	switch typed := expr.(type) {
	case *parser.VectorSelector:
		for _, matcher := range typed.LabelMatchers {
			add(matcher.Name)
		}
	case *parser.MatrixSelector:
		merge(analyzer.explicitStrippedOutputLabels(typed.VectorSelector))
	case *parser.SubqueryExpr:
		merge(analyzer.explicitStrippedOutputLabels(typed.Expr))
	case *parser.ParenExpr:
		merge(analyzer.explicitStrippedOutputLabels(typed.Expr))
	case *parser.UnaryExpr:
		merge(analyzer.explicitStrippedOutputLabels(typed.Expr))
	case *parser.StepInvariantExpr:
		merge(analyzer.explicitStrippedOutputLabels(typed.Expr))
	case *parser.AggregateExpr:
		merge(analyzer.explicitStrippedAggregateLabels(typed))
	case *parser.Call:
		merge(analyzer.explicitStrippedCallLabels(typed))
	case *parser.BinaryExpr:
		merge(analyzer.explicitStrippedBinaryLabels(typed))
	}
	return labels
}

func (analyzer *Analyzer) explicitStrippedAggregateLabels(expression *parser.AggregateExpr) map[string]bool {
	labels := make(map[string]bool)
	if selectionAggregator(expression.Op) || expression.Without {
		mergeStringSet(labels, analyzer.explicitStrippedOutputLabels(expression.Expr))
		if selectionAggregator(expression.Op) {
			for _, name := range expression.Grouping {
				analyzer.addStrippedLabel(labels, name)
			}
		}
		if expression.Without {
			for _, name := range expression.Grouping {
				delete(labels, analyzer.TargetLabel(name))
			}
		}
	} else {
		for _, name := range expression.Grouping {
			analyzer.addStrippedLabel(labels, name)
		}
	}
	if parameter, ok := aggregateLabelParameter(expression); ok {
		analyzer.addStrippedLabel(labels, parameter.Val)
	}
	return labels
}

// hasTargetOnlySemanticLabelUse detects source operations whose result can be
// changed before SigNoz strips target-only labels from the response. Unlike a
// merely retained output label, these reads remain unsafe even when an outer
// aggregation later drops the affected label.
func (analyzer *Analyzer) hasTargetOnlySemanticLabelUse(expr parser.Expr) bool {
	unsafe := false
	isTargetOnly := func(name string) bool {
		name = analyzer.TargetLabel(name)
		return isTargetOnlyPrometheusLabel(name)
	}
	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		switch typed := node.(type) {
		case *parser.VectorSelector:
			for _, matcher := range typed.LabelMatchers {
				unsafe = unsafe || isTargetOnly(matcher.Name)
			}
		case *parser.AggregateExpr:
			if !typed.Without {
				for _, name := range typed.Grouping {
					unsafe = unsafe || isTargetOnly(name)
				}
			}
			// Selection aggregators inspect the complete input label set when
			// choosing or hashing series. A receiver-only fingerprint can
			// therefore change the selected source series even though the query
			// never names that label explicitly.
			if (typed.Op == parser.LIMITK || typed.Op == parser.LIMIT_RATIO) &&
				len(analyzer.logicalOutputShape(typed.Expr).targetOnly) > 0 {
				unsafe = true
			}
		case *parser.Call:
			// Classic histogram functions implicitly group bucket samples by
			// every label except le and __name__. SigNoz's per-series
			// fingerprint would split those buckets into separate groups unless
			// an inner operation has already removed it. Native histogram
			// samples do not combine separate bucket series and are exempt.
			if argument, ok := classicHistogramFunctionArgument(typed); ok &&
				len(analyzer.logicalOutputShape(argument).targetOnly) > 0 {
				provenNative := analyzer.possibleNativeHistogramOutput(argument) &&
					!analyzer.hasClassicHistogramBucketSelector(argument)
				unsafe = unsafe || !provenNative
			}
			for _, index := range semanticCallLabelArgumentIndexes(typed) {
				if index < len(typed.Args) {
					if label, ok := typed.Args[index].(*parser.StringLiteral); ok {
						unsafe = unsafe || isTargetOnly(label.Val)
					}
				}
			}
		case *parser.BinaryExpr:
			if typed.VectorMatching == nil {
				break
			}
			if typed.VectorMatching.On {
				for _, name := range typed.VectorMatching.MatchingLabels {
					unsafe = unsafe || isTargetOnly(name)
				}
			}
			for _, name := range typed.VectorMatching.Include {
				unsafe = unsafe || isTargetOnly(name)
			}
		}
		return nil
	})
	return unsafe
}

func classicHistogramFunctionArgument(call *parser.Call) (parser.Expr, bool) {
	switch call.Func.Name {
	case "histogram_quantile":
		if len(call.Args) == 2 {
			return call.Args[1], true
		}
	case "histogram_fraction":
		if len(call.Args) == 3 {
			return call.Args[2], true
		}
	}
	return nil, false
}

func (analyzer *Analyzer) hasClassicHistogramBucketSelector(expr parser.Expr) bool {
	classic := false
	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		selector, ok := node.(*parser.VectorSelector)
		if !ok {
			return nil
		}
		source := metricName(selector)
		metadata := analyzer.options.Metrics[source]
		target := metadata.Name
		if target == "" {
			target = source
		}
		classic = classic || strings.HasSuffix(source, "_bucket") || strings.HasSuffix(target, ".bucket") ||
			slices.Contains(metadata.Attributes, "le")
		return nil
	})
	return classic
}

func semanticCallLabelArgumentIndexes(call *parser.Call) []int {
	switch call.Func.Name {
	case "label_replace":
		return []int{3}
	case "label_join":
		indexes := make([]int, 0, max(len(call.Args)-3, 0))
		for index := 3; index < len(call.Args); index++ {
			indexes = append(indexes, index)
		}
		return indexes
	case "sort_by_label", "sort_by_label_desc":
		indexes := make([]int, 0, max(len(call.Args)-1, 0))
		for index := 1; index < len(call.Args); index++ {
			indexes = append(indexes, index)
		}
		return indexes
	default:
		return nil
	}
}

func (analyzer *Analyzer) hasRemappedMetricNameSemanticUse(expr parser.Expr) bool {
	metricRemapped := false
	semanticRead := false
	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		switch typed := node.(type) {
		case *parser.VectorSelector:
			source := metricName(typed)
			metadata, found := analyzer.options.Metrics[source]
			metricRemapped = metricRemapped || found && metadata.Name != "" && metadata.Name != source
		case *parser.AggregateExpr:
			if !typed.Without {
				semanticRead = semanticRead || slices.Contains(typed.Grouping, "__name__")
			}
		case *parser.Call:
			for _, index := range semanticCallLabelArgumentIndexes(typed) {
				if index < len(typed.Args) {
					if label, ok := typed.Args[index].(*parser.StringLiteral); ok {
						semanticRead = semanticRead || label.Val == "__name__"
					}
				}
			}
		case *parser.BinaryExpr:
			if typed.VectorMatching == nil {
				break
			}
			semanticRead = semanticRead ||
				(typed.VectorMatching.On && slices.Contains(typed.VectorMatching.MatchingLabels, "__name__")) ||
				slices.Contains(typed.VectorMatching.Include, "__name__")
		}
		return nil
	})
	return metricRemapped && semanticRead
}

func (analyzer *Analyzer) explicitStrippedCallLabels(call *parser.Call) map[string]bool {
	labels := make(map[string]bool)
	if len(call.Args) == 0 {
		return labels
	}
	switch call.Func.Name {
	case "label_replace", "label_join":
		mergeStringSet(labels, analyzer.explicitStrippedOutputLabels(call.Args[0]))
		if len(call.Args) > 1 {
			if destination, ok := call.Args[1].(*parser.StringLiteral); ok {
				analyzer.addStrippedLabel(labels, destination.Val)
			}
		}
	case "histogram_quantile":
		if len(call.Args) > 1 {
			mergeStringSet(labels, analyzer.explicitStrippedOutputLabels(call.Args[1]))
		}
	case "absent", "absent_over_time":
		mergeStringSet(labels, analyzer.explicitStrippedOutputLabels(call.Args[0]))
	default:
		if argument, preserving := labelPreservingFunctionArgument(call.Func.Name); preserving && argument < len(call.Args) {
			mergeStringSet(labels, analyzer.explicitStrippedOutputLabels(call.Args[argument]))
		}
	}
	return labels
}

func (analyzer *Analyzer) explicitStrippedBinaryLabels(expression *parser.BinaryExpr) map[string]bool {
	labels := make(map[string]bool)
	leftVector := expression.LHS.Type() == parser.ValueTypeVector
	rightVector := expression.RHS.Type() == parser.ValueTypeVector
	switch {
	case leftVector && rightVector && expression.Op.IsSetOperator():
		mergeStringSet(labels, analyzer.explicitStrippedOutputLabels(expression.LHS))
		mergeStringSet(labels, analyzer.explicitStrippedOutputLabels(expression.RHS))
	case leftVector && rightVector && expression.VectorMatching != nil && expression.VectorMatching.Card == parser.CardOneToMany:
		mergeStringSet(labels, analyzer.explicitStrippedOutputLabels(expression.RHS))
	case leftVector:
		mergeStringSet(labels, analyzer.explicitStrippedOutputLabels(expression.LHS))
	case rightVector:
		mergeStringSet(labels, analyzer.explicitStrippedOutputLabels(expression.RHS))
	}
	if expression.VectorMatching != nil {
		for _, name := range expression.VectorMatching.Include {
			analyzer.addStrippedLabel(labels, name)
		}
	}
	return labels
}

func (analyzer *Analyzer) addStrippedLabel(destination map[string]bool, name string) {
	name = analyzer.TargetLabel(name)
	if name == "fingerprint" || strings.HasPrefix(name, "__") && name != "__name__" {
		destination[name] = true
	}
}

func mergeStringSet(destination, source map[string]bool) {
	for name := range source {
		destination[name] = true
	}
}
