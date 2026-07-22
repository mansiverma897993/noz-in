package app

// Live metric metadata resolution against the SigNoz target and the metric
// evidence marking applied to migration reports.

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/mansiverma897993/noz-in/internal/target/signoz"
	"github.com/mansiverma897993/noz-in/internal/transpile"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
)

func resolveMetricMetadata(
	ctx context.Context,
	client *signoz.Client,
	dashboard model.Dashboard,
	analyzer *transpile.Analyzer,
	metrics map[string]model.TargetMetric,
	missing map[string]bool,
	metadataErrors map[string]bool,
	metricNames map[string]string,
	start time.Time,
	end time.Time,
) error {
	for _, panel := range dashboard.Panels {
		if !panelCanRequireMetricMetadata(panel, analyzer) {
			continue
		}
		for _, query := range panel.Queries {
			if err := resolveQueryMetricMetadata(
				ctx, client, query, analyzer, metrics, missing, metadataErrors, metricNames, start, end,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func panelCanRequireMetricMetadata(panel model.Panel, analyzer *transpile.Analyzer) bool {
	if panel.Kind == model.PanelKindRow || panel.Kind == model.PanelKindText || panel.Kind == model.PanelKindUnknown {
		return false
	}
	for _, feature := range panel.SourceFeatures {
		if feature.Reason == model.ReasonLibraryPanel {
			return false
		}
	}
	hasVisible := false
	for _, query := range panel.Queries {
		if query.Hidden {
			continue
		}
		hasVisible = true
		if analyzer.IsStaticallyNonExecutable(query) {
			return false
		}
	}
	return hasVisible
}

func resolveQueryMetricMetadata(
	ctx context.Context,
	client *signoz.Client,
	query model.Query,
	analyzer *transpile.Analyzer,
	metrics map[string]model.TargetMetric,
	missing map[string]bool,
	metadataErrors map[string]bool,
	metricNames map[string]string,
	start time.Time,
	end time.Time,
) error {
	for _, name := range analyzer.MetricNames(query) {
		if missing[name] || metadataErrors[name] {
			continue
		}
		if metricMetadataResolved(metrics[name]) {
			continue
		}
		var metadata signoz.MetricMetadata
		var targetName string
		metadataUnavailable := false
		for _, candidate := range metricNameCandidates(name, metricNames) {
			resolved, err := client.MetricMetadata(ctx, candidate)
			if err != nil {
				if signoz.IsNotFound(err) {
					continue
				}
				if localMetricMetadataError(err) {
					metadataUnavailable = true
					break
				}
				metadataErrors[name] = true
				return err
			}
			metadata = resolved
			targetName = candidate
			break
		}
		if targetName == "" {
			if metadataUnavailable {
				metadataErrors[name] = true
			} else {
				missing[name] = true
			}
			continue
		}
		// Preserve the discovered selector remap even when the independently
		// queried attribute inventory is unavailable. metadataErrors still
		// prevents Builder qualification, while PromQL must use targetName.
		// Do not cache type qualification until attributes also succeed: a
		// fatal attribute request must not poison later dashboards with a
		// partially resolved metric.
		metrics[name] = model.TargetMetric{Name: targetName}
		delete(missing, name)
		attributes, err := client.MetricAttributes(ctx, targetName, start, end)
		if err != nil {
			if signoz.IsNotFound(err) {
				metadataErrors[name] = true
				continue
			}
			if localMetricMetadataError(err) {
				metadataErrors[name] = true
				continue
			}
			metadataErrors[name] = true
			return err
		}
		metric := model.TargetMetric{
			Name:        targetName,
			Type:        metadata.Type,
			Temporality: metadata.Temporality,
			IsMonotonic: metadata.IsMonotonic,
		}
		for _, attribute := range attributes {
			metric.Attributes = append(metric.Attributes, attribute.Key)
		}
		metrics[name] = metric
		delete(missing, name)
		delete(metadataErrors, name)
	}
	return nil
}

func cloneTargetMetrics(source map[string]model.TargetMetric) map[string]model.TargetMetric {
	clone := make(map[string]model.TargetMetric, len(source))
	maps.Copy(clone, source)
	return clone
}

func cloneBoolMap(source map[string]bool) map[string]bool {
	clone := make(map[string]bool, len(source))
	maps.Copy(clone, source)
	return clone
}

func metricMetadataWindow(end time.Time, lookback time.Duration) (time.Time, time.Time) {
	if lookback <= 0 {
		lookback = time.Hour
	}
	return end.Add(-lookback), end
}

func metricMetadataResolved(metric model.TargetMetric) bool {
	return strings.TrimSpace(metric.Type) != ""
}

func localMetricMetadataError(err error) bool {
	var apiError *signoz.APIError
	if !errors.As(err, &apiError) {
		return false
	}
	if apiError.StatusCode >= http.StatusInternalServerError && apiError.StatusCode <= 599 {
		return true
	}
	switch apiError.StatusCode {
	case http.StatusBadRequest, http.StatusConflict, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return true
	default:
		return false
	}
}

func metricNameCandidates(name string, mappings map[string]string) []string {
	if mapped := strings.TrimSpace(mappings[name]); mapped != "" {
		return []string{mapped}
	}
	result := []string{name}
	for _, suffix := range []string{"bucket", "sum", "count", "quantile"} {
		if base, ok := strings.CutSuffix(name, "_"+suffix); ok {
			result = append(result, base+"."+suffix)
			break
		}
	}
	return result
}

func mappedMetrics(mappings map[string]string) map[string]model.TargetMetric {
	result := make(map[string]model.TargetMetric, len(mappings))
	for source, target := range mappings {
		result[source] = model.TargetMetric{Name: target}
	}
	return result
}

func markMetricEvidence(
	dashboard model.Dashboard,
	analyzer *transpile.Analyzer,
	metrics map[string]model.TargetMetric,
	missing map[string]bool,
	metadataErrors map[string]bool,
	evidence *reporttypes.Report,
) {
	for panelIndex, panel := range dashboard.Panels {
		if panelIndex >= len(evidence.Panels) {
			break
		}
		for queryIndex, query := range panel.Queries {
			if queryIndex >= len(evidence.Panels[panelIndex].Queries) {
				break
			}
			names := analyzer.MetricNames(query)
			if len(names) == 0 {
				continue
			}
			validation := &evidence.Panels[panelIndex].Queries[queryIndex].Validation
			validation.MetricChecked = true
			validation.MetricFound = true
			for _, name := range names {
				resolved := metricMetadataResolved(metrics[name])
				validation.MetricChecked = validation.MetricChecked && (resolved || missing[name]) && !metadataErrors[name]
				validation.MetricFound = validation.MetricFound && resolved && !metadataErrors[name]
			}
		}
	}
}

func markUnresolvedMetricMetadata(
	dashboard model.Dashboard,
	analyzer *transpile.Analyzer,
	metrics map[string]model.TargetMetric,
	missing map[string]bool,
	metadataErrors map[string]bool,
) {
	for _, panel := range dashboard.Panels {
		if panel.Kind == model.PanelKindRow {
			continue
		}
		for _, query := range panel.Queries {
			for _, name := range analyzer.MetricNames(query) {
				if !metricMetadataResolved(metrics[name]) && !missing[name] {
					metadataErrors[name] = true
				}
			}
		}
	}
}
