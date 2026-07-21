package transpile

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mansiverma897993/signoz/internal/model"
)

// MaterializeSourceExpression rewrites Grafana globals and substitutes concrete dashboard values.
func (analyzer *Analyzer) MaterializeSourceExpression(expression string, values map[string]string) (string, []string) {
	rewritten := analyzer.rewriteSourceGlobals(expression, time.Time{}, time.Time{})
	return MaterializeVariables(rewritten, values)
}

// MaterializeSourceQuery applies the same query-specific interval controls as
// Analyze before substituting source-side dashboard variables.
func (analyzer *Analyzer) MaterializeSourceQuery(query model.Query, values map[string]string) (string, []string) {
	return analyzer.MaterializeSourceQueryWithMulti(query, values, nil)
}

// MaterializeSourceQueryWithMulti applies Grafana's pinned Prometheus
// interpolation for multi-selected values whose escaping is invariant across
// the Grafana 10/11 feature-toggle variants. Values containing characters whose
// two upstream escape modes differ remain unresolved instead of guessing.
func (analyzer *Analyzer) MaterializeSourceQueryWithMulti(
	query model.Query,
	values map[string]string,
	multiValues map[string][]string,
) (string, []string) {
	return analyzer.MaterializeSourceQueryForWindow(query, values, multiValues, time.Time{}, time.Time{})
}

// MaterializeSourceQueryForWindow applies Grafana's source-side epoch-millisecond
// values for $__from and $__to using the exact differential evaluation window.
func (analyzer *Analyzer) MaterializeSourceQueryForWindow(
	query model.Query,
	values map[string]string,
	multiValues map[string][]string,
	start time.Time,
	end time.Time,
) (string, []string) {
	effective := *analyzer
	effective.applyQueryIntervalControls(query)
	rewritten := effective.rewriteSourceGlobals(query.Expression, start, end)
	return MaterializeVariablesWithMulti(rewritten, values, multiValues)
}

func (analyzer *Analyzer) rewriteSourceGlobals(expression string, start, end time.Time) string {
	rewritten := analyzer.rewriteDurationGlobals(expression)
	if !start.IsZero() {
		rewritten = replaceGrafanaGlobal(rewritten, "__from", strconv.FormatInt(start.UnixMilli(), 10))
	}
	if !end.IsZero() {
		rewritten = replaceGrafanaGlobal(rewritten, "__to", strconv.FormatInt(end.UnixMilli(), 10))
	}
	return rewritten
}

// MaterializeVariables substitutes Grafana variable forms and reports missing values.
func MaterializeVariables(expression string, values map[string]string) (string, []string) {
	return MaterializeVariablesWithMulti(expression, values, nil)
}

// MaterializeVariablesWithMulti substitutes scalar Grafana values and the
// exact common Prometheus multi-value forms. The datasource formatter emits a
// parenthesized regex alternation for unformatted multi-values, the generic
// regex formatter does the same, and the pipe formatter omits parentheses.
func MaterializeVariablesWithMulti(
	expression string,
	values map[string]string,
	multiValues map[string][]string,
) (string, []string) {
	missing := make([]string, 0)
	materialized := variablePattern.ReplaceAllStringFunc(expression, func(variable string) string {
		reference, parsed := parseGrafanaVariableReference(variable)
		if !parsed || reference.fieldPath != "" {
			if parsed {
				missing = append(missing, reference.name)
			}
			return variable
		}
		format := strings.ToLower(strings.TrimSpace(reference.format))
		if format != "" && format != "regex" && format != "pipe" {
			missing = append(missing, reference.name)
			return variable
		}
		name := reference.name
		value, ok := values[name]
		if ok {
			return value
		}
		selected, ok := multiValues[name]
		if !ok {
			missing = append(missing, name)
			return variable
		}
		replacement, exact := materializePrometheusMultiValue(variable, selected)
		if !exact {
			missing = append(missing, name)
			return variable
		}
		return replacement
	})
	slices.Sort(missing)
	missing = slices.Compact(missing)
	return strings.TrimSpace(materialized), missing
}

func materializePrometheusMultiValue(variable string, values []string) (string, bool) {
	if len(values) == 0 {
		return "", false
	}
	reference, parsed := parseGrafanaVariableReference(variable)
	if !parsed || reference.fieldPath != "" {
		return "", false
	}
	format := strings.ToLower(strings.TrimSpace(reference.format))
	switch format {
	case "", "regex":
		for _, value := range values {
			if !prometheusMultiValueEscapeInvariant(value) {
				return "", false
			}
		}
		if len(values) == 1 {
			return values[0], true
		}
		return "(" + strings.Join(values, "|") + ")", true
	case "pipe":
		// Grafana's pipe formatter deliberately joins the raw values. Pinned
		// SigNoz also joins an array with a raw pipe before substitution, so this
		// path is exact even when a regular/regex formatter would escape bytes.
		return strings.Join(values, "|"), true
	default:
		return "", false
	}
}

// TargetRawVariableSubstitutionExact reports whether every reference to name
// in expression has the same query meaning under pinned Grafana Prometheus
// interpolation and pinned SigNoz raw variable substitution. A multi-value
// parenthesized alternation may differ byte-for-byte but is equivalent only as
// the complete value of a regex matcher. Parser and other query-role safety
// remain the analyzer's separate responsibility.
func TargetRawVariableSubstitutionExact(
	expression string,
	name string,
	values []string,
	multiOrAll bool,
	runtimeVariableNames []string,
) bool {
	if !targetInsertedValuesSecondPassExact(name, values, runtimeVariableNames) {
		return false
	}
	for _, location := range variablePattern.FindAllStringIndex(expression, -1) {
		start, end := location[0], location[1]
		variable := expression[start:end]
		if variableName(variable) != name {
			continue
		}
		reference, parsed := parseGrafanaVariableReference(variable)
		if !parsed || reference.fieldPath != "" {
			return false
		}
		format := strings.ToLower(strings.TrimSpace(reference.format))
		switch format {
		case "pipe":
			if !exactRegexMatcherValue(expression, start, end) {
				return false
			}
			continue
		case "regex":
			if !exactRegexMatcherValue(expression, start, end) {
				return false
			}
			if !prometheusRegexValuesRawExact(values) {
				return false
			}
		case "":
			if multiOrAll {
				if len(values) > 1 && !exactRegexMatcherValue(expression, start, end) {
					return false
				}
				if !prometheusRegexValuesRawExact(values) {
					return false
				}
				continue
			}
			if !prometheusRegularValuesRawExact(values) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// targetInsertedValuesSecondPassExact checks behavior after the first raw
// replacement. Pinned SigNoz continues replacing shorter runtime keys and
// then parses the complete query as a Go template. Grafana performs neither
// recursive step on an inserted value. A longer key is safe because SigNoz's
// length-ordered renderer has already processed it; equal-length peer keys are
// rejected because their map-derived ordering is not a stable contract.
func targetInsertedValuesSecondPassExact(
	name string,
	values []string,
	runtimeVariableNames []string,
) bool {
	runtimeNames := append([]string(nil), targetReservedVariableNames...)
	runtimeNames = append(runtimeNames, runtimeVariableNames...)
	for _, value := range values {
		if strings.Contains(value, "{{") || strings.Contains(value, "}}") {
			return false
		}
		for _, runtimeName := range runtimeNames {
			if runtimeName == name {
				continue
			}
			key := "$" + runtimeName
			legacyKey := "[[" + runtimeName + "]]"
			if (strings.Contains(value, key) || strings.Contains(value, legacyKey)) &&
				len(runtimeName) <= len(name) {
				return false
			}
		}
	}
	return true
}

func prometheusRegexValuesRawExact(values []string) bool {
	for _, value := range values {
		if !prometheusMultiValueEscapeInvariant(value) {
			return false
		}
	}
	return true
}

func prometheusRegularValuesRawExact(values []string) bool {
	for _, value := range values {
		// Both pinned modes double a backslash. They disagree about quote
		// escaping, so neither quote can be proven equal to raw substitution.
		if strings.ContainsAny(value, `\"'`) {
			return false
		}
	}
	return true
}

func prometheusMultiValueEscapeInvariant(value string) bool {
	// Grafana 10/11 has two Prometheus escaping modes. They agree for values
	// without backslashes, quotes, or regex metacharacters; supporting only this
	// intersection keeps differential source requests exact without needing an
	// unrecorded frontend feature-toggle value.
	return !strings.ContainsAny(value, `\\"'$^*{}[]+?.()|`)
}
