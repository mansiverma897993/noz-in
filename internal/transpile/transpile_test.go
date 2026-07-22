package transpile

import (
	"testing"
	"time"

	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnalyze(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{Interval: 5 * time.Minute, Metrics: map[string]model.TargetMetric{
		"node_cpu_seconds_total":               {Type: "sum", Temporality: "cumulative", IsMonotonic: true},
		"http_request_duration_seconds_bucket": {Type: "histogram"},
		"http_requests_total":                  {Type: "sum", Temporality: "cumulative", IsMonotonic: true},
	}})
	tests := []struct {
		name      string
		query     model.Query
		kind      model.TranslationKind
		verdict   model.Verdict
		reasons   []model.ReasonCode
		assertion func(*testing.T, model.Translation)
	}{
		{
			name:    "aggregate rate becomes builder candidate",
			query:   model.Query{RefID: "A", Expression: `sum by (instance) (rate(node_cpu_seconds_total{mode!="idle",job=~"node.+"}[5m]))`},
			kind:    model.TranslationBuilder,
			verdict: model.VerdictNeedsReview,
			reasons: []model.ReasonCode{model.ReasonBuilderRateIncrease},
			assertion: func(t *testing.T, translation model.Translation) {
				require.NotNil(t, translation.Builder)
				assert.Equal(t, "node_cpu_seconds_total", translation.Builder.MetricName)
				assert.Equal(t, "rate", translation.Builder.TimeAggregation)
				assert.Equal(t, "sum", translation.Builder.SpaceAggregation)
				assert.Equal(t, []string{"service.instance.id"}, translation.Builder.GroupBy)
				assert.Equal(t, "^(?:node.+)$", translation.Builder.Filters[1].Value)
				assert.Equal(t, "service.name", translation.Builder.Filters[1].Label)
				assert.Contains(t, translation.PromQL, `by ("service.instance.id")`)
				assert.Contains(t, translation.PromQL, `"service.name"=~"^(?:node.+)$"`)
			},
		},
		{
			name:    "passthrough remaps resource labels and anchors regex",
			query:   model.Query{RefID: "A", Expression: `irate(requests_total{job=~"api.+",instance="$node"}[5m])`},
			kind:    model.TranslationPromQL,
			verdict: model.VerdictPassthrough,
			reasons: []model.ReasonCode{model.ReasonUnsupportedFunction, model.ReasonResourceLabelRemap},
			assertion: func(t *testing.T, translation model.Translation) {
				assert.Contains(t, translation.PromQL, `"service.name"=~"^(?:api.+)$"`)
				assert.Contains(t, translation.PromQL, `"service.instance.id"="$node"`)
			},
		},
		{
			name:    "variable regex needs review as promql",
			query:   model.Query{RefID: "A", Expression: `sum(up{job=~"$job"})`},
			kind:    model.TranslationPromQL,
			verdict: model.VerdictNeedsReview,
			reasons: []model.ReasonCode{model.ReasonRegexVariable},
			assertion: func(t *testing.T, translation model.Translation) {
				assert.Contains(t, translation.PromQL, `"service.name"=~"$job"`)
			},
		},
		{
			name:    "canonical histogram becomes percentile builder candidate",
			query:   model.Query{RefID: "B", Expression: `histogram_quantile(0.99, sum by (le, service) (rate(http_request_duration_seconds_bucket[5m])))`},
			kind:    model.TranslationBuilder,
			verdict: model.VerdictNeedsReview,
			reasons: []model.ReasonCode{model.ReasonBuilderHistogramPercentile},
			assertion: func(t *testing.T, translation model.Translation) {
				assert.Equal(t, "http_request_duration_seconds_bucket", translation.Builder.MetricName)
				assert.Equal(t, "p99", translation.Builder.SpaceAggregation)
				assert.Empty(t, translation.Builder.TimeAggregation)
				assert.Equal(t, []string{"service"}, translation.Builder.GroupBy)
			},
		},
		{
			name:    "ratio becomes builder formula candidate",
			query:   model.Query{RefID: "A", Expression: `sum(rate(http_requests_total{code=~"5.."}[5m])) / sum(rate(http_requests_total[5m])) * 100`},
			kind:    model.TranslationFormula,
			verdict: model.VerdictNeedsReview,
			reasons: []model.ReasonCode{model.ReasonBuilderFormulaEvaluation, model.ReasonBuilderRateIncrease},
			assertion: func(t *testing.T, translation model.Translation) {
				require.NotNil(t, translation.Formula)
				assert.Equal(t, "A", translation.Formula.Name)
				require.Len(t, translation.Formula.Queries, 2)
				left := translation.Formula.Queries[0].Name
				right := translation.Formula.Queries[1].Name
				assert.Equal(t, "(("+left+" / "+right+") * 100)", translation.Formula.Expression)
				assert.NotEqual(t, "A_1", left)
				assert.Equal(t, "^(?:5..)$", translation.Formula.Queries[0].Filters[0].Value)
			},
		},
		{
			name:    "plain selector waits for metric metadata",
			query:   model.Query{RefID: "A", Expression: `up{job="api"}`},
			kind:    model.TranslationPromQL,
			verdict: model.VerdictPassthrough,
			reasons: []model.ReasonCode{model.ReasonMetricTypeRequired},
		},
		{
			name:    "vector matching stays promql",
			query:   model.Query{RefID: "A", Expression: `sum(rate(requests_total[5m])) by (service) / on(service) group_left(team) service_info`},
			kind:    model.TranslationPromQL,
			verdict: model.VerdictPassthrough,
			reasons: []model.ReasonCode{model.ReasonVectorMatching},
		},
		{
			name:    "recording rule needs review",
			query:   model.Query{RefID: "A", Expression: `sum(instance:node_cpu_utilisation:rate5m)`},
			kind:    model.TranslationPromQL,
			verdict: model.VerdictNeedsReview,
			reasons: []model.ReasonCode{model.ReasonRecordingRuleMetric},
		},
		{
			name:    "nonstandard quantile stays promql",
			query:   model.Query{RefID: "A", Expression: `histogram_quantile(0.97, sum by (le) (rate(latency_bucket[5m])))`},
			kind:    model.TranslationPromQL,
			verdict: model.VerdictPassthrough,
			reasons: []model.ReasonCode{model.ReasonNonstandardQuantile},
		},
		{
			name:    "invalid promql is explicit review",
			query:   model.Query{RefID: "A", Expression: `sum(rate(`},
			kind:    model.TranslationNone,
			verdict: model.VerdictNeedsReview,
			reasons: []model.ReasonCode{model.ReasonParseError},
			assertion: func(t *testing.T, translation model.Translation) {
				assert.NotEmpty(t, translation.ParseErrors)
			},
		},
		{
			name: "non-prometheus datasource is not passed through",
			query: model.Query{
				RefID:      "A",
				Expression: `{app="api"} |= "error"`,
				Datasource: model.Datasource{Type: "loki"},
			},
			kind:    model.TranslationNone,
			verdict: model.VerdictNeedsReview,
			reasons: []model.ReasonCode{model.ReasonNonPromDatasource},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			translation := analyzer.Analyze(test.query)
			assert.Equal(t, test.kind, translation.Kind)
			assert.Equal(t, test.verdict, translation.Decision.Verdict)
			for _, reason := range test.reasons {
				assert.Contains(t, translation.Decision.Reasons, reason)
			}
			if test.assertion != nil {
				test.assertion(t, translation)
			}
		})
	}
}

func TestAnalyzeKeepsEveryBuilderShapeCandidateOnly(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{Interval: time.Minute, Metrics: map[string]model.TargetMetric{
		"counter_total":   {Type: "sum", Temporality: "cumulative", IsMonotonic: true},
		"gauge_value":     {Type: "gauge"},
		"other_gauge":     {Type: "gauge"},
		"duration_bucket": {Type: "histogram", Temporality: "cumulative"},
	}})
	tests := []struct {
		name       string
		expression string
		kind       model.TranslationKind
		reasons    []model.ReasonCode
	}{
		{name: "rate", expression: `rate(counter_total[1m])`, kind: model.TranslationBuilder, reasons: []model.ReasonCode{model.ReasonBuilderRateIncrease}},
		{name: "increase", expression: `increase(counter_total[1m])`, kind: model.TranslationBuilder, reasons: []model.ReasonCode{model.ReasonBuilderRateIncrease}},
		{name: "latest", expression: `gauge_value`, kind: model.TranslationBuilder, reasons: []model.ReasonCode{model.ReasonBuilderLatestLookback}},
		{name: "histogram percentile", expression: `histogram_quantile(0.95, sum by (le) (rate(duration_bucket[1m])))`, kind: model.TranslationBuilder, reasons: []model.ReasonCode{model.ReasonBuilderHistogramPercentile}},
		{name: "formula", expression: `sum(gauge_value) / sum(other_gauge)`, kind: model.TranslationFormula, reasons: []model.ReasonCode{model.ReasonBuilderFormulaEvaluation, model.ReasonBuilderLatestLookback}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			translation := analyzer.Analyze(model.Query{RefID: "A", Expression: test.expression})

			assert.Equal(t, test.kind, translation.Kind)
			assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
			assert.NotEmpty(t, translation.PromQL)
			if test.kind == model.TranslationFormula {
				assert.NotNil(t, translation.Formula)
			} else {
				assert.NotNil(t, translation.Builder)
			}
			for _, reason := range test.reasons {
				assert.Contains(t, translation.Decision.Reasons, reason)
				assert.True(t, model.IsBuilderCandidateSemanticReason(reason))
			}
		})
	}
}

func TestAnalyzeAccountsForGrafanaQueryFormat(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{
		"up": {Type: "gauge"},
	}})
	tests := []struct {
		name       string
		format     string
		expression string
		hidden     bool
		kind       model.TranslationKind
		verdict    model.Verdict
		reasons    []model.ReasonCode
	}{
		{name: "unset", expression: "sum(up)", kind: model.TranslationBuilder, verdict: model.VerdictNeedsReview, reasons: []model.ReasonCode{model.ReasonBuilderLatestLookback}},
		{name: "empty", format: "   ", expression: "sum(up)", kind: model.TranslationBuilder, verdict: model.VerdictNeedsReview, reasons: []model.ReasonCode{model.ReasonBuilderLatestLookback}},
		{name: "default", format: "time_series", expression: "sum(up)", kind: model.TranslationBuilder, verdict: model.VerdictNeedsReview, reasons: []model.ReasonCode{model.ReasonBuilderLatestLookback}},
		{name: "trimmed case-insensitive default", format: "  TIME_SERIES  ", expression: "sum(up)", kind: model.TranslationBuilder, verdict: model.VerdictNeedsReview, reasons: []model.ReasonCode{model.ReasonBuilderLatestLookback}},
		{name: "table", format: " table ", expression: "sum(up)", kind: model.TranslationBuilder, verdict: model.VerdictNeedsReview, reasons: []model.ReasonCode{model.ReasonBuilderLatestLookback, model.ReasonGrafanaQueryFormat}},
		{name: "heatmap", format: "heatmap", expression: "sum(up)", kind: model.TranslationBuilder, verdict: model.VerdictNeedsReview, reasons: []model.ReasonCode{model.ReasonBuilderLatestLookback, model.ReasonGrafanaQueryFormat}},
		{name: "unknown preserves passthrough", format: "dataframe", expression: "irate(up[1m])", kind: model.TranslationPromQL, verdict: model.VerdictNeedsReview, reasons: []model.ReasonCode{model.ReasonUnsupportedFunction, model.ReasonGrafanaQueryFormat}},
		{name: "format combines with existing review", format: "table", expression: "sum(up)", hidden: true, kind: model.TranslationBuilder, verdict: model.VerdictNeedsReview, reasons: []model.ReasonCode{model.ReasonBuilderLatestLookback, model.ReasonGrafanaQueryFormat, model.ReasonHiddenTarget}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			translation := analyzer.Analyze(model.Query{
				RefID: "A", Expression: test.expression, Format: test.format, Hidden: test.hidden,
			})

			assert.Equal(t, test.kind, translation.Kind)
			assert.Equal(t, test.verdict, translation.Decision.Verdict)
			assert.ElementsMatch(t, test.reasons, translation.Decision.Reasons)
		})
	}
}

func TestAnalyzeNormalizesTargetVariableSyntaxWithoutMutatingSource(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{Interval: 5 * time.Minute, Metrics: map[string]model.TargetMetric{
		"up": {Type: "gauge"},
	}})

	t.Run("builder matcher values", func(t *testing.T) {
		expression := `sum(up{job="${job}",instance="[[instance]]"})`
		query := model.Query{RefID: "A", Expression: expression}
		translation := analyzer.Analyze(query)

		require.Equal(t, model.TranslationBuilder, translation.Kind)
		require.NotNil(t, translation.Builder)
		require.Len(t, translation.Builder.Filters, 2)
		assert.Equal(t, "$job", translation.Builder.Filters[0].Value)
		assert.Equal(t, "$instance", translation.Builder.Filters[1].Value)
		assert.Contains(t, translation.PromQL, `"service.name"="$job"`)
		assert.Contains(t, translation.PromQL, `"service.instance.id"="$instance"`)
		assert.Equal(t, expression, query.Expression)
	})

	t.Run("duration metric and grouping grammar", func(t *testing.T) {
		expression := `sum by (${group}) (rate(${metric}{job="${job}"}[${window}]))`
		translation := analyzer.Analyze(model.Query{RefID: "A", Expression: expression})

		assert.Equal(t, model.TranslationNone, translation.Kind)
		assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
		assert.Empty(t, translation.PromQL)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonDynamicStructure)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonDynamicRewriteConflict)
	})

	t.Run("dynamic grammar remains executable when no target rewrite is required", func(t *testing.T) {
		expression := `rate(${metric}{}[${window}])`
		translation := analyzer.Analyze(model.Query{RefID: "A", Expression: expression})

		assert.Equal(t, model.TranslationPromQL, translation.Kind)
		assert.Equal(t, model.VerdictPassthrough, translation.Decision.Verdict)
		assert.Equal(t, `rate($metric{}[$window])`, translation.PromQL)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonDynamicStructure)
		assert.NotContains(t, translation.Decision.Reasons, model.ReasonDynamicRewriteConflict)
	})

	t.Run("replacement capture is not a dashboard variable", func(t *testing.T) {
		translation := analyzer.Analyze(model.Query{
			RefID: "A", Expression: `label_replace(up, "dst", "${1}", "src", "(.*)")`,
		})

		assert.Contains(t, translation.PromQL, `"${1}"`)
		assert.NotContains(t, translation.Decision.Reasons, model.ReasonGrafanaVariableFormat)
	})

	t.Run("regex and pipe formats in exact regex matcher", func(t *testing.T) {
		for _, variable := range []string{"${job:regex}", "${job:pipe}", "[[job:regex]]", "[[job:pipe]]"} {
			translation := analyzer.Analyze(model.Query{
				RefID: "A", Expression: `sum(up{job=~"` + variable + `"})`,
			})

			assert.Equal(t, model.TranslationPromQL, translation.Kind)
			assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
			assert.Contains(t, translation.PromQL, `"service.name"=~"$job"`)
			assert.Contains(t, translation.Decision.Reasons, model.ReasonRegexVariable)
		}
	})

	t.Run("unproven formatter is not executable", func(t *testing.T) {
		for _, expression := range []string{
			`sum(up{job="${job:csv}"})`,
			`sum(up{job="${job:regex}"})`,
			`sum(up{job="${job.value}"})`,
			`sum(up{job=~"${job.value:regex}"})`,
			`sum(up{job="$__dashboard"})`,
			`sum(up{job="${__org}"})`,
			`sum(up{job="[[__timezone]]"})`,
		} {
			translation := analyzer.Analyze(model.Query{RefID: "A", Expression: expression})

			assert.Equal(t, model.TranslationNone, translation.Kind)
			assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
			assert.Contains(t, translation.Decision.Reasons, model.ReasonGrafanaVariableFormat)
			assert.Empty(t, translation.PromQL)
			assert.Nil(t, translation.Builder)
		}
	})

	t.Run("Grafana time globals map to reserved SigNoz runtime variables", func(t *testing.T) {
		translation := analyzer.Analyze(model.Query{
			RefID: "A", Expression: `vector($__from) + vector(${__to}) + vector([[__from]])`,
		})

		assert.Equal(t, model.TranslationPromQL, translation.Kind)
		assert.Contains(t, translation.PromQL, `$SIGNOZ_START_TIME`)
		assert.Contains(t, translation.PromQL, `$SIGNOZ_END_TIME`)
		assert.NotContains(t, VariableNames(translation.PromQL), targetStartTimeVariable)
		assert.NotContains(t, VariableNames(translation.PromQL), targetEndTimeVariable)
	})

	t.Run("formatted Grafana time global fails closed", func(t *testing.T) {
		translation := analyzer.Analyze(model.Query{
			RefID: "A", Expression: `vector(${__from:date:seconds})`,
		})

		assert.Equal(t, model.TranslationNone, translation.Kind)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonGrafanaVariableFormat)
		assert.Empty(t, translation.PromQL)
	})

	t.Run("Grafana globals remain deterministic", func(t *testing.T) {
		translation := analyzer.Analyze(model.Query{RefID: "A", Expression: `rate(up[${__interval}])`})

		assert.NotContains(t, translation.PromQL, "${__interval}")
		assert.Contains(t, translation.PromQL, "[5m]")
		assert.NotContains(t, translation.Decision.Reasons, model.ReasonGrafanaVariableFormat)
	})
}

func TestAnalyzeRejectsLabelRemapCollisions(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{
		LabelMap: map[string]string{"legacy": "target"},
		Metrics:  map[string]model.TargetMetric{"up": {Type: "gauge"}},
	})
	for _, expression := range []string{
		`up{legacy="a",target="b"}`,
		`sum by (legacy, target) (up)`,
		`up + on(legacy, target) up`,
		`label_replace(up, "target", "$1", "legacy", "(.*)")`,
	} {
		t.Run(expression, func(t *testing.T) {
			t.Parallel()

			translation := analyzer.Analyze(model.Query{RefID: "A", Expression: expression})

			assert.Equal(t, model.TranslationNone, translation.Kind)
			assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
			assert.Empty(t, translation.PromQL)
			assert.Contains(t, translation.Decision.Reasons, model.ReasonLabelRemapCollision)
		})
	}
}

func TestAnalyzeOmitsExplicitLabelsStrippedByTargetPromQLResponse(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{})
	for _, expression := range []string{
		`label_replace(up,"__tenant","$1","instance","(.*)")`,
		`label_join(up,"fingerprint",",","job","instance")`,
		`count_values("__bucket", up)`,
	} {
		translation := analyzer.Analyze(model.Query{RefID: "A", Expression: expression})
		assert.Equal(t, model.TranslationNone, translation.Kind, expression)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonTargetResponseLabelStripped, expression)
	}

	for _, expression := range []string{
		`sum by(fingerprint)(label_replace(up,"fingerprint","$1","instance","(.*)"))`,
		`up{fingerprint="abc"}`,
		`histogram_quantile(0.9, sum by(le,fingerprint)(rate(request_duration_bucket[5m])))`,
		`absent(up{fingerprint="abc"})`,
		`up / on(job) group_right(fingerprint) up{fingerprint="abc"}`,
		`label_replace(up,"copy","$1","fingerprint","(.+)")`,
		`label_replace(up,"copy","$1","__scope.name__","(.+)")`,
		`label_join(up,"copy",",","fingerprint","job")`,
		`label_join(up,"copy",",","__scope.name__")`,
		`topk by(fingerprint)(1, up)`,
		`sum(label_replace(up,"copy","$1","fingerprint","(.+)"))`,
		`sum(up{fingerprint="abc"})`,
		`sum(up / on(fingerprint) up)`,
		`sum by ("server.address") (up)`,
		`sum(up / on ("server.address") up)`,
		`sum(up / on(job) group_left ("server.port") up)`,
		`histogram_quantile(0.9, rate(request_duration_seconds_bucket[5m]))`,
		`histogram_fraction(0.1, 0.9, rate(request_duration_seconds_bucket[5m]))`,
	} {
		translation := analyzer.Analyze(model.Query{RefID: "A", Expression: expression})
		assert.Equal(t, model.TranslationNone, translation.Kind, expression)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonTargetOnlyLabelSemanticUse, expression)
	}

	for _, expression := range []string{
		`sum(label_replace(up,"fingerprint","$1","instance","(.*)"))`,
		`sum without(fingerprint)(up)`,
		`sum by(__name__)(up)`,
		`histogram_quantile(0.9, sum by(le)(rate(request_duration_seconds_bucket[5m])))`,
		`histogram_fraction(0.1, 0.9, sum by(le)(rate(request_duration_seconds_bucket[5m])))`,
	} {
		translation := analyzer.Analyze(model.Query{RefID: "A", Expression: expression})
		assert.NotEqual(t, model.TranslationNone, translation.Kind, expression)
		assert.NotContains(t, translation.Decision.Reasons, model.ReasonTargetResponseLabelStripped, expression)
	}

	remapped := NewAnalyzer(Options{LabelMap: map[string]string{"fingerprint": "source.fingerprint"}}).Analyze(
		model.Query{RefID: "A", Expression: `up{fingerprint="abc"}`},
	)
	assert.NotEqual(t, model.TranslationNone, remapped.Kind)
}

func TestAnalyzeOmitsNativeHistogramResultsDroppedByTargetPromQLResponse(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{
		"native_hist": {Type: "histogram"},
		"expo_hist":   {Type: "exponential_histogram"},
	}})
	for _, expression := range []string{
		"native_hist",
		"sum(native_hist)",
		"rate(native_hist[5m])",
		"expo_hist + expo_hist",
	} {
		translation := analyzer.Analyze(model.Query{RefID: "A", Expression: expression})
		assert.Equal(t, model.TranslationNone, translation.Kind, expression)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonTargetNativeHistogramDropped, expression)
	}

	for _, expression := range []string{
		"histogram_count(native_hist)",
		"histogram_sum(native_hist)",
		"histogram_avg(native_hist)",
		"histogram_quantile(0.9, native_hist)",
		"count(native_hist)",
		"present_over_time(native_hist[5m])",
	} {
		translation := analyzer.Analyze(model.Query{RefID: "A", Expression: expression})
		assert.NotEqual(t, model.TranslationNone, translation.Kind, expression)
		assert.NotContains(t, translation.Decision.Reasons, model.ReasonTargetNativeHistogramDropped, expression)
	}
}

func TestAnalyzeDoesNotMistakeClassicBucketMetadataForNativeHistogramSamples(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{
		"request_duration_seconds_bucket": {
			Name:       "request_duration_seconds.bucket",
			Type:       "histogram",
			Attributes: []string{"le", "service.name"},
		},
	}})
	for _, expression := range []string{
		`histogram_quantile(0.9, rate(request_duration_seconds_bucket[5m]))`,
		`histogram_fraction(0.1, 0.9, rate(request_duration_seconds_bucket[5m]))`,
	} {
		translation := analyzer.Analyze(model.Query{Expression: expression})
		assert.Equal(t, model.TranslationNone, translation.Kind, expression)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonTargetOnlyLabelSemanticUse, expression)
	}
}

func TestAnalyzeOmitsMetricNameSemanticReadsAfterSelectorRemap(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{
		"source_a": {Name: "target.a", Type: "gauge"},
	}})
	for _, expression := range []string{
		`label_replace(source_a,"copy","$1","__name__","(.+)")`,
		`label_join(source_a,"copy",",","__name__")`,
		`sum by(__name__)(source_a)`,
		`source_a / on(__name__) source_a`,
	} {
		translation := analyzer.Analyze(model.Query{Expression: expression})
		assert.Equal(t, model.TranslationNone, translation.Kind, expression)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonMetricNameSemanticUse, expression)
	}

	safe := analyzer.Analyze(model.Query{Expression: `source_a`})
	assert.NotEqual(t, model.TranslationNone, safe.Kind)
	assert.Contains(t, safe.PromQL, `target.a`)
}

func TestAnalyzeRejectsCrossNodeLabelRemapCollisions(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{"up": {Type: "gauge"}}})
	for _, expression := range []string{
		`sum by ("service.name") (up{job="api"})`,
		`label_replace(up{"service.name"="original"}, "job", "$1", "source", "(.*)")`,
	} {
		t.Run(expression, func(t *testing.T) {
			t.Parallel()

			translation := analyzer.Analyze(model.Query{RefID: "A", Expression: expression})

			assert.Equal(t, model.TranslationNone, translation.Kind)
			assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
			assert.Empty(t, translation.PromQL)
			assert.Contains(t, translation.Decision.Reasons, model.ReasonLabelRemapCollision)
		})
	}
}

func TestAnalyzeRejectsExpressionMetricRemapCollisions(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{
		"foo":      {Name: "combined", Type: "gauge"},
		"bar":      {Name: "combined", Type: "gauge"},
		"combined": {Type: "gauge"},
	}})
	for _, expression := range []string{
		`foo / bar`,
		`foo + combined`,
	} {
		t.Run(expression, func(t *testing.T) {
			t.Parallel()

			translation := analyzer.Analyze(model.Query{RefID: "A", Expression: expression})

			assert.Equal(t, model.TranslationNone, translation.Kind)
			assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
			assert.Empty(t, translation.PromQL)
			assert.Contains(t, translation.Decision.Reasons, model.ReasonMetricRemapCollision)
		})
	}

	for _, expression := range []string{`foo`, `bar`, `foo + foo`} {
		translation := analyzer.Analyze(model.Query{RefID: "A", Expression: expression})
		assert.NotEqual(t, model.TranslationNone, translation.Kind, expression)
		assert.NotContains(t, translation.Decision.Reasons, model.ReasonMetricRemapCollision, expression)
	}
}

func TestAnalyzeRejectsVariablesInMappedIdentifierRoles(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{"up": {Type: "gauge"}}})
	for _, expression := range []string{
		`label_replace(up, "copied", "$1", "$src", "(.*)")`,
		`label_replace(up, "$destination", "$1", "job", "(.*)")`,
		`label_join(up, "joined", ",", "$source")`,
		`label_join(up, "$destination", ",", "job")`,
		`count_values("$label", up)`,
		`sum by ("$group") (up)`,
		`up{"$label"="api"}`,
		`up + on("$label") up`,
		`up + on(instance) group_left("$label") up`,
	} {
		t.Run(expression, func(t *testing.T) {
			t.Parallel()

			translation := analyzer.Analyze(model.Query{RefID: "A", Expression: expression})

			assert.Equal(t, model.TranslationNone, translation.Kind)
			assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
			assert.Empty(t, translation.PromQL)
			assert.Contains(t, translation.Decision.Reasons, model.ReasonDynamicStructure)
			assert.Contains(t, translation.Decision.Reasons, model.ReasonDynamicRewriteConflict)
		})
	}

	matcherValue := analyzer.Analyze(model.Query{RefID: "A", Expression: `up{environment="$environment"}`})
	assert.NotEqual(t, model.TranslationNone, matcherValue.Kind)
	assert.NotContains(t, matcherValue.Decision.Reasons, model.ReasonDynamicRewriteConflict)

	dynamicMetric := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{
		"foo": {Name: "target.foo", Type: "gauge"},
	}}).Analyze(model.Query{RefID: "A", Expression: `{"$metric"}`})
	assert.Equal(t, model.TranslationNone, dynamicMetric.Kind)
	assert.Contains(t, dynamicMetric.Decision.Reasons, model.ReasonDynamicRewriteConflict)

	unmappedDynamicMetric := NewAnalyzer(Options{}).Analyze(model.Query{RefID: "A", Expression: `{"$metric"}`})
	assert.NotEqual(t, model.TranslationNone, unmappedDynamicMetric.Kind)
	assert.Contains(t, unmappedDynamicMetric.Decision.Reasons, model.ReasonDynamicStructure)
}

func TestAnalyzeRemapsCountValuesLabelParameter(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{"up": {Type: "gauge"}}})
	translation := analyzer.Analyze(model.Query{RefID: "A", Expression: `count_values("job", up)`})

	assert.NotEqual(t, model.TranslationNone, translation.Kind)
	assert.Contains(t, translation.PromQL, `count_values("service.name", up)`)
	assert.Contains(t, translation.Decision.Reasons, model.ReasonResourceLabelRemap)

	collision := analyzer.Analyze(model.Query{
		RefID: "A", Expression: `count_values("job", up{"service.name"="original"})`,
	})
	assert.Equal(t, model.TranslationNone, collision.Kind)
	assert.Contains(t, collision.Decision.Reasons, model.ReasonLabelRemapCollision)
}

func TestQuotedMatcherTextIsNotPromQLStructure(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{"up": {Type: "gauge"}}})
	for _, expression := range []string{
		`up{environment=~"[$value]"}`,
		`up{job=~"[$value]"}`,
		`up{environment=~"offset $value"}`,
		`up{job="offset $value"}`,
		`label_replace(up, "copied", "$1", "job", "offset $pattern")`,
	} {
		t.Run(expression, func(t *testing.T) {
			t.Parallel()

			translation := analyzer.Analyze(model.Query{RefID: "A", Expression: expression})

			assert.NotEqual(t, model.TranslationNone, translation.Kind)
			assert.NotContains(t, translation.Decision.Reasons, model.ReasonDynamicStructure)
			assert.NotContains(t, translation.Decision.Reasons, model.ReasonDynamicRewriteConflict)
		})
	}
}

func TestAnalyzeRequiresMetadataBeforeNativeAggregate(t *testing.T) {
	t.Parallel()

	for _, expression := range []string{
		`sum(rate(arbitrary_gauge[1m]))`,
		`sum(up)`,
		`sum(rate(requests_total[1m])) / sum(rate(limits_total[1m]))`,
	} {
		translation := NewAnalyzer(Options{}).Analyze(model.Query{RefID: "A", Expression: expression})

		assert.Equal(t, model.TranslationPromQL, translation.Kind, expression)
		assert.Nil(t, translation.Builder, expression)
		assert.Nil(t, translation.Formula, expression)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonMetricTypeRequired, expression)
	}
}

func TestAnalyzeRequiresMetadataForEveryFormulaDependency(t *testing.T) {
	t.Parallel()

	translation := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{
		"requests_total": {Type: "sum", Temporality: "cumulative", IsMonotonic: true},
	}}).Analyze(model.Query{
		RefID: "A", Expression: `sum(rate(requests_total[1m])) / sum(rate(unknown_total[1m]))`,
	})

	assert.Equal(t, model.TranslationPromQL, translation.Kind)
	assert.Nil(t, translation.Formula)
	assert.Contains(t, translation.Decision.Reasons, model.ReasonMetricTypeRequired)
}

func TestAnalyzeRejectsFormulaWithExplicitVectorMatching(t *testing.T) {
	t.Parallel()

	attributes := []string{"server.address", "service.instance.id"}
	translation := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{
		"metric_a": {Type: "gauge", Attributes: attributes},
		"metric_b": {Type: "gauge", Attributes: attributes},
	}}).Analyze(model.Query{
		Expression: `metric_a / on("service.instance.id") metric_b`,
	})

	assert.Equal(t, model.TranslationPromQL, translation.Kind)
	assert.Equal(t, model.VerdictPassthrough, translation.Decision.Verdict)
	assert.Nil(t, translation.Formula)
	assert.Contains(t, translation.PromQL, `on ("service.instance.id")`)
	assert.Contains(t, translation.Decision.Reasons, model.ReasonVectorMatching)
	assert.NotContains(t, translation.Decision.Reasons, model.ReasonFormulaLabelMismatch)
}

func TestAnalyzeInlinesRecordingRule(t *testing.T) {
	t.Parallel()

	translation := NewAnalyzer(Options{Interval: 5 * time.Minute, RecordingRules: map[string]model.Rule{
		"instance:node_cpu:rate5m": {
			Record:     "instance:node_cpu:rate5m",
			Expression: `sum by (instance) (rate(node_cpu_seconds_total[5m]))`,
		},
	}}).Analyze(model.Query{
		RefID:      "A",
		Expression: `100 * instance:node_cpu:rate5m`,
	})

	assert.NotEqual(t, model.VerdictNeedsReview, translation.Decision.Verdict)
	assert.Contains(t, translation.Decision.Reasons, model.ReasonRecordingRuleInlined)
	assert.NotContains(t, translation.Decision.Reasons, model.ReasonRecordingRuleMetric)
	assert.Contains(t, translation.PromQL, "node_cpu_seconds_total")
	assert.NotContains(t, translation.PromQL, "instance:node_cpu:rate5m")
}

func TestAnalyzeDoesNotInlineConstrainedOrCyclicRecordingRule(t *testing.T) {
	t.Parallel()

	rules := map[string]model.Rule{
		"constrained:record": {
			Record:     "constrained:record",
			Expression: "up",
		},
		"cycle:a": {
			Record:     "cycle:a",
			Expression: "cycle:b",
		},
		"cycle:b": {
			Record:     "cycle:b",
			Expression: "cycle:a",
		},
	}
	tests := []string{
		`constrained:record{service="api"}`,
		"cycle:a",
	}
	for _, expression := range tests {
		translation := NewAnalyzer(Options{RecordingRules: rules}).Analyze(model.Query{Expression: expression})
		assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonRecordingRuleMetric)
		assert.NotContains(t, translation.Decision.Reasons, model.ReasonRecordingRuleInlined)
	}
}

func TestAnalyzeDoesNotInlineRecordingRuleIntoDynamicQuery(t *testing.T) {
	t.Parallel()

	rules := map[string]model.Rule{
		"my:rule": {
			Record:     "my:rule",
			Expression: `sum by (pod) (base_metric)`,
		},
	}
	// Each expression combines the recording-rule metric with a Grafana variable.
	// Inlining would rewrite the sentinel-substituted parse AST and bake the
	// variable's placeholder literal into the emitted query. The fix keeps the
	// recording-rule metric intact and routes the query to review with the
	// pristine variable preserved verbatim.
	cases := []struct {
		name       string
		expression string
	}{
		{"scalar factor", `my:rule * $factor`},
		{"threshold comparison", `my:rule > $threshold`},
		{"range variable operand", `my:rule + on(pod) rate(other_metric[$window])`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			translation := NewAnalyzer(Options{Interval: 5 * time.Minute, RecordingRules: rules}).
				Analyze(model.Query{RefID: "A", Expression: testCase.expression})

			assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
			assert.Contains(t, translation.Decision.Reasons, model.ReasonRecordingRuleMetric)
			assert.NotContains(t, translation.Decision.Reasons, model.ReasonRecordingRuleInlined)
			// The recording-rule metric is not expanded, so its expansion body
			// must not appear, and the pristine variable must survive untouched.
			assert.NotContains(t, translation.PromQL, "base_metric")
			assert.Contains(t, translation.PromQL, "my:rule")
			// No sentinel placeholder literal may leak into the emitted query.
			assert.NotContains(t, translation.PromQL, "* 1")
			assert.NotContains(t, translation.PromQL, "> 1")
			assert.NotContains(t, translation.PromQL, "sm_var_")
		})
	}
}

func TestAnalyzeUsesMetadataMetricNameAndTemporality(t *testing.T) {
	t.Parallel()

	translation := NewAnalyzer(Options{Interval: 5 * time.Minute, Metrics: map[string]model.TargetMetric{
		"http_request_duration_seconds_bucket": {
			Name: "http_request_duration_seconds.bucket", Type: "histogram", Temporality: "cumulative",
		},
	}}).Analyze(model.Query{
		RefID: "A", Expression: `histogram_quantile(0.95, sum by (le) (rate(http_request_duration_seconds_bucket[5m])))`,
	})

	assert.Equal(t, model.TranslationBuilder, translation.Kind)
	assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
	require.NotNil(t, translation.Builder)
	assert.Equal(t, "http_request_duration_seconds.bucket", translation.Builder.MetricName)
	assert.Equal(t, "cumulative", translation.Builder.Temporality)
	assert.Contains(t, translation.Decision.Reasons, model.ReasonMetricNameRemap)
	assert.Contains(t, translation.Decision.Reasons, model.ReasonBuilderHistogramPercentile)
	assert.Contains(t, translation.PromQL, `http_request_duration_seconds.bucket`)
}

func TestAnalyzeRejectsClassicHistogramBucketFilters(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{Interval: 5 * time.Minute, Metrics: map[string]model.TargetMetric{
		"request_duration_seconds_bucket": {Type: "histogram", Temporality: "cumulative"},
	}})
	for _, matcher := range []string{`le="+Inf"`, `le!="+Inf"`, `le=~".+"`, `le!~"0\\..+"`} {
		translation := analyzer.Analyze(model.Query{
			RefID: "A", Expression: `histogram_quantile(0.95, sum by (le) (rate(request_duration_seconds_bucket{` + matcher + `}[5m])))`,
		})

		assert.Equal(t, model.TranslationPromQL, translation.Kind, matcher)
		assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict, matcher)
		assert.Nil(t, translation.Builder, matcher)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonHistogramBucketFilter, matcher)
	}
}

func TestAnalyzeRemapsPromQLLabelReferences(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{})
	translation := analyzer.Analyze(model.Query{
		RefID: "A",
		Expression: `sum by (instance) (rate(requests_total{job=~"api"}[5m]))
			/ on(instance) group_left(job) label_replace(service_info, "job", "$1", "instance", "(.+)")`,
	})

	assert.Equal(t, model.TranslationPromQL, translation.Kind)
	assert.Contains(t, translation.PromQL, `by ("service.instance.id")`)
	assert.Contains(t, translation.PromQL, `on ("service.instance.id")`)
	assert.Contains(t, translation.PromQL, `group_left ("service.name")`)
	assert.Contains(t, translation.PromQL, `label_replace(service_info, "service.name", "$1", "service.instance.id", "(.+)")`)
}

func TestAnalyzeRewritesGrafanaGlobals(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{RateInterval: 10 * time.Minute, Interval: 10 * time.Minute})
	translation := analyzer.Analyze(model.Query{
		RefID:      "A",
		Expression: `sum(rate(requests_total{job="$job"}[$__rate_interval]))`,
	})

	// Without live metadata the metric cannot be qualified for a Builder, so it is
	// a faithful passthrough: the rate interval is resolved to a literal, the job
	// label is remapped, and the $job variable is preserved. Resolving a global is
	// no longer a review gate on its own.
	assert.Equal(t, model.TranslationPromQL, translation.Kind)
	assert.Equal(t, model.VerdictPassthrough, translation.Decision.Verdict)
	assert.Nil(t, translation.Builder)
	assert.Contains(t, translation.Decision.Reasons, model.ReasonRateIntervalRewrite)
	assert.Contains(t, translation.PromQL, `[10m]`)
	assert.Contains(t, translation.PromQL, `"service.name"="$job"`)
}

func TestRewriteGrafanaGlobalSuffixesWithoutPrefixCorruption(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{
		RateInterval: 5 * time.Minute,
		Interval:     90 * time.Second,
		Range:        2*time.Hour + 30*time.Minute,
	})
	rewritten, reasons := analyzer.rewriteGlobals(
		`vector($__interval_ms) + vector(${__interval_s}) + vector($__range_ms) + ` +
			`vector(${__range_s}) + vector($__rate_interval_ms) + vector(${__rate_interval_s}) + ` +
			`rate(requests_total[$__interval]) + vector($__interval_ms_extra)`,
	)

	assert.Equal(t,
		`vector(90000) + vector(90) + vector(9000000) + vector(9000) + `+
			`vector(300000) + vector(300) + rate(requests_total[90s]) + vector($__interval_ms_extra)`,
		rewritten,
	)
	assert.Equal(t, []model.ReasonCode{model.ReasonRateIntervalRewrite}, reasons)
}

func TestAnalyzeEmitsInstantQueryAsReviewablePassthrough(t *testing.T) {
	t.Parallel()

	translation := NewAnalyzer(Options{}).Analyze(model.Query{
		RefID: "A", Expression: "up", Instant: true,
	})

	assert.Equal(t, model.TranslationPromQL, translation.Kind)
	assert.Equal(t, "up", translation.PromQL)
	assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
	assert.Contains(t, translation.Decision.Reasons, model.ReasonInstantQuery)
}

func TestAnalyzeUsesLiveMetadataWithoutCollapsingSeries(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{Interval: 5 * time.Minute, Metrics: map[string]model.TargetMetric{
		"node_disk_reads_completed_total": {
			Type: "sum", Temporality: "cumulative", IsMonotonic: true,
			Attributes: []string{"device", "service.instance.id", "service.name"},
		},
	}})
	translation := analyzer.Analyze(model.Query{
		RefID: "A", Expression: `rate(node_disk_reads_completed_total{instance="$node",job="$job"}[5m])`,
	})

	assert.Equal(t, model.TranslationBuilder, translation.Kind)
	assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
	require.NotNil(t, translation.Builder)
	assert.Equal(t, "rate", translation.Builder.TimeAggregation)
	assert.Equal(t, []string{"device", "service.instance.id", "service.name"}, translation.Builder.GroupBy)
	assert.Equal(t, "service.instance.id", translation.Builder.Filters[0].Label)
	assert.Equal(t, "service.name", translation.Builder.Filters[1].Label)
	assert.Contains(t, translation.Decision.Reasons, model.ReasonBuilderRateIncrease)
}

func TestAnalyzeDemotesMetadataFormulaWithTargetOnlyGroupBy(t *testing.T) {
	t.Parallel()

	for _, targetOnly := range targetOnlyPrometheusLabels {
		t.Run(targetOnly, func(t *testing.T) {
			attributes := []string{targetOnly, "service.instance.id", "service.name"}
			analyzer := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{
				"node_memory_MemAvailable_bytes": {Type: "gauge", Attributes: attributes},
				"node_memory_MemTotal_bytes":     {Type: "gauge", Attributes: attributes},
			}})
			translation := analyzer.Analyze(model.Query{
				RefID:      "A",
				Expression: `100 * (1 - node_memory_MemAvailable_bytes{instance="$node",job="$job"} / node_memory_MemTotal_bytes{instance="$node",job="$job"})`,
			})

			assert.Equal(t, model.TranslationPromQL, translation.Kind)
			assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
			assert.Nil(t, translation.Formula)
			assert.Contains(t, translation.PromQL, `on ("service.instance.id", "service.name")`)
			assert.NotContains(t, translation.PromQL, `"`+targetOnly+`"`)
			assert.Contains(t, translation.Decision.Reasons, model.ReasonTargetVectorMatching)
			assert.NotContains(t, translation.Decision.Reasons, model.ReasonFormulaLabelMismatch)
		})
	}
}

func TestAnalyzePreservesSafeFormulaMatchingBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("vector scalar", func(t *testing.T) {
		attributes := []string{"server.address", "server.port", "service.instance.id", "url.scheme"}
		translation := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{
			"node_network_speed_bytes": {Type: "gauge", Attributes: attributes},
		}}).Analyze(model.Query{RefID: "A", Expression: `node_network_speed_bytes * 8`})

		assert.Equal(t, model.TranslationFormula, translation.Kind)
		assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
		require.NotNil(t, translation.Formula)
		require.Len(t, translation.Formula.Queries, 1)
		assert.Equal(t, attributes, translation.Formula.Queries[0].GroupBy)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonBuilderFormulaEvaluation)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonBuilderLatestLookback)
		assert.NotContains(t, translation.Decision.Reasons, model.ReasonTargetVectorMatching)
	})

	t.Run("ungrouped aggregates", func(t *testing.T) {
		translation := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{
			"metric_a": {Type: "gauge", Attributes: []string{"server.address", "service.instance.id"}},
			"metric_b": {Type: "gauge", Attributes: []string{"server.address", "service.instance.id"}},
		}}).Analyze(model.Query{RefID: "A", Expression: `sum(metric_a) / sum(metric_b)`})

		assert.Equal(t, model.TranslationFormula, translation.Kind)
		assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
		require.NotNil(t, translation.Formula)
		require.Len(t, translation.Formula.Queries, 2)
		assert.Empty(t, translation.Formula.Queries[0].GroupBy)
		assert.Empty(t, translation.Formula.Queries[1].GroupBy)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonBuilderFormulaEvaluation)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonBuilderLatestLookback)
	})

	t.Run("logical attributes only", func(t *testing.T) {
		attributes := []string{"device", "service.instance.id"}
		translation := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{
			"metric_a": {Type: "gauge", Attributes: attributes},
			"metric_b": {Type: "gauge", Attributes: attributes},
		}}).Analyze(model.Query{RefID: "A", Expression: `metric_a / metric_b`})

		assert.Equal(t, model.TranslationFormula, translation.Kind)
		assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
		require.NotNil(t, translation.Formula)
		require.Len(t, translation.Formula.Queries, 2)
		assert.Equal(t, attributes, translation.Formula.Queries[0].GroupBy)
		assert.Equal(t, attributes, translation.Formula.Queries[1].GroupBy)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonBuilderFormulaEvaluation)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonBuilderLatestLookback)
	})
}

func TestAnalyzeKeepsDifferentFormulaGroupByAsLabelMismatch(t *testing.T) {
	t.Parallel()

	translation := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{
		"metric_a": {Type: "gauge", Attributes: []string{"server.address", "service.instance.id"}},
		"metric_b": {Type: "gauge", Attributes: []string{"service.instance.id"}},
	}}).Analyze(model.Query{RefID: "A", Expression: `metric_a / metric_b`})

	assert.Equal(t, model.TranslationPromQL, translation.Kind)
	assert.Equal(t, model.VerdictPassthrough, translation.Decision.Verdict)
	assert.Nil(t, translation.Formula)
	assert.Contains(t, translation.Decision.Reasons, model.ReasonFormulaLabelMismatch)
}

func TestAnalyzeUsesAllLogicalMetricLabelsForTargetVectorMatching(t *testing.T) {
	t.Parallel()

	attributes := []string{
		"device", "fstype", "mountpoint", "server.address", "server.port",
		"service.instance.id", "service.name", "url.scheme",
	}
	analyzer := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{
		"node_filesystem_avail_bytes": {Type: "gauge", Attributes: attributes},
		"node_filesystem_size_bytes":  {Type: "gauge", Attributes: attributes},
	}})
	translation := analyzer.Analyze(model.Query{
		Expression: `node_filesystem_avail_bytes{instance="$node",mountpoint="/"} / node_filesystem_size_bytes{instance="$node",mountpoint="/"}`,
	})

	assert.Contains(t, translation.PromQL, `on (device, fstype, mountpoint, "service.instance.id", "service.name")`)
	assert.NotContains(t, translation.PromQL, `"server.port"`)
	assert.Contains(t, translation.Decision.Reasons, model.ReasonTargetVectorMatching)
}

func TestAnalyzeExcludesTargetOnlyLabelsFromWithoutAggregation(t *testing.T) {
	t.Parallel()

	resourceAttributes := []string{"server.address", "service.instance.id", "service.name", "url.scheme"}
	cpuAttributes := append([]string{"cpu", "mode"}, resourceAttributes...)
	analyzer := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{
		"node_load1":             {Type: "gauge", Attributes: resourceAttributes},
		"node_cpu_seconds_total": {Type: "sum", IsMonotonic: true, Temporality: "cumulative", Attributes: cpuAttributes},
	}})
	translation := analyzer.Analyze(model.Query{
		Expression: `node_load1{job="node-exporter"} / count without (cpu, mode) (node_cpu_seconds_total{job="node-exporter",mode="idle"})`,
	})

	assert.Contains(t, translation.PromQL, `on ("service.instance.id", "service.name")`)
	assert.Contains(t, translation.PromQL, `without ("__scope.name__", "__scope.schema_url__", "__scope.version__", __temporality__, cpu, fingerprint, mode, "server.address", "server.port", "url.scheme")`)
	assert.Contains(t, translation.Decision.Reasons, model.ReasonWithoutClause)
	assert.Contains(t, translation.Decision.Reasons, model.ReasonTargetVectorMatching)
}

func TestMetricNamesReturnsEveryUniqueSelector(t *testing.T) {
	t.Parallel()

	names := NewAnalyzer(Options{}).MetricNames(model.Query{
		Expression: `rate(requests_total[5m]) / limits + rate(requests_total[5m])`,
	})

	assert.Equal(t, []string{"limits", "requests_total"}, names)
}

func TestAnalyzeMarksMissingLiveMetricForReview(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{MissingMetrics: map[string]bool{"missing_total": true}})
	translation := analyzer.Analyze(model.Query{RefID: "A", Expression: `rate(missing_total[5m])`})

	assert.Equal(t, model.TranslationPromQL, translation.Kind)
	assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
	assert.Contains(t, translation.Decision.Reasons, model.ReasonMissingMetric)
}

func TestAnalyzeMarksUnavailableMetricMetadataForReview(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{MetadataErrors: map[string]bool{"unavailable_total": true}})
	translation := analyzer.Analyze(model.Query{RefID: "A", Expression: `rate(unavailable_total[5m])`})

	assert.Equal(t, model.TranslationPromQL, translation.Kind)
	assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
	assert.Contains(t, translation.Decision.Reasons, model.ReasonMetricMetadataUnavailable)
	assert.NotContains(t, translation.Decision.Reasons, model.ReasonMissingMetric)
}

func TestAnalyzeCanonicalizesHiddenTargets(t *testing.T) {
	t.Parallel()

	translation := NewAnalyzer(Options{}).Analyze(model.Query{
		RefID:      "A",
		Expression: `rate(node_schedstat_running_seconds_total{instance="$node",job="$job"}[$__rate_interval])`,
		Hidden:     true,
	})

	assert.Equal(t, model.TranslationPromQL, translation.Kind)
	assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
	assert.Contains(t, translation.Decision.Reasons, model.ReasonHiddenTarget)
	assert.Contains(t, translation.PromQL, `[5m]`)
	assert.Contains(t, translation.PromQL, `"service.instance.id"="$node"`)
	assert.Contains(t, translation.PromQL, `"service.name"="$job"`)
}

func TestAnalyzeRejectsDisabledExtendedRangeModifiersAtParseTime(t *testing.T) {
	t.Parallel()

	for _, modifier := range []string{"anchored", "smoothed"} {
		t.Run(modifier, func(t *testing.T) {
			translation := NewAnalyzer(Options{}).Analyze(model.Query{
				RefID: "A", Expression: `rate(m[5m] ` + modifier + `)`,
			})

			assert.Equal(t, model.TranslationNone, translation.Kind)
			assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
			assert.Contains(t, translation.Decision.Reasons, model.ReasonParseError)
			assert.NotContains(t, translation.Decision.Reasons, model.ReasonUnsupportedModifier)
			require.NotEmpty(t, translation.ParseErrors)
			assert.Contains(t, translation.ParseErrors[0].Message, modifier+" modifier is experimental and not enabled")
		})
	}
}

func TestAnalyzeTreatsExtendedModifierKeywordsAsIdentifiers(t *testing.T) {
	t.Parallel()

	for _, keyword := range []string{"anchored", "smoothed"} {
		t.Run(keyword+" metric", func(t *testing.T) {
			translation := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{
				keyword: {Type: "gauge"},
			}}).Analyze(model.Query{RefID: "A", Expression: keyword + `{job="test"}`})

			assert.Equal(t, model.TranslationBuilder, translation.Kind)
			assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
			require.NotNil(t, translation.Builder)
			assert.Equal(t, keyword, translation.Builder.MetricName)
			assert.NotContains(t, translation.Decision.Reasons, model.ReasonParseError)
			assert.NotContains(t, translation.Decision.Reasons, model.ReasonUnsupportedModifier)
			assert.Contains(t, translation.Decision.Reasons, model.ReasonBuilderLatestLookback)
		})

		t.Run(keyword+" grouping", func(t *testing.T) {
			translation := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{
				"some_metric": {Type: "gauge"},
			}}).Analyze(model.Query{
				RefID: "A", Expression: `sum by (` + keyword + `) (some_metric)`,
			})

			assert.Equal(t, model.TranslationBuilder, translation.Kind)
			assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
			require.NotNil(t, translation.Builder)
			assert.Equal(t, []string{keyword}, translation.Builder.GroupBy)
			assert.NotContains(t, translation.Decision.Reasons, model.ReasonParseError)
			assert.NotContains(t, translation.Decision.Reasons, model.ReasonUnsupportedModifier)
			assert.Contains(t, translation.Decision.Reasons, model.ReasonBuilderLatestLookback)
		})
	}
}

func TestAnalyzeRejectsUnsoundNativeMappings(t *testing.T) {
	t.Parallel()

	t.Run("offset modifier", func(t *testing.T) {
		translation := NewAnalyzer(Options{Interval: 5 * time.Minute}).Analyze(model.Query{
			RefID: "A",
			Expression: `sum(rate(requests_total[5m])) - ` +
				`sum(rate(requests_total[5m] offset 30m))`,
		})

		assert.Equal(t, model.TranslationPromQL, translation.Kind)
		assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonUnsupportedModifier)
		assert.Nil(t, translation.Formula)
	})

	t.Run("at modifier", func(t *testing.T) {
		translation := NewAnalyzer(Options{Interval: 5 * time.Minute}).Analyze(model.Query{
			RefID: "A", Expression: `sum(rate(requests_total[5m] @ end()))`,
		})

		assert.Equal(t, model.TranslationPromQL, translation.Kind)
		assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonUnsupportedModifier)
	})

	t.Run("dynamic offset modifier", func(t *testing.T) {
		translation := NewAnalyzer(Options{Interval: 5 * time.Minute}).Analyze(model.Query{
			RefID: "A", Expression: `sum(rate(requests_total[5m] offset step()))`,
		})

		assert.Equal(t, model.TranslationPromQL, translation.Kind)
		assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonUnsupportedModifier)
	})

	t.Run("range without metadata is a faithful passthrough", func(t *testing.T) {
		// Without live metadata the metric cannot be qualified for a Builder, so it
		// ships as verbatim PromQL. The range window is preserved exactly, so this
		// is not a review item and carries no range-step mismatch.
		translation := NewAnalyzer(Options{Interval: time.Minute}).Analyze(model.Query{
			RefID: "A", Expression: `sum(increase(node_context_switches_total[30m]))`,
		})

		assert.Equal(t, model.TranslationPromQL, translation.Kind)
		assert.Equal(t, model.VerdictPassthrough, translation.Decision.Verdict)
		assert.Contains(t, translation.PromQL, "[30m]")
		assert.NotContains(t, translation.Decision.Reasons, model.ReasonRangeStepMismatch)
		assert.Nil(t, translation.Builder)
	})

	t.Run("power operator", func(t *testing.T) {
		translation := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{
			"node_load1": {Type: "gauge"},
		}}).Analyze(model.Query{RefID: "A", Expression: `sum(node_load1) ^ 2`})

		assert.Equal(t, model.TranslationPromQL, translation.Kind)
		assert.Equal(t, model.VerdictPassthrough, translation.Decision.Verdict)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonUnsupportedOperator)
		assert.Nil(t, translation.Formula)
	})

	t.Run("regex end anchor is not a dashboard variable", func(t *testing.T) {
		translation := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{
			"node_cpu_seconds_total": {Type: "sum"},
		}}).Analyze(model.Query{
			RefID: "A", Expression: `sum(node_cpu_seconds_total{mode=~"user$|system"})`,
		})

		require.NotNil(t, translation.Builder)
		assert.Equal(t, `^(?:user$|system)$`, translation.Builder.Filters[0].Value)
	})

	t.Run("regex metric name selector", func(t *testing.T) {
		translation := NewAnalyzer(Options{}).Analyze(model.Query{
			RefID: "A", Expression: `{__name__=~".*_total"}`,
		})

		assert.Equal(t, model.TranslationPromQL, translation.Kind)
		assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
		assert.Contains(t, translation.Decision.Reasons, model.ReasonNonExactMetricSelector)
		assert.Nil(t, translation.Builder)
	})

	for name, expression := range map[string]string{
		"equality and regex":    `{__name__="up",__name__=~"node.*"}`,
		"equality and negative": `{__name__="up",__name__!="down"}`,
		"duplicate equality":    `{__name__="up",__name__="node_load1"}`,
	} {
		t.Run(name+" metric matchers", func(t *testing.T) {
			translation := NewAnalyzer(Options{}).Analyze(model.Query{RefID: "A", Expression: expression})

			assert.Equal(t, model.TranslationPromQL, translation.Kind)
			assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
			assert.Contains(t, translation.Decision.Reasons, model.ReasonNonExactMetricSelector)
			assert.Nil(t, translation.Builder)
		})
	}
}

func TestAnalyzeQuotesRemappedUTF8MetricNameInPassthrough(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{
		Interval: 5 * time.Minute,
		Metrics: map[string]model.TargetMetric{
			"http_request_duration_seconds_bucket": {
				Name: "http_request_duration_seconds.bucket", Type: "gauge", Temporality: "cumulative",
			},
		},
	})
	translation := analyzer.Analyze(model.Query{
		RefID: "A", Expression: `topk(5, rate(http_request_duration_seconds_bucket{job="api"}[5m]))`,
	})

	assert.Equal(t, model.TranslationPromQL, translation.Kind)
	assert.Contains(t, translation.PromQL, `{"http_request_duration_seconds.bucket","service.name"="api"}`)
	assert.NotContains(t, translation.PromQL, `rate(http_request_duration_seconds.bucket`)
	_, err := analyzer.parser.ParseExpr(translation.PromQL)
	require.NoError(t, err)
}

func TestQuoteMetricNamesOnlyRewritesSelectorSyntax(t *testing.T) {
	t.Parallel()

	input := `label_replace(up, "dst", "{__name__=\"foo.bar\"}", "src", "(.*)") + ` +
		`{__name__="foo.bar"} + {__name__="foo.bar",job="api"}`
	result := quoteMetricNames(input, []string{"foo.bar"})

	assert.Contains(t, result, `"{__name__=\"foo.bar\"}"`)
	assert.Contains(t, result, `+ {"foo.bar"} + {"foo.bar",job="api"}`)
}

func TestAnalyzeSetsNativeBuilderStepFromTargetInterval(t *testing.T) {
	t.Parallel()

	translation := NewAnalyzer(Options{Interval: 5 * time.Minute, Metrics: map[string]model.TargetMetric{
		"requests_total": {Type: "sum", Temporality: "cumulative", IsMonotonic: true},
	}}).Analyze(model.Query{
		RefID: "A", Expression: `sum(rate(requests_total[5m]))`,
	})

	require.NotNil(t, translation.Builder)
	assert.Equal(t, 300, translation.Builder.StepSeconds)
}

func TestAnalyzeMaterializesSourceIntervalControlsWithoutNativeClaim(t *testing.T) {
	t.Parallel()

	translation := NewAnalyzer(Options{
		Interval: time.Minute,
		Range:    time.Hour,
		Metrics: map[string]model.TargetMetric{
			"requests_total": {Type: "sum", Temporality: "cumulative", IsMonotonic: true},
		},
	}).Analyze(model.Query{
		RefID: "A", Expression: `sum(increase(requests_total[$__interval]))`,
		Interval: "30m", IntervalFactor: 2, MaxDataPoints: 1,
	})

	// The interval control produces a Builder candidate, but it is never claimed
	// native offline: the verdict stays needs_review and the emitted panel ships
	// the materialized PromQL passthrough until the live promotion gate proves the
	// Builder form equivalent.
	assert.Equal(t, model.TranslationBuilder, translation.Kind)
	assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
	assert.Contains(t, translation.PromQL, `[2h]`)
	assert.Contains(t, translation.Decision.Reasons, model.ReasonRateIntervalRewrite)
	assert.Contains(t, translation.Decision.Reasons, model.ReasonGrafanaIntervalControl)
}

func TestAnalyzeUsesIntervalControlsForBuilderStep(t *testing.T) {
	t.Parallel()

	translation := NewAnalyzer(Options{
		Interval: time.Minute,
		Range:    time.Hour,
		Metrics:  map[string]model.TargetMetric{"up": {Type: "gauge"}},
	}).Analyze(model.Query{
		RefID: "A", Expression: `sum(up)`, MaxDataPoints: 1,
	})

	require.NotNil(t, translation.Builder)
	assert.Equal(t, 3600, translation.Builder.StepSeconds)
	assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
	assert.Contains(t, translation.Decision.Reasons, model.ReasonGrafanaIntervalControl)
}

func TestAnalyzeAccountsForExplicitTargetStepWithoutPrecedenceClaim(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{
		Interval: time.Minute,
		Metrics:  map[string]model.TargetMetric{"up": {Type: "gauge"}},
	})
	for _, test := range []struct {
		name            string
		query           model.Query
		wantBuilderStep int
	}{
		{name: "step only", query: model.Query{RefID: "A", Expression: "sum(up)", Step: 30}, wantBuilderStep: 60},
		{name: "step and interval factor", query: model.Query{RefID: "A", Expression: "sum(up)", Step: 30, IntervalFactor: 2}, wantBuilderStep: 120},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			translation := analyzer.Analyze(test.query)

			require.NotNil(t, translation.Builder)
			assert.Equal(t, test.wantBuilderStep, translation.Builder.StepSeconds)
			assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
			assert.Equal(t, []model.ReasonCode{model.ReasonBuilderLatestLookback, model.ReasonGrafanaIntervalControl}, translation.Decision.Reasons)
		})
	}
}

func TestAnalyzeAccountsForQuerySourceFeaturesAcrossEarlyReturns(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{"up": {Type: "gauge"}}})
	rangeFeature := model.SourceFeature{
		Kind: "query_range", SourcePath: "/panels/0/targets/0/range", Detail: "false",
		Reason: model.ReasonUnmappedQueryConfig,
	}
	exemplarFeature := model.SourceFeature{
		Kind: "query_exemplar", SourcePath: "/panels/0/targets/0/exemplar", Detail: "true",
		Reason: model.ReasonUnmappedQueryConfig,
	}
	for _, test := range []struct {
		name    string
		query   model.Query
		kind    model.TranslationKind
		reasons []model.ReasonCode
	}{
		{
			name: "builder candidate with range and exemplar", kind: model.TranslationBuilder,
			query:   model.Query{RefID: "A", Expression: "sum(up)", SourceFeatures: []model.SourceFeature{rangeFeature, exemplarFeature}},
			reasons: []model.ReasonCode{model.ReasonBuilderLatestLookback, model.ReasonUnmappedQueryConfig},
		},
		{
			name: "range false with instant", kind: model.TranslationPromQL,
			query:   model.Query{RefID: "A", Expression: "sum(up)", Instant: true, SourceFeatures: []model.SourceFeature{rangeFeature}},
			reasons: []model.ReasonCode{model.ReasonInstantQuery, model.ReasonUnmappedQueryConfig},
		},
		{
			name: "empty target", kind: model.TranslationNone,
			query:   model.Query{RefID: "A", SourceFeatures: []model.SourceFeature{exemplarFeature}},
			reasons: []model.ReasonCode{model.ReasonEmptyExpression, model.ReasonUnmappedQueryConfig},
		},
		{
			name: "Grafana expression target", kind: model.TranslationNone,
			query:   model.Query{RefID: "A", Expression: "$B", QueryType: "math", Datasource: model.Datasource{Type: "__expr__"}, SourceFeatures: []model.SourceFeature{rangeFeature}},
			reasons: []model.ReasonCode{model.ReasonGrafanaExpression, model.ReasonUnmappedQueryConfig},
		},
		{
			name: "hidden target", kind: model.TranslationBuilder,
			query:   model.Query{RefID: "A", Expression: "sum(up)", Hidden: true, SourceFeatures: []model.SourceFeature{exemplarFeature}},
			reasons: []model.ReasonCode{model.ReasonBuilderLatestLookback, model.ReasonUnmappedQueryConfig, model.ReasonHiddenTarget},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			translation := analyzer.Analyze(test.query)

			assert.Equal(t, test.kind, translation.Kind)
			assert.Equal(t, model.VerdictNeedsReview, translation.Decision.Verdict)
			assert.ElementsMatch(t, test.reasons, translation.Decision.Reasons)
		})
	}
}

func TestAnalyzeNormalizesLegendPlaceholdersWithLabelMap(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{LabelMap: map[string]string{
		"job": "service.name", "instance": "service.instance.id",
	}})
	translation := analyzer.Analyze(model.Query{
		RefID: "A", Expression: "up", Legend: "{{ job }} on {{instance}} ({{zone}})",
	})

	require.NotNil(t, translation.Legend)
	assert.Equal(t, "{{service.name}} on {{service.instance.id}} ({{zone}})", *translation.Legend)
	auto := analyzer.Analyze(model.Query{RefID: "A", Expression: "up", Legend: "__auto"})
	require.NotNil(t, auto.Legend)
	assert.Empty(t, *auto.Legend)
}

func TestAnalyzeRejectsFormulaWithDifferentGroupByLabels(t *testing.T) {
	t.Parallel()

	translation := NewAnalyzer(Options{Interval: time.Minute, Metrics: map[string]model.TargetMetric{
		"requests_total": {Type: "sum", Temporality: "cumulative", IsMonotonic: true},
		"limits_total":   {Type: "sum", Temporality: "cumulative", IsMonotonic: true},
	}}).Analyze(model.Query{
		RefID: "A", Expression: `sum by (service) (rate(requests_total[1m])) / sum by (instance) (rate(limits_total[1m]))`,
	})

	assert.Equal(t, model.TranslationPromQL, translation.Kind)
	assert.Nil(t, translation.Formula)
	assert.Contains(t, translation.Decision.Reasons, model.ReasonFormulaLabelMismatch)
}

func TestAnalyzeShipsMetadataFreeRateAsFaithfulPassthrough(t *testing.T) {
	t.Parallel()

	// A rate query with no live metadata cannot be qualified for a Builder and
	// ships as verbatim PromQL with its range preserved. Preserving the range
	// means there is no range-step mismatch and no review gate.
	translation := NewAnalyzer(Options{Interval: 30 * time.Second}).Analyze(model.Query{
		RefID: "A", Expression: `sum(rate(requests_total[30s]))`,
	})

	assert.Equal(t, model.TranslationPromQL, translation.Kind)
	assert.Equal(t, model.VerdictPassthrough, translation.Decision.Verdict)
	assert.Contains(t, translation.PromQL, "[30s]")
	assert.NotContains(t, translation.Decision.Reasons, model.ReasonRangeStepMismatch)
}
