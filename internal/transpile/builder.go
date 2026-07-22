package transpile

import (
	"strings"

	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
)

func buildAggregate(expr parser.Expr, name string) (model.BuilderQuery, bool) {
	aggregate, ok := unwrap(expr).(*parser.AggregateExpr)
	if !ok || aggregate.Without {
		return model.BuilderQuery{}, false
	}
	spaceAggregation, ok := aggregateName(aggregate.Op)
	if !ok {
		return model.BuilderQuery{}, false
	}

	builder, ok := buildMetricExpression(aggregate.Expr, name)
	if !ok {
		return model.BuilderQuery{}, false
	}
	builder.SpaceAggregation = spaceAggregation
	builder.GroupBy = append([]string(nil), aggregate.Grouping...)
	return builder, true
}

func buildMetricExpression(expr parser.Expr, name string) (model.BuilderQuery, bool) {
	switch typed := unwrap(expr).(type) {
	case *parser.VectorSelector:
		return builderFromSelector(typed, name, "latest")
	case *parser.Call:
		timeAggregation, ok := builderTimeAggregation(typed.Func.Name)
		if !ok || len(typed.Args) != 1 {
			return model.BuilderQuery{}, false
		}
		matrix, ok := unwrap(typed.Args[0]).(*parser.MatrixSelector)
		if !ok {
			return model.BuilderQuery{}, false
		}
		selector, ok := unwrap(matrix.VectorSelector).(*parser.VectorSelector)
		if !ok {
			return model.BuilderQuery{}, false
		}
		builder, ok := builderFromSelector(selector, name, timeAggregation)
		if !ok {
			return model.BuilderQuery{}, false
		}
		// Align the builder step to the source range so the SigNoz per-step
		// aggregation is computed over the same window as the PromQL
		// function(m[range]). A literal range (RangeExpr == nil) is required; a
		// variable range cannot be aligned.
		if matrix.RangeExpr == nil && matrix.Range > 0 {
			builder.StepSeconds = int(matrix.Range.Seconds())
		}
		return builder, true
	default:
		return model.BuilderQuery{}, false
	}
}

// builderTimeAggregation maps a supported PromQL range function to a SigNoz
// Builder time aggregation. rate/increase carry their own names; the *_over_time
// family maps to the corresponding per-step aggregation, which is equivalent to
// the range aggregation when the Builder step equals the source range. Every
// mapping remains gated by the live differential, so a case where the two
// nonetheless diverge (for example a bare per-series over_time whose labels the
// Builder space aggregation collapses) falls back to passthrough.
func builderTimeAggregation(function string) (string, bool) {
	switch function {
	case "rate":
		return "rate", true
	case "increase":
		return "increase", true
	case "avg_over_time":
		return "avg", true
	case "max_over_time":
		return "max", true
	case "min_over_time":
		return "min", true
	case "sum_over_time":
		return "sum", true
	case "count_over_time":
		return "count", true
	case "last_over_time":
		return "latest", true
	default:
		return "", false
	}
}

func builderFromSelector(selector *parser.VectorSelector, name, timeAggregation string) (model.BuilderQuery, bool) {
	metric := metricName(selector)
	if metric == "" {
		return model.BuilderQuery{}, false
	}
	return model.BuilderQuery{
		Name:             queryName(name),
		MetricName:       metric,
		TimeAggregation:  timeAggregation,
		SpaceAggregation: "sum",
		Filters:          filters(selector.LabelMatchers),
	}, true
}

func (analyzer *Analyzer) qualifyBuilder(builder *model.BuilderQuery) (bool, bool) {
	metadata, found := analyzer.options.Metrics[builder.MetricName]
	if !found {
		return true, false
	}
	builder.Temporality = strings.ToLower(metadata.Temporality)
	switch builder.TimeAggregation {
	case "rate", "increase":
		if metadata.Type != "sum" || !metadata.IsMonotonic || strings.EqualFold(metadata.Temporality, "delta") {
			return false, true
		}
	case "latest":
		if metadata.Type != "gauge" && metadata.Type != "sum" {
			return false, true
		}
	case "avg", "min", "max", "sum", "count":
		// The *_over_time family reduces samples in the step window; it applies to
		// gauge (and, permissively, sum) metrics. The live differential remains the
		// final arbiter of equivalence.
		if metadata.Type != "gauge" && metadata.Type != "sum" {
			return false, true
		}
	}
	if strings.HasPrefix(builder.SpaceAggregation, "p") && metadata.Type != "histogram" && metadata.Type != "exponential_histogram" {
		return false, true
	}
	return true, true
}

func (analyzer *Analyzer) qualifyFormula(formula *model.Formula) (bool, bool) {
	allKnown := true
	for index := range formula.Queries {
		compatible, metricKnown := analyzer.qualifyBuilder(&formula.Queries[index])
		allKnown = allKnown && metricKnown
		if !compatible {
			return false, allKnown
		}
	}
	return true, allKnown
}

func metricName(selector *parser.VectorSelector) string {
	name := ""
	nameMatchers := 0
	for _, matcher := range selector.LabelMatchers {
		if matcher.Name != "__name__" {
			continue
		}
		nameMatchers++
		if matcher.Type == labels.MatchEqual && matcher.Value != "" {
			name = matcher.Value
		}
	}
	if nameMatchers != 1 || name == "" || selector.Name != "" && selector.Name != name {
		return ""
	}
	return name
}

func filters(matchers []*labels.Matcher) []model.Filter {
	result := make([]model.Filter, 0, len(matchers))
	for _, matcher := range matchers {
		if matcher.Name == "__name__" {
			continue
		}
		operator, value := filterOperator(matcher)
		result = append(result, model.Filter{Label: matcher.Name, Operator: operator, Value: value})
	}
	return result
}

func filterOperator(matcher *labels.Matcher) (string, string) {
	switch matcher.Type {
	case labels.MatchNotEqual:
		return "!=", matcher.Value
	case labels.MatchRegexp:
		return "REGEXP", anchoredRegex(matcher.Value)
	case labels.MatchNotRegexp:
		return "NOT REGEXP", anchoredRegex(matcher.Value)
	default:
		return "=", matcher.Value
	}
}

func anchoredRegex(value string) string {
	if variablePattern.MatchString(value) {
		return value
	}
	if strings.HasPrefix(value, "^(?:") && strings.HasSuffix(value, ")$") {
		return value
	}
	return "^(?:" + value + ")$"
}

func aggregateName(operator parser.ItemType) (string, bool) {
	switch operator {
	case parser.SUM:
		return "sum", true
	case parser.AVG:
		return "avg", true
	case parser.MIN:
		return "min", true
	case parser.MAX:
		return "max", true
	case parser.COUNT:
		return "count", true
	default:
		return "", false
	}
}

func queryName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "A"
	}
	return name
}
