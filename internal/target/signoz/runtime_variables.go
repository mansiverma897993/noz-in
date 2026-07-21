package signoz

import (
	"fmt"
	"slices"
)

// RuntimeVariableValues returns the values the pinned v0.133 dashboard loads
// and sends to query_range. It validates CUSTOM reload semantics instead of
// trusting selectedValue, which that frontend path ignores on initialization.
func RuntimeVariableValues(dashboard DashboardV5) (map[string]any, error) {
	values := make(map[string]any, len(dashboard.Variables))
	for _, variable := range dashboard.Variables {
		if variable.Name == "" {
			continue
		}
		if variable.Type == "CUSTOM" {
			if variable.AllSelected {
				return nil, fmt.Errorf("custom variable %q cannot persist All in the pinned target", variable.Name)
			}
			runtimeValue, err := StableCustomRuntimeValue(variable.CustomValue, variable.MultiSelect)
			if err != nil {
				return nil, fmt.Errorf("custom variable %q reload value: %w", variable.Name, err)
			}
			if !sameRuntimeVariableValue(variable.SelectedValue, runtimeValue) {
				return nil, fmt.Errorf(
					"custom variable %q selectedValue does not match its pinned reload value",
					variable.Name,
				)
			}
			values[variable.Name] = runtimeValue
			continue
		}
		if variable.AllSelected && variable.Type == "DYNAMIC" &&
			variable.ShowAllOption && variable.MultiSelect {
			values[variable.Name] = "__all__"
			continue
		}
		selected, selectedIsString := variable.SelectedValue.(string)
		if variable.SelectedValue != nil && (!selectedIsString || selected != "") {
			values[variable.Name] = variable.SelectedValue
			continue
		}
		// The frontend treats an empty selected string as absent and falls
		// through to the configured default.
		if variable.Type == "TEXTBOX" && variable.TextboxValue != "" {
			values[variable.Name] = variable.TextboxValue
			continue
		}
		if variable.DefaultValue != "" {
			values[variable.Name] = variable.DefaultValue
		}
	}
	return values, nil
}

func sameRuntimeVariableValue(actual any, expected any) bool {
	switch expected := expected.(type) {
	case string:
		actual, ok := actual.(string)
		return ok && actual == expected
	case []string:
		actualValues, ok := runtimeStringSlice(actual)
		return ok && slices.Equal(actualValues, expected)
	default:
		return false
	}
}

func runtimeStringSlice(value any) ([]string, bool) {
	switch value := value.(type) {
	case []string:
		return value, true
	case []any:
		result := make([]string, len(value))
		for index, item := range value {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			result[index] = text
		}
		return result, true
	default:
		return nil, false
	}
}
