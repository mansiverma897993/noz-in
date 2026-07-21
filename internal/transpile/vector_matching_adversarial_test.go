package transpile

import (
	"fmt"
	"maps"
	"testing"

	"github.com/mansiverma897993/signoz/internal/model"
	"github.com/prometheus/prometheus/promql/parser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnalyzeExpandsExplicitIgnoringWithEveryReceiverOnlyLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		expression  string
		cardinality parser.VectorMatchCardinality
		mappedLabel string
	}{
		{name: "one to one", expression: `metric_a / ignoring(job) metric_b`, cardinality: parser.CardOneToOne, mappedLabel: "service.name"},
		{name: "empty ignoring group left", expression: `metric_a / ignoring() group_left(extra) metric_b`, cardinality: parser.CardManyToOne},
		{name: "many to many set", expression: `metric_a and ignoring(zone) metric_b`, cardinality: parser.CardManyToMany, mappedLabel: "zone"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			translation := receiverMatchingAnalyzer(nil).Analyze(model.Query{Expression: test.expression})

			require.Equal(t, model.TranslationPromQL, translation.Kind)
			root := rootBinary(t, translation.PromQL)
			require.NotNil(t, root.VectorMatching)
			assert.False(t, root.VectorMatching.On)
			assert.Equal(t, test.cardinality, root.VectorMatching.Card)
			for _, label := range targetOnlyPrometheusLabels {
				assert.Contains(t, root.VectorMatching.MatchingLabels, label)
			}
			if test.mappedLabel != "" {
				assert.Contains(t, root.VectorMatching.MatchingLabels, test.mappedLabel)
			}
			assert.Contains(t, translation.Decision.Reasons, model.ReasonTargetVectorMatching)
		})
	}
}

func TestAnalyzeInjectsManyToManyMatchingForImplicitSetOperators(t *testing.T) {
	t.Parallel()

	for _, operator := range []string{"and", "or", "unless"} {
		t.Run(operator, func(t *testing.T) {
			t.Parallel()
			translation := receiverMatchingAnalyzer(nil).Analyze(model.Query{
				Expression: fmt.Sprintf("metric_a %s metric_b", operator),
			})

			require.Equal(t, model.TranslationPromQL, translation.Kind)
			root := rootBinary(t, translation.PromQL)
			require.NotNil(t, root.VectorMatching)
			assert.True(t, root.VectorMatching.On)
			assert.Equal(t, parser.CardManyToMany, root.VectorMatching.Card)
			assert.Equal(t, []string{"service.name", "zone"}, root.VectorMatching.MatchingLabels)
			assert.Contains(t, translation.Decision.Reasons, model.ReasonTargetVectorMatching)
		})
	}
}

func TestAnalyzeUsesExactSetOperatorOutputLabelsForOuterMatching(t *testing.T) {
	t.Parallel()

	tests := []struct {
		operator      string
		outerMatching []string
	}{
		{operator: "and", outerMatching: []string{"left"}},
		{operator: "unless", outerMatching: []string{"left"}},
		{operator: "or", outerMatching: []string{"left", "right"}},
	}
	for _, test := range tests {
		t.Run(test.operator, func(t *testing.T) {
			t.Parallel()
			analyzer := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{
				"left_metric":  gaugeMetric("left", "server.address"),
				"right_metric": gaugeMetric("right", "server.address"),
				"outer_metric": gaugeMetric(append(test.outerMatching, "server.address")...),
			}})
			translation := analyzer.Analyze(model.Query{
				Expression: fmt.Sprintf("(left_metric %s right_metric) / outer_metric", test.operator),
			})

			require.Equal(t, model.TranslationPromQL, translation.Kind)
			root := rootBinary(t, translation.PromQL)
			assert.Equal(t, test.outerMatching, root.VectorMatching.MatchingLabels)
			inner := binaryOperand(t, root.LHS)
			assert.Equal(t, parser.CardManyToMany, inner.VectorMatching.Card)
			assert.Equal(t, []string{"left", "right"}, inner.VectorMatching.MatchingLabels)
		})
	}
}

func TestAnalyzeNormalizesMetadataLabelsAndExcludesFullTargetOnlyInventory(t *testing.T) {
	t.Parallel()

	attributes := []string{"__name__", "instance", "job", "zone"}
	attributes = append(attributes, targetOnlyPrometheusLabels...)
	translation := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{
		"metric_a": gaugeMetric(attributes...),
		"metric_b": gaugeMetric(attributes...),
	}}).Analyze(model.Query{Expression: `metric_a / metric_b`})

	root := rootBinary(t, translation.PromQL)
	assert.Equal(t, []string{"service.instance.id", "service.name", "zone"}, root.VectorMatching.MatchingLabels)
	for _, label := range targetOnlyPrometheusLabels {
		assert.NotContains(t, root.VectorMatching.MatchingLabels, label)
	}

	custom := NewAnalyzer(Options{
		LabelMap: map[string]string{"pod": "k8s.pod.name"},
		Metrics: map[string]model.TargetMetric{
			"metric_a": gaugeMetric("pod", "server.address"),
			"metric_b": gaugeMetric("pod", "server.address"),
		},
	}).Analyze(model.Query{Expression: `metric_a / metric_b`})
	assert.Equal(t, []string{"k8s.pod.name"}, rootBinary(t, custom.PromQL).VectorMatching.MatchingLabels)
}

func TestAnalyzePlansMatchingBeforeMetricNameRemap(t *testing.T) {
	t.Parallel()

	translation := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{
		"source_a": {Name: "target.a", Type: "gauge", Attributes: []string{"job", "server.address"}},
		"source_b": {Name: "target.b", Type: "gauge", Attributes: []string{"job", "server.address"}},
	}}).Analyze(model.Query{Expression: `source_a / source_b`})

	require.Equal(t, model.TranslationPromQL, translation.Kind)
	assert.Contains(t, translation.PromQL, `{"target.a"}`)
	assert.Contains(t, translation.PromQL, `{"target.b"}`)
	assert.Equal(t, []string{"service.name"}, rootBinary(t, translation.PromQL).VectorMatching.MatchingLabels)
	assert.Contains(t, translation.Decision.Reasons, model.ReasonMetricNameRemap)
	assert.Contains(t, translation.Decision.Reasons, model.ReasonTargetVectorMatching)
}

func TestAnalyzeProvesCommonLabelPreservingFunctions(t *testing.T) {
	t.Parallel()

	tests := []string{
		`avg_over_time(metric_a[5m])`,
		`changes(metric_a[5m])`,
		`count_over_time(metric_a[5m])`,
		`last_over_time(metric_a[5m])`,
		`max_over_time(metric_a[5m])`,
		`min_over_time(metric_a[5m])`,
		`present_over_time(metric_a[5m])`,
		`quantile_over_time(0.9, metric_a[5m])`,
		`stddev_over_time(metric_a[5m])`,
		`stdvar_over_time(metric_a[5m])`,
		`sum_over_time(metric_a[5m])`,
	}
	for _, left := range tests {
		t.Run(left, func(t *testing.T) {
			t.Parallel()
			right := replaceMetricName(left, "metric_a", "metric_b")
			translation := receiverMatchingAnalyzer(nil).Analyze(model.Query{Expression: left + " / " + right})

			require.NotEqual(t, model.TranslationNone, translation.Kind)
			assert.Equal(t, []string{"service.name", "zone"}, rootBinary(t, translation.PromQL).VectorMatching.MatchingLabels)
			assert.Contains(t, translation.Decision.Reasons, model.ReasonTargetVectorMatching)
		})
	}
}

func TestAnalyzeFailsClosedWhenRequiredMatchingShapeIsUnknown(t *testing.T) {
	t.Parallel()

	tests := []string{
		`label_replace(metric_a, "copy", "$1", "service.name", "(.*)") / metric_b`,
		`absent_over_time(metric_a[5m]) / metric_b`,
		`histogram_quantile(0.9, metric_a) / metric_b`,
		`(label_replace(metric_a, "copy", "$1", "job", "(.*)") + on(job) group_left(extra) metric_b) / metric_c`,
	}
	for _, expression := range tests {
		t.Run(expression, func(t *testing.T) {
			t.Parallel()
			translation := receiverMatchingAnalyzer(map[string]model.TargetMetric{
				"metric_c": gaugeMetric("copy", "extra", "service.name", "server.address", "zone"),
			}).Analyze(model.Query{Expression: expression})

			assert.Equal(t, model.TranslationNone, translation.Kind)
			assert.Empty(t, translation.PromQL)
			if expression == `histogram_quantile(0.9, metric_a) / metric_b` {
				assert.Contains(t, translation.Decision.Reasons, model.ReasonTargetOnlyLabelSemanticUse)
			} else {
				assert.Contains(t, translation.Decision.Reasons, model.ReasonTargetVectorMatchingUnresolved)
			}
			assert.NotContains(t, translation.Decision.Reasons, model.ReasonTargetVectorMatching)
		})
	}
}

func TestAnalyzeFailsUnknownShapesClosedForUniversalFingerprint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		expression string
		metrics    map[string]model.TargetMetric
		errors     map[string]bool
		wantSafe   bool
	}{
		{
			name:       "metadata endpoint omits universal fingerprint",
			expression: `label_replace(metric_a, "copy", "$1", "service.name", "(.*)") / metric_b`,
			metrics: map[string]model.TargetMetric{
				"metric_a": gaugeMetric("service.name", "zone"),
				"metric_b": gaugeMetric("service.name", "zone"),
			},
		},
		{
			name:       "both unknown call shapes retain universal fingerprint",
			expression: `label_replace(metric_a, "copy", "$1", "service.name", "(.*)") / label_replace(metric_b, "copy", "$1", "service.name", "(.*)")`,
			metrics: map[string]model.TargetMetric{
				"metric_a": gaugeMetric("service.name", "zone"),
				"metric_b": gaugeMetric("service.name", "zone"),
			},
		},
		{
			name:       "offline metadata absence still retains universal fingerprint",
			expression: `label_replace(metric_a, "copy", "$1", "service.name", "(.*)") / label_replace(metric_b, "copy", "$1", "service.name", "(.*)")`,
		},
		{
			name:       "name-only metric remaps do not imply known empty labels",
			expression: `metric_a / metric_b`,
			metrics: map[string]model.TargetMetric{
				"metric_a": {Name: "target.a"},
				"metric_b": {Name: "target.b"},
			},
		},
		{
			name:       "partial metadata errors retain unknown label shapes",
			expression: `metric_a / metric_b`,
			metrics: map[string]model.TargetMetric{
				"metric_a": {Name: "target.a"},
				"metric_b": {Name: "target.b"},
			},
			errors: map[string]bool{"metric_a": true, "metric_b": true},
		},
		{
			name:       "aggregation removes receiver only labels",
			expression: `label_replace(sum(metric_a), "copy", "$1", "service.name", "(.*)") / sum(metric_b)`,
			metrics: map[string]model.TargetMetric{
				"metric_a": gaugeMetric("service.name", "server.address"),
				"metric_b": gaugeMetric("service.name", "server.address"),
			},
			wantSafe: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			translation := NewAnalyzer(Options{Metrics: test.metrics, MetadataErrors: test.errors}).Analyze(model.Query{Expression: test.expression})
			if test.wantSafe {
				assert.Equal(t, model.TranslationPromQL, translation.Kind)
				assert.NotContains(t, translation.Decision.Reasons, model.ReasonTargetVectorMatchingUnresolved)
				return
			}
			assert.Equal(t, model.TranslationNone, translation.Kind)
			assert.Contains(t, translation.Decision.Reasons, model.ReasonTargetVectorMatchingUnresolved)
		})
	}
}

func TestAnalyzeComputesSelectionAndCountValuesOutputLabels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		expression string
		left       model.TargetMetric
		right      model.TargetMetric
		expected   []string
	}{
		{name: "topk", expression: `topk(1, metric_a) / metric_b`, left: gaugeMetric("service.name", "server.address", "zone"), right: gaugeMetric("service.name", "server.address", "zone"), expected: []string{"service.name", "zone"}},
		{name: "bottomk", expression: `bottomk(1, metric_a) / metric_b`, left: gaugeMetric("service.name", "server.address", "zone"), right: gaugeMetric("service.name", "server.address", "zone"), expected: []string{"service.name", "zone"}},
		{name: "count values", expression: `count_values("bucket", metric_a) / metric_b`, left: gaugeMetric("service.name", "server.address", "zone"), right: gaugeMetric("bucket", "server.address"), expected: []string{"bucket"}},
		{name: "count values by", expression: `count_values by (job) ("bucket", metric_a) / metric_b`, left: gaugeMetric("service.name", "server.address", "zone"), right: gaugeMetric("bucket", "service.name", "server.address"), expected: []string{"bucket", "service.name"}},
		{name: "count values without", expression: `count_values without (zone) ("bucket", metric_a) / metric_b`, left: gaugeMetric("service.name", "server.address", "zone"), right: gaugeMetric("bucket", "service.name", "server.address"), expected: []string{"bucket", "service.name"}},
		{name: "mapped count values parameter", expression: `count_values("job", metric_a) / metric_b`, left: gaugeMetric("service.name", "server.address"), right: gaugeMetric("service.name", "server.address"), expected: []string{"service.name"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			translation := NewAnalyzer(Options{Metrics: map[string]model.TargetMetric{
				"metric_a": test.left,
				"metric_b": test.right,
			}}).Analyze(model.Query{Expression: test.expression})

			require.NotEqual(t, model.TranslationNone, translation.Kind)
			assert.Equal(t, test.expected, rootBinary(t, translation.PromQL).VectorMatching.MatchingLabels)
			assert.Contains(t, translation.Decision.Reasons, model.ReasonTargetVectorMatching)
		})
	}
}

func TestLogicalOutputLabelsPreserveExperimentalSelectionAggregators(t *testing.T) {
	t.Parallel()

	for _, expression := range []string{
		`limitk(1, metric_a) / metric_b`,
		`limit_ratio(0.5, metric_a) / metric_b`,
	} {
		t.Run(expression, func(t *testing.T) {
			t.Parallel()
			expr, err := parser.NewParser(parser.Options{EnableExperimentalFunctions: true}).ParseExpr(expression)
			require.NoError(t, err)
			analyzer := receiverMatchingAnalyzer(nil)
			analyzer.remapPassthroughLabels(expr)
			shape := analyzer.logicalOutputShape(expr)
			assert.True(t, shape.known)
			assert.False(t, shape.unsafe)
			require.True(t, analyzer.injectTargetVectorMatching(expr))
			assert.Equal(t, []string{"service.name", "zone"}, binaryOperand(t, expr).VectorMatching.MatchingLabels)
		})
	}
}

func TestAnalyzeComputesNestedExplicitGroupMatchingOutputLabels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		expression string
		metrics    map[string]model.TargetMetric
		expected   []string
	}{
		{
			name:       "group left adds labels from one side",
			expression: `(metric_a + on(job) group_left(extra) metric_b) / metric_c`,
			metrics: map[string]model.TargetMetric{
				"metric_a": gaugeMetric("left", "service.name", "server.address"),
				"metric_b": gaugeMetric("extra", "service.name", "server.address"),
				"metric_c": gaugeMetric("extra", "left", "service.name", "server.address"),
			},
			expected: []string{"extra", "left", "service.name"},
		},
		{
			name:       "group right uses right side as result base",
			expression: `(metric_a + on(job) group_right(extra) metric_b) / metric_c`,
			metrics: map[string]model.TargetMetric{
				"metric_a": gaugeMetric("extra", "service.name", "server.address"),
				"metric_b": gaugeMetric("right", "service.name", "server.address"),
				"metric_c": gaugeMetric("extra", "right", "service.name", "server.address"),
			},
			expected: []string{"extra", "right", "service.name"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			query := model.Query{Expression: test.expression}
			translation := NewAnalyzer(Options{Metrics: test.metrics}).Analyze(query)

			require.Equal(t, model.TranslationPromQL, translation.Kind)
			assert.Equal(t, test.expected, rootBinary(t, translation.PromQL).VectorMatching.MatchingLabels)
			assert.Equal(t, test.expression, query.Expression, "the source expression must remain immutable")
		})
	}
}

func receiverMatchingAnalyzer(extra map[string]model.TargetMetric) *Analyzer {
	metrics := map[string]model.TargetMetric{
		"metric_a": gaugeMetric("service.name", "server.address", "zone"),
		"metric_b": gaugeMetric("service.name", "server.address", "zone"),
	}
	maps.Copy(metrics, extra)
	return NewAnalyzer(Options{Metrics: metrics})
}

func gaugeMetric(attributes ...string) model.TargetMetric {
	return model.TargetMetric{Type: "gauge", Attributes: attributes}
}

func replaceMetricName(expression, source, target string) string {
	for index := 0; index+len(source) <= len(expression); index++ {
		if expression[index:index+len(source)] == source {
			return expression[:index] + target + expression[index+len(source):]
		}
	}
	return expression
}

func rootBinary(t *testing.T, expression string) *parser.BinaryExpr {
	t.Helper()
	expr, err := parser.NewParser(parser.Options{}).ParseExpr(expression)
	require.NoError(t, err)
	return binaryOperand(t, expr)
}

func binaryOperand(t *testing.T, expression parser.Expr) *parser.BinaryExpr {
	t.Helper()
	for {
		switch typed := expression.(type) {
		case *parser.ParenExpr:
			expression = typed.Expr
		case *parser.StepInvariantExpr:
			expression = typed.Expr
		default:
			binary, ok := expression.(*parser.BinaryExpr)
			require.True(t, ok, "expected binary expression, got %T", expression)
			return binary
		}
	}
}
