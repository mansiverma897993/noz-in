package app

import (
	"sort"

	"github.com/mansiverma897993/signoz/internal/target/signoz"
	"github.com/mansiverma897993/signoz/pkg/reporttypes"
)

// validationSafeDashboard removes whole widgets that failed live validation.
// A widget is the isolation boundary because formulas and their dependencies
// must remain in one composite query. The returned payload is a deep-enough
// copy for layout and panel-map pruning; the candidate is left unchanged so it
// can be retained as evidence of everything the migration attempted to emit.
func validationSafeDashboard(
	candidate signoz.DashboardV5,
	evidence reporttypes.Report,
) (signoz.DashboardV5, []string, []string) {
	rejectedPaths := make(map[string]bool)
	blockedPaths := make(map[string]bool)
	for _, panel := range evidence.Panels {
		eligible := false
		failed := false
		transient := false
		for _, query := range panel.Queries {
			if query.Disabled || query.EmittedKind == "none" {
				continue
			}
			eligible = true
			if !query.Validation.PreviewOK || !query.Validation.Executed || query.Validation.ErrorCode != "" {
				failed = true
			}
			if query.Validation.HTTPStatus == 409 || query.Validation.HTTPStatus >= 500 {
				transient = true
			}
		}
		if eligible && failed {
			if transient || panel.Kind == "row" {
				blockedPaths[panel.SourcePath] = true
			} else {
				rejectedPaths[panel.SourcePath] = true
			}
		}
	}

	safe := candidate
	safe.Widgets = make([]signoz.Widget, 0, len(candidate.Widgets))
	keptIDs := make(map[string]bool, len(candidate.Widgets))
	rejected := make([]string, 0, len(rejectedPaths))
	for _, widget := range candidate.Widgets {
		if rejectedPaths[widget.SourcePath] {
			rejected = append(rejected, widget.SourcePath)
			continue
		}
		safe.Widgets = append(safe.Widgets, widget)
		keptIDs[widget.ID] = true
	}
	sort.Strings(rejected)
	blocked := make([]string, 0, len(blockedPaths))
	for path := range blockedPaths {
		blocked = append(blocked, path)
	}
	sort.Strings(blocked)

	if candidate.Layout != nil {
		safe.Layout = keptLayouts(candidate.Layout, keptIDs)
	}
	if candidate.PanelMap != nil {
		safe.PanelMap = make(map[string]signoz.PanelGroup, len(candidate.PanelMap))
	}
	for rowID, group := range candidate.PanelMap {
		if !keptIDs[rowID] {
			continue
		}
		group.Widgets = keptLayouts(group.Widgets, keptIDs)
		safe.PanelMap[rowID] = group
	}
	return safe, rejected, blocked
}

func keptLayouts(layouts []signoz.Layout, keptIDs map[string]bool) []signoz.Layout {
	kept := make([]signoz.Layout, 0, len(layouts))
	for _, layout := range layouts {
		if keptIDs[layout.I] {
			kept = append(kept, layout)
		}
	}
	return kept
}

// enabledExecutableWidgetCount returns the number of widgets that contain at
// least one enabled query or formula. Disabled query envelopes are preserved
// in artifacts for fidelity, but they cannot make an otherwise empty payload
// safe to import.
func enabledExecutableWidgetCount(dashboard signoz.DashboardV5) int {
	count := 0
	for _, widget := range dashboard.Widgets {
		if widgetHasEnabledExecutableQuery(widget) {
			count++
		}
	}
	return count
}

func widgetHasEnabledExecutableQuery(widget signoz.Widget) bool {
	switch widget.Query.QueryType {
	case "builder":
		for _, query := range widget.Query.Builder.QueryData {
			if !query.Disabled {
				return true
			}
		}
		for _, formula := range widget.Query.Builder.QueryFormulas {
			if !formula.Disabled {
				return true
			}
		}
		return false
	case "clickhouse_sql":
		for _, query := range widget.Query.ClickHouseSQL {
			if !query.Disabled {
				return true
			}
		}
		return false
	default:
		for _, query := range widget.Query.PromQL {
			if !query.Disabled {
				return true
			}
		}
		return false
	}
}
