package grafana

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type rawDashboard struct {
	Title         string                     `json:"title"`
	Description   string                     `json:"description"`
	UID           string                     `json:"uid"`
	Tags          []string                   `json:"tags"`
	SchemaVersion int                        `json:"schemaVersion"`
	Panels        []rawPanel                 `json:"panels"`
	Rows          []rawRow                   `json:"rows"`
	Templating    rawTemplating              `json:"templating"`
	Inputs        []rawInput                 `json:"__inputs"`
	Annotations   rawAnnotations             `json:"annotations"`
	Links         []json.RawMessage          `json:"links"`
	Unmapped      map[string]json.RawMessage `json:"-"`
}

type rawRow struct {
	ID        json.RawMessage            `json:"id"`
	Title     string                     `json:"title"`
	Collapse  bool                       `json:"collapse"`
	Collapsed bool                       `json:"collapsed"`
	Height    json.RawMessage            `json:"height"`
	Panels    []rawPanel                 `json:"panels"`
	Unmapped  map[string]json.RawMessage `json:"-"`
}

type rawAnnotations struct {
	List     []rawAnnotation            `json:"list"`
	Unmapped map[string]json.RawMessage `json:"-"`
}

type rawAnnotation struct {
	Name               string                     `json:"name"`
	Expr               string                     `json:"expr"`
	Query              string                     `json:"query"`
	Datasource         json.RawMessage            `json:"datasource"`
	Raw                json.RawMessage            `json:"-"`
	DatasourceUnmapped map[string]json.RawMessage `json:"-"`
	Unmapped           map[string]json.RawMessage `json:"-"`
}

type rawPanel struct {
	ID                 json.RawMessage            `json:"id"`
	Title              string                     `json:"title"`
	Description        string                     `json:"description"`
	Type               string                     `json:"type"`
	GridPos            *rawGrid                   `json:"gridPos"`
	Span               flexibleNumber             `json:"span"`
	Collapsed          bool                       `json:"collapsed"`
	Panels             []rawPanel                 `json:"panels"`
	Targets            []rawTarget                `json:"targets"`
	Datasource         json.RawMessage            `json:"datasource"`
	Repeat             json.RawMessage            `json:"repeat"`
	FieldConfig        rawFieldConfig             `json:"fieldConfig"`
	YAxes              []rawAxis                  `json:"yaxes"`
	Format             string                     `json:"format"`
	Options            rawPanelOptions            `json:"options"`
	Content            string                     `json:"content"`
	Transforms         []rawTransform             `json:"transformations"`
	TimeFrom           string                     `json:"timeFrom"`
	TimeShift          string                     `json:"timeShift"`
	Interval           string                     `json:"interval"`
	MaxDataPoints      flexibleNumber             `json:"maxDataPoints"`
	Alert              json.RawMessage            `json:"alert"`
	Links              []json.RawMessage          `json:"links"`
	LibraryPanel       json.RawMessage            `json:"libraryPanel"`
	DatasourceUnmapped map[string]json.RawMessage `json:"-"`
	Unmapped           map[string]json.RawMessage `json:"-"`
}

type rawPanelOptions struct {
	Content  string                     `json:"content"`
	Unmapped map[string]json.RawMessage `json:"-"`
}

type rawTransform struct {
	ID       string                     `json:"id"`
	Options  json.RawMessage            `json:"options"`
	Unmapped map[string]json.RawMessage `json:"-"`
}

type rawFieldConfig struct {
	Defaults  rawFieldDefaults           `json:"defaults"`
	Overrides []json.RawMessage          `json:"overrides"`
	Unmapped  map[string]json.RawMessage `json:"-"`
}

type rawFieldDefaults struct {
	Unit       string                     `json:"unit"`
	Thresholds json.RawMessage            `json:"thresholds"`
	Unmapped   map[string]json.RawMessage `json:"-"`
}

type rawAxis struct {
	Format    string                     `json:"format"`
	FormatRaw json.RawMessage            `json:"-"`
	Unmapped  map[string]json.RawMessage `json:"-"`
}

type rawGrid struct {
	X        int                        `json:"x"`
	Y        int                        `json:"y"`
	W        int                        `json:"w"`
	H        int                        `json:"h"`
	Unmapped map[string]json.RawMessage `json:"-"`
}

type rawTarget struct {
	RefID              string                     `json:"refId"`
	Expr               string                     `json:"expr"`
	Legend             string                     `json:"legendFormat"`
	Hide               bool                       `json:"hide"`
	Instant            bool                       `json:"instant"`
	Step               presentFlexibleNumber      `json:"step"`
	Range              json.RawMessage            `json:"range"`
	Exemplar           json.RawMessage            `json:"exemplar"`
	Format             string                     `json:"format"`
	QueryType          string                     `json:"queryType"`
	Type               string                     `json:"type"`
	Expression         string                     `json:"expression"`
	Interval           string                     `json:"interval"`
	IntervalFactor     flexibleNumber             `json:"intervalFactor"`
	MaxDataPoints      flexibleNumber             `json:"maxDataPoints"`
	Datasource         json.RawMessage            `json:"datasource"`
	DatasourceUnmapped map[string]json.RawMessage `json:"-"`
	Unmapped           map[string]json.RawMessage `json:"-"`
}

type rawTemplating struct {
	List     []rawVariable              `json:"list"`
	Unmapped map[string]json.RawMessage `json:"-"`
}

type rawVariable struct {
	Name               string                     `json:"name"`
	Label              string                     `json:"label"`
	Type               string                     `json:"type"`
	Query              json.RawMessage            `json:"query"`
	Current            json.RawMessage            `json:"current"`
	Multi              bool                       `json:"multi"`
	IncludeAll         bool                       `json:"includeAll"`
	AllValue           string                     `json:"allValue"`
	Regex              string                     `json:"regex"`
	Datasource         json.RawMessage            `json:"datasource"`
	QueryUnmapped      map[string]json.RawMessage `json:"-"`
	CurrentUnmapped    map[string]json.RawMessage `json:"-"`
	DatasourceUnmapped map[string]json.RawMessage `json:"-"`
	Unmapped           map[string]json.RawMessage `json:"-"`
}

type rawInput struct {
	Name       string                     `json:"name"`
	Type       string                     `json:"type"`
	PluginID   string                     `json:"pluginId"`
	PluginName string                     `json:"pluginName"`
	Unmapped   map[string]json.RawMessage `json:"-"`
}

type flexibleNumber float64

type presentFlexibleNumber struct {
	Value flexibleNumber
	Raw   json.RawMessage
}

func (number *presentFlexibleNumber) UnmarshalJSON(data []byte) error {
	var value flexibleNumber
	if err := value.UnmarshalJSON(data); err != nil {
		return err
	}
	number.Value = value
	number.Raw = append(number.Raw[:0], data...)
	return nil
}

func (number *flexibleNumber) UnmarshalJSON(data []byte) error {
	value := strings.TrimSpace(string(data))
	if value == "" || value == "null" {
		*number = 0
		return nil
	}
	if strings.HasPrefix(value, `"`) {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		value = strings.TrimSpace(text)
		if value == "" {
			*number = 0
			return nil
		}
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return fmt.Errorf("expected a finite number, numeric string, empty string, or null, got %q", value)
	}
	*number = flexibleNumber(parsed)
	return nil
}

func (dashboard *rawDashboard) UnmarshalJSON(data []byte) error {
	type rawDashboardAlias rawDashboard
	var decoded rawDashboardAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*dashboard = rawDashboard(decoded)
	unmapped, err := unmappedJSONFieldsPresent(data, []string{
		"title", "description", "uid", "tags", "schemaVersion", "panels", "rows", "templating",
		"__inputs", "annotations", "links",
		// Artifact identity, revision, and plugin-dependency metadata do not
		// affect dashboard execution or presentation.
		"id", "version", "gnetId", "iteration", "__requires",
	})
	if err != nil {
		return err
	}
	dashboard.Unmapped = unmapped
	return nil
}

func (row *rawRow) UnmarshalJSON(data []byte) error {
	type rawRowAlias rawRow
	var decoded rawRowAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*row = rawRow(decoded)
	unmapped, err := unmappedJSONFieldsPresent(data, []string{"id", "title", "collapse", "collapsed", "height", "panels"})
	if err != nil {
		return err
	}
	row.Unmapped = unmapped
	return nil
}

func (annotations *rawAnnotations) UnmarshalJSON(data []byte) error {
	type rawAnnotationsAlias rawAnnotations
	var decoded rawAnnotationsAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*annotations = rawAnnotations(decoded)
	unmapped, err := unmappedJSONFieldsPresent(data, []string{"list"})
	if err != nil {
		return err
	}
	annotations.Unmapped = unmapped
	return nil
}

func (annotation *rawAnnotation) UnmarshalJSON(data []byte) error {
	type rawAnnotationAlias rawAnnotation
	var decoded rawAnnotationAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*annotation = rawAnnotation(decoded)
	annotation.Raw = append(annotation.Raw[:0], data...)
	unmapped, err := unmappedJSONFieldsPresent(data, []string{"name", "expr", "query", "datasource"})
	if err != nil {
		return err
	}
	annotation.Unmapped = unmapped
	annotation.DatasourceUnmapped, err = unmappedJSONObjectFields(annotation.Datasource, []string{"type", "uid", "name"})
	return err
}

func (templating *rawTemplating) UnmarshalJSON(data []byte) error {
	type rawTemplatingAlias rawTemplating
	var decoded rawTemplatingAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*templating = rawTemplating(decoded)
	unmapped, err := unmappedJSONFieldsPresent(data, []string{"list"})
	if err != nil {
		return err
	}
	templating.Unmapped = unmapped
	return nil
}

func (input *rawInput) UnmarshalJSON(data []byte) error {
	type rawInputAlias rawInput
	var decoded rawInputAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*input = rawInput(decoded)
	unmapped, err := unmappedJSONFieldsPresent(data, []string{"name", "type", "pluginId", "pluginName"})
	if err != nil {
		return err
	}
	input.Unmapped = unmapped
	return nil
}

func (grid *rawGrid) UnmarshalJSON(data []byte) error {
	type rawGridAlias rawGrid
	var decoded rawGridAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*grid = rawGrid(decoded)
	unmapped, err := unmappedJSONFieldsPresent(data, []string{"x", "y", "w", "h"})
	if err != nil {
		return err
	}
	grid.Unmapped = unmapped
	return nil
}

func (target *rawTarget) UnmarshalJSON(data []byte) error {
	type rawTargetAlias rawTarget
	var decoded rawTargetAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*target = rawTarget(decoded)
	unmapped, err := unmappedJSONFieldsPresent(data, []string{
		"refId", "expr", "legendFormat", "hide", "instant", "step", "range", "exemplar", "format",
		"queryType", "type", "expression", "interval", "intervalFactor", "maxDataPoints", "datasource",
	})
	if err != nil {
		return err
	}
	target.Unmapped = unmapped
	target.DatasourceUnmapped, err = unmappedJSONObjectFields(target.Datasource, []string{"type", "uid", "name"})
	return err
}

func (variable *rawVariable) UnmarshalJSON(data []byte) error {
	type rawVariableAlias rawVariable
	var decoded rawVariableAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*variable = rawVariable(decoded)
	unmapped, err := unmappedJSONFieldsPresent(data, []string{
		"name", "label", "type", "query", "current", "multi", "includeAll", "allValue", "regex", "datasource",
	})
	if err != nil {
		return err
	}
	variable.Unmapped = unmapped
	variable.QueryUnmapped, err = unmappedJSONObjectFields(variable.Query, []string{"query"})
	if err != nil {
		return err
	}
	variable.CurrentUnmapped, err = unmappedJSONObjectFields(variable.Current, []string{"value"})
	if err != nil {
		return err
	}
	variable.DatasourceUnmapped, err = unmappedJSONObjectFields(variable.Datasource, []string{"type", "uid", "name"})
	return err
}

func (fieldConfig *rawFieldConfig) UnmarshalJSON(data []byte) error {
	type rawFieldConfigAlias rawFieldConfig
	var decoded rawFieldConfigAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*fieldConfig = rawFieldConfig(decoded)
	unmapped, err := unmappedJSONFieldsPresent(data, []string{"defaults", "overrides"})
	if err != nil {
		return err
	}
	fieldConfig.Unmapped = unmapped
	return nil
}

func (axis *rawAxis) UnmarshalJSON(data []byte) error {
	type rawAxisAlias rawAxis
	var decoded rawAxisAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*axis = rawAxis(decoded)
	fields, err := unmappedJSONFieldsPresent(data, nil)
	if err != nil {
		return err
	}
	axis.FormatRaw = append(axis.FormatRaw[:0], fields["format"]...)
	delete(fields, "format")
	axis.Unmapped = fields
	return nil
}

func (transform *rawTransform) UnmarshalJSON(data []byte) error {
	type rawTransformAlias rawTransform
	var decoded rawTransformAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*transform = rawTransform(decoded)
	unmapped, err := unmappedJSONFieldsPresent(data, []string{"id", "options"})
	if err != nil {
		return err
	}
	transform.Unmapped = unmapped
	return nil
}

func (panel *rawPanel) UnmarshalJSON(data []byte) error {
	type rawPanelAlias rawPanel
	var decoded rawPanelAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*panel = rawPanel(decoded)
	unmapped, err := unmappedJSONFieldsPresent(data, []string{
		"id", "title", "description", "type", "gridPos", "span", "collapsed", "panels", "targets",
		"datasource", "repeat", "fieldConfig", "yaxes", "format", "options", "content", "transformations",
		"timeFrom", "timeShift", "alert", "links", "libraryPanel", "interval", "maxDataPoints",
		"pluginVersion",
	})
	if err != nil {
		return err
	}
	panel.Unmapped = unmapped
	panel.DatasourceUnmapped, err = unmappedJSONObjectFields(panel.Datasource, []string{"type", "uid", "name"})
	return err
}

func (options *rawPanelOptions) UnmarshalJSON(data []byte) error {
	type rawPanelOptionsAlias rawPanelOptions
	var decoded rawPanelOptionsAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*options = rawPanelOptions(decoded)
	unmapped, err := unmappedJSONFieldsPresent(data, []string{"content"})
	if err != nil {
		return err
	}
	options.Unmapped = unmapped
	return nil
}

func (defaults *rawFieldDefaults) UnmarshalJSON(data []byte) error {
	type rawFieldDefaultsAlias rawFieldDefaults
	var decoded rawFieldDefaultsAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*defaults = rawFieldDefaults(decoded)
	unmapped, err := unmappedJSONFieldsPresent(data, []string{"unit", "thresholds"})
	if err != nil {
		return err
	}
	defaults.Unmapped = unmapped
	return nil
}

func unmappedJSONFieldsPresent(data []byte, known []string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for _, name := range known {
		delete(fields, name)
	}
	return fields, nil
}

func unmappedJSONObjectFields(raw json.RawMessage, known []string) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return map[string]json.RawMessage{}, nil
	}
	return unmappedJSONFieldsPresent(trimmed, known)
}
