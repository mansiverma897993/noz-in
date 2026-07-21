package grafana

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"regexp"
	"strings"

	"github.com/mansiverma897993/signoz/internal/model"
)

const maxDashboardSize = 64 << 20

var (
	queryNamePattern                = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	grafanaDisplayVariablePattern   = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*(?:\.[^:^}]+)?(?::[^}]+)?\}|\$[A-Za-z_][A-Za-z0-9_]*|\[\[[A-Za-z_][A-Za-z0-9_]*(?::[A-Za-z0-9_]+)?\]\]`)
	grafanaLegendPlaceholderPattern = regexp.MustCompile(`\{\{\s*[^{}]+?\s*\}\}`)
)

var targetRuntimeVariableNames = map[string]bool{
	"start_timestamp":      true,
	"end_timestamp":        true,
	"start_timestamp_ms":   true,
	"end_timestamp_ms":     true,
	"SIGNOZ_START_TIME":    true,
	"SIGNOZ_END_TIME":      true,
	"start_timestamp_nano": true,
	"end_timestamp_nano":   true,
	"start_datetime":       true,
	"end_datetime":         true,
}

// ParseFile reads a Grafana dashboard file into the neutral model.
func ParseFile(path string) (model.Dashboard, error) {
	file, err := os.Open(path)
	if err != nil {
		return model.Dashboard{}, fmt.Errorf("open Grafana dashboard %q: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return model.Dashboard{}, fmt.Errorf("inspect Grafana dashboard %q: %w", path, err)
	}
	if info.Size() > maxDashboardSize {
		_ = file.Close()
		return model.Dashboard{}, fmt.Errorf("grafana dashboard %q exceeds %d bytes", path, maxDashboardSize)
	}
	dashboard, parseErr := Parse(file, path)
	closeErr := file.Close()
	if parseErr != nil {
		return model.Dashboard{}, fmt.Errorf("parse Grafana dashboard %q: %w", path, parseErr)
	}
	if closeErr != nil {
		return model.Dashboard{}, fmt.Errorf("close Grafana dashboard %q: %w", path, closeErr)
	}
	return dashboard, nil
}

// Parse reads a Grafana dashboard into the neutral model.
func Parse(reader io.Reader, path string) (model.Dashboard, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxDashboardSize+1))
	if err != nil {
		return model.Dashboard{}, fmt.Errorf("read Grafana dashboard: %w", err)
	}
	if len(data) > maxDashboardSize {
		return model.Dashboard{}, fmt.Errorf("grafana dashboard %q exceeds %d bytes", path, maxDashboardSize)
	}
	digest := sha256.Sum256(data)
	if err := validateJSONObjectKeys(data); err != nil {
		return model.Dashboard{}, fmt.Errorf("decode Grafana dashboard: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()

	var raw rawDashboard
	if err := decoder.Decode(&raw); err != nil {
		return model.Dashboard{}, fmt.Errorf("decode Grafana dashboard: %w", err)
	}
	if strings.TrimSpace(raw.Title) == "" {
		return model.Dashboard{}, fmt.Errorf("decode Grafana dashboard: title is required")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return model.Dashboard{}, fmt.Errorf("decode Grafana dashboard: unexpected trailing JSON value")
		}
		return model.Dashboard{}, fmt.Errorf("decode Grafana dashboard trailing data: %w", err)
	}

	dashboard := model.Dashboard{
		Title:       raw.Title,
		Description: raw.Description,
		UID:         raw.UID,
		Tags:        append([]string(nil), raw.Tags...),
		Source: model.Source{
			Kind:          "grafana",
			SchemaVersion: raw.SchemaVersion,
			Path:          path,
			Identity:      path,
			SHA256:        fmt.Sprintf("%x", digest[:]),
		},
		InputBindings:   inputBindings(raw.Inputs),
		SourceInventory: sourceInventory(raw),
	}
	dashboard.SourceFeatures = dashboardSourceFeatures(raw)

	datasourceBindings := make(map[string]string, len(dashboard.InputBindings))
	maps.Copy(datasourceBindings, dashboard.InputBindings)
	variablePaths := make(map[string]string, len(raw.Templating.List))
	for index, variable := range raw.Templating.List {
		normalized := normalizeVariable(variable, index, dashboard.InputBindings)
		if !queryNamePattern.MatchString(normalized.Name) {
			return model.Dashboard{}, fmt.Errorf(
				"decode Grafana dashboard: variable name %q at %s cannot be represented safely; names must match [A-Za-z_][A-Za-z0-9_]*",
				normalized.Name, normalized.SourcePath,
			)
		}
		if targetRuntimeVariableNames[normalized.Name] {
			return model.Dashboard{}, fmt.Errorf(
				"decode Grafana dashboard: variable name %q at %s collides with a reserved SigNoz runtime variable",
				normalized.Name, normalized.SourcePath,
			)
		}
		if previous, exists := variablePaths[normalized.Name]; exists {
			return model.Dashboard{}, fmt.Errorf(
				"decode Grafana dashboard: duplicate variable name %q at %s (already defined at %s)",
				normalized.Name, normalized.SourcePath, previous,
			)
		}
		variablePaths[normalized.Name] = normalized.SourcePath
		dashboard.Variables = append(dashboard.Variables, normalized)
		if normalized.Kind == model.VariableKindDatasource {
			datasourceBindings[normalized.Name] = normalized.Query
		}
	}

	yCursor := 0
	for index, panel := range raw.Panels {
		walkPanel(&dashboard.Panels, panel, fmt.Sprintf("/panels/%d", index), &yCursor, datasourceBindings)
	}
	for rowIndex, row := range raw.Rows {
		walkLegacyRow(&dashboard.Panels, row, rowIndex, &yCursor, datasourceBindings)
	}
	if accounted := normalizedInventory(dashboard); accounted != dashboard.SourceInventory {
		return model.Dashboard{}, fmt.Errorf("reconcile Grafana source inventory: decoded %+v, normalized %+v", dashboard.SourceInventory, accounted)
	}

	return dashboard, nil
}

func sourceInventory(raw rawDashboard) model.SourceInventory {
	inventory := model.SourceInventory{
		Captured: true, Variables: len(raw.Templating.List),
		SourceFeatures: len(dashboardSourceFeatures(raw)),
	}
	for _, variable := range raw.Templating.List {
		inventory.SourceFeatures += len(variableSourceFeatures(variable, ""))
	}
	for _, panel := range raw.Panels {
		inventoryPanel(&inventory, panel)
	}
	for _, row := range raw.Rows {
		inventory.Panels++ // Legacy row headers are explicit source layout objects.
		inventory.SourceFeatures += len(panelSourceFeatures(rawPanel{Title: row.Title, Type: "row"}, ""))
		inventory.SourceFeatures += len(rowSourceFeatures(row, ""))
		for _, panel := range row.Panels {
			inventoryPanel(&inventory, panel)
		}
	}
	return inventory
}

func inventoryPanel(inventory *model.SourceInventory, panel rawPanel) {
	inventory.Panels++
	inventory.Queries += len(panel.Targets)
	inventory.SourceFeatures += rawPanelFeatureCount(panel)
	for _, target := range panel.Targets {
		inventory.SourceFeatures += rawTargetSourceFeatureCount(target)
	}
	for _, child := range panel.Panels {
		inventoryPanel(inventory, child)
	}
}

func rawPanelFeatureCount(panel rawPanel) int {
	return len(panelSourceFeatures(panel, ""))
}

func normalizedInventory(dashboard model.Dashboard) model.SourceInventory {
	inventory := model.SourceInventory{
		Captured: true, Panels: len(dashboard.Panels), Variables: len(dashboard.Variables),
		SourceFeatures: len(dashboard.SourceFeatures),
	}
	for _, panel := range dashboard.Panels {
		inventory.Queries += len(panel.Queries)
		inventory.SourceFeatures += len(panel.SourceFeatures)
		for _, query := range panel.Queries {
			inventory.SourceFeatures += len(query.SourceFeatures)
		}
	}
	for _, variable := range dashboard.Variables {
		inventory.SourceFeatures += len(variable.SourceFeatures)
	}
	return inventory
}
