package grafana

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mansiverma897993/signoz/internal/model"
)

func normalizeVariable(raw rawVariable, index int, bindings map[string]string) model.Variable {
	query := queryValue(raw.Query)
	path := fmt.Sprintf("/templating/list/%d", index)
	return model.Variable{
		Name:           raw.Name,
		Label:          raw.Label,
		Kind:           normalizeVariableKind(raw.Type),
		Query:          query,
		Regex:          raw.Regex,
		Current:        currentValues(raw.Current),
		Multi:          raw.Multi,
		IncludeAll:     raw.IncludeAll,
		AllValue:       raw.AllValue,
		Datasource:     normalizeDatasource(raw.Datasource, bindings),
		SourcePath:     path,
		SourceFeatures: variableSourceFeatures(raw, path),
	}
}

func normalizeVariableKind(kind string) model.VariableKind {
	switch strings.ToLower(kind) {
	case "query":
		return model.VariableKindQuery
	case "custom":
		return model.VariableKindCustom
	case "interval":
		return model.VariableKindInterval
	case "constant":
		return model.VariableKindConstant
	case "datasource":
		return model.VariableKindDatasource
	case "textbox":
		return model.VariableKindText
	default:
		return model.VariableKindUnknown
	}
}

func inputBindings(inputs []rawInput) map[string]string {
	if len(inputs) == 0 {
		return nil
	}
	bindings := make(map[string]string, len(inputs))
	for _, input := range inputs {
		value := input.PluginID
		if value == "" {
			value = input.PluginName
		}
		if value == "" {
			value = input.Type
		}
		bindings[input.Name] = value
	}
	return bindings
}

func queryValue(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if raw[0] == '{' {
		var object struct {
			Query string `json:"query"`
		}
		if json.Unmarshal(raw, &object) == nil {
			return object.Query
		}
	}
	return stringValue(raw)
}

func currentValues(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
		return nil
	}
	var current struct {
		Value json.RawMessage `json:"value"`
	}
	if json.Unmarshal(raw, &current) != nil {
		return nil
	}
	var values []string
	if json.Unmarshal(current.Value, &values) == nil {
		return values
	}
	if value := stringValue(current.Value); value != "" {
		return []string{value}
	}
	return nil
}

func stringValue(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return number.String()
	}
	return ""
}
