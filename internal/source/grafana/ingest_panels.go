package grafana

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/mansiverma897993/noz-in/internal/model"
)

func panelUnit(panel rawPanel) string {
	if unit := strings.TrimSpace(panel.FieldConfig.Defaults.Unit); unit != "" {
		return unit
	}
	if unit := strings.TrimSpace(panel.Format); unit != "" {
		return unit
	}
	for _, axis := range panel.YAxes {
		if unit := strings.TrimSpace(axis.Format); unit != "" && unit != "short" {
			return unit
		}
	}
	if strings.EqualFold(strings.TrimSpace(panel.Type), "graph") {
		// Legacy Grafana graph axes default to the short K/Mil/Bil formatter.
		// SigNoz's "none" unit is raw decimal, so absence is not equivalent.
		return "short"
	}
	return ""
}

func walkPanel(destination *[]model.Panel, raw rawPanel, path string, yCursor *int, variables map[string]string) {
	grid := normalizeGrid(raw.GridPos, float64(raw.Span), *yCursor)
	if raw.GridPos == nil {
		*yCursor = max(*yCursor, grid.Y+grid.H)
	} else {
		*yCursor = max(*yCursor, raw.GridPos.Y+raw.GridPos.H)
	}

	panelDatasource := normalizeDatasource(raw.Datasource, variables)
	panel := model.Panel{
		ID:          normalizeID(raw.ID, path),
		Title:       raw.Title,
		Kind:        normalizePanelKind(raw.Type),
		SourceType:  raw.Type,
		Description: raw.Description,
		Content:     firstNonEmpty(raw.Options.Content, raw.Content),
		Unit:        panelUnit(raw),
		Grid:        grid,
		Datasource:  panelDatasource,
		Repeat:      stringValue(raw.Repeat),
		TimeFrom:    raw.TimeFrom,
		TimeShift:   raw.TimeShift,
		Collapsed:   raw.Collapsed,
		SourcePath:  path,
	}
	panel.SourceFeatures = panelSourceFeatures(raw, path)
	for _, transform := range raw.Transforms {
		if id := strings.TrimSpace(transform.ID); id != "" && !slices.Contains(panel.Transforms, id) {
			panel.Transforms = append(panel.Transforms, id)
		}
	}
	reserved := make(map[string]bool, len(raw.Targets))
	for _, target := range raw.Targets {
		if refID := target.RefID; refID == strings.TrimSpace(refID) && queryNamePattern.MatchString(refID) {
			reserved[refID] = true
		}
	}
	used := make(map[string]bool, len(raw.Targets))
	for index, target := range raw.Targets {
		datasource := normalizeDatasource(target.Datasource, variables)
		if datasource == (model.Datasource{}) {
			datasource = panelDatasource
		}
		refID, normalized := uniqueRefID(target.RefID, reserved, used)
		queryPath := fmt.Sprintf("%s/targets/%d", path, index)
		maxDataPoints := float64(target.MaxDataPoints)
		if maxDataPoints <= 0 {
			maxDataPoints = float64(raw.MaxDataPoints)
		}
		panel.Queries = append(panel.Queries, model.Query{
			RefID:           refID,
			Expression:      strings.TrimSpace(firstNonEmpty(target.Expr, target.Expression)),
			Legend:          target.Legend,
			Hidden:          target.Hide,
			Instant:         target.Instant,
			Format:          target.Format,
			QueryType:       firstNonEmpty(target.QueryType, target.Type),
			Step:            normalizedPositiveInt(target.Step.Value),
			Interval:        firstNonEmpty(target.Interval, raw.Interval),
			IntervalFactor:  max(int(math.Ceil(float64(target.IntervalFactor))), 0),
			MaxDataPoints:   max(int(math.Ceil(maxDataPoints)), 0),
			Datasource:      datasource,
			SourcePath:      queryPath,
			OriginalRefID:   target.RefID,
			RefIDNormalized: normalized,
			SourceFeatures:  targetSourceFeatures(target, queryPath),
		})
	}
	*destination = append(*destination, panel)

	for index, child := range raw.Panels {
		walkPanel(destination, child, fmt.Sprintf("%s/panels/%d", path, index), yCursor, variables)
	}
}

func walkLegacyRow(destination *[]model.Panel, row rawRow, rowIndex int, yCursor *int, variables map[string]string) {
	rowPath := fmt.Sprintf("/rows/%d", rowIndex)
	collapsed := row.Collapsed || row.Collapse
	header := rawPanel{
		ID: row.ID, Title: row.Title, Type: "row", Collapsed: collapsed,
		GridPos: &rawGrid{X: 0, Y: *yCursor, W: 24, H: 1},
	}
	headerIndex := len(*destination)
	walkPanel(destination, header, rowPath, yCursor, variables)
	(*destination)[headerIndex].SourceFeatures = append(
		(*destination)[headerIndex].SourceFeatures,
		rowSourceFeatures(row, rowPath)...,
	)

	headerBottom := *yCursor
	childCursor := headerBottom
	x := 0
	y := childCursor
	height := legacyHeight(row.Height)
	lineBottom := y
	for panelIndex, panel := range row.Panels {
		span := 12
		if panel.Span > 0 {
			span = min(max(int(math.Ceil(float64(panel.Span))), 1), 12)
		}
		if x+span > 12 {
			x = 0
			y = lineBottom
		}
		panel.GridPos = &rawGrid{X: x * 2, Y: y, W: span * 2, H: height}
		walkPanel(destination, panel, fmt.Sprintf("%s/panels/%d", rowPath, panelIndex), &childCursor, variables)
		lineBottom = max(lineBottom, y+height)
		x += span
	}
	if collapsed {
		*yCursor = headerBottom
		return
	}
	*yCursor = max(childCursor, lineBottom)
}

func legacyHeight(raw json.RawMessage) int {
	value := strings.TrimSuffix(strings.TrimSpace(stringValue(raw)), "px")
	if value == "" {
		return 9
	}
	pixels, err := strconv.ParseFloat(value, 64)
	if err != nil || pixels <= 0 {
		return 9
	}
	return max(int(math.Ceil(pixels/30)), 1)
}

func uniqueRefID(raw string, reserved, used map[string]bool) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	exactCanonical := raw == trimmed && queryNamePattern.MatchString(raw)
	if exactCanonical && !used[raw] {
		used[raw] = true
		return raw, false
	}
	if queryNamePattern.MatchString(trimmed) && !reserved[trimmed] && !used[trimmed] {
		used[trimmed] = true
		return trimmed, true
	}
	for candidateIndex := 0; ; candidateIndex++ {
		candidate := spreadsheetRef(candidateIndex)
		if used[candidate] || reserved[candidate] {
			continue
		}
		used[candidate] = true
		return candidate, true
	}
}

func spreadsheetRef(index int) string {
	index++
	var result string
	for index > 0 {
		index--
		result = string(rune('A'+index%26)) + result
		index /= 26
	}
	return result
}

func normalizeGrid(raw *rawGrid, span float64, y int) model.Grid {
	if raw != nil {
		return model.Grid{X: raw.X, Y: raw.Y, W: max(raw.W, 1), H: max(raw.H, 1)}
	}
	width := 24
	if span > 0 {
		width = max(int(span*2), 1)
	}
	return model.Grid{X: 0, Y: y, W: min(width, 24), H: 9}
}

func normalizeID(raw json.RawMessage, fallback string) string {
	value := stringValue(raw)
	if value != "" {
		return value
	}
	return strings.TrimPrefix(strings.ReplaceAll(fallback, "/", "-"), "-")
}

func normalizePanelKind(kind string) model.PanelKind {
	switch strings.ToLower(kind) {
	case "graph", "timeseries":
		return model.PanelKindGraph
	case "singlestat", "stat", "gauge", "grafana-singlestat-panel":
		return model.PanelKindValue
	case "bargauge", "barchart":
		return model.PanelKindBar
	case "table", "table-old":
		return model.PanelKindTable
	case "piechart", "piechart-panel", "grafana-piechart-panel":
		return model.PanelKindPie
	case "heatmap":
		return model.PanelKindHistogram
	case "row":
		return model.PanelKindRow
	case "text":
		return model.PanelKindText
	default:
		return model.PanelKindUnknown
	}
}

func normalizeDatasource(raw json.RawMessage, variables map[string]string) model.Datasource {
	if len(raw) == 0 || string(raw) == "null" {
		return model.Datasource{}
	}

	var object struct {
		Type string `json:"type"`
		UID  string `json:"uid"`
		Name string `json:"name"`
	}
	if raw[0] == '{' && json.Unmarshal(raw, &object) == nil {
		return model.Datasource{Type: object.Type, UID: object.UID, Name: object.Name}
	}

	value := stringValue(raw)
	name := strings.Trim(value, "${}")
	datasource := model.Datasource{Name: value, UID: value}
	if query, ok := variables[name]; ok {
		datasource.Type = datasourceType(query)
	}
	return datasource
}

func datasourceType(value string) string {
	lower := strings.ToLower(value)
	for _, candidate := range []string{"prometheus", "loki", "cloudwatch", "elasticsearch"} {
		if strings.Contains(lower, candidate) {
			return candidate
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func normalizedPositiveInt(value flexibleNumber) int {
	if value <= 0 {
		return 0
	}
	rounded := math.Ceil(float64(value))
	maximum := int(^uint(0) >> 1)
	if rounded >= float64(maximum) {
		return maximum
	}
	return int(rounded)
}
