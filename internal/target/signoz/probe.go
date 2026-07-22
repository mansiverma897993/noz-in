package signoz

import (
	"time"

	"github.com/mansiverma897993/noz-in/internal/model"
)

// NativeProbeRequests builds the two v5 query_range requests used to prove that a
// Builder/formula candidate is numerically equivalent to its PromQL passthrough:
// the Builder envelope and the verbatim PromQL. Both run over the same window and
// variables so a differential comparison isolates only the translation itself.
func NativeProbeRequests(
	translation model.Translation,
	promql string,
	values map[string]VariableItem,
	now time.Time,
	window time.Duration,
) (builderRequest QueryRangeRequest, promqlRequest QueryRangeRequest, ok bool) {
	if window <= 0 {
		window = time.Hour
	}
	end := now.UnixMilli()
	start := now.Add(-window).UnixMilli()
	if start < 0 || end < 0 {
		return QueryRangeRequest{}, QueryRangeRequest{}, false
	}

	builderQueries := builderProbeEnvelopes(translation)
	if len(builderQueries) == 0 || promql == "" {
		return QueryRangeRequest{}, QueryRangeRequest{}, false
	}

	envelope := func(queries []QueryEnvelope) QueryRangeRequest {
		return QueryRangeRequest{
			SchemaVersion:  "v1",
			Start:          uint64(start),
			End:            uint64(end),
			RequestType:    "time_series",
			CompositeQuery: CompositeQuery{Queries: queries},
			Variables:      values,
			NoCache:        true,
		}
	}
	// Evaluate the PromQL passthrough at the same step the Builder uses. Without
	// this the two probes sample at different timestamps, and a volatile gauge
	// (for example a load average) diverges purely from resolution mismatch,
	// producing a false rejection of an otherwise equivalent conversion.
	promqlEnvelope := []QueryEnvelope{{Type: "promql", Spec: PromQLSpec{
		Name: probeQueryName(translation), Query: promql, Disabled: false,
		Step: builderProbeStep(builderQueries),
	}}}
	return envelope(builderQueries), envelope(promqlEnvelope), true
}

// builderProbeStep returns the step interval of the first metric query in the
// probe so the PromQL passthrough is evaluated at the same resolution.
func builderProbeStep(queries []QueryEnvelope) int {
	for _, query := range queries {
		if spec, ok := query.Spec.(BuilderQuerySpec); ok && spec.StepInterval != nil {
			return *spec.StepInterval
		}
	}
	return 0
}

// builderProbeEnvelopes converts a Builder or formula translation into executable
// v5 query envelopes, preserving the range-aligned step of every metric query.
func builderProbeEnvelopes(translation model.Translation) []QueryEnvelope {
	switch translation.Kind {
	case model.TranslationBuilder:
		if translation.Builder == nil {
			return nil
		}
		return []QueryEnvelope{builderQueryEnvelope(*translation.Builder)}
	case model.TranslationFormula:
		if translation.Formula == nil {
			return nil
		}
		queries := make([]QueryEnvelope, 0, len(translation.Formula.Queries)+1)
		for _, builder := range translation.Formula.Queries {
			envelope := builderQueryEnvelope(builder)
			if spec, ok := envelope.Spec.(BuilderQuerySpec); ok {
				spec.Disabled = true
				envelope.Spec = spec
			}
			queries = append(queries, envelope)
		}
		queries = append(queries, QueryEnvelope{Type: "builder_formula", Spec: FormulaSpec{
			Name: translation.Formula.Name, Expression: translation.Formula.Expression,
		}})
		return queries
	default:
		return nil
	}
}

func builderQueryEnvelope(builder model.BuilderQuery) QueryEnvelope {
	groupBy := make([]GroupBy, 0, len(builder.GroupBy))
	for _, name := range builder.GroupBy {
		groupBy = append(groupBy, GroupBy{
			Name:         name,
			FieldContext: dashboardFieldContext(name),
		})
	}
	functions := make([]Function, 0, len(builder.Functions))
	for _, function := range builder.Functions {
		args := make([]FunctionArg, 0, len(function.Args))
		for _, value := range function.Args {
			args = append(args, FunctionArg{Value: value})
		}
		functions = append(functions, Function{Name: function.Name, Args: args})
	}
	spec := BuilderQuerySpec{
		Name:         builder.Name,
		StepInterval: optionalStepInterval(builder.StepSeconds),
		Signal:       "metrics",
		Aggregations: []MetricAggregationSpec{{
			MetricName:       builder.MetricName,
			Temporality:      builder.Temporality,
			TimeAggregation:  builder.TimeAggregation,
			SpaceAggregation: builder.SpaceAggregation,
		}},
		Filter:    Expression{Expression: filterExpression(builder.Filters)},
		GroupBy:   groupBy,
		Functions: functions,
	}
	return QueryEnvelope{Type: "builder_query", Spec: spec}
}

func probeQueryName(translation model.Translation) string {
	switch translation.Kind {
	case model.TranslationBuilder:
		if translation.Builder != nil {
			return translation.Builder.Name
		}
	case model.TranslationFormula:
		if translation.Formula != nil {
			return translation.Formula.Name
		}
	}
	return "A"
}
