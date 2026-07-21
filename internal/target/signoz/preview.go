package signoz

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// QueryRangeRequest is the strict v5 query API envelope.
type QueryRangeRequest struct {
	SchemaVersion  string                  `json:"schemaVersion"`
	Start          uint64                  `json:"start"`
	End            uint64                  `json:"end"`
	RequestType    string                  `json:"requestType"`
	CompositeQuery CompositeQuery          `json:"compositeQuery"`
	FormatOptions  *FormatOptions          `json:"formatOptions,omitempty"`
	Variables      map[string]VariableItem `json:"variables"`
	NoCache        bool                    `json:"noCache,omitempty"`
}

// FormatOptions mirrors the dashboard frontend's v5 result-format contract.
// Both booleans are intentionally serialized even when false.
type FormatOptions struct {
	FormatTableResultForUI bool `json:"formatTableResultForUI"`
	FillGaps               bool `json:"fillGaps"`
}

// CompositeQuery contains the queries evaluated as one panel.
type CompositeQuery struct {
	Queries []QueryEnvelope `json:"queries"`
}

// QueryEnvelope selects one v5 query specification.
type QueryEnvelope struct {
	Type string `json:"type"`
	Spec any    `json:"spec"`
}

// VariableItem supplies one scalar or scalar-list value during dashboard
// execution and preview validation. This mirrors querybuildertypesv5.VariableItem
// in the pinned SigNoz API; multi-select query/custom variables travel as
// arrays, while dynamic All uses the string sentinel "__all__".
type VariableItem struct {
	Type  string `json:"type"`
	Value any    `json:"value"`
}

// BuilderQuerySpec is the canonical v5 metric Builder query.
type BuilderQuerySpec struct {
	Name         string                  `json:"name"`
	StepInterval *int                    `json:"stepInterval"`
	Signal       string                  `json:"signal"`
	Source       string                  `json:"source"`
	Aggregations []MetricAggregationSpec `json:"aggregations"`
	Disabled     bool                    `json:"disabled"`
	Filter       Expression              `json:"filter"`
	GroupBy      []GroupBy               `json:"groupBy,omitempty"`
	Having       Expression              `json:"having"`
	Functions    []Function              `json:"functions,omitempty"`
	Legend       string                  `json:"legend,omitempty"`
}

// MetricAggregationSpec is the query-range representation of a persisted
// metric aggregation. The dashboard frontend drops absent and empty
// temporalities instead of serializing the persisted null value.
type MetricAggregationSpec struct {
	MetricName       string `json:"metricName,omitempty"`
	Temporality      string `json:"temporality,omitempty"`
	TimeAggregation  string `json:"timeAggregation,omitempty"`
	SpaceAggregation string `json:"spaceAggregation,omitempty"`
	ReduceTo         string `json:"reduceTo,omitempty"`
}

// FormulaSpec is the canonical v5 Builder formula.
type FormulaSpec struct {
	Name       string `json:"name"`
	Expression string `json:"expression"`
	Disabled   bool   `json:"disabled"`
	Legend     string `json:"legend,omitempty"`
}

// PromQLSpec is the canonical v5 PromQL query.
type PromQLSpec struct {
	Name     string `json:"name"`
	Query    string `json:"query"`
	Disabled bool   `json:"disabled"`
	Step     int    `json:"step,omitempty"`
	Stats    bool   `json:"stats"`
	Legend   string `json:"legend,omitempty"`
}

// PreviewResult is SigNoz's dry-run verdict for one named query.
type PreviewResult struct {
	Valid      bool              `json:"valid"`
	Error      json.RawMessage   `json:"error"`
	Warnings   []json.RawMessage `json:"warnings"`
	Statements []json.RawMessage `json:"statements"`
}

// Preview validates a v5 request without executing it.
func (client *Client) Preview(ctx context.Context, request QueryRangeRequest) (map[string]PreviewResult, error) {
	var response struct {
		Data struct {
			CompositeQuery map[string]PreviewResult `json:"compositeQuery"`
		} `json:"data"`
	}
	if err := client.do(ctx, http.MethodPost, "/api/v5/query_range/preview", nil, request, &response); err != nil {
		return nil, err
	}
	return response.Data.CompositeQuery, nil
}

// PreviewRequestForWidget converts a stored dashboard widget into a strict v5 request.
func PreviewRequestForWidget(widget Widget, values map[string]string, now time.Time) (QueryRangeRequest, error) {
	return PreviewRequestForWidgetWindow(widget, values, now, time.Hour)
}

// PreviewRequestForWidgetWindow converts a widget using an explicit validation lookback.
func PreviewRequestForWidgetWindow(widget Widget, values map[string]string, now time.Time, window time.Duration) (QueryRangeRequest, error) {
	return PreviewRequestForWidgetWindowWithVariableTypes(widget, scalarVariableValues(values), nil, now, window)
}

// PreviewRequestForWidgetWindowWithVariableTypes preserves the runtime types
// of variables emitted in the stored dashboard while enabling cache bypass for
// live validation.
func PreviewRequestForWidgetWindowWithVariableTypes(
	widget Widget,
	values map[string]any,
	variableTypes map[string]string,
	now time.Time,
	window time.Duration,
) (QueryRangeRequest, error) {
	request, err := dashboardRequestForWidgetWindow(widget, values, variableTypes, now, window)
	if err != nil {
		return QueryRangeRequest{}, err
	}
	// Live validation deliberately bypasses cached results. This flag is not
	// part of the request generated by the dashboard frontend.
	request.NoCache = true
	return request, nil
}

// DashboardRequestForWidgetWindow mirrors the v5 query-range payload generated
// by SigNoz's pinned dashboard frontend for the stored widget. In particular,
// dashboard PromQL leaves step unset so the backend selects its range-aware
// metric interval.
func DashboardRequestForWidgetWindow(
	widget Widget,
	values map[string]string,
	now time.Time,
	window time.Duration,
) (QueryRangeRequest, error) {
	return DashboardRequestForWidgetWindowWithVariableTypes(widget, scalarVariableValues(values), nil, now, window)
}

// DashboardRequestForWidgetWindowWithVariableTypes constructs the exact
// dashboard request while retaining dynamic/custom/text variable behavior.
func DashboardRequestForWidgetWindowWithVariableTypes(
	widget Widget,
	values map[string]any,
	variableTypes map[string]string,
	now time.Time,
	window time.Duration,
) (QueryRangeRequest, error) {
	return dashboardRequestForWidgetWindow(widget, values, variableTypes, now, window)
}

func dashboardRequestForWidgetWindow(
	widget Widget,
	values map[string]any,
	variableTypes map[string]string,
	now time.Time,
	window time.Duration,
) (QueryRangeRequest, error) {
	if window <= 0 {
		window = time.Hour
	}
	queries := make([]QueryEnvelope, 0)
	switch widget.Query.QueryType {
	case "builder":
		for _, query := range widget.Query.Builder.QueryData {
			groupBy := make([]GroupBy, 0, len(query.GroupBy))
			for _, field := range query.GroupBy {
				groupBy = append(groupBy, GroupBy{
					Name:          field.Key,
					FieldDataType: field.DataType,
					FieldContext:  field.Type,
				})
			}
			spec := BuilderQuerySpec{
				Name:         query.QueryName,
				StepInterval: optionalStepInterval(query.StepInterval),
				Signal:       "metrics",
				Source:       "",
				Aggregations: queryRangeMetricAggregations(query.Aggregations),
				Disabled:     query.Disabled,
				Filter:       query.Filter,
				GroupBy:      groupBy,
				Having:       query.Having,
				Functions:    query.Functions,
				Legend:       query.Legend,
			}
			queries = append(queries, QueryEnvelope{Type: "builder_query", Spec: spec})
		}
		for _, formula := range widget.Query.Builder.QueryFormulas {
			queries = append(queries, QueryEnvelope{Type: "builder_formula", Spec: FormulaSpec{
				Name: formula.QueryName, Expression: formula.Expression, Disabled: formula.Disabled, Legend: formula.Legend,
			}})
		}
	case "promql":
		for _, query := range widget.Query.PromQL {
			queries = append(queries, QueryEnvelope{Type: "promql", Spec: PromQLSpec{
				Name: query.Name, Query: query.Query, Disabled: query.Disabled, Stats: false, Legend: query.Legend,
			}})
		}
	default:
		return QueryRangeRequest{}, fmt.Errorf("widget %q uses unsupported query type %q", widget.Title, widget.Query.QueryType)
	}

	variables, err := VariableItems(values, variableTypes)
	if err != nil {
		return QueryRangeRequest{}, err
	}
	end := now.UnixMilli()
	start := now.Add(-window).UnixMilli()
	if start < 0 || end < 0 {
		return QueryRangeRequest{}, fmt.Errorf("widget %q validation window must not precede the Unix epoch", widget.Title)
	}
	return QueryRangeRequest{
		SchemaVersion:  "v1",
		Start:          uint64(start),
		End:            uint64(end),
		RequestType:    requestType(widget.PanelTypes),
		CompositeQuery: CompositeQuery{Queries: queries},
		FormatOptions: &FormatOptions{
			FormatTableResultForUI: widget.PanelTypes == "table",
			FillGaps:               false,
		},
		Variables: variables,
	}, nil
}

// VariableItems normalizes resolved dashboard variable values into v5 variable
// items keyed by name, defaulting an unknown variable to the query type.
func VariableItems(values map[string]any, variableTypes map[string]string) (map[string]VariableItem, error) {
	variables := make(map[string]VariableItem, len(values))
	for name, value := range values {
		variableType := variableTypes[name]
		if variableType == "" {
			variableType = "query"
		}
		normalized, err := normalizeVariableValue(value)
		if err != nil {
			return nil, fmt.Errorf("dashboard variable %q: %w", name, err)
		}
		variables[name] = VariableItem{Type: variableType, Value: normalized}
	}
	return variables, nil
}

func optionalStepInterval(value int) *int {
	if value <= 0 {
		return nil
	}
	result := value
	return &result
}

func queryRangeMetricAggregations(aggregations []MetricAggregation) []MetricAggregationSpec {
	result := make([]MetricAggregationSpec, 0, len(aggregations))
	for _, aggregation := range aggregations {
		temporality := ""
		if aggregation.Temporality != nil {
			temporality = *aggregation.Temporality
		}
		result = append(result, MetricAggregationSpec{
			MetricName:       aggregation.MetricName,
			Temporality:      temporality,
			TimeAggregation:  aggregation.TimeAggregation,
			SpaceAggregation: aggregation.SpaceAggregation,
			ReduceTo:         aggregation.ReduceTo,
		})
	}
	return result
}

// DashboardVariableTypes indexes the API variable type for every named v5
// dashboard variable. The stored schema uses uppercase display types while the
// query API uses lowercase discriminator values.
func DashboardVariableTypes(dashboard DashboardV5) map[string]string {
	result := make(map[string]string, len(dashboard.Variables))
	for _, variable := range dashboard.Variables {
		if variable.Name == "" {
			continue
		}
		switch variable.Type {
		case "DYNAMIC":
			result[variable.Name] = "dynamic"
		case "CUSTOM":
			result[variable.Name] = "custom"
		case "TEXTBOX":
			result[variable.Name] = "text"
		default:
			result[variable.Name] = "query"
		}
	}
	return result
}

func requestType(panelType string) string {
	switch panelType {
	case "value", "table", "pie":
		return "scalar"
	case "histogram":
		// Legacy v5 dashboards use the distribution response and render its
		// buckets. The newer v6/Perses dashboard path bins time_series
		// client-side, but this project deliberately emits version "v5".
		return "distribution"
	default:
		return "time_series"
	}
}

func scalarVariableValues(values map[string]string) map[string]any {
	if values == nil {
		return nil
	}
	result := make(map[string]any, len(values))
	for name, value := range values {
		result[name] = value
	}
	return result
}

func normalizeVariableValue(value any) (any, error) {
	switch typed := value.(type) {
	case string, bool, json.Number,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return typed, nil
	case []string:
		values := make([]string, len(typed))
		copy(values, typed)
		return values, nil
	case []any:
		values := make([]any, len(typed))
		for index, item := range typed {
			normalized, err := normalizeVariableScalar(item)
			if err != nil {
				return nil, fmt.Errorf("value %d: %w", index, err)
			}
			values[index] = normalized
		}
		return values, nil
	default:
		return nil, fmt.Errorf("value has unsupported type %T; expected a string, number, boolean, or list of those scalars", value)
	}
}

func normalizeVariableScalar(value any) (any, error) {
	switch typed := value.(type) {
	case string, bool, json.Number,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return typed, nil
	default:
		return nil, fmt.Errorf("has unsupported type %T", value)
	}
}
