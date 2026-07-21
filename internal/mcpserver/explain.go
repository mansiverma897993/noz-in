package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/mansiverma897993/signoz/pkg/reporttypes"
	"github.com/mark3labs/mcp-go/mcp"
)

type explainResponse struct {
	MigrationID string        `json:"migration_id"`
	Items       []explainItem `json:"items"`
}

type explainItem struct {
	Kind         string                 `json:"kind"`
	SourcePath   string                 `json:"source_path"`
	Panel        string                 `json:"panel"`
	Query        string                 `json:"query,omitempty"`
	Verdict      string                 `json:"verdict"`
	ReasonCodes  []string               `json:"reason_codes"`
	Explanation  []string               `json:"explanation"`
	SourceDetail string                 `json:"source_detail,omitempty"`
	OriginalExpr string                 `json:"original_expr,omitempty"`
	EmittedQuery map[string]any         `json:"emitted_query,omitempty"`
	PreviewSQL   []json.RawMessage      `json:"preview_sql,omitempty"`
	Validation   reporttypes.Validation `json:"validation"`
	SuggestedFix string                 `json:"suggested_fix"`
}

func (service *Service) handleExplainVerdict(
	ctx context.Context,
	request mcp.CallToolRequest,
) (*mcp.CallToolResult, error) {
	release, err := service.acquireTool(ctx)
	if err != nil {
		return toolError("RESOURCE_BUSY", err), nil
	}
	defer release()
	id, err := request.RequireString("migration_id")
	if err != nil {
		return toolError("INVALID_INPUT", err), nil
	}
	if _, err := service.migrationDirectory(id); err != nil {
		return toolError("INVALID_INPUT", err), nil
	}
	state, err := service.readManifest(id)
	if err != nil {
		return toolError("MIGRATION_NOT_FOUND", err), nil
	}
	snapshot, err := service.readDashboardReport(id, state)
	if err != nil {
		return toolError("REPORT_READ_FAILED", err), nil
	}
	evidence := snapshot.Evidence
	sourcePath := strings.TrimSpace(request.GetString("source_path", ""))
	kind := strings.ToLower(strings.TrimSpace(request.GetString("kind", "")))
	if sourcePath != "" || kind != "" {
		if sourcePath == "" || kind == "" {
			return toolError("INVALID_INPUT", fmt.Errorf("kind and source_path must be provided together")), nil
		}
		items, err := explainSourcePath(evidence, kind, sourcePath)
		if err != nil {
			return toolError("VERDICT_NOT_FOUND", err), nil
		}
		response := explainResponse{MigrationID: id, Items: items}
		return mcp.NewToolResultStructured(response, fmt.Sprintf("Explained %d verdict record(s) at %s", len(items), sourcePath)), nil
	}
	panelSelector := strings.TrimSpace(request.GetString("panel", ""))
	if panelSelector == "" {
		return toolError("INVALID_INPUT", fmt.Errorf("provide kind and source_path, or the legacy panel selector")), nil
	}
	panel, err := selectPanel(evidence.Panels, panelSelector)
	if err != nil {
		return toolError("PANEL_NOT_FOUND", err), nil
	}
	querySelector := strings.TrimSpace(request.GetString("query", ""))
	items, err := explainPanel(evidence, panel, querySelector)
	if err != nil {
		return toolError("QUERY_NOT_FOUND", err), nil
	}
	response := explainResponse{MigrationID: id, Items: items}
	return mcp.NewToolResultStructured(response, fmt.Sprintf("Explained %d verdict record(s) for %s", len(items), panel.Title)), nil
}

func decodeDashboardReport(data []byte) (reporttypes.Report, error) {
	var evidence reporttypes.Report
	if err := json.Unmarshal(data, &evidence); err != nil {
		return reporttypes.Report{}, fmt.Errorf("decode dashboard report: %w", err)
	}
	if evidence.SchemaVersion == "" || evidence.Dashboard.Title == "" {
		return reporttypes.Report{}, fmt.Errorf("report does not contain dashboard evidence")
	}
	return evidence, nil
}

func selectPanel(panels []reporttypes.PanelRecord, selector string) (reporttypes.PanelRecord, error) {
	selector = strings.TrimSpace(selector)
	if index, err := strconv.Atoi(selector); err == nil {
		if index < 0 || index >= len(panels) {
			return reporttypes.PanelRecord{}, fmt.Errorf("panel index %d is outside 0..%d", index, max(len(panels)-1, 0))
		}
		return panels[index], nil
	}
	var exact []reporttypes.PanelRecord
	for _, panel := range panels {
		if strings.EqualFold(panel.Title, selector) {
			exact = append(exact, panel)
		}
	}
	if len(exact) == 1 {
		return exact[0], nil
	}
	if len(exact) > 1 {
		paths := make([]string, 0, len(exact))
		for _, panel := range exact {
			paths = append(paths, panel.SourcePath)
		}
		return reporttypes.PanelRecord{}, fmt.Errorf(
			"panel title %q is ambiguous; use kind=panel with one of these source paths: %s",
			selector, strings.Join(paths, ", "),
		)
	}
	var matches []reporttypes.PanelRecord
	for _, panel := range panels {
		if strings.Contains(strings.ToLower(panel.Title), strings.ToLower(selector)) {
			matches = append(matches, panel)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, panel := range matches {
			names = append(names, panel.Title)
		}
		return reporttypes.PanelRecord{}, fmt.Errorf("panel selector %q is ambiguous; candidates: %s", selector, strings.Join(names, ", "))
	}
	return reporttypes.PanelRecord{}, fmt.Errorf("no panel matches %q", selector)
}

func explainSourcePath(evidence reporttypes.Report, kind string, sourcePath string) ([]explainItem, error) {
	var items []explainItem
	switch kind {
	case "dashboard":
		for _, feature := range evidence.SourceFeatures {
			if feature.SourcePath == sourcePath {
				items = append(items, explainFeature(evidence, "dashboard", sourcePath, "Dashboard", feature))
			}
		}
	case "variable":
		for _, variable := range evidence.Variables {
			if variable.SourcePath == sourcePath {
				items = append(items, explainItem{
					Kind: "variable", SourcePath: sourcePath, Panel: "Variable: " + variable.Name,
					Verdict: variable.Verdict, ReasonCodes: variable.ReasonCodes,
					Explanation:  explainReasons(evidence.ReasonCodes, variable.ReasonCodes),
					SuggestedFix: suggestedFix(variable.ReasonCodes),
				})
			}
			for _, feature := range variable.SourceFeatures {
				if feature.SourcePath == sourcePath {
					items = append(items, explainFeature(evidence, "variable", sourcePath, "Variable: "+variable.Name, feature))
				}
			}
		}
	case "panel":
		for _, panel := range evidence.Panels {
			if panel.SourcePath == sourcePath {
				items = append(items, explainItem{
					Kind: "panel", SourcePath: sourcePath, Panel: panel.Title,
					Verdict: panel.Verdict, ReasonCodes: panel.ReasonCodes,
					Explanation:  explainReasons(evidence.ReasonCodes, panel.ReasonCodes),
					SuggestedFix: suggestedFix(panel.ReasonCodes),
				})
			}
			for _, feature := range panel.SourceFeatures {
				if feature.SourcePath == sourcePath {
					items = append(items, explainFeature(evidence, "panel", sourcePath, panel.Title, feature))
				}
			}
		}
	case "query":
		for _, panel := range evidence.Panels {
			for _, query := range panel.Queries {
				if query.SourcePath == sourcePath {
					items = append(items, explainQuery(evidence, panel, query))
				}
				for _, feature := range query.SourceFeatures {
					if feature.SourcePath == sourcePath {
						items = append(items, explainFeature(evidence, "query", sourcePath, panel.Title, feature))
					}
				}
			}
		}
	default:
		return nil, fmt.Errorf("kind must be one of dashboard, variable, panel, or query")
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("no %s verdict record has source_path %q", kind, sourcePath)
	}
	if len(items) > 1 {
		return nil, fmt.Errorf("%s source_path %q is ambiguous in the report", kind, sourcePath)
	}
	return items, nil
}

func explainFeature(
	evidence reporttypes.Report,
	kind string,
	sourcePath string,
	owner string,
	feature reporttypes.SourceFeatureRecord,
) explainItem {
	reasons := []string{feature.ReasonCode}
	return explainItem{
		Kind: kind, SourcePath: sourcePath, Panel: owner, Verdict: feature.Verdict,
		ReasonCodes: reasons, Explanation: explainReasons(evidence.ReasonCodes, reasons),
		SourceDetail: feature.Detail, SuggestedFix: suggestedFix(reasons),
	}
}

func explainPanel(evidence reporttypes.Report, panel reporttypes.PanelRecord, querySelector string) ([]explainItem, error) {
	if len(panel.Queries) == 0 {
		return []explainItem{{
			Kind: "panel", SourcePath: panel.SourcePath,
			Panel: panel.Title, Verdict: panel.Verdict, ReasonCodes: panel.ReasonCodes,
			Explanation:  explainReasons(evidence.ReasonCodes, panel.ReasonCodes),
			SuggestedFix: suggestedFix(panel.ReasonCodes),
		}}, nil
	}
	items := make([]explainItem, 0, len(panel.Queries))
	for _, query := range panel.Queries {
		if querySelector != "" && !strings.EqualFold(query.RefID, querySelector) {
			continue
		}
		items = append(items, explainQuery(evidence, panel, query))
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("panel %q does not contain query %q", panel.Title, querySelector)
	}
	return items, nil
}

func explainQuery(evidence reporttypes.Report, panel reporttypes.PanelRecord, query reporttypes.QueryRecord) explainItem {
	return explainItem{
		Kind: "query", SourcePath: query.SourcePath,
		Panel: panel.Title, Query: query.RefID, Verdict: query.Verdict,
		ReasonCodes: query.ReasonCodes, Explanation: explainReasons(evidence.ReasonCodes, query.ReasonCodes),
		OriginalExpr: query.Original, EmittedQuery: emittedQuery(query),
		PreviewSQL: query.Validation.PreviewStatements, Validation: query.Validation,
		SuggestedFix: suggestedFix(query.ReasonCodes),
	}
}

func emittedQuery(query reporttypes.QueryRecord) map[string]any {
	switch query.EmittedKind {
	case "builder":
		return map[string]any{"type": "builder", "spec": query.Builder}
	case "formula":
		return map[string]any{"type": "formula", "spec": query.Formula}
	case "promql":
		return map[string]any{"type": "promql", "query": query.PromQL}
	default:
		return nil
	}
}

func explainReasons(index map[string]string, codes []string) []string {
	result := make([]string, 0, len(codes))
	for _, code := range codes {
		description := index[code]
		if description == "" {
			description = "No description is available in this report schema."
		}
		result = append(result, code+": "+description)
	}
	return result
}

func suggestedFix(codes []string) string {
	for _, code := range codes {
		switch code {
		case "RECORDING_RULE_METRIC":
			return "Provide the Prometheus rule pack to migrate_dashboard so the recording rule can be inlined, or continue ingesting that recorded series into SigNoz."
		case "MISSING_METRIC_IN_TARGET":
			return "Confirm the metric is being ingested, check its OpenTelemetry dot-normalized name, then run validate_queries again."
		case "NON_PROMETHEUS_DATASOURCE":
			return "Rebuild this query with the equivalent SigNoz signal and query language; the source expression was not PromQL."
		case "PROMQL_PARSE_ERROR", "EMPTY_EXPRESSION":
			return "Fix or remove the invalid source expression in Grafana, then migrate the dashboard again."
		case "GRAFANA_TRANSFORMATION":
			return "Recreate the Grafana transformation in SigNoz or replace it with an equivalent query expression before accepting the panel."
		case "DATASOURCE_VARIABLE_OMITTED", "UNSUPPORTED_VARIABLE", "VARIABLE_REGEX_FILTER":
			return "Set an explicit target variable or replace the source variable with a SigNoz dynamic, custom, or textbox variable."
		case "ROW_PANEL_TARGET_UNSUPPORTED":
			return "Remove the stale target from the Grafana row header or recreate it on a child panel; row headers remain structural in SigNoz."
		case "UNMAPPED_DASHBOARD_CONFIG", "UNMAPPED_VARIABLE_CONFIG", "UNMAPPED_VISUALIZATION_CONFIG", "UNMAPPED_QUERY_CONFIG":
			return "Inspect the exact source_path and raw source detail in the report, then recreate or explicitly accept that behavior in SigNoz."
		}
	}
	return "Compare the original expression with the emitted query and accept or adjust the migration explicitly; do not clear the review state without checking semantics."
}
