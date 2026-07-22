package app

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/mansiverma897993/noz-in/internal/diff"
	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/mansiverma897993/noz-in/internal/target/signoz"
)

// Source and target variable values deliberately use distinct types. Source
// values expand Grafana PromQL into one string, while the pinned SigNoz v5
// target contract accepts a scalar or scalar list; they must not silently
// share fallback rules.
type sourceVariableValues map[string]string
type targetVariableValues map[string]any

type variableResolutionIssue struct {
	Reasons []model.ReasonCode
	Detail  string
}

type sourceVariableResolution struct {
	Values sourceVariableValues
	Multi  map[string][]string
	Issues map[string]variableResolutionIssue
}

type targetVariableResolution struct {
	Values targetVariableValues
	Issues map[string]variableResolutionIssue
}

func resolveSourceVariables(dashboard model.Dashboard, overrides map[string]string) sourceVariableResolution {
	resolution := sourceVariableResolution{
		Values: make(sourceVariableValues, len(dashboard.Variables)+len(overrides)),
		Multi:  make(map[string][]string),
		Issues: make(map[string]variableResolutionIssue),
	}
	for _, variable := range dashboard.Variables {
		if value, ok := overrides[variable.Name]; ok {
			resolution.Values[variable.Name] = value
			continue
		}
		if sourcePrometheusRegexSelection(variable) {
			resolution.Multi[variable.Name] = append([]string(nil), variable.Current...)
			resolution.Issues[variable.Name] = multipleVariableIssue()
			continue
		}
		if sourceScalarValueNeedsOverride(variable) {
			resolution.Issues[variable.Name] = scalarVariableEscapingIssue()
			continue
		}
		value, issue, ok := sourceVariableValue(variable)
		if ok {
			if !variable.Multi && !variable.IncludeAll && len(variable.Current) == 1 {
				// Both pinned Grafana Prometheus escaping modes double a regular
				// scalar's backslashes. Quotes are rejected above because the modes
				// disagree over which quote is escaped.
				value = strings.ReplaceAll(value, `\`, `\\`)
			}
			resolution.Values[variable.Name] = value
		} else {
			resolution.Issues[variable.Name] = issue
		}
	}
	for name, value := range overrides {
		resolution.Values[name] = value
		delete(resolution.Multi, name)
		delete(resolution.Issues, name)
	}
	return resolution
}

func sourcePrometheusRegexSelection(variable model.Variable) bool {
	// Grafana's Prometheus datasource uses its regex interpolation callback for
	// both multi variables and variables that merely expose an All option.
	if (!variable.Multi && !variable.IncludeAll) || len(variable.Current) == 0 {
		return false
	}
	for _, value := range variable.Current {
		if strings.TrimSpace(value) == "" || isGrafanaAllValue(variable, strings.TrimSpace(value)) {
			return false
		}
	}
	return true
}

func sourceScalarValueNeedsOverride(variable model.Variable) bool {
	if variable.Multi || variable.IncludeAll || len(variable.Current) != 1 {
		return false
	}
	// The pinned Grafana modes disagree over single- versus double-quote
	// escaping. Backslashes are invariant and are materialized below.
	return strings.ContainsAny(variable.Current[0], `"'`)
}

func resolveTargetVariables(
	dashboard model.Dashboard,
	overrides map[string]string,
	variableTypes map[string]string,
) targetVariableResolution {
	resolution := targetVariableResolution{
		Values: make(targetVariableValues, len(dashboard.Variables)+len(overrides)),
		Issues: make(map[string]variableResolutionIssue),
	}
	defined := make(map[string]struct{}, len(dashboard.Variables))
	for _, variable := range dashboard.Variables {
		defined[variable.Name] = struct{}{}
		if value, ok := overrides[variable.Name]; ok {
			resolution.Values[variable.Name] = targetOverrideValue(variable, variableTypes[variable.Name], value)
			continue
		}
		value, issue, ok := targetVariableValue(variable, variableTypes[variable.Name])
		if ok {
			resolution.Values[variable.Name] = value
		} else {
			resolution.Issues[variable.Name] = issue
		}
	}
	for name := range overrides {
		if _, known := defined[name]; !known {
			resolution.Issues[name] = missingVariableIssue(
				"target override has no persisted dashboard variable definition",
			)
			delete(resolution.Values, name)
		}
	}
	return resolution
}

func resolveStoredTargetVariables(
	dashboard signoz.DashboardV5,
	overrides map[string]string,
) (targetVariableResolution, error) {
	values, err := signoz.RuntimeVariableValues(dashboard)
	if err != nil {
		return targetVariableResolution{}, err
	}
	resolution := targetVariableResolution{
		Values: targetVariableValues(values),
		Issues: make(map[string]variableResolutionIssue),
	}
	defined := make(map[string]signoz.VariableV5, len(dashboard.Variables))
	for _, variable := range dashboard.Variables {
		if variable.Name == "" {
			continue
		}
		defined[variable.Name] = variable
		if _, found := resolution.Values[variable.Name]; !found {
			resolution.Issues[variable.Name] = missingVariableIssue(
				"persisted target dashboard variable has no runtime selection",
			)
		}
	}
	for name, value := range overrides {
		variable, known := defined[name]
		if !known {
			resolution.Issues[name] = missingVariableIssue(
				"target override has no persisted dashboard variable definition",
			)
			delete(resolution.Values, name)
			continue
		}
		resolution.Values[name] = storedTargetOverrideValue(variable, value)
		delete(resolution.Issues, name)
	}
	return resolution, nil
}

func storedTargetOverrideValue(variable signoz.VariableV5, value string) any {
	if variable.Type == "DYNAMIC" && variable.ShowAllOption && variable.MultiSelect {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "all", "$__all", "__all__":
			return "__all__"
		}
	}
	if variable.MultiSelect {
		return []string{value}
	}
	return value
}

func sourceVariableValue(variable model.Variable) (string, variableResolutionIssue, bool) {
	switch len(variable.Current) {
	case 0:
		return "", missingVariableIssue("Grafana variable has no current value"), false
	case 1:
		current := strings.TrimSpace(variable.Current[0])
		if current == "" {
			return "", missingVariableIssue("Grafana variable current value is empty"), false
		}
		if isGrafanaAllValue(variable, current) {
			allValue := strings.TrimSpace(variable.AllValue)
			if allValue == "" {
				return "", variableResolutionIssue{
					Reasons: []model.ReasonCode{model.ReasonMissingVariableValue, model.ReasonVariableAllValue},
					Detail:  "Grafana All is selected but the source export has no explicit custom All value",
				}, false
			}
			return allValue, variableResolutionIssue{}, true
		}
		return variable.Current[0], variableResolutionIssue{}, true
	default:
		return "", multipleVariableIssue(), false
	}
}

func targetVariableValue(variable model.Variable, variableType string) (any, variableResolutionIssue, bool) {
	if !variable.Multi && len(variable.Current) > 1 {
		return nil, variableResolutionIssue{
			Reasons: []model.ReasonCode{model.ReasonMissingVariableValue, model.ReasonMultiVariableValue},
			Detail: fmt.Sprintf(
				"Grafana variable has %d current values while multi is disabled",
				len(variable.Current),
			),
		}, false
	}
	switch len(variable.Current) {
	case 0:
		return nil, missingVariableIssue("Grafana variable has no current value"), false
	case 1:
		current := strings.TrimSpace(variable.Current[0])
		if current == "" {
			return nil, missingVariableIssue("Grafana variable current value is empty"), false
		}
		if isGrafanaAllValue(variable, current) {
			if variableType == "dynamic" {
				return "__all__", variableResolutionIssue{}, true
			}
			return nil, variableResolutionIssue{
				Reasons: []model.ReasonCode{model.ReasonMissingVariableValue, model.ReasonVariableAllValue},
				Detail: fmt.Sprintf(
					"Grafana All cannot be reconstructed as the scalar list required by target variable type %q",
					variableType,
				),
			}, false
		}
		if variable.Multi {
			return append([]string(nil), variable.Current...), variableResolutionIssue{}, true
		}
		return variable.Current[0], variableResolutionIssue{}, true
	default:
		// SigNoz v0.133 carries query/custom multi-selections as scalar arrays.
		// Keep the individual values instead of flattening them into a string;
		// flattening would exercise a different target query than the dashboard.
		return append([]string(nil), variable.Current...), variableResolutionIssue{}, true
	}
}

func targetOverrideValue(variable model.Variable, variableType string, value string) any {
	if variableType == "dynamic" && isGrafanaAllValue(variable, strings.TrimSpace(value)) {
		return "__all__"
	}
	if variable.Multi {
		// The v5 dashboard persists a multi-variable override as a one-element
		// selectedValue array. Preview and execution must use that same shape.
		return []string{value}
	}
	return value
}

func isGrafanaAllValue(variable model.Variable, value string) bool {
	if !variable.IncludeAll {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "all", "$__all", "__all__":
		return true
	default:
		return false
	}
}

func missingVariableIssue(detail string) variableResolutionIssue {
	return variableResolutionIssue{Reasons: []model.ReasonCode{model.ReasonMissingVariableValue}, Detail: detail}
}

func multipleVariableIssue() variableResolutionIssue {
	return variableResolutionIssue{
		Reasons: []model.ReasonCode{model.ReasonMissingVariableValue, model.ReasonMultiVariableValue},
		Detail:  "Grafana multi/All interpolation requires escaping that is not invariant across the supported frontend modes",
	}
}

func scalarVariableEscapingIssue() variableResolutionIssue {
	return variableResolutionIssue{
		Reasons: []model.ReasonCode{model.ReasonMissingVariableValue, model.ReasonVariableValueEscaping},
		Detail:  "scalar value requires Grafana Prometheus escaping, but the export does not record the escaping feature mode",
	}
}

func scalarTargetVariableValues(values targetVariableValues) map[string]string {
	result := make(map[string]string, len(values))
	for name, value := range values {
		if scalar, ok := value.(string); ok {
			result[name] = scalar
		}
	}
	return result
}

func variableValueContains(value any, needle string) bool {
	switch typed := value.(type) {
	case string:
		return strings.Contains(typed, needle)
	case []string:
		for _, item := range typed {
			if strings.Contains(item, needle) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if variableValueContains(item, needle) {
				return true
			}
		}
	}
	return false
}

var labelValuesVariablePattern = regexp.MustCompile(`(?i)^\s*label_values\s*\(.*?,\s*([A-Za-z_:][A-Za-z0-9_:.-]*)\s*\)\s*$`)

func scopedVariableAliases(
	dashboard model.Dashboard,
	sourceValues sourceVariableValues,
	targetValues targetVariableValues,
	sourceVariableNames map[string]struct{},
	targetVariableNames map[string]struct{},
	provesExactOutputLabel func(variableName, labelName string) bool,
) (map[string]map[string]string, []DifferentialLabelValueAliasBinding, error) {
	aliases := make(map[string]map[string]string)
	bindings := make([]DifferentialLabelValueAliasBinding, 0)
	origins := make(map[string]map[string]string)
	for _, variable := range dashboard.Variables {
		if _, usedBySource := sourceVariableNames[variable.Name]; !usedBySource {
			continue
		}
		if _, usedByTarget := targetVariableNames[variable.Name]; !usedByTarget {
			continue
		}
		match := labelValuesVariablePattern.FindStringSubmatch(variable.Query)
		if len(match) != 2 {
			continue
		}
		label := match[1]
		if provesExactOutputLabel == nil || !provesExactOutputLabel(variable.Name, label) {
			continue
		}
		sourceValue, sourceOK := sourceValues[variable.Name]
		targetValue, targetOK := targetValues[variable.Name].(string)
		if !sourceOK || !targetOK {
			continue
		}
		// SigNoz interprets __all__ as matcher removal for dynamic variables. It
		// is control-plane syntax, never a literal target label-value alias.
		if targetValue == "__all__" {
			continue
		}
		mapping := diff.AliasMap(sourceValue, targetValue)
		if mapping == nil {
			continue
		}
		if aliases[label] == nil {
			aliases[label] = make(map[string]string)
			origins[label] = make(map[string]string)
		}
		for targetAlias, sourceAlias := range mapping {
			if existing, found := aliases[label][targetAlias]; found && existing != sourceAlias {
				return nil, nil, fmt.Errorf(
					"dashboard variables %q and %q define conflicting aliases for label %q: target value %q maps to both %q and %q",
					origins[label][targetAlias], variable.Name, label, targetAlias, existing, sourceAlias,
				)
			}
			aliases[label][targetAlias] = sourceAlias
			origins[label][targetAlias] = variable.Name
			bindings = append(bindings, DifferentialLabelValueAliasBinding{
				VariableName: variable.Name,
				SourceLabel:  label,
				TargetLabel:  differentialTargetLabel(label),
				SourceValue:  sourceAlias,
				TargetValue:  targetAlias,
			})
		}
	}
	if len(aliases) == 0 {
		return map[string]map[string]string{}, nil, nil
	}
	return aliases, bindings, nil
}
