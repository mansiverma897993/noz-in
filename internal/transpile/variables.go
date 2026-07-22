package transpile

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/mansiverma897993/noz-in/internal/model"
)

var (
	rangeSelectorPattern     = regexp.MustCompile(`\[[^\]]*\]`)
	variablePattern          = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*(?:\.[^:^}]+)?(?::[^}]+)?\}|\[\[[A-Za-z_][A-Za-z0-9_]*(?::[A-Za-z0-9_]+)?\]\]|\$[A-Za-z_][A-Za-z0-9_]*`)
	dollarVariablePattern    = regexp.MustCompile(`^\$([A-Za-z_][A-Za-z0-9_]*)$`)
	legacyVariablePattern    = regexp.MustCompile(`^\[\[([A-Za-z_][A-Za-z0-9_]*)(?::([A-Za-z0-9_]+))?\]\]$`)
	bracedVariablePattern    = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)(?:\.([^:^}]+))?(?::([^}]+))?\}$`)
	targetDollarNamePattern  = regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)`)
	offsetVariablePattern    = regexp.MustCompile(`\boffset\s+(\$\{?[A-Za-z_][A-Za-z0-9_]*\}?)`)
	legendPlaceholderPattern = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)
)

var targetReservedVariableNames = []string{
	"start_timestamp",
	"end_timestamp",
	"start_timestamp_ms",
	"end_timestamp_ms",
	targetStartTimeVariable,
	targetEndTimeVariable,
	"start_timestamp_nano",
	"end_timestamp_nano",
	"start_datetime",
	"end_datetime",
}

var grafanaGlobalVariableNames = []string{
	"__rate_interval_ms", "__rate_interval_s", "__interval_ms", "__interval_s",
	"__range_ms", "__range_s", "__rate_interval", "__interval", "__range", "__from", "__to",
}

const (
	targetStartTimeVariable = "SIGNOZ_START_TIME"
	targetEndTimeVariable   = "SIGNOZ_END_TIME"
)

type grafanaVariableReference struct {
	name      string
	fieldPath string
	format    string
}

func parseGrafanaVariableReference(value string) (grafanaVariableReference, bool) {
	if match := dollarVariablePattern.FindStringSubmatch(value); len(match) == 2 {
		return grafanaVariableReference{name: match[1]}, true
	}
	if match := legacyVariablePattern.FindStringSubmatch(value); len(match) == 3 {
		return grafanaVariableReference{name: match[1], format: match[2]}, true
	}
	if match := bracedVariablePattern.FindStringSubmatch(value); len(match) == 4 {
		return grafanaVariableReference{name: match[1], fieldPath: match[2], format: match[3]}, true
	}
	return grafanaVariableReference{}, false
}

type preparedExpression struct {
	parse              string
	passthrough        string
	reasons            []model.ReasonCode
	dynamicIdentifiers map[string]bool
	dynamic            bool
	executable         bool
}

func (analyzer *Analyzer) normalizeLegend(legend string) string {
	if strings.EqualFold(strings.TrimSpace(legend), "__auto") {
		return ""
	}
	return legendPlaceholderPattern.ReplaceAllStringFunc(legend, func(placeholder string) string {
		match := legendPlaceholderPattern.FindStringSubmatch(placeholder)
		if len(match) != 2 {
			return placeholder
		}
		label := strings.TrimSpace(match[1])
		if mapped, ok := analyzer.labelMap[label]; ok {
			label = mapped
		}
		return "{{" + label + "}}"
	})
}

func (analyzer *Analyzer) prepareExpression(expression string) preparedExpression {
	passthrough, reasons := analyzer.rewriteGlobals(expression)
	passthrough, variableReasons, executable := normalizeTargetVariableSyntax(passthrough)
	reasons = append(reasons, variableReasons...)
	parseExpression := passthrough
	dynamic := false

	parseExpression, rangeDynamic := rewriteUnquotedSpans(parseExpression, func(span string) (string, bool) {
		changed := false
		rewritten := rangeSelectorPattern.ReplaceAllStringFunc(span, func(selector string) string {
			if !variablePattern.MatchString(selector) {
				return selector
			}
			changed = true
			return variablePattern.ReplaceAllStringFunc(selector, func(variable string) string {
				if strings.Contains(strings.ToLower(variable), "interval") || strings.Contains(strings.ToLower(variable), "resolution") {
					return promDuration(analyzer.options.Interval)
				}
				return promDuration(analyzer.options.RateInterval)
			})
		})
		return rewritten, changed
	})
	dynamic = dynamic || rangeDynamic
	parseExpression, offsetDynamic := rewriteUnquotedSpans(parseExpression, func(span string) (string, bool) {
		changed := false
		rewritten := offsetVariablePattern.ReplaceAllStringFunc(span, func(string) string {
			changed = true
			return "offset " + promDuration(analyzer.options.Interval)
		})
		return rewritten, changed
	})
	dynamic = dynamic || offsetDynamic
	parseExpression, outsideQuotes, dynamicIdentifiers := replaceVariablesOutsideQuotes(parseExpression)
	dynamic = dynamic || outsideQuotes
	if dynamic {
		reasons = append(reasons, model.ReasonDynamicStructure)
	}
	return preparedExpression{
		parse:              parseExpression,
		passthrough:        passthrough,
		reasons:            uniqueReasons(reasons),
		dynamicIdentifiers: dynamicIdentifiers,
		dynamic:            dynamic,
		executable:         executable,
	}
}

func rewriteUnquotedSpans(expression string, rewrite func(string) (string, bool)) (string, bool) {
	var result strings.Builder
	result.Grow(len(expression))
	changed := false
	for index := 0; index < len(expression); {
		nextQuote := strings.IndexAny(expression[index:], "\"'`")
		if nextQuote < 0 {
			rewritten, spanChanged := rewrite(expression[index:])
			result.WriteString(rewritten)
			changed = changed || spanChanged
			break
		}
		quoteStart := index + nextQuote
		rewritten, spanChanged := rewrite(expression[index:quoteStart])
		result.WriteString(rewritten)
		changed = changed || spanChanged
		quoteEnd := quotedEnd(expression, quoteStart, expression[quoteStart])
		result.WriteString(expression[quoteStart:quoteEnd])
		index = quoteEnd
	}
	return result.String(), changed
}

// VariableNames returns the distinct dashboard variables referenced by an
// emitted expression. Grafana replacement captures such as ${1} are excluded
// because they are not dashboard variables.
func VariableNames(expression string) []string {
	names := make([]string, 0)
	for _, value := range variablePattern.FindAllString(expression, -1) {
		if reference, ok := parseGrafanaVariableReference(value); ok {
			if isTargetRuntimeVariable(reference.name) {
				continue
			}
			names = append(names, reference.name)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

func isTargetRuntimeVariable(name string) bool {
	return name == targetStartTimeVariable || name == targetEndTimeVariable
}

// TargetPromQLRuntimeSubstitutionExact verifies the pinned SigNoz renderVars
// boundary. SigNoz injects reserved variables, sorts all runtime keys by
// descending byte length, performs raw ReplaceAll substitutions without token
// boundaries, and finally executes the result as a Go template. Grafana's
// Prometheus interpolation does none of those extra substitutions.
func TargetPromQLRuntimeSubstitutionExact(
	sourceExpression string,
	targetExpression string,
	dashboardVariableNames []string,
) bool {
	if strings.Contains(targetExpression, "{{") || strings.Contains(targetExpression, "}}") {
		return false
	}
	if hasGrafanaVariableNamed(sourceExpression, targetStartTimeVariable) ||
		hasGrafanaVariableNamed(sourceExpression, targetEndTimeVariable) {
		// These names are injected and replaced by SigNoz even when Grafana
		// treats the same bytes as an undefined/literal source reference. Do
		// not let one legitimate $__from/$__to occurrence authorize another.
		return false
	}
	for _, sourceVariable := range variablePattern.FindAllString(sourceExpression, -1) {
		reference, ok := parseGrafanaVariableReference(sourceVariable)
		if !ok || isPlainDollarVariable(sourceVariable) {
			continue
		}
		if !slices.Contains(dashboardVariableNames, reference.name) &&
			!slices.Contains(grafanaGlobalVariableNames, reference.name) {
			// Grafana preserves an undefined braced/legacy reference byte-for-byte,
			// while target normalization would change it to $name.
			return false
		}
	}
	runtimeNames := make(map[string]bool, len(targetReservedVariableNames)+len(dashboardVariableNames))
	for _, name := range targetReservedVariableNames {
		runtimeNames[name] = true
	}
	for _, name := range dashboardVariableNames {
		runtimeNames[name] = true
	}
	sourceNames := VariableNames(sourceExpression)
	for _, match := range targetDollarNamePattern.FindAllStringSubmatch(targetExpression, -1) {
		name := match[1]
		if runtimeNames[name] {
			switch name {
			case targetStartTimeVariable:
				if !hasUnformattedGrafanaVariable(sourceExpression, "__from") {
					return false
				}
			case targetEndTimeVariable:
				if !hasUnformattedGrafanaVariable(sourceExpression, "__to") {
					return false
				}
			default:
				if slices.Contains(targetReservedVariableNames, name) || !slices.Contains(sourceNames, name) {
					return false
				}
			}
			continue
		}
		for runtimeName := range runtimeNames {
			if strings.HasPrefix(name, runtimeName) {
				return false
			}
		}
	}
	return true
}

func isPlainDollarVariable(value string) bool {
	return strings.HasPrefix(value, "$") && !strings.HasPrefix(value, "${")
}

// TargetDynamicAllMatcherRemovalExact reports whether SigNoz's DYNAMIC-All
// matcher deletion can preserve a Grafana positive match-all regex. Every
// reference must be the complete value of an =~ matcher; negative, equality,
// partial, and non-matcher uses change meaning when SigNoz removes a matcher.
func TargetDynamicAllMatcherRemovalExact(expression, name string) bool {
	found := false
	for _, location := range variablePattern.FindAllStringIndex(expression, -1) {
		start, end := location[0], location[1]
		if variableName(expression[start:end]) != name {
			continue
		}
		found = true
		if !exactPositiveRegexMatcherValue(expression, start, end) {
			return false
		}
	}
	return found
}

func hasUnformattedGrafanaVariable(expression, name string) bool {
	for _, value := range variablePattern.FindAllString(expression, -1) {
		reference, ok := parseGrafanaVariableReference(value)
		if ok && reference.name == name && reference.fieldPath == "" && reference.format == "" {
			return true
		}
	}
	return false
}

func hasGrafanaVariableNamed(expression, name string) bool {
	for _, value := range variablePattern.FindAllString(expression, -1) {
		if reference, ok := parseGrafanaVariableReference(value); ok && reference.name == name {
			return true
		}
	}
	return false
}

// normalizeTargetVariableSyntax converts Grafana's braced variable form to
// the SigNoz query form. Formatted values are only accepted when the whole
// value of a regex matcher is a regex/pipe variable; all other formatters stay
// non-executable so the source cannot be mistaken for an equivalent query.
func normalizeTargetVariableSyntax(expression string) (string, []model.ReasonCode, bool) {
	locations := variablePattern.FindAllStringIndex(expression, -1)
	if len(locations) == 0 {
		return expression, nil, true
	}
	var result strings.Builder
	result.Grow(len(expression))
	last := 0
	executable := true
	var reasons []model.ReasonCode
	for _, location := range locations {
		start, end := location[0], location[1]
		result.WriteString(expression[last:start])
		variable := expression[start:end]
		reference, parsed := parseGrafanaVariableReference(variable)
		if !parsed {
			result.WriteString(variable)
			reasons = append(reasons, model.ReasonGrafanaVariableFormat)
			executable = false
			last = end
			continue
		}
		name := reference.name
		format := strings.ToLower(strings.TrimSpace(reference.format))
		switch {
		case reference.fieldPath != "":
			result.WriteString(variable)
			reasons = append(reasons, model.ReasonGrafanaVariableFormat)
			executable = false
		case strings.HasPrefix(name, "__"):
			// Supported interval/range/time globals were already rewritten.
			// Any remaining Grafana system variable or macro has runtime
			// semantics that SigNoz cannot reproduce from templating.list.
			result.WriteString(variable)
			reasons = append(reasons, model.ReasonGrafanaVariableFormat)
			executable = false
		case format == "":
			result.WriteByte('$')
			result.WriteString(name)
		case (format == "regex" || format == "pipe") && exactRegexMatcherValue(expression, start, end):
			result.WriteByte('$')
			result.WriteString(name)
			reasons = append(reasons, model.ReasonRegexVariable)
		default:
			result.WriteString(variable)
			reasons = append(reasons, model.ReasonGrafanaVariableFormat)
			executable = false
		}
		last = end
	}
	result.WriteString(expression[last:])
	return result.String(), uniqueReasons(reasons), executable
}

func exactRegexMatcherValue(expression string, start, end int) bool {
	if start == 0 || end >= len(expression) || expression[start-1] != '"' || expression[end] != '"' {
		return false
	}
	prefix := strings.TrimSpace(expression[:start-1])
	return strings.HasSuffix(prefix, "=~") || strings.HasSuffix(prefix, "!~")
}

func exactPositiveRegexMatcherValue(expression string, start, end int) bool {
	if start == 0 || end >= len(expression) || expression[start-1] != '"' || expression[end] != '"' {
		return false
	}
	prefix := strings.TrimSpace(expression[:start-1])
	return strings.HasSuffix(prefix, "=~") && !strings.HasSuffix(prefix, "!~")
}

func replaceVariablesOutsideQuotes(expression string) (string, bool, map[string]bool) {
	var result strings.Builder
	result.Grow(len(expression))
	replaced := false
	dynamicIdentifiers := make(map[string]bool)
	sentinelPrefix := "sm_var_"
	for strings.Contains(expression, sentinelPrefix) {
		sentinelPrefix = "_" + sentinelPrefix
	}
	sentinelIndex := 0
	for index := 0; index < len(expression); {
		if expression[index] == '"' || expression[index] == '\'' || expression[index] == '`' {
			quote := expression[index]
			end := quotedEnd(expression, index, quote)
			result.WriteString(expression[index:end])
			index = end
			continue
		}

		location := variablePattern.FindStringSubmatchIndex(expression[index:])
		if location == nil {
			result.WriteString(expression[index:])
			break
		}
		start := index + location[0]
		end := index + location[1]
		nextQuote := strings.IndexAny(expression[index:], "\"'`")
		if nextQuote >= 0 && index+nextQuote < start {
			result.WriteString(expression[index : index+nextQuote])
			index += nextQuote
			continue
		}

		result.WriteString(expression[index:start])
		replacement, identifier := variableSentinel(expression, start, end, sentinelPrefix, sentinelIndex)
		result.WriteString(replacement)
		if identifier != "" {
			dynamicIdentifiers[identifier] = true
			sentinelIndex++
		}
		replaced = true
		index = end
	}
	return result.String(), replaced, dynamicIdentifiers
}

func variableSentinel(expression string, start, end int, prefix string, index int) (string, string) {
	name := prefix + strconv.Itoa(index) + "_" + variableName(expression[start:end])
	if strings.LastIndex(expression[:start], "{") > strings.LastIndex(expression[:start], "}") {
		return name + `="1"`, name
	}
	if isLabelOrMetricPosition(expression, start, end) {
		return name, name
	}
	return "1", ""
}

func isLabelOrMetricPosition(expression string, start, end int) bool {
	if start > 0 && isIdentifierByte(expression[start-1]) {
		return true
	}
	if end < len(expression) && (isIdentifierByte(expression[end]) || expression[end] == '{') {
		return true
	}

	open := strings.LastIndex(expression[:start], "(")
	if open < 0 {
		return false
	}
	prefix := strings.TrimSpace(expression[:open])
	wordStart := strings.LastIndexAny(prefix, " +-*/%^<>=!,()") + 1
	operator := strings.ToLower(prefix[wordStart:])
	switch operator {
	case "by", "without", "on", "ignoring", "group_left", "group_right", "sum", "avg", "min", "max", "count", "group", "stddev", "stdvar":
		return true
	default:
		return false
	}
}

func isIdentifierByte(value byte) bool {
	return value == '_' || value == ':' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func quotedEnd(expression string, start int, quote byte) int {
	for index := start + 1; index < len(expression); index++ {
		if expression[index] == '\\' {
			index++
			continue
		}
		if expression[index] == quote {
			return index + 1
		}
	}
	return len(expression)
}

func variableName(variable string) string {
	if reference, ok := parseGrafanaVariableReference(variable); ok {
		return reference.name
	}
	return "value"
}
