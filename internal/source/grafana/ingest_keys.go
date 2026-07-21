package grafana

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"unicode"
)

type jsonKeyContext uint8

const (
	jsonKeysUnknown jsonKeyContext = iota
	jsonKeysDashboard
	jsonKeysPanelArray
	jsonKeysPanel
	jsonKeysRowArray
	jsonKeysRow
	jsonKeysTargetArray
	jsonKeysTarget
	jsonKeysTemplating
	jsonKeysVariableArray
	jsonKeysVariable
	jsonKeysInputArray
	jsonKeysInput
	jsonKeysAnnotations
	jsonKeysAnnotationArray
	jsonKeysAnnotation
	jsonKeysGrid
	jsonKeysFieldConfig
	jsonKeysFieldDefaults
	jsonKeysAxisArray
	jsonKeysAxis
	jsonKeysPanelOptions
	jsonKeysTransformArray
	jsonKeysTransform
	jsonKeysDatasource
	jsonKeysVariableQuery
	jsonKeysVariableCurrent
)

var canonicalJSONNamesByContext = map[jsonKeyContext]map[string]string{
	jsonKeysDashboard: canonicalJSONNames(
		"title", "description", "uid", "tags", "schemaVersion", "panels", "rows", "templating",
		"__inputs", "annotations", "links",
	),
	jsonKeysPanel: canonicalJSONNames(
		"id", "title", "description", "type", "gridPos", "span", "collapsed", "panels", "targets",
		"datasource", "repeat", "fieldConfig", "yaxes", "format", "options", "content", "transformations",
		"timeFrom", "timeShift", "interval", "maxDataPoints", "alert", "links", "libraryPanel",
	),
	jsonKeysRow: canonicalJSONNames("id", "title", "collapse", "collapsed", "height", "panels"),
	jsonKeysTarget: canonicalJSONNames(
		"refId", "expr", "legendFormat", "hide", "instant", "step", "range", "exemplar", "format",
		"queryType", "type", "expression", "interval", "intervalFactor", "maxDataPoints", "datasource",
	),
	jsonKeysTemplating:      canonicalJSONNames("list"),
	jsonKeysVariable:        canonicalJSONNames("name", "label", "type", "query", "current", "multi", "includeAll", "allValue", "regex", "datasource"),
	jsonKeysInput:           canonicalJSONNames("name", "type", "pluginId", "pluginName"),
	jsonKeysAnnotations:     canonicalJSONNames("list"),
	jsonKeysAnnotation:      canonicalJSONNames("name", "expr", "query", "datasource"),
	jsonKeysGrid:            canonicalJSONNames("x", "y", "w", "h"),
	jsonKeysFieldConfig:     canonicalJSONNames("defaults", "overrides"),
	jsonKeysFieldDefaults:   canonicalJSONNames("unit", "thresholds"),
	jsonKeysAxis:            canonicalJSONNames("format"),
	jsonKeysPanelOptions:    canonicalJSONNames("content"),
	jsonKeysTransform:       canonicalJSONNames("id", "options"),
	jsonKeysDatasource:      canonicalJSONNames("type", "uid", "name"),
	jsonKeysVariableQuery:   canonicalJSONNames("query"),
	jsonKeysVariableCurrent: canonicalJSONNames("value"),
}

func canonicalJSONNames(names ...string) map[string]string {
	canonical := make(map[string]string, len(names))
	for _, name := range names {
		canonical[foldJSONName(name)] = name
	}
	return canonical
}

// validateJSONObjectKeys rejects ambiguous objects before encoding/json can
// apply its last-value-wins and case-insensitive struct-field matching rules.
// Exact duplicate checking visits every object. Case-folded collisions are
// limited to the typed object contexts where encoding/json would bind both
// spellings to the same canonical field; map-owned keys remain case-sensitive.
func validateJSONObjectKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return validateJSONValueKeys(decoder, "", jsonKeysDashboard)
}

func validateJSONValueKeys(decoder *json.Decoder, path string, context jsonKeyContext) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}

	switch delimiter {
	case '{':
		seenExact := make(map[string]bool)
		seenCanonical := make(map[string]string)
		canonicalNames := canonicalJSONNamesByContext[context]
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("expected JSON object key at %s", displayJSONPointer(path))
			}
			keyPath := path + "/" + jsonPointerSegment(key)
			if seenExact[key] {
				return fmt.Errorf("duplicate JSON object key %q at %s", key, keyPath)
			}
			seenExact[key] = true
			folded := foldJSONName(key)
			canonical, isCanonical := canonicalNames[folded]
			if isCanonical {
				if previous, exists := seenCanonical[folded]; exists {
					return fmt.Errorf(
						"case-insensitive JSON object key collision %q with %q at %s (canonical key %q)",
						key, previous, keyPath, canonical,
					)
				}
				seenCanonical[folded] = key
			}
			if err := validateJSONValueKeys(decoder, keyPath, childJSONKeyContext(context, canonical)); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return fmt.Errorf("expected closing JSON object delimiter at %s", displayJSONPointer(path))
		}
	case '[':
		for index := 0; decoder.More(); index++ {
			if err := validateJSONValueKeys(decoder, path+"/"+strconv.Itoa(index), arrayElementJSONKeyContext(context)); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return fmt.Errorf("expected closing JSON array delimiter at %s", displayJSONPointer(path))
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q at %s", delimiter, displayJSONPointer(path))
	}
	return nil
}

func childJSONKeyContext(parent jsonKeyContext, canonical string) jsonKeyContext {
	switch parent {
	case jsonKeysDashboard:
		switch canonical {
		case "panels":
			return jsonKeysPanelArray
		case "rows":
			return jsonKeysRowArray
		case "templating":
			return jsonKeysTemplating
		case "__inputs":
			return jsonKeysInputArray
		case "annotations":
			return jsonKeysAnnotations
		}
	case jsonKeysPanel:
		switch canonical {
		case "gridPos":
			return jsonKeysGrid
		case "panels":
			return jsonKeysPanelArray
		case "targets":
			return jsonKeysTargetArray
		case "datasource":
			return jsonKeysDatasource
		case "fieldConfig":
			return jsonKeysFieldConfig
		case "yaxes":
			return jsonKeysAxisArray
		case "options":
			return jsonKeysPanelOptions
		case "transformations":
			return jsonKeysTransformArray
		}
	case jsonKeysRow:
		if canonical == "panels" {
			return jsonKeysPanelArray
		}
	case jsonKeysTarget:
		if canonical == "datasource" {
			return jsonKeysDatasource
		}
	case jsonKeysTemplating:
		if canonical == "list" {
			return jsonKeysVariableArray
		}
	case jsonKeysVariable:
		switch canonical {
		case "query":
			return jsonKeysVariableQuery
		case "current":
			return jsonKeysVariableCurrent
		case "datasource":
			return jsonKeysDatasource
		}
	case jsonKeysAnnotations:
		if canonical == "list" {
			return jsonKeysAnnotationArray
		}
	case jsonKeysFieldConfig:
		if canonical == "defaults" {
			return jsonKeysFieldDefaults
		}
	}
	return jsonKeysUnknown
}

func arrayElementJSONKeyContext(parent jsonKeyContext) jsonKeyContext {
	switch parent {
	case jsonKeysPanelArray:
		return jsonKeysPanel
	case jsonKeysRowArray:
		return jsonKeysRow
	case jsonKeysTargetArray:
		return jsonKeysTarget
	case jsonKeysVariableArray:
		return jsonKeysVariable
	case jsonKeysInputArray:
		return jsonKeysInput
	case jsonKeysAnnotationArray:
		return jsonKeysAnnotation
	case jsonKeysAxisArray:
		return jsonKeysAxis
	case jsonKeysTransformArray:
		return jsonKeysTransform
	default:
		return jsonKeysUnknown
	}
}

// foldJSONName mirrors encoding/json's Unicode simple-fold equivalence class
// selection. That makes the preflight collision check cover the same names the
// typed decoder would consider equal, including the Kelvin sign and long s.
func foldJSONName(value string) string {
	folded := make([]rune, 0, len(value))
	for _, current := range value {
		for {
			next := unicode.SimpleFold(current)
			if next <= current {
				folded = append(folded, next)
				break
			}
			current = next
		}
	}
	return string(folded)
}

func displayJSONPointer(path string) string {
	if path == "" {
		return "/"
	}
	return path
}
