// Package perses emits the SigNoz v6 (Perses-native) dashboard shape used by the
// v2 dashboard API (POST /api/v2/dashboards). It is a transform of the verified
// v5 dashboard into the Perses spec, so the v5 emission remains the single source
// of query truth and the v6 shape rides on top of it. The v5 path stays the
// verified primary import target; v6 is emitted alongside it behind an opt-in
// flag until the exact SigNoz release schema is pinned.
package perses

import (
	"strings"

	"github.com/mansiverma897993/noz-in/internal/target/signoz"
)

// SchemaVersion is the Perses dashboard schema version string SigNoz's v2 API
// expects. Reconcile against the pinned SigNoz release before live import.
const SchemaVersion = "v6"

// PostableDashboardV2 is the body of POST /api/v2/dashboards.
type PostableDashboardV2 struct {
	SchemaVersion string        `json:"schemaVersion"`
	GenerateName  bool          `json:"generateName,omitempty"`
	Name          string        `json:"name,omitempty"`
	Image         string        `json:"image,omitempty"`
	Tags          []Tag         `json:"tags"`
	Spec          DashboardSpec `json:"spec"`
}

// Tag is a v2 dashboard tag.
type Tag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// DashboardSpec is the Perses dashboard spec.
type DashboardSpec struct {
	Display   Display          `json:"display"`
	Variables []Variable       `json:"variables"`
	Panels    map[string]Panel `json:"panels"`
	Layouts   []Layout         `json:"layouts"`
}

// Display is a Perses display block.
type Display struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Panel is a Perses panel (kind "Panel").
type Panel struct {
	Kind string    `json:"kind"`
	Spec PanelSpec `json:"spec"`
}

// PanelSpec holds a panel's display, plugin, and queries.
type PanelSpec struct {
	Display Display `json:"display"`
	Plugin  Plugin  `json:"plugin"`
	Queries []Query `json:"queries"`
}

// Plugin is a Perses plugin envelope (panel or query implementation).
type Plugin struct {
	Kind string `json:"kind"`
	Spec any    `json:"spec"`
}

// Query is a Perses query entry inside a panel.
type Query struct {
	Kind string    `json:"kind"`
	Spec QuerySpec `json:"spec"`
}

// QuerySpec wraps the query plugin.
type QuerySpec struct {
	Plugin Plugin `json:"plugin"`
}

// Layout is a Perses layout (kind "Grid").
type Layout struct {
	Kind string     `json:"kind"`
	Spec LayoutSpec `json:"spec"`
}

// LayoutSpec holds grid items.
type LayoutSpec struct {
	Items []LayoutItem `json:"items"`
}

// LayoutItem places a panel on the grid via a JSON reference.
type LayoutItem struct {
	X       int        `json:"x"`
	Y       int        `json:"y"`
	Width   int        `json:"width"`
	Height  int        `json:"height"`
	Content ContentRef `json:"content"`
}

// ContentRef references a panel by its spec path.
type ContentRef struct {
	Ref string `json:"$ref"`
}

// Variable is a Perses variable (ListVariable or TextVariable).
type Variable struct {
	Kind string       `json:"kind"`
	Spec VariableSpec `json:"spec"`
}

// VariableSpec holds a variable's identity and plugin.
type VariableSpec struct {
	Name          string   `json:"name"`
	Display       Display  `json:"display"`
	AllowMultiple bool     `json:"allowMultiple,omitempty"`
	AllowAllValue bool     `json:"allowAllValue,omitempty"`
	Sort          string   `json:"sort,omitempty"`
	Value         string   `json:"value,omitempty"`
	Plugin        *Plugin  `json:"plugin,omitempty"`
	Values        []string `json:"values,omitempty"`
}

// FromV5 transforms a verified v5 dashboard into the Perses v6 shape.
func FromV5(dashboard signoz.DashboardV5) PostableDashboardV2 {
	panels := make(map[string]Panel, len(dashboard.Widgets))
	for _, widget := range dashboard.Widgets {
		kind, ok := panelPluginKind(widget.PanelTypes)
		if !ok {
			// Rows and empty widgets carry no Perses panel; they are layout only.
			continue
		}
		panels[widget.ID] = Panel{
			Kind: "Panel",
			Spec: PanelSpec{
				Display: Display{Name: widget.Title, Description: widget.Description},
				Plugin: Plugin{Kind: kind, Spec: map[string]any{
					"formatting": map[string]any{"unit": widget.YAxisUnit},
				}},
				Queries: queriesFromWidget(widget.Query),
			},
		}
	}

	items := make([]LayoutItem, 0, len(dashboard.Layout))
	for _, layout := range dashboard.Layout {
		if _, ok := panels[layout.I]; !ok {
			continue
		}
		items = append(items, LayoutItem{
			X: layout.X, Y: layout.Y, Width: layout.W, Height: layout.H,
			Content: ContentRef{Ref: "#/spec/panels/" + layout.I},
		})
	}

	return PostableDashboardV2{
		SchemaVersion: SchemaVersion,
		GenerateName:  true,
		Tags:          tagsFromV5(dashboard.Tags),
		Spec: DashboardSpec{
			Display:   Display{Name: dashboard.Title, Description: dashboard.Description},
			Variables: variablesFromV5(dashboard.Variables),
			Panels:    panels,
			Layouts:   []Layout{{Kind: "Grid", Spec: LayoutSpec{Items: items}}},
		},
	}
}

// panelPluginKind maps a v5 panel type to its Perses signoz plugin kind. Rows and
// empty widgets have no panel plugin.
func panelPluginKind(panelType string) (string, bool) {
	switch panelType {
	case "graph":
		return "signoz/TimeSeriesPanel", true
	case "value":
		return "signoz/StatChartPanel", true
	case "table":
		return "signoz/TablePanel", true
	case "bar":
		return "signoz/BarChartPanel", true
	case "pie":
		return "signoz/PieChartPanel", true
	case "histogram":
		return "signoz/HistogramChartPanel", true
	default:
		return "", false
	}
}

// queriesFromWidget wraps the v5 query bodies (already the query source of truth)
// in Perses query envelopes. The inner spec reuses the v5 query shape.
func queriesFromWidget(query signoz.WidgetQuery) []Query {
	switch query.QueryType {
	case "promql":
		queries := make([]Query, 0, len(query.PromQL))
		for _, promql := range query.PromQL {
			if promql.Disabled || strings.TrimSpace(promql.Query) == "" {
				continue
			}
			queries = append(queries, Query{
				Kind: "TimeSeriesQuery",
				Spec: QuerySpec{Plugin: Plugin{Kind: "signoz/PromQLQuery", Spec: promql}},
			})
		}
		return queries
	case "builder":
		return []Query{{
			Kind: "TimeSeriesQuery",
			Spec: QuerySpec{Plugin: Plugin{Kind: "signoz/BuilderQuery", Spec: query.Builder}},
		}}
	default:
		return []Query{}
	}
}

func tagsFromV5(tags []string) []Tag {
	result := make([]Tag, 0, len(tags))
	for _, tag := range tags {
		key, value, found := strings.Cut(tag, ":")
		if !found {
			key, value = "tag", tag
		}
		result = append(result, Tag{Key: key, Value: value})
	}
	return result
}

// variablesFromV5 maps v5 variables to Perses list/text variables.
func variablesFromV5(variables map[string]signoz.VariableV5) []Variable {
	result := make([]Variable, 0, len(variables))
	for _, variable := range variables {
		switch strings.ToUpper(variable.Type) {
		case "TEXTBOX", "CONSTANT":
			result = append(result, Variable{
				Kind: "TextVariable",
				Spec: VariableSpec{
					Name:    variable.Name,
					Display: Display{Name: variable.Name, Description: variable.Description},
					Value:   textValue(variable),
				},
			})
		case "CUSTOM":
			result = append(result, Variable{
				Kind: "ListVariable",
				Spec: VariableSpec{
					Name:          variable.Name,
					Display:       Display{Name: variable.Name, Description: variable.Description},
					AllowMultiple: variable.MultiSelect,
					AllowAllValue: variable.ShowAllOption,
					Sort:          "none",
					Values:        customValues(variable.CustomValue),
				},
			})
		default: // DYNAMIC / QUERY
			result = append(result, Variable{
				Kind: "ListVariable",
				Spec: VariableSpec{
					Name:          variable.Name,
					Display:       Display{Name: variable.Name, Description: variable.Description},
					AllowMultiple: variable.MultiSelect,
					AllowAllValue: variable.ShowAllOption,
					Sort:          "none",
					Plugin: &Plugin{Kind: "signoz/DynamicVariable", Spec: map[string]any{
						"name":   variable.DynamicVariablesAttribute,
						"signal": variable.DynamicVariablesSource,
					}},
				},
			})
		}
	}
	return result
}

func textValue(variable signoz.VariableV5) string {
	if variable.TextboxValue != "" {
		return variable.TextboxValue
	}
	return variable.CustomValue
}

func customValues(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			values = append(values, trimmed)
		}
	}
	return values
}
