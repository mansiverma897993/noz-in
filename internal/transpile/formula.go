package transpile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"

	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/prometheus/prometheus/promql/parser"
)

type formulaBuilder struct {
	prefix  string
	queries []model.BuilderQuery
	metrics map[string]model.TargetMetric
}

func (analyzer *Analyzer) buildFormula(expr parser.Expr, query model.Query) (model.Formula, bool, bool) {
	if _, ok := unwrap(expr).(*parser.BinaryExpr); !ok {
		return model.Formula{}, false, false
	}
	builder := formulaBuilder{prefix: formulaDependencyPrefix(query), metrics: analyzer.options.Metrics}
	expression, ok := builder.render(expr)
	if !ok || len(builder.queries) == 0 {
		return model.Formula{}, false, false
	}
	if !formulaLabelSetsEqual(builder.queries) {
		return model.Formula{}, false, true
	}
	return model.Formula{
		Name:       queryName(query.RefID),
		Expression: expression,
		Queries:    builder.queries,
	}, true, false
}

func formulaLabelSetsEqual(queries []model.BuilderQuery) bool {
	if len(queries) < 2 {
		return true
	}
	expected := append([]string(nil), queries[0].GroupBy...)
	slices.Sort(expected)
	for _, query := range queries[1:] {
		actual := append([]string(nil), query.GroupBy...)
		slices.Sort(actual)
		if !slices.Equal(expected, actual) {
			return false
		}
	}
	return true
}

func formulaHasTargetOnlyVectorMatchingRisk(formula model.Formula) bool {
	if len(formula.Queries) < 2 {
		return false
	}
	for _, query := range formula.Queries {
		if slices.ContainsFunc(query.GroupBy, isTargetOnlyPrometheusLabel) {
			return true
		}
	}
	return false
}

func formulaDependencyPrefix(query model.Query) string {
	identity := query.SourcePath
	if identity == "" {
		identity = query.RefID + "\x00" + query.Expression
	}
	digest := sha256.Sum256([]byte(identity))
	return "SM_" + hex.EncodeToString(digest[:8])
}

func (builder *formulaBuilder) render(expr parser.Expr) (string, bool) {
	switch typed := unwrap(expr).(type) {
	case *parser.BinaryExpr:
		if typed.ReturnBool || !formulaOperator(typed.Op) || hasExplicitVectorMatching(typed) {
			return "", false
		}
		left, ok := builder.render(typed.LHS)
		if !ok {
			return "", false
		}
		right, ok := builder.render(typed.RHS)
		if !ok {
			return "", false
		}
		return fmt.Sprintf("(%s %s %s)", left, typed.Op.String(), right), true
	case *parser.NumberLiteral:
		return strconv.FormatFloat(typed.Val, 'g', -1, 64), true
	case *parser.UnaryExpr:
		if typed.Op != parser.ADD && typed.Op != parser.SUB {
			return "", false
		}
		value, ok := builder.render(typed.Expr)
		if !ok {
			return "", false
		}
		return typed.Op.String() + value, true
	default:
		return builder.addQuery(expr)
	}
}

func hasExplicitVectorMatching(expression *parser.BinaryExpr) bool {
	matching := expression.VectorMatching
	return matching != nil && (matching.On || len(matching.MatchingLabels) > 0 || len(matching.Include) > 0 || matching.Card != parser.CardOneToOne)
}

func (builder *formulaBuilder) addQuery(expr parser.Expr) (string, bool) {
	name := fmt.Sprintf("%s_%d", builder.prefix, len(builder.queries)+1)
	query, ok := buildHistogram(expr, name)
	if !ok {
		query, ok = buildAggregate(expr, name)
	}
	if !ok {
		metricName, metricOK := metadataMetricName(expr)
		metadata, found := builder.metrics[metricName]
		if metricOK && found {
			query, ok = buildMetadataQuery(expr, name, metadata)
		}
	}
	if !ok {
		return "", false
	}
	builder.queries = append(builder.queries, query)
	return name, true
}

func formulaOperator(operator parser.ItemType) bool {
	switch operator {
	case parser.ADD, parser.SUB, parser.MUL, parser.DIV, parser.MOD:
		return true
	default:
		return false
	}
}
