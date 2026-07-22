package app

// Dashboard evidence-report annotation: target outcome flags, emitted widget
// and query identities, and primary-artifact presence.

import (
	"fmt"
	"strings"

	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/mansiverma897993/noz-in/internal/target/signoz"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
)

func recordTargetOutcome(evidence *reporttypes.Report, result GrafanaResult) {
	evidence.Run.Flags["importRequested"] = result.ImportRequested
	evidence.Run.Flags["importAttempted"] = result.ImportAttempted
	evidence.Run.Flags["importSucceeded"] = result.ImportSucceeded
	evidence.Run.Flags["partialImportPerformed"] = result.PartialImportPerformed
	evidence.Run.Flags["targetAction"] = result.TargetAction
	if result.TargetDashboardID == "" {
		delete(evidence.Run.Flags, "targetDashboardID")
	} else {
		evidence.Run.Flags["targetDashboardID"] = result.TargetDashboardID
	}
	if result.TargetSkipped == "" {
		delete(evidence.Run.Flags, "targetSkippedReason")
	} else {
		evidence.Run.Flags["targetSkippedReason"] = result.TargetSkipped
	}
	if result.TargetError == "" {
		delete(evidence.Run.Flags, "targetError")
	} else {
		evidence.Run.Flags["targetError"] = result.TargetError
	}
}

func annotateEmittedWidgetIDs(evidence *reporttypes.Report, dashboard signoz.DashboardV5) error {
	widgets := make(map[string]signoz.Widget, len(dashboard.Widgets))
	for _, widget := range dashboard.Widgets {
		if strings.TrimSpace(widget.SourcePath) == "" {
			return fmt.Errorf("emitted widget %q has an empty source path", widget.Title)
		}
		if _, exists := widgets[widget.SourcePath]; exists {
			return fmt.Errorf("emitted dashboard contains duplicate widget source path %q", widget.SourcePath)
		}
		widgets[widget.SourcePath] = widget
	}
	panelPaths := make(map[string]bool, len(evidence.Panels))
	queryPaths := make(map[string]bool)
	for index := range evidence.Panels {
		panel := &evidence.Panels[index]
		if strings.TrimSpace(panel.SourcePath) == "" {
			return fmt.Errorf("migration panel %q has an empty source path", panel.Title)
		}
		if panelPaths[panel.SourcePath] {
			return fmt.Errorf("migration report contains duplicate panel source path %q", panel.SourcePath)
		}
		panelPaths[panel.SourcePath] = true
		widget, widgetFound := widgets[panel.SourcePath]
		if widgetFound {
			panel.EmittedWidgetID = widget.ID
		}
		for queryIndex := range panel.Queries {
			query := &panel.Queries[queryIndex]
			if strings.TrimSpace(query.SourcePath) == "" {
				return fmt.Errorf("migration query %q in panel %q has an empty source path", query.RefID, panel.Title)
			}
			if queryPaths[query.SourcePath] {
				return fmt.Errorf("migration report contains duplicate query source path %q", query.SourcePath)
			}
			queryPaths[query.SourcePath] = true
			expectedKind, ok := targetKindForEmittedKind(query.EmittedKind)
			if !ok {
				return fmt.Errorf("migration query %q has unsupported emitted kind %q", query.SourcePath, query.EmittedKind)
			}

			var identity emittedQueryIdentity
			var err error
			if expectedKind == targetKindNone {
				identity, err = nonEmittedQuerySpec(query.RefID)
			} else {
				if !widgetFound {
					return fmt.Errorf("migration query %q claims emitted kind %q but panel has no emitted widget", query.SourcePath, query.EmittedKind)
				}
				var found bool
				identity, found, err = emittedQuerySpec(widget, query.RefID)
				if err == nil && !found {
					return fmt.Errorf("migration query %q was not found in emitted widget %q", query.SourcePath, widget.Title)
				}
			}
			if err != nil {
				return fmt.Errorf("identify emitted query %q: %w", query.SourcePath, err)
			}
			if identity.TargetKind != expectedKind {
				return fmt.Errorf(
					"migration query %q reports emitted kind %q but widget contains %q",
					query.SourcePath, query.EmittedKind, identity.TargetKind,
				)
			}
			query.EmittedQueryName = identity.TargetQueryName
			query.EmittedExpression = identity.TargetExpression
			query.EmittedSpecSHA256 = identity.SHA256
		}
	}
	return nil
}

func annotatePrimaryWidgetPresence(evidence *reporttypes.Report, dashboard signoz.DashboardV5) {
	present := make(map[string]bool, len(dashboard.Widgets))
	for _, widget := range dashboard.Widgets {
		present[widget.ID] = true
	}
	for index := range evidence.Panels {
		evidence.Panels[index].PrimaryArtifact = present[evidence.Panels[index].EmittedWidgetID]
	}
}

func hasMissingVariableValidation(evidence reporttypes.Report) bool {
	for _, panel := range evidence.Panels {
		for _, query := range panel.Queries {
			if query.Validation.ErrorCode == string(model.ReasonMissingVariableValue) {
				return true
			}
		}
	}
	return false
}
