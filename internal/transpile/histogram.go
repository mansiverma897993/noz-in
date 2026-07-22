package transpile

import (
	"math"
	"slices"

	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/prometheus/prometheus/promql/parser"
)

func buildHistogram(expr parser.Expr, name string) (model.BuilderQuery, bool) {
	call, ok := unwrap(expr).(*parser.Call)
	if !ok || call.Func.Name != "histogram_quantile" || len(call.Args) != 2 {
		return model.BuilderQuery{}, false
	}
	quantile, ok := unwrap(call.Args[0]).(*parser.NumberLiteral)
	if !ok {
		return model.BuilderQuery{}, false
	}
	percentile, ok := percentileName(quantile.Val)
	if !ok {
		return model.BuilderQuery{}, false
	}
	aggregate, ok := unwrap(call.Args[1]).(*parser.AggregateExpr)
	if !ok || aggregate.Op != parser.SUM || aggregate.Without || !slices.Contains(aggregate.Grouping, "le") {
		return model.BuilderQuery{}, false
	}
	builder, ok := buildMetricExpression(aggregate.Expr, name)
	if !ok || (builder.TimeAggregation != "rate" && builder.TimeAggregation != "increase") {
		return model.BuilderQuery{}, false
	}
	if slices.ContainsFunc(builder.Filters, func(filter model.Filter) bool { return filter.Label == "le" }) {
		return model.BuilderQuery{}, false
	}

	builder.TimeAggregation = ""
	builder.SpaceAggregation = percentile
	for _, label := range aggregate.Grouping {
		if label != "le" {
			builder.GroupBy = append(builder.GroupBy, label)
		}
	}
	return builder, true
}

func hasHistogramBucketFilter(expr parser.Expr) bool {
	call, ok := unwrap(expr).(*parser.Call)
	if !ok || call.Func.Name != "histogram_quantile" || len(call.Args) != 2 {
		return false
	}
	filtered := false
	parser.Inspect(call.Args[1], func(node parser.Node, _ []parser.Node) error {
		selector, ok := node.(*parser.VectorSelector)
		if !ok {
			return nil
		}
		for _, matcher := range selector.LabelMatchers {
			if matcher.Name == "le" {
				filtered = true
			}
		}
		return nil
	})
	return filtered
}

func percentileName(value float64) (string, bool) {
	percentiles := map[float64]string{
		0.50: "p50",
		0.75: "p75",
		0.90: "p90",
		0.95: "p95",
		0.99: "p99",
	}
	for quantile, name := range percentiles {
		if math.Abs(value-quantile) < 1e-9 {
			return name, true
		}
	}
	return "", false
}
