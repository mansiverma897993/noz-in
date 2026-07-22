package transpile

// This file walks the PromQL AST to collect risk reasons and review flags,
// and converts parser failures into structured parse errors.

import (
	"errors"
	"strings"
	"time"

	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
)

func parseErrors(err error) []model.ParseError {
	var parserErrors parser.ParseErrors
	if !errors.As(err, &parserErrors) {
		return []model.ParseError{{Message: err.Error()}}
	}
	errorsOut := make([]model.ParseError, 0, len(parserErrors))
	for _, parseError := range parserErrors {
		errorsOut = append(errorsOut, model.ParseError{
			Message: parseError.Err.Error(),
			Start:   int(parseError.PositionRange.Start),
			End:     int(parseError.PositionRange.End),
		})
	}
	return errorsOut
}

func inspectRisks(expr parser.Expr, step time.Duration) ([]model.ReasonCode, bool) {
	inspector := riskInspector{step: step}
	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		inspector.inspect(node)
		return nil
	})
	return uniqueReasons(inspector.reasons), inspector.review
}

type riskInspector struct {
	step    time.Duration
	reasons []model.ReasonCode
	review  bool
}

func (inspector *riskInspector) inspect(node parser.Node) {
	switch typed := node.(type) {
	case *parser.SubqueryExpr:
		inspector.reasons = append(inspector.reasons, model.ReasonSubquery)
	case *parser.AggregateExpr:
		inspector.inspectAggregate(typed)
	case *parser.BinaryExpr:
		inspector.inspectBinary(typed)
	case *parser.MatrixSelector:
		inspector.inspectMatrix(typed)
	case *parser.VectorSelector:
		inspector.inspectVector(typed)
	case *parser.Call:
		inspector.inspectCall(typed)
	}
}

func (inspector *riskInspector) inspectAggregate(expression *parser.AggregateExpr) {
	if expression.Without {
		inspector.reasons = append(inspector.reasons, model.ReasonWithoutClause)
	}
	if expression.Op == parser.TOPK || expression.Op == parser.BOTTOMK {
		inspector.reasons = append(inspector.reasons, model.ReasonTopKSemantics)
	}
}

func (inspector *riskInspector) inspectBinary(expression *parser.BinaryExpr) {
	matching := expression.VectorMatching
	if matching != nil && (matching.On || len(matching.MatchingLabels) > 0 || len(matching.Include) > 0 ||
		(!expression.Op.IsSetOperator() && matching.Card != parser.CardOneToOne)) {
		inspector.reasons = append(inspector.reasons, model.ReasonVectorMatching)
	}
	if expression.Op == parser.POW {
		inspector.reasons = append(inspector.reasons, model.ReasonUnsupportedOperator)
	}
}

func (inspector *riskInspector) inspectMatrix(_ *parser.MatrixSelector) {
	// A range window is not itself a risk. When the query is shipped as verbatim
	// PromQL the range is preserved exactly, so range-vs-step is a non-issue. When
	// the query is emitted as a Builder, the builder step is aligned to the source
	// range (setBuilderStep) so the two are numerically equivalent; any residual
	// point-resolution difference is recorded as an informational reason at
	// builder emission (builderCandidateSemanticReasons), never a review gate.
}

func (inspector *riskInspector) inspectVector(expression *parser.VectorSelector) {
	if strings.Contains(metricName(expression), ":") {
		inspector.reasons = append(inspector.reasons, model.ReasonRecordingRuleMetric)
		inspector.review = true
	}
	if expression.OriginalOffset != 0 || expression.OriginalOffsetExpr != nil || expression.Timestamp != nil || expression.StartOrEnd != 0 {
		inspector.reasons = append(inspector.reasons, model.ReasonUnsupportedModifier)
		inspector.review = true
	}
	if metricName(expression) == "" {
		inspector.reasons = append(inspector.reasons, model.ReasonNonExactMetricSelector)
		inspector.review = true
	}
	for _, matcher := range expression.LabelMatchers {
		if (matcher.Type == labels.MatchRegexp || matcher.Type == labels.MatchNotRegexp) && variablePattern.MatchString(matcher.Value) {
			inspector.reasons = append(inspector.reasons, model.ReasonRegexVariable)
			inspector.review = true
		}
	}
}

func (inspector *riskInspector) inspectCall(expression *parser.Call) {
	if expression.Func.Name != "histogram_quantile" || len(expression.Args) != 2 {
		return
	}
	quantile, ok := unwrap(expression.Args[0]).(*parser.NumberLiteral)
	if !ok {
		inspector.reasons = append(inspector.reasons, model.ReasonNonstandardQuantile)
		return
	}
	if _, supported := percentileName(quantile.Val); !supported {
		inspector.reasons = append(inspector.reasons, model.ReasonNonstandardQuantile)
	}
}
