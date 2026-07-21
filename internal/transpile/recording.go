package transpile

import (
	"maps"
	"strings"

	"github.com/prometheus/prometheus/promql/parser"
)

func (analyzer *Analyzer) inlineRecordingRules(expression parser.Expr) (parser.Expr, bool) {
	if len(analyzer.options.RecordingRules) == 0 {
		return expression, false
	}
	original := expression.String()
	replaced, changed, unsafe := analyzer.replaceRecordingRules(expression, nil)
	if !unsafe {
		return replaced, changed
	}
	restored, err := analyzer.parser.ParseExpr(original)
	if err != nil {
		return expression, false
	}
	return restored, false
}

func (analyzer *Analyzer) replaceRecordingRules(expression parser.Expr, stack map[string]bool) (parser.Expr, bool, bool) {
	switch typed := expression.(type) {
	case *parser.VectorSelector:
		name := metricName(typed)
		rule, found := analyzer.options.RecordingRules[name]
		if !found {
			return expression, false, false
		}
		if recordingSelectorHasConstraints(typed) || len(rule.Labels) > 0 || stack[name] {
			return expression, false, true
		}
		replacement, err := analyzer.parser.ParseExpr(strings.TrimSpace(rule.Expression))
		if err != nil {
			return expression, false, true
		}
		nextStack := maps.Clone(stack)
		if nextStack == nil {
			nextStack = make(map[string]bool)
		}
		nextStack[name] = true
		replacement, _, unsafe := analyzer.replaceRecordingRules(replacement, nextStack)
		return replacement, true, unsafe
	case *parser.MatrixSelector:
		selector, ok := typed.VectorSelector.(*parser.VectorSelector)
		if ok {
			_, found := analyzer.options.RecordingRules[metricName(selector)]
			if found {
				return expression, false, true
			}
		}
		return expression, false, false
	case *parser.AggregateExpr:
		replaced, changed, unsafe := analyzer.replaceRecordingRules(typed.Expr, stack)
		typed.Expr = replaced
		if typed.Param != nil {
			param, paramChanged, paramUnsafe := analyzer.replaceRecordingRules(typed.Param, stack)
			typed.Param = param
			changed = changed || paramChanged
			unsafe = unsafe || paramUnsafe
		}
		return typed, changed, unsafe
	case *parser.BinaryExpr:
		left, leftChanged, leftUnsafe := analyzer.replaceRecordingRules(typed.LHS, stack)
		right, rightChanged, rightUnsafe := analyzer.replaceRecordingRules(typed.RHS, stack)
		typed.LHS = left
		typed.RHS = right
		return typed, leftChanged || rightChanged, leftUnsafe || rightUnsafe
	case *parser.Call:
		changed := false
		unsafe := false
		for index := range typed.Args {
			replaced, argumentChanged, argumentUnsafe := analyzer.replaceRecordingRules(typed.Args[index], stack)
			typed.Args[index] = replaced
			changed = changed || argumentChanged
			unsafe = unsafe || argumentUnsafe
		}
		return typed, changed, unsafe
	case *parser.ParenExpr:
		replaced, changed, unsafe := analyzer.replaceRecordingRules(typed.Expr, stack)
		typed.Expr = replaced
		return typed, changed, unsafe
	case *parser.UnaryExpr:
		replaced, changed, unsafe := analyzer.replaceRecordingRules(typed.Expr, stack)
		typed.Expr = replaced
		return typed, changed, unsafe
	case *parser.SubqueryExpr:
		replaced, changed, unsafe := analyzer.replaceRecordingRules(typed.Expr, stack)
		typed.Expr = replaced
		return typed, changed, unsafe
	case *parser.StepInvariantExpr:
		replaced, changed, unsafe := analyzer.replaceRecordingRules(typed.Expr, stack)
		typed.Expr = replaced
		return typed, changed, unsafe
	default:
		return expression, false, false
	}
}

func recordingSelectorHasConstraints(selector *parser.VectorSelector) bool {
	if selector.OriginalOffset != 0 || selector.OriginalOffsetExpr != nil || selector.Timestamp != nil || selector.StartOrEnd != 0 {
		return true
	}
	for _, matcher := range selector.LabelMatchers {
		if matcher.Name != "__name__" {
			return true
		}
	}
	return false
}
