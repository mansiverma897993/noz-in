package rules

import (
	"fmt"
	"math"

	"github.com/prometheus/prometheus/promql/parser"
)

func extractThreshold(expression string) (string, string, float64, bool, error) {
	promParser := parser.NewParser(parser.Options{})
	expr, err := promParser.ParseExpr(expression)
	if err != nil {
		return "", "", 0, false, fmt.Errorf("parse alert expression: %w", err)
	}
	binary, ok := unwrap(expr).(*parser.BinaryExpr)
	if !ok || binary.ReturnBool || !binary.Op.IsComparisonOperator() {
		return expr.String(), "", 0, false, nil
	}
	if value, numeric := number(binary.RHS); numeric {
		operator, supported := comparisonOperator(binary.Op)
		if supported && binary.LHS.Type() != parser.ValueTypeScalar {
			return binary.LHS.String(), operator, value, true, nil
		}
	}
	if value, numeric := number(binary.LHS); numeric {
		operator, supported := reversedComparisonOperator(binary.Op)
		if supported && binary.RHS.Type() != parser.ValueTypeScalar {
			return binary.RHS.String(), operator, value, true, nil
		}
	}
	return expr.String(), "", 0, false, nil
}

func unwrap(expr parser.Expr) parser.Expr {
	for {
		parentheses, ok := expr.(*parser.ParenExpr)
		if !ok {
			return expr
		}
		expr = parentheses.Expr
	}
}

func number(expr parser.Expr) (float64, bool) {
	switch typed := unwrap(expr).(type) {
	case *parser.NumberLiteral:
		return typed.Val, !math.IsNaN(typed.Val) && !math.IsInf(typed.Val, 0)
	case *parser.UnaryExpr:
		value, ok := number(typed.Expr)
		if !ok {
			return 0, false
		}
		if typed.Op == parser.SUB {
			value = -value
		}
		return value, true
	default:
		return 0, false
	}
}

func comparisonOperator(operator parser.ItemType) (string, bool) {
	switch operator {
	case parser.GTR:
		return "above", true
	case parser.LSS:
		return "below", true
	case parser.GTE:
		return "above_or_equal", true
	case parser.LTE:
		return "below_or_equal", true
	case parser.EQLC:
		return "equal", true
	case parser.NEQ:
		return "not_equal", true
	default:
		return "", false
	}
}

func reversedComparisonOperator(operator parser.ItemType) (string, bool) {
	switch operator {
	case parser.GTR:
		return "below", true
	case parser.LSS:
		return "above", true
	case parser.GTE:
		return "below_or_equal", true
	case parser.LTE:
		return "above_or_equal", true
	case parser.EQLC:
		return "equal", true
	case parser.NEQ:
		return "not_equal", true
	default:
		return "", false
	}
}
