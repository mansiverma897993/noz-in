package app

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/mansiverma897993/noz-in/internal/diff"
	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/mansiverma897993/noz-in/internal/target/signoz"
	"github.com/mansiverma897993/noz-in/internal/transpile"
	promlabels "github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
)

func variableNameSet(expressions ...string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, expression := range expressions {
		for _, name := range transpile.VariableNames(expression) {
			result[name] = struct{}{}
		}
	}
	return result
}

func targetVariableNamesForQuery(widget signoz.Widget, identity emittedQueryIdentity) map[string]struct{} {
	result := variableNameSet(identity.TargetExpression)
	if identity.TargetKind != string(diff.TargetKindBuilderFormula) {
		return result
	}

	queries := make(map[string]signoz.BuilderQueryData, len(widget.Query.Builder.QueryData))
	for _, query := range widget.Query.Builder.QueryData {
		queries[query.QueryName] = query
	}
	formulas := make(map[string]signoz.BuilderFormula, len(widget.Query.Builder.QueryFormulas))
	for _, formula := range widget.Query.Builder.QueryFormulas {
		formulas[formula.QueryName] = formula
	}
	visited := make(map[string]struct{})
	var collectFormula func(string)
	collectFormula = func(name string) {
		if _, seen := visited[name]; seen {
			return
		}
		visited[name] = struct{}{}
		formula, found := formulas[name]
		if !found {
			return
		}
		for variable := range variableNameSet(formula.Expression) {
			result[variable] = struct{}{}
		}
		for _, token := range formulaVariablePattern.FindAllString(formula.Expression, -1) {
			dependency, _, _ := strings.Cut(token, ".")
			if query, found := queries[dependency]; found {
				for variable := range variableNameSet(
					query.Expression,
					query.Filter.Expression,
					query.Having.Expression,
				) {
					result[variable] = struct{}{}
				}
			}
			if _, found := formulas[dependency]; found {
				collectFormula(dependency)
			}
		}
	}
	collectFormula(identity.TargetQueryName)
	return result
}

func differentialAliasHasExactLabelProvenance(
	runtime differentialRuntime,
	query model.Query,
	widget signoz.Widget,
	identity emittedQueryIdentity,
	variableName string,
	labelName string,
	window DifferentialWindow,
) bool {
	sourceValue, sourceOK := runtime.sourceResolution.Values[variableName]
	targetValue, targetOK := runtime.targetResolution.Values[variableName].(string)
	if !sourceOK || !targetOK || runtime.targetVarTypes[variableName] != "dynamic" || targetValue == "__all__" {
		return false
	}
	sentinel := differentialAliasSentinel(query.SourcePath, variableName, labelName)
	if strings.Contains(query.Expression, sentinel) || strings.Contains(identity.TargetExpression, sentinel) {
		return false
	}
	for _, value := range runtime.sourceResolution.Values {
		if strings.Contains(value, sentinel) {
			return false
		}
	}
	for _, value := range runtime.targetResolution.Values {
		if variableValueContains(value, sentinel) {
			return false
		}
	}

	sourceValues := maps.Clone(runtime.sourceResolution.Values)
	sourceValues[variableName] = sentinel
	sourceExpression, missing := runtime.analyzer.MaterializeSourceQueryForWindow(
		query, map[string]string(sourceValues), nil, window.Start, window.End,
	)
	if len(missing) > 0 || !promQLAliasLabelProof(sourceExpression, labelName, sentinel, sourceValue) {
		return false
	}

	targetValues := scalarTargetVariableValues(runtime.targetResolution.Values)
	targetValues[variableName] = sentinel
	targetLabelName := differentialTargetLabel(labelName)
	switch identity.TargetKind {
	case string(diff.TargetKindPromQL):
		targetExpression, targetMissing := transpile.MaterializeVariables(
			identity.TargetExpression,
			targetValues,
		)
		return len(targetMissing) == 0 && promQLAliasLabelProof(
			targetExpression, targetLabelName, sentinel, targetValue,
		)
	case string(diff.TargetKindBuilderQuery):
		return builderAliasLabelProof(
			widget, identity.TargetQueryName, targetLabelName, sentinel, targetValue, targetValues,
		)
	default:
		// Formula label propagation and mixed query dependencies require a
		// separate proof. Omitting an alias yields an explicit mismatch instead
		// of certifying a coincidental label value.
		return false
	}
}

func differentialAliasSentinel(sourcePath, variableName, labelName string) string {
	digest := sha256.Sum256([]byte(sourcePath + "\x00" + variableName + "\x00" + labelName))
	return fmt.Sprintf("__promcast_alias_probe_%x__", digest[:8])
}

func differentialTargetLabel(label string) string {
	switch label {
	case "job":
		return "service.name"
	case "instance":
		return "service.instance.id"
	default:
		return label
	}
}

func promQLAliasLabelProof(expression, labelName, sentinel, resolvedValue string) bool {
	occurrences := strings.Count(expression, sentinel)
	if occurrences == 0 {
		return false
	}
	expr, err := parser.NewParser(parser.Options{}).ParseExpr(expression)
	if err != nil {
		return false
	}
	safeMatchers := 0
	invalidOccurrence := false
	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		selector, ok := node.(*parser.VectorSelector)
		if !ok {
			return nil
		}
		for _, matcher := range selector.LabelMatchers {
			if !strings.Contains(matcher.Value, sentinel) {
				continue
			}
			if matcher.Name != labelName || matcher.Value != sentinel ||
				(matcher.Type != promlabels.MatchEqual && matcher.Type != promlabels.MatchRegexp) ||
				(matcher.Type == promlabels.MatchRegexp && regexp.QuoteMeta(resolvedValue) != resolvedValue) {
				invalidOccurrence = true
				continue
			}
			safeMatchers++
		}
		return nil
	})
	return !invalidOccurrence && safeMatchers == occurrences && promQLExpressionPreservesAlias(expr, labelName, sentinel)
}

func promQLResolvedAliasLabelProof(expression, labelName, resolvedValue string) bool {
	if strings.TrimSpace(resolvedValue) == "" {
		return false
	}
	expr, err := parser.NewParser(parser.Options{}).ParseExpr(expression)
	if err != nil {
		return false
	}
	safeMatchers := 0
	invalidMatcher := false
	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		selector, ok := node.(*parser.VectorSelector)
		if !ok {
			return nil
		}
		for _, matcher := range selector.LabelMatchers {
			if matcher.Name != labelName || matcher.Value != resolvedValue {
				continue
			}
			if matcher.Type != promlabels.MatchEqual &&
				(matcher.Type != promlabels.MatchRegexp || regexp.QuoteMeta(resolvedValue) != resolvedValue) {
				invalidMatcher = true
				continue
			}
			safeMatchers++
		}
		return nil
	})
	return !invalidMatcher && safeMatchers > 0 &&
		promQLExpressionPreservesAlias(expr, labelName, resolvedValue)
}

var labelPreservingPromQLFunctions = map[string]struct{}{
	"abs": {}, "avg_over_time": {}, "ceil": {}, "changes": {}, "clamp": {},
	"clamp_max": {}, "clamp_min": {}, "count_over_time": {}, "day_of_month": {},
	"day_of_week": {}, "day_of_year": {}, "days_in_month": {}, "delta": {},
	"deriv": {}, "double_exponential_smoothing": {}, "exp": {}, "floor": {},
	"histogram_avg": {}, "histogram_count": {}, "histogram_fraction": {},
	"histogram_stddev": {}, "histogram_stdvar": {}, "histogram_sum": {},
	"holt_winters": {}, "hour": {}, "idelta": {}, "increase": {}, "irate": {},
	"last_over_time": {}, "ln": {}, "log10": {}, "log2": {}, "mad_over_time": {},
	"max_over_time": {}, "min_over_time": {}, "minute": {}, "month": {},
	"predict_linear": {}, "present_over_time": {}, "quantile_over_time": {},
	"rate": {}, "resets": {}, "round": {}, "sgn": {}, "sort": {},
	"sort_by_label": {}, "sort_by_label_desc": {}, "sort_desc": {}, "sqrt": {},
	"stddev_over_time": {}, "stdvar_over_time": {}, "sum_over_time": {},
	"timestamp": {}, "year": {},
}

func promQLExpressionPreservesAlias(expr parser.Expr, labelName, sentinel string) bool {
	switch typed := expr.(type) {
	case *parser.ParenExpr:
		return promQLExpressionPreservesAlias(typed.Expr, labelName, sentinel)
	case *parser.UnaryExpr:
		return promQLExpressionPreservesAlias(typed.Expr, labelName, sentinel)
	case *parser.MatrixSelector:
		return promQLExpressionPreservesAlias(typed.VectorSelector, labelName, sentinel)
	case *parser.SubqueryExpr:
		return promQLExpressionPreservesAlias(typed.Expr, labelName, sentinel)
	case *parser.VectorSelector:
		for _, matcher := range typed.LabelMatchers {
			if matcher.Name == labelName && matcher.Value == sentinel &&
				(matcher.Type == promlabels.MatchEqual || matcher.Type == promlabels.MatchRegexp) {
				return true
			}
		}
		return false
	case *parser.Call:
		if typed.Func == nil || len(typed.Args) == 0 {
			return false
		}
		if _, safe := labelPreservingPromQLFunctions[typed.Func.Name]; !safe {
			return false
		}
		return promQLExpressionPreservesAlias(typed.Args[0], labelName, sentinel)
	case *parser.AggregateExpr:
		if !aggregatePreservesAliasLabel(typed, labelName) {
			return false
		}
		return promQLExpressionPreservesAlias(typed.Expr, labelName, sentinel)
	case *parser.BinaryExpr:
		leftScalar := typed.LHS.Type() == parser.ValueTypeScalar
		rightScalar := typed.RHS.Type() == parser.ValueTypeScalar
		switch {
		case leftScalar && !rightScalar:
			return promQLExpressionPreservesAlias(typed.RHS, labelName, sentinel)
		case rightScalar && !leftScalar:
			return promQLExpressionPreservesAlias(typed.LHS, labelName, sentinel)
		default:
			return false
		}
	default:
		return false
	}
}

func aggregatePreservesAliasLabel(aggregation *parser.AggregateExpr, labelName string) bool {
	grouped := slices.Contains(aggregation.Grouping, labelName)
	switch aggregation.Op {
	case parser.TOPK, parser.BOTTOMK, parser.LIMITK, parser.LIMIT_RATIO:
		return true
	case parser.SUM, parser.AVG, parser.COUNT, parser.MIN, parser.MAX,
		parser.GROUP, parser.STDDEV, parser.STDVAR, parser.QUANTILE:
		if aggregation.Without {
			return !grouped
		}
		return grouped
	case parser.COUNT_VALUES:
		parameter, ok := aggregation.Param.(*parser.StringLiteral)
		if !ok || parameter.Val == labelName {
			return false
		}
		if aggregation.Without {
			return !grouped
		}
		return grouped
	default:
		return false
	}
}

type emittedBuilderFilter struct {
	label    string
	operator string
	value    string
}

func builderAliasLabelProof(
	widget signoz.Widget,
	queryName string,
	labelName string,
	sentinel string,
	resolvedValue string,
	values map[string]string,
) bool {
	var candidates []signoz.BuilderQueryData
	for _, query := range widget.Query.Builder.QueryData {
		if query.QueryName == queryName {
			candidates = append(candidates, query)
		}
	}
	if len(candidates) != 1 || !slices.ContainsFunc(candidates[0].GroupBy, func(group signoz.DashboardGroupBy) bool {
		return group.Key == labelName
	}) {
		return false
	}
	query := candidates[0]
	encoded, err := json.Marshal(query)
	if err != nil {
		return false
	}
	materializedQuery, missing := transpile.MaterializeVariables(string(encoded), values)
	if len(missing) > 0 {
		return false
	}
	filterExpression, filterMissing := transpile.MaterializeVariables(query.Filter.Expression, values)
	if len(filterMissing) > 0 {
		return false
	}
	filters, ok := parseEmittedBuilderFilters(filterExpression)
	if !ok {
		return false
	}
	safeOccurrences := 0
	for _, filter := range filters {
		if !strings.Contains(filter.value, sentinel) {
			continue
		}
		if filter.label != labelName || filter.value != sentinel ||
			(filter.operator != "=" && filter.operator != "=~") ||
			(filter.operator == "=~" && regexp.QuoteMeta(resolvedValue) != resolvedValue) {
			return false
		}
		safeOccurrences++
	}
	return safeOccurrences > 0 && safeOccurrences == strings.Count(materializedQuery, sentinel)
}

func targetArtifactAliasLabelProof(
	request signoz.QueryRangeRequest,
	targetKind string,
	queryName string,
	labelName string,
	sentinel string,
	resolvedValue string,
	values targetVariableValues,
) bool {
	scalarValues := scalarTargetVariableValues(values)
	switch targetKind {
	case string(diff.TargetKindPromQL):
		var candidates []signoz.PromQLSpec
		for _, envelope := range request.CompositeQuery.Queries {
			if envelope.Type != "promql" {
				continue
			}
			spec, ok := envelope.Spec.(signoz.PromQLSpec)
			if ok && spec.Name == queryName {
				candidates = append(candidates, spec)
			}
		}
		if len(candidates) != 1 {
			return false
		}
		expression, missing := transpile.MaterializeVariables(
			candidates[0].Query, scalarValues,
		)
		return len(missing) == 0 && promQLAliasLabelProof(
			expression, labelName, sentinel, resolvedValue,
		)
	case string(diff.TargetKindBuilderQuery):
		var candidates []signoz.BuilderQuerySpec
		for _, envelope := range request.CompositeQuery.Queries {
			if envelope.Type != "builder_query" {
				continue
			}
			spec, ok := envelope.Spec.(signoz.BuilderQuerySpec)
			if ok && spec.Name == queryName {
				candidates = append(candidates, spec)
			}
		}
		return len(candidates) == 1 && builderRequestAliasLabelProof(
			candidates[0], labelName, sentinel, resolvedValue, scalarValues,
		)
	default:
		return false
	}
}

func builderRequestAliasLabelProof(
	query signoz.BuilderQuerySpec,
	labelName string,
	sentinel string,
	resolvedValue string,
	values map[string]string,
) bool {
	if !slices.ContainsFunc(query.GroupBy, func(group signoz.GroupBy) bool {
		return group.Name == labelName
	}) {
		return false
	}
	encoded, err := json.Marshal(query)
	if err != nil {
		return false
	}
	materializedQuery, missing := transpile.MaterializeVariables(string(encoded), values)
	if len(missing) > 0 {
		return false
	}
	filterExpression, filterMissing := transpile.MaterializeVariables(
		query.Filter.Expression, values,
	)
	if len(filterMissing) > 0 {
		return false
	}
	filters, ok := parseEmittedBuilderFilters(filterExpression)
	if !ok {
		return false
	}
	safeOccurrences := 0
	for _, filter := range filters {
		if !strings.Contains(filter.value, sentinel) {
			continue
		}
		if filter.label != labelName || filter.value != sentinel ||
			(filter.operator != "=" && filter.operator != "=~") ||
			(filter.operator == "=~" && regexp.QuoteMeta(resolvedValue) != resolvedValue) {
			return false
		}
		safeOccurrences++
	}
	return safeOccurrences > 0 && safeOccurrences == strings.Count(materializedQuery, sentinel)
}

func parseEmittedBuilderFilters(expression string) ([]emittedBuilderFilter, bool) {
	if expression == "" {
		return nil, true
	}
	result := make([]emittedBuilderFilter, 0, strings.Count(expression, " AND ")+1)
	for position := 0; position < len(expression); {
		labelEnd := strings.IndexByte(expression[position:], ' ')
		if labelEnd <= 0 {
			return nil, false
		}
		labelEnd += position
		label := expression[position:labelEnd]
		position = labelEnd + 1
		operatorEnd := strings.IndexByte(expression[position:], ' ')
		if operatorEnd <= 0 {
			return nil, false
		}
		operatorEnd += position
		operator := expression[position:operatorEnd]
		if operator != "=" && operator != "!=" && operator != "=~" && operator != "!~" {
			return nil, false
		}
		position = operatorEnd + 1
		if position >= len(expression) || expression[position] != '\'' {
			return nil, false
		}
		position++
		var value strings.Builder
		closed := false
		for position < len(expression) {
			character := expression[position]
			position++
			switch character {
			case '\\':
				if position >= len(expression) {
					return nil, false
				}
				escaped := expression[position]
				if escaped != '\\' && escaped != '\'' {
					return nil, false
				}
				value.WriteByte(escaped)
				position++
			case '\'':
				closed = true
			default:
				value.WriteByte(character)
			}
			if closed {
				break
			}
		}
		if !closed {
			return nil, false
		}
		result = append(result, emittedBuilderFilter{label: label, operator: operator, value: value.String()})
		if position == len(expression) {
			break
		}
		if !strings.HasPrefix(expression[position:], " AND ") {
			return nil, false
		}
		position += len(" AND ")
	}
	return result, true
}
