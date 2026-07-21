package transpile

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/mansiverma897993/signoz/internal/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
)

var legacyMetricNamePattern = regexp.MustCompile(`^[A-Za-z_:][A-Za-z0-9_:]*$`)

func (analyzer *Analyzer) remapBuilder(builder *model.BuilderQuery) bool {
	remapped := false
	for index := range builder.Filters {
		if target, ok := analyzer.labelMap[builder.Filters[index].Label]; ok {
			builder.Filters[index].Label = target
			remapped = true
		}
	}
	for index := range builder.GroupBy {
		if target, ok := analyzer.labelMap[builder.GroupBy[index]]; ok {
			builder.GroupBy[index] = target
			remapped = true
		}
	}
	return remapped
}

func (analyzer *Analyzer) remapBuilderMetric(builder *model.BuilderQuery) bool {
	metadata, ok := analyzer.options.Metrics[builder.MetricName]
	if !ok || metadata.Name == "" || metadata.Name == builder.MetricName {
		return false
	}
	builder.MetricName = metadata.Name
	return true
}

func (analyzer *Analyzer) remapFormula(formula *model.Formula) bool {
	remapped := false
	for index := range formula.Queries {
		remapped = analyzer.remapBuilder(&formula.Queries[index]) || remapped
	}
	return remapped
}

func (analyzer *Analyzer) remapFormulaMetrics(formula *model.Formula) bool {
	remapped := false
	for index := range formula.Queries {
		remapped = analyzer.remapBuilderMetric(&formula.Queries[index]) || remapped
	}
	return remapped
}

func (analyzer *Analyzer) rewritePassthrough(expr parser.Expr, fallback string, dynamic bool) (string, bool, bool, bool) {
	if dynamic {
		return fallback, false, false, false
	}
	labelRemapped, targetMatchingRewritten := analyzer.remapPassthroughLabels(expr)
	targetMatchingRewritten = analyzer.injectTargetVectorMatching(expr) || targetMatchingRewritten
	metricRemapped := false
	quotedMetricNames := make([]string, 0)
	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		selector, ok := node.(*parser.VectorSelector)
		if !ok {
			return nil
		}
		sourceMetric := metricName(selector)
		if metadata, ok := analyzer.options.Metrics[sourceMetric]; ok && metadata.Name != "" && metadata.Name != sourceMetric {
			selector.Name = metadata.Name
			if !legacyMetricNamePattern.MatchString(metadata.Name) {
				selector.Name = ""
				if !slices.Contains(quotedMetricNames, metadata.Name) {
					quotedMetricNames = append(quotedMetricNames, metadata.Name)
				}
			}
			nameMatcherFound := false
			for _, matcher := range selector.LabelMatchers {
				if matcher.Name == "__name__" && matcher.Type == labels.MatchEqual {
					matcher.Value = metadata.Name
					nameMatcherFound = true
				}
			}
			if !nameMatcherFound {
				selector.LabelMatchers = append(selector.LabelMatchers, labels.MustNewMatcher(labels.MatchEqual, "__name__", metadata.Name))
			}
			metricRemapped = true
		}
		return nil
	})
	rendered := quoteMetricNames(expr.String(), quotedMetricNames)
	return rendered, labelRemapped, metricRemapped, targetMatchingRewritten
}

func (analyzer *Analyzer) remapPassthroughLabels(expr parser.Expr) (bool, bool) {
	labelRemapped := false
	targetMatchingRewritten := false
	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		switch typed := node.(type) {
		case *parser.VectorSelector:
			for _, matcher := range typed.LabelMatchers {
				if target, ok := analyzer.labelMap[matcher.Name]; ok {
					matcher.Name = target
					labelRemapped = true
				}
				if matcher.Type == labels.MatchRegexp || matcher.Type == labels.MatchNotRegexp {
					matcher.Value = anchoredRegex(matcher.Value)
				}
			}
		case *parser.AggregateExpr:
			labelRemapped = analyzer.remapLabelNames(typed.Grouping) || labelRemapped
			labelRemapped = analyzer.remapAggregateLabelParameter(typed) || labelRemapped
			if typed.Without {
				labelRemapped = addTargetOnlyWithoutLabels(&typed.Grouping) || labelRemapped
			}
		case *parser.BinaryExpr:
			if typed.VectorMatching == nil {
				break
			}
			labelRemapped = analyzer.remapLabelNames(typed.VectorMatching.MatchingLabels) || labelRemapped
			labelRemapped = analyzer.remapLabelNames(typed.VectorMatching.Include) || labelRemapped
			if isExplicitIgnoring(typed) {
				targetMatchingRewritten = addTargetOnlyPrometheusLabels(&typed.VectorMatching.MatchingLabels) || targetMatchingRewritten
			}
		case *parser.Call:
			labelRemapped = analyzer.remapCallLabelArguments(typed) || labelRemapped
		}
		return nil
	})
	return labelRemapped, targetMatchingRewritten
}

func (analyzer *Analyzer) requiredRewriteForDynamic(expr parser.Expr) bool {
	baseline := expr.String()
	clone, err := analyzer.parser.ParseExpr(baseline)
	if err != nil {
		// The expression was already parsed successfully. If its canonical form
		// cannot be reparsed, never claim that the unrevised dynamic query is safe.
		return true
	}
	rendered, _, _, _ := analyzer.rewritePassthrough(clone, baseline, false)
	return rendered != baseline
}

func (analyzer *Analyzer) hasLabelRemapCollision(expr parser.Expr) bool {
	names := make([]string, 0)
	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		switch typed := node.(type) {
		case *parser.VectorSelector:
			for _, matcher := range typed.LabelMatchers {
				names = append(names, matcher.Name)
			}
		case *parser.AggregateExpr:
			names = append(names, typed.Grouping...)
			if label, ok := aggregateLabelParameter(typed); ok {
				names = append(names, label.Val)
			}
		case *parser.BinaryExpr:
			if typed.VectorMatching != nil {
				names = append(names, typed.VectorMatching.MatchingLabels...)
				names = append(names, typed.VectorMatching.Include...)
			}
		case *parser.Call:
			for _, index := range callLabelArgumentIndexes(typed) {
				if index >= len(typed.Args) {
					continue
				}
				literal, ok := typed.Args[index].(*parser.StringLiteral)
				if ok {
					names = append(names, literal.Val)
				}
			}
		}
		return nil
	})
	return analyzer.labelNamesCollide(names)
}

func (analyzer *Analyzer) hasMetricRemapCollision(expr parser.Expr) bool {
	origins := make(map[string]string)
	collision := false
	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		if collision {
			return nil
		}
		selector, ok := node.(*parser.VectorSelector)
		if !ok {
			return nil
		}
		source := metricName(selector)
		if source == "" {
			return nil
		}
		target := source
		if metadata, found := analyzer.options.Metrics[source]; found && metadata.Name != "" {
			target = metadata.Name
		}
		if previous, exists := origins[target]; exists && previous != source {
			collision = true
			return nil
		}
		origins[target] = source
		return nil
	})
	return collision
}

func (analyzer *Analyzer) dynamicIdentifierRisks(expr parser.Expr, sentinels map[string]bool) (bool, bool) {
	dynamicLabel := false
	dynamicMetric := false
	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		if dynamicLabel && dynamicMetric {
			return nil
		}
		switch typed := node.(type) {
		case *parser.VectorSelector:
			for _, matcher := range typed.LabelMatchers {
				if matcher.Name == "__name__" && dynamicIdentifier(matcher.Value, sentinels) {
					dynamicMetric = true
				}
				if matcher.Name != "__name__" && dynamicIdentifier(matcher.Name, sentinels) {
					dynamicLabel = true
				}
			}
		case *parser.AggregateExpr:
			if anyDynamicIdentifier(typed.Grouping, sentinels) {
				dynamicLabel = true
			}
			if label, ok := aggregateLabelParameter(typed); ok && dynamicIdentifier(label.Val, sentinels) {
				dynamicLabel = true
			}
		case *parser.BinaryExpr:
			if typed.VectorMatching != nil && (anyDynamicIdentifier(typed.VectorMatching.MatchingLabels, sentinels) ||
				anyDynamicIdentifier(typed.VectorMatching.Include, sentinels)) {
				dynamicLabel = true
			}
		case *parser.Call:
			for _, index := range callLabelArgumentIndexes(typed) {
				if index >= len(typed.Args) {
					continue
				}
				literal, ok := typed.Args[index].(*parser.StringLiteral)
				if ok && dynamicIdentifier(literal.Val, sentinels) {
					dynamicLabel = true
				}
			}
		}
		return nil
	})
	return dynamicLabel, dynamicMetric
}

func dynamicIdentifier(value string, sentinels map[string]bool) bool {
	return sentinels[value] || variablePattern.MatchString(value)
}

func anyDynamicIdentifier(values []string, sentinels map[string]bool) bool {
	for _, value := range values {
		if dynamicIdentifier(value, sentinels) {
			return true
		}
	}
	return false
}

func (analyzer *Analyzer) hasLabelRemappings() bool {
	for source, target := range analyzer.labelMap {
		if target != source {
			return true
		}
	}
	return false
}

func (analyzer *Analyzer) hasMetricRemappings() bool {
	for source, metadata := range analyzer.options.Metrics {
		if metadata.Name != "" && metadata.Name != source {
			return true
		}
	}
	return false
}

func (analyzer *Analyzer) labelNamesCollide(names []string) bool {
	origins := make(map[string]string, len(names))
	for _, source := range names {
		target := source
		if mapped, ok := analyzer.labelMap[source]; ok {
			target = mapped
		}
		if previous, exists := origins[target]; exists && previous != source {
			return true
		}
		origins[target] = source
	}
	return false
}

func quoteMetricNames(expression string, names []string) string {
	if len(names) == 0 {
		return expression
	}
	var result strings.Builder
	result.Grow(len(expression))
	for index := 0; index < len(expression); {
		switch expression[index] {
		case '"', '\'', '`':
			end := quotedEnd(expression, index, expression[index])
			result.WriteString(expression[index:end])
			index = end
		case '{':
			end := selectorEnd(expression, index)
			if end < 0 {
				result.WriteString(expression[index:])
				return result.String()
			}
			result.WriteString(quoteSelectorMetric(expression[index:end], names))
			index = end
		default:
			result.WriteByte(expression[index])
			index++
		}
	}
	return result.String()
}

func selectorEnd(expression string, start int) int {
	for index := start + 1; index < len(expression); index++ {
		switch expression[index] {
		case '"', '\'', '`':
			index = quotedEnd(expression, index, expression[index]) - 1
		case '}':
			return index + 1
		}
	}
	return -1
}

func quoteSelectorMetric(selector string, names []string) string {
	inner := selector[1 : len(selector)-1]
	parts := splitSelectorParts(inner)
	for _, name := range names {
		matcher := `__name__=` + strconv.Quote(name)
		for index, part := range parts {
			if strings.TrimSpace(part) != matcher {
				continue
			}
			remaining := append([]string(nil), parts[:index]...)
			remaining = append(remaining, parts[index+1:]...)
			quoted := strconv.Quote(name)
			if len(remaining) == 0 {
				return "{" + quoted + "}"
			}
			return "{" + quoted + "," + strings.Join(remaining, ",") + "}"
		}
	}
	return selector
}

func splitSelectorParts(value string) []string {
	parts := make([]string, 0, 4)
	start := 0
	for index := 0; index < len(value); index++ {
		switch value[index] {
		case '"', '\'', '`':
			index = quotedEnd(value, index, value[index]) - 1
		case ',':
			parts = append(parts, value[start:index])
			start = index + 1
		}
	}
	return append(parts, value[start:])
}

var targetOnlyPrometheusLabels = []string{
	"__scope.name__",
	"__scope.schema_url__",
	"__scope.version__",
	"__temporality__",
	"fingerprint",
	"server.address",
	"server.port",
	"url.scheme",
}

func addTargetOnlyWithoutLabels(grouping *[]string) bool {
	return addTargetOnlyPrometheusLabels(grouping)
}

func isTargetOnlyPrometheusLabel(label string) bool {
	return slices.Contains(targetOnlyPrometheusLabels, label)
}

func addTargetOnlyPrometheusLabels(grouping *[]string) bool {
	changed := false
	for _, label := range targetOnlyPrometheusLabels {
		if !slices.Contains(*grouping, label) {
			*grouping = append(*grouping, label)
			changed = true
		}
	}
	slices.Sort(*grouping)
	*grouping = slices.Compact(*grouping)
	return changed
}

func isExplicitIgnoring(binary *parser.BinaryExpr) bool {
	matching := binary.VectorMatching
	if matching == nil || matching.On {
		return false
	}
	if len(matching.MatchingLabels) > 0 || len(matching.Include) > 0 {
		return true
	}
	return !binary.Op.IsSetOperator() && matching.Card != parser.CardOneToOne
}

func isImplicitVectorMatching(binary *parser.BinaryExpr) bool {
	matching := binary.VectorMatching
	if matching == nil {
		return true
	}
	if matching.On || len(matching.MatchingLabels) > 0 || len(matching.Include) > 0 {
		return false
	}
	if binary.Op.IsSetOperator() {
		return matching.Card == parser.CardManyToMany
	}
	return matching.Card == parser.CardOneToOne
}

func (analyzer *Analyzer) injectTargetVectorMatching(expr parser.Expr) bool {
	changed := false
	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		binary, ok := node.(*parser.BinaryExpr)
		if !ok || binary.LHS.Type() != parser.ValueTypeVector || binary.RHS.Type() != parser.ValueTypeVector || !isImplicitVectorMatching(binary) {
			return nil
		}
		left, leftKnown := analyzer.logicalOutputLabels(binary.LHS)
		right, rightKnown := analyzer.logicalOutputLabels(binary.RHS)
		if !leftKnown || !rightKnown {
			// Unknown shapes are left alone only when no known receiver-only
			// label makes a correction mandatory. startAnalysis rejects the
			// mandatory-but-unknown case before emission.
			return nil
		}
		matching := append(append([]string(nil), left...), right...)
		slices.Sort(matching)
		matching = slices.Compact(matching)
		if binary.VectorMatching == nil {
			binary.VectorMatching = &parser.VectorMatching{}
		}
		binary.VectorMatching.On = true
		binary.VectorMatching.MatchingLabels = matching
		if binary.Op.IsSetOperator() {
			binary.VectorMatching.Card = parser.CardManyToMany
		} else {
			binary.VectorMatching.Card = parser.CardOneToOne
		}
		changed = true
		return nil
	})
	return changed
}

type logicalVectorShape struct {
	labels     []string
	targetOnly []string
	known      bool
	unsafe     bool
}

func (analyzer *Analyzer) logicalOutputLabels(expr parser.Expr) ([]string, bool) {
	shape := analyzer.logicalOutputShape(expr)
	return shape.labels, shape.known
}

func (analyzer *Analyzer) logicalOutputShape(expr parser.Expr) logicalVectorShape {
	switch typed := expr.(type) {
	case *parser.VectorSelector:
		metadata, ok := analyzer.options.Metrics[metricName(typed)]
		if !ok || strings.TrimSpace(metadata.Type) == "" {
			// Pinned SigNoz adds fingerprint to every stored series even though
			// the metrics-explorer attribute endpoint does not report it. Keep the
			// universal target-only label visible when logical labels are unknown.
			// A Name-only entry proves selector remapping, not a complete attribute
			// inventory, so it must fail closed just like absent metadata.
			return logicalVectorShape{targetOnly: []string{"fingerprint"}}
		}
		logical, targetOnly := analyzer.splitLogicalLabels(metadata.Attributes)
		targetOnly = unionLabels(targetOnly, []string{"fingerprint"})
		return logicalVectorShape{labels: logical, targetOnly: targetOnly, known: true}
	case *parser.MatrixSelector:
		return analyzer.logicalOutputShape(typed.VectorSelector)
	case *parser.SubqueryExpr:
		return analyzer.logicalOutputShape(typed.Expr)
	case *parser.ParenExpr:
		return analyzer.logicalOutputShape(typed.Expr)
	case *parser.UnaryExpr:
		return analyzer.logicalOutputShape(typed.Expr)
	case *parser.StepInvariantExpr:
		return analyzer.logicalOutputShape(typed.Expr)
	case *parser.AggregateExpr:
		input := analyzer.logicalOutputShape(typed.Expr)
		parameter := analyzer.logicalOutputShape(typed.Param)
		input.unsafe = input.unsafe || parameter.unsafe
		if selectionAggregator(typed.Op) {
			return input
		}
		shape := logicalVectorShape{known: true, unsafe: input.unsafe}
		if typed.Without {
			shape.known = input.known
			if input.known {
				shape.labels = removeLabels(input.labels, typed.Grouping)
			}
			// The target rewrite adds every known receiver-only label to a
			// without list, so none can survive this aggregation.
		} else {
			shape.labels, shape.targetOnly = splitFinalLabels(typed.Grouping)
		}
		if typed.Op == parser.COUNT_VALUES {
			parameter, ok := aggregateLabelParameter(typed)
			if !ok {
				shape.known = false
				return shape
			}
			logical, targetOnly := splitFinalLabels([]string{parameter.Val})
			if shape.known {
				shape.labels = unionLabels(shape.labels, logical)
			}
			shape.targetOnly = unionLabels(shape.targetOnly, targetOnly)
		}
		return normalizeShape(shape)
	case *parser.Call:
		argument, preserving := labelPreservingFunctionArgument(typed.Func.Name)
		if preserving && argument < len(typed.Args) {
			shape := analyzer.logicalOutputShape(typed.Args[argument])
			for index, other := range typed.Args {
				if index != argument {
					shape.unsafe = shape.unsafe || analyzer.logicalOutputShape(other).unsafe
				}
			}
			return shape
		}
		shape := logicalVectorShape{}
		for _, argument := range typed.Args {
			argumentShape := analyzer.logicalOutputShape(argument)
			shape.targetOnly = unionLabels(shape.targetOnly, argumentShape.targetOnly)
			shape.unsafe = shape.unsafe || argumentShape.unsafe
		}
		return normalizeShape(shape)
	case *parser.BinaryExpr:
		return analyzer.logicalBinaryOutputShape(typed)
	default:
		return logicalVectorShape{}
	}
}

func (analyzer *Analyzer) logicalBinaryOutputShape(typed *parser.BinaryExpr) logicalVectorShape {
	left := analyzer.logicalOutputShape(typed.LHS)
	right := analyzer.logicalOutputShape(typed.RHS)
	unsafe := left.unsafe || right.unsafe
	leftVector := typed.LHS.Type() == parser.ValueTypeVector
	rightVector := typed.RHS.Type() == parser.ValueTypeVector
	if leftVector && !rightVector {
		left.unsafe = unsafe
		return left
	}
	if rightVector && !leftVector {
		right.unsafe = unsafe
		return right
	}
	if !leftVector || !rightVector {
		return logicalVectorShape{unsafe: unsafe}
	}

	implicit := isImplicitVectorMatching(typed)
	rewriteRequired := implicit && (len(left.targetOnly) > 0 || len(right.targetOnly) > 0)
	if rewriteRequired && (!left.known || !right.known) {
		unsafe = true
	}

	if typed.Op.IsSetOperator() {
		switch typed.Op {
		case parser.LAND, parser.LUNLESS:
			left.unsafe = unsafe
			return left
		case parser.LOR:
			return normalizeShape(logicalVectorShape{
				labels:     unionLabels(left.labels, right.labels),
				targetOnly: unionLabels(left.targetOnly, right.targetOnly),
				known:      left.known && right.known,
				unsafe:     unsafe,
			})
		default:
			return logicalVectorShape{unsafe: true}
		}
	}

	matching := typed.VectorMatching
	cardinality := parser.CardOneToOne
	if matching != nil {
		cardinality = matching.Card
	}
	switch cardinality {
	case parser.CardOneToOne:
		shape := logicalVectorShape{labels: left.labels, targetOnly: left.targetOnly, known: left.known, unsafe: unsafe}
		if matching != nil && matching.On {
			shape.labels = keepLabels(shape.labels, matching.MatchingLabels)
			shape.targetOnly = keepLabels(shape.targetOnly, matching.MatchingLabels)
		} else if rewriteRequired {
			// The emitted on(...) key contains only logical source labels, so
			// receiver-only labels are dropped from the one-to-one result.
			shape.targetOnly = nil
		} else if matching != nil {
			shape.labels = removeLabels(shape.labels, matching.MatchingLabels)
			shape.targetOnly = removeLabels(shape.targetOnly, matching.MatchingLabels)
		}
		return normalizeShape(shape)
	case parser.CardManyToOne:
		return includedOutputShape(left, right, matching.Include, unsafe)
	case parser.CardOneToMany:
		return includedOutputShape(right, left, matching.Include, unsafe)
	default:
		return logicalVectorShape{unsafe: true}
	}
}

func includedOutputShape(base, oneSide logicalVectorShape, included []string, unsafe bool) logicalVectorShape {
	shape := logicalVectorShape{
		labels:     append([]string(nil), base.labels...),
		targetOnly: append([]string(nil), base.targetOnly...),
		known:      base.known && (len(included) == 0 || oneSide.known),
		unsafe:     unsafe,
	}
	shape.labels = removeLabels(shape.labels, included)
	shape.targetOnly = removeLabels(shape.targetOnly, included)
	for _, label := range included {
		switch {
		case slices.Contains(oneSide.labels, label):
			shape.labels = append(shape.labels, label)
		case slices.Contains(oneSide.targetOnly, label):
			shape.targetOnly = append(shape.targetOnly, label)
		}
	}
	return normalizeShape(shape)
}

func (analyzer *Analyzer) splitLogicalLabels(attributes []string) ([]string, []string) {
	normalized := make([]string, 0, len(attributes))
	for _, attribute := range attributes {
		if target, ok := analyzer.labelMap[attribute]; ok {
			attribute = target
		}
		normalized = append(normalized, attribute)
	}
	return splitFinalLabels(normalized)
}

func splitFinalLabels(names []string) ([]string, []string) {
	logical := make([]string, 0, len(names))
	targetOnly := make([]string, 0, len(names))
	for _, name := range names {
		switch {
		case name == "__name__":
			continue
		case isTargetOnlyPrometheusLabel(name):
			targetOnly = append(targetOnly, name)
		default:
			logical = append(logical, name)
		}
	}
	return normalizeLabels(logical), normalizeLabels(targetOnly)
}

func normalizeShape(shape logicalVectorShape) logicalVectorShape {
	shape.labels = normalizeLabels(shape.labels)
	shape.targetOnly = normalizeLabels(shape.targetOnly)
	return shape
}

func normalizeLabels(values []string) []string {
	result := append([]string(nil), values...)
	slices.Sort(result)
	return slices.Compact(result)
}

func unionLabels(left, right []string) []string {
	return normalizeLabels(append(append([]string(nil), left...), right...))
}

func keepLabels(values, kept []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if slices.Contains(kept, value) {
			result = append(result, value)
		}
	}
	return normalizeLabels(result)
}

func selectionAggregator(operator parser.ItemType) bool {
	switch operator {
	case parser.TOPK, parser.BOTTOMK, parser.LIMITK, parser.LIMIT_RATIO:
		return true
	default:
		return false
	}
}

func (analyzer *Analyzer) hasUnsafeTargetVectorMatching(expr parser.Expr) bool {
	clone, err := analyzer.parser.ParseExpr(expr.String())
	if err != nil {
		return true
	}
	analyzer.remapPassthroughLabels(clone)
	return analyzer.logicalOutputShape(clone).unsafe
}

func removeLabels(labels, removed []string) []string {
	result := make([]string, 0, len(labels))
	for _, label := range labels {
		if !slices.Contains(removed, label) {
			result = append(result, label)
		}
	}
	return normalizeLabels(result)
}

func labelPreservingFunctionArgument(name string) (int, bool) {
	switch name {
	case "quantile_over_time":
		return 1, true
	case "histogram_fraction":
		return 2, true
	case "abs", "avg_over_time", "ceil", "changes", "clamp", "clamp_max", "clamp_min",
		"count_over_time", "day_of_month", "day_of_week", "day_of_year", "days_in_month", "deg",
		"delta", "deriv", "double_exponential_smoothing", "exp", "first_over_time", "floor",
		"histogram_avg", "histogram_count", "histogram_stddev", "histogram_stdvar", "histogram_sum",
		"holt_winters", "hour", "idelta", "increase", "irate", "last_over_time", "ln", "log10",
		"log2", "mad_over_time", "max_over_time", "min_over_time", "minute", "month",
		"predict_linear", "present_over_time", "rad", "rate", "resets", "round", "sgn", "sort",
		"sort_by_label", "sort_by_label_desc", "sort_desc", "sqrt", "stddev_over_time",
		"stdvar_over_time", "sum_over_time", "timestamp", "ts_of_first_over_time",
		"ts_of_last_over_time", "ts_of_max_over_time", "ts_of_min_over_time", "year":
		return 0, true
	default:
		return 0, false
	}
}

func (analyzer *Analyzer) remapLabelNames(names []string) bool {
	remapped := false
	for index, name := range names {
		if target, ok := analyzer.labelMap[name]; ok {
			names[index] = target
			remapped = true
		}
	}
	return remapped
}

func (analyzer *Analyzer) remapCallLabelArguments(call *parser.Call) bool {
	indexes := callLabelArgumentIndexes(call)

	remapped := false
	for _, index := range indexes {
		if index >= len(call.Args) {
			continue
		}
		literal, ok := call.Args[index].(*parser.StringLiteral)
		if !ok {
			continue
		}
		if target, ok := analyzer.labelMap[literal.Val]; ok {
			literal.Val = target
			remapped = true
		}
	}
	return remapped
}

func (analyzer *Analyzer) remapAggregateLabelParameter(aggregate *parser.AggregateExpr) bool {
	literal, ok := aggregateLabelParameter(aggregate)
	if !ok {
		return false
	}
	target, remapped := analyzer.labelMap[literal.Val]
	if remapped {
		literal.Val = target
	}
	return remapped
}

func aggregateLabelParameter(aggregate *parser.AggregateExpr) (*parser.StringLiteral, bool) {
	if aggregate.Op != parser.COUNT_VALUES {
		return nil, false
	}
	literal, ok := aggregate.Param.(*parser.StringLiteral)
	return literal, ok
}

func callLabelArgumentIndexes(call *parser.Call) []int {
	var indexes []int
	switch call.Func.Name {
	case "label_replace":
		indexes = []int{1, 3}
	case "label_join":
		for index := 1; index < len(call.Args); index++ {
			if index != 2 {
				indexes = append(indexes, index)
			}
		}
	case "sort_by_label", "sort_by_label_desc":
		for index := 1; index < len(call.Args); index++ {
			indexes = append(indexes, index)
		}
	}
	return indexes
}
