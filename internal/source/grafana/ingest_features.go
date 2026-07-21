package grafana

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/mansiverma897993/signoz/internal/model"
)

func dashboardSourceFeatures(raw rawDashboard) []model.SourceFeature {
	features := make([]model.SourceFeature, 0, len(raw.Annotations.List)+len(raw.Links)+len(raw.Unmapped))
	for index, annotation := range raw.Annotations.List {
		annotationPath := fmt.Sprintf("/annotations/list/%d", index)
		features = append(features, model.SourceFeature{
			Kind: "annotation", SourcePath: annotationPath,
			Detail: compactRaw(annotation.Raw), Reason: model.ReasonAnnotationQuery,
		})
		features = append(features, rawMapSourceFeatures(
			annotation.Unmapped, annotationPath, "annotation_property", model.ReasonAnnotationQuery,
		)...)
		features = append(features, rawMapSourceFeatures(
			annotation.DatasourceUnmapped, annotationPath+"/datasource", "annotation_datasource_property", model.ReasonAnnotationQuery,
		)...)
	}
	features = append(features, rawMapSourceFeatures(
		raw.Annotations.Unmapped, "/annotations", "annotations_property", model.ReasonUnmappedDashboardConfig,
	)...)
	for index, link := range raw.Links {
		features = append(features, model.SourceFeature{
			Kind: "dashboard_link", SourcePath: fmt.Sprintf("/links/%d", index),
			Detail: compactRaw(link), Reason: model.ReasonDashboardLink,
		})
	}
	features = append(features, rawMapSourceFeatures(
		raw.Templating.Unmapped, "/templating", "templating_property", model.ReasonUnmappedDashboardConfig,
	)...)
	for index, input := range raw.Inputs {
		features = append(features, rawMapSourceFeatures(
			input.Unmapped, fmt.Sprintf("/__inputs/%d", index), "input_property", model.ReasonUnmappedDashboardConfig,
		)...)
	}
	for _, name := range sortedRawKeys(raw.Unmapped) {
		features = append(features, model.SourceFeature{
			Kind: "dashboard_property", SourcePath: "/" + jsonPointerSegment(name), Detail: compactRaw(raw.Unmapped[name]),
			Reason: model.ReasonUnmappedDashboardConfig,
		})
	}
	return features
}

func rowSourceFeatures(raw rawRow, path string) []model.SourceFeature {
	features := make([]model.SourceFeature, 0, len(raw.Unmapped))
	for _, name := range sortedRawKeys(raw.Unmapped) {
		features = append(features, model.SourceFeature{
			Kind: "legacy_row_property", SourcePath: path + "/" + jsonPointerSegment(name), Detail: compactRaw(raw.Unmapped[name]),
			Reason: model.ReasonUnmappedDashboardConfig,
		})
	}
	return features
}

func variableSourceFeatures(raw rawVariable, path string) []model.SourceFeature {
	features := make([]model.SourceFeature, 0, len(raw.Unmapped))
	for _, name := range sortedRawKeys(raw.Unmapped) {
		features = append(features, model.SourceFeature{
			Kind: "variable_property", SourcePath: path + "/" + jsonPointerSegment(name), Detail: compactRaw(raw.Unmapped[name]),
			Reason: model.ReasonUnmappedVariableConfig,
		})
	}
	features = append(features, rawMapSourceFeatures(
		raw.QueryUnmapped, path+"/query", "variable_query_property", model.ReasonUnmappedVariableConfig,
	)...)
	features = append(features, rawMapSourceFeatures(
		raw.CurrentUnmapped, path+"/current", "variable_current_property", model.ReasonUnmappedVariableConfig,
	)...)
	features = append(features, rawMapSourceFeatures(
		raw.DatasourceUnmapped, path+"/datasource", "variable_datasource_property", model.ReasonUnmappedVariableConfig,
	)...)
	return features
}

func panelSourceFeatures(raw rawPanel, path string) []model.SourceFeature {
	features := make([]model.SourceFeature, 0)
	if grafanaDisplayVariablePattern.MatchString(raw.Title) {
		features = append(features, model.SourceFeature{
			Kind: "panel_title_variable", SourcePath: path + "/title", Detail: raw.Title,
			Reason: model.ReasonGrafanaVariablePanelTitle,
		})
	}
	if strings.TrimSpace(raw.Description) != "" {
		features = append(features, model.SourceFeature{
			Kind: "panel_description_semantics", SourcePath: path + "/description", Detail: raw.Description,
			Reason: model.ReasonGrafanaPanelDescription,
		})
	}
	if visualizationTypeDowngraded(raw.Type) {
		features = append(features, model.SourceFeature{
			Kind: "visualization_type", SourcePath: path + "/type", Detail: raw.Type,
			Reason: model.ReasonVisualizationDowngrade,
		})
	}
	if rawJSONPresent(raw.Alert) {
		features = append(features, model.SourceFeature{
			Kind: "legacy_alert", SourcePath: path + "/alert", Detail: compactRaw(raw.Alert), Reason: model.ReasonLegacyPanelAlert,
		})
	}
	for index, link := range raw.Links {
		features = append(features, model.SourceFeature{
			Kind: "panel_link", SourcePath: fmt.Sprintf("%s/links/%d", path, index), Detail: compactRaw(link), Reason: model.ReasonPanelLink,
		})
	}
	if rawJSONPresent(raw.FieldConfig.Defaults.Thresholds) {
		features = append(features, model.SourceFeature{
			Kind: "field_thresholds", SourcePath: path + "/fieldConfig/defaults/thresholds",
			Detail: compactRaw(raw.FieldConfig.Defaults.Thresholds), Reason: model.ReasonFieldThresholds,
		})
	}
	for index, override := range raw.FieldConfig.Overrides {
		features = append(features, model.SourceFeature{
			Kind: "field_override", SourcePath: fmt.Sprintf("%s/fieldConfig/overrides/%d", path, index),
			Detail: compactRaw(override), Reason: model.ReasonFieldOverrides,
		})
	}
	if rawJSONPresent(raw.LibraryPanel) {
		features = append(features, model.SourceFeature{
			Kind: "library_panel", SourcePath: path + "/libraryPanel", Detail: compactRaw(raw.LibraryPanel), Reason: model.ReasonLibraryPanel,
		})
	}
	if raw.GridPos != nil {
		features = append(features, rawMapSourceFeatures(
			raw.GridPos.Unmapped, path+"/gridPos", "grid_property", model.ReasonUnmappedVisualization,
		)...)
	}
	features = append(features, rawMapSourceFeatures(
		raw.DatasourceUnmapped, path+"/datasource", "panel_datasource_property", model.ReasonUnmappedVisualization,
	)...)
	for _, name := range sortedRawKeys(raw.FieldConfig.Unmapped) {
		features = append(features, model.SourceFeature{
			Kind: "field_config", SourcePath: path + "/fieldConfig/" + jsonPointerSegment(name), Detail: compactRaw(raw.FieldConfig.Unmapped[name]),
			Reason: model.ReasonUnmappedVisualization,
		})
	}
	for _, name := range sortedRawKeys(raw.FieldConfig.Defaults.Unmapped) {
		features = append(features, model.SourceFeature{
			Kind: "field_default", SourcePath: path + "/fieldConfig/defaults/" + jsonPointerSegment(name), Detail: compactRaw(raw.FieldConfig.Defaults.Unmapped[name]),
			Reason: model.ReasonUnmappedVisualization,
		})
	}
	features = append(features, axisSourceFeatures(raw, path)...)
	for index, transform := range raw.Transforms {
		transformPath := fmt.Sprintf("%s/transformations/%d", path, index)
		if len(transform.Options) > 0 {
			features = append(features, model.SourceFeature{
				Kind: "transformation_options", SourcePath: transformPath + "/options", Detail: compactRaw(transform.Options),
				Reason: model.ReasonUnmappedVisualization,
			})
		}
		for _, name := range sortedRawKeys(transform.Unmapped) {
			features = append(features, model.SourceFeature{
				Kind: "transformation_property", SourcePath: transformPath + "/" + jsonPointerSegment(name), Detail: compactRaw(transform.Unmapped[name]),
				Reason: model.ReasonUnmappedVisualization,
			})
		}
	}
	for _, name := range sortedRawKeys(raw.Options.Unmapped) {
		features = append(features, model.SourceFeature{
			Kind: "panel_option", SourcePath: path + "/options/" + jsonPointerSegment(name), Detail: compactRaw(raw.Options.Unmapped[name]),
			Reason: model.ReasonUnmappedVisualization,
		})
	}
	for _, name := range sortedRawKeys(raw.Unmapped) {
		features = append(features, model.SourceFeature{
			Kind: "panel_property", SourcePath: path + "/" + jsonPointerSegment(name), Detail: compactRaw(raw.Unmapped[name]),
			Reason: model.ReasonUnmappedVisualization,
		})
	}
	return features
}

func axisSourceFeatures(raw rawPanel, path string) []model.SourceFeature {
	effectiveFormatIndex := -1
	if strings.TrimSpace(raw.FieldConfig.Defaults.Unit) == "" && strings.TrimSpace(raw.Format) == "" {
		for index, axis := range raw.YAxes {
			format := strings.TrimSpace(axis.Format)
			if format != "" && format != "short" {
				effectiveFormatIndex = index
				break
			}
		}
	}

	features := make([]model.SourceFeature, 0)
	for index, axis := range raw.YAxes {
		axisPath := fmt.Sprintf("%s/yaxes/%d", path, index)
		if len(axis.FormatRaw) > 0 && index != effectiveFormatIndex {
			features = append(features, model.SourceFeature{
				Kind: "yaxis_property", SourcePath: axisPath + "/format", Detail: compactRaw(axis.FormatRaw),
				Reason: model.ReasonUnmappedVisualization,
			})
		}
		for _, name := range sortedRawKeys(axis.Unmapped) {
			features = append(features, model.SourceFeature{
				Kind: "yaxis_property", SourcePath: axisPath + "/" + jsonPointerSegment(name), Detail: compactRaw(axis.Unmapped[name]),
				Reason: model.ReasonUnmappedVisualization,
			})
		}
	}
	return features
}

func rawTargetSourceFeatureCount(raw rawTarget) int {
	count := len(raw.Unmapped) + len(raw.DatasourceUnmapped)
	if grafanaLegendRequiresReview(raw.Legend) {
		count++
	}
	if len(raw.Step.Raw) > 0 {
		count++
	}
	if len(raw.Range) > 0 {
		count++
	}
	if len(raw.Exemplar) > 0 {
		count++
	}
	return count
}

func targetSourceFeatures(raw rawTarget, path string) []model.SourceFeature {
	features := make([]model.SourceFeature, 0, rawTargetSourceFeatureCount(raw))
	if grafanaLegendRequiresReview(raw.Legend) {
		features = append(features, model.SourceFeature{
			Kind: "query_legend_semantics", SourcePath: path + "/legendFormat", Detail: raw.Legend,
			Reason: model.ReasonGrafanaVariableLegend,
		})
	}
	if len(raw.Step.Raw) > 0 {
		reason := model.ReasonUnmappedQueryConfig
		if raw.Step.Value > 0 {
			reason = model.ReasonGrafanaIntervalControl
		}
		features = append(features, model.SourceFeature{
			Kind: "query_step", SourcePath: path + "/step", Detail: compactRaw(raw.Step.Raw), Reason: reason,
		})
	}
	if len(raw.Range) > 0 {
		features = append(features, model.SourceFeature{
			Kind: "query_range", SourcePath: path + "/range", Detail: compactRaw(raw.Range),
			Reason: model.ReasonUnmappedQueryConfig,
		})
	}
	if len(raw.Exemplar) > 0 {
		features = append(features, model.SourceFeature{
			Kind: "query_exemplar", SourcePath: path + "/exemplar", Detail: compactRaw(raw.Exemplar),
			Reason: model.ReasonUnmappedQueryConfig,
		})
	}
	features = append(features, rawMapSourceFeatures(
		raw.DatasourceUnmapped, path+"/datasource", "query_datasource_property", model.ReasonUnmappedQueryConfig,
	)...)
	for _, name := range sortedRawKeys(raw.Unmapped) {
		features = append(features, model.SourceFeature{
			Kind: "query_property", SourcePath: path + "/" + jsonPointerSegment(name), Detail: compactRaw(raw.Unmapped[name]),
			Reason: model.ReasonUnmappedQueryConfig,
		})
	}
	return features
}

func grafanaLegendRequiresReview(legend string) bool {
	return strings.EqualFold(strings.TrimSpace(legend), "__auto") ||
		grafanaDisplayVariablePattern.MatchString(legend) ||
		grafanaLegendPlaceholderPattern.MatchString(legend)
}

func visualizationTypeDowngraded(panelType string) bool {
	switch strings.ToLower(strings.TrimSpace(panelType)) {
	case "gauge", "bargauge", "barchart", "heatmap":
		return true
	default:
		return false
	}
}

func sortedRawKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func rawMapSourceFeatures(
	values map[string]json.RawMessage,
	path string,
	kind string,
	reason model.ReasonCode,
) []model.SourceFeature {
	features := make([]model.SourceFeature, 0, len(values))
	for _, name := range sortedRawKeys(values) {
		features = append(features, model.SourceFeature{
			Kind: kind, SourcePath: path + "/" + jsonPointerSegment(name), Detail: compactRaw(values[name]), Reason: reason,
		})
	}
	return features
}

func compactRaw(value json.RawMessage) string {
	var output bytes.Buffer
	if err := json.Compact(&output, value); err != nil {
		return string(value)
	}
	return output.String()
}

func jsonPointerSegment(value string) string {
	value = strings.ReplaceAll(value, "~", "~0")
	return strings.ReplaceAll(value, "/", "~1")
}

func rawJSONPresent(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) > 0
}
