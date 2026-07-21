package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"unicode"

	"github.com/mansiverma897993/signoz/internal/app"
)

func jsonOutput(commandFlag interface{ GetBool(string) (bool, error) }) bool {
	enabled, err := commandFlag.GetBool("json")
	return err == nil && enabled
}

func writeJSONLine(writer io.Writer, value any) error {
	return json.NewEncoder(writer).Encode(value)
}

func writeGrafanaResults(writer io.Writer, results []app.GrafanaResult, asJSON bool) error {
	if asJSON {
		for _, result := range results {
			for _, panel := range result.Evidence.Panels {
				if err := writeJSONLine(writer, map[string]any{
					"type": "panel", "dashboard": result.Evidence.Dashboard.Title, "panel": panel,
				}); err != nil {
					return fmt.Errorf("write dashboard panel result: %w", err)
				}
			}
			if err := writeJSONLine(writer, map[string]any{
				"type": "summary", "dashboard": result.Evidence.Dashboard.Title, "summary": result.Summary,
				"artifacts": map[string]string{
					"dashboard": result.DashboardPath, "candidateDashboard": result.CandidateDashboardPath,
					"report": result.ReportPath, "html": result.HTMLPath,
				},
				"target": result.Target, "targetSkipped": result.TargetSkipped, "targetError": result.TargetError,
				"importRequested": result.ImportRequested, "importAttempted": result.ImportAttempted,
				"importSucceeded": result.ImportSucceeded, "targetAction": result.TargetAction,
				"targetDashboardId":      result.TargetDashboardID,
				"partialImportEligible":  result.PartialImportEligible,
				"partialImportPerformed": result.PartialImportPerformed,
				"importedWidgets":        result.ImportedWidgets, "validationRejectedWidgets": result.ValidationRejected,
				"validationBlockedWidgets": result.ValidationBlocked,
				"published":                result.Published,
			}); err != nil {
				return fmt.Errorf("write dashboard summary result: %w", err)
			}
		}
		return nil
	}
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	for _, result := range results {
		if _, err := fmt.Fprintf(table, "DASHBOARD\t%s\n", terminalSafe(result.Evidence.Dashboard.Title)); err != nil {
			return err
		}
		for _, panel := range result.Evidence.Panels {
			if _, err := fmt.Fprintf(table, "%s\t%s\t%s\n", verdictMark(panel.Verdict), terminalSafe(panel.Title), terminalSafe(strings.Join(panel.ReasonCodes, ","))); err != nil {
				return err
			}
			for _, query := range panel.Queries {
				if _, err := fmt.Fprintf(table, "  %s\tquery %s: %s\t%s\n", verdictMark(query.Verdict), terminalSafe(query.RefID), terminalSafe(query.EmittedKind), terminalSafe(strings.Join(query.ReasonCodes, ","))); err != nil {
					return err
				}
			}
		}
		outcome := grafanaTargetOutcome(result)
		if _, err := fmt.Fprintf(table, "summary\t%s\t%s\n", terminalSafe(result.Summary.Headline), terminalSafe(outcome)); err != nil {
			return err
		}
		// Only claim artifacts exist when the on-disk set was durably committed.
		if result.Published {
			if _, err := fmt.Fprintf(table, "artifacts\t%s\t%s\t%s\n", terminalSafe(result.DashboardPath), terminalSafe(result.ReportPath), terminalSafe(result.HTMLPath)); err != nil {
				return err
			}
			if result.CandidateDashboardPath != "" {
				if _, err := fmt.Fprintf(table, "candidate\t%s\n", terminalSafe(result.CandidateDashboardPath)); err != nil {
					return err
				}
			}
			if result.V6Path != "" {
				if _, err := fmt.Fprintf(table, "v6\t%s\n", terminalSafe(result.V6Path)); err != nil {
					return err
				}
			}
		} else {
			if _, err := fmt.Fprintf(table, "artifacts\t%s\n", "not written: artifact publication failed"); err != nil {
				return err
			}
		}
	}
	return table.Flush()
}

func grafanaTargetOutcome(result app.GrafanaResult) string {
	if result.Target != nil {
		outcome := result.Target.Action + " " + result.Target.ID
		if len(result.ValidationRejected) > 0 {
			outcome = fmt.Sprintf(
				"%s (%d widgets imported; %d validation-rejected widgets retained in candidate artifact)",
				outcome, result.ImportedWidgets, len(result.ValidationRejected),
			)
		}
		return outcome
	}
	if result.ImportAttempted || result.TargetAction == "failed" {
		outcome := "target operation failed or outcome unknown"
		if result.ImportAttempted {
			outcome = "target import attempted; failed or outcome unknown"
		}
		detail := strings.TrimSpace(result.TargetError)
		if detail == "" {
			detail = strings.TrimSpace(result.TargetSkipped)
		}
		if detail != "" {
			outcome += ": " + detail
		}
		return outcome
	}
	if result.TargetSkipped != "" {
		return "target skipped: " + result.TargetSkipped
	}
	if !result.Published {
		return "artifact publication failed; no artifacts written"
	}
	return "offline artifacts written"
}

func writeRuleResults(writer io.Writer, results []app.RuleResult, asJSON bool) error {
	if asJSON {
		for _, result := range results {
			for _, group := range result.Evidence.Groups {
				for _, rule := range group.Rules {
					if err := writeJSONLine(writer, map[string]any{
						"type": "rule", "group": group.Name, "rule": rule,
					}); err != nil {
						return fmt.Errorf("write rule result: %w", err)
					}
				}
			}
			if err := writeJSONLine(writer, map[string]any{
				"type": "summary", "summary": result.Summary,
				"artifacts":      map[string]string{"rules": result.RulesPath, "report": result.ReportPath, "html": result.HTMLPath},
				"writeRequested": result.WriteRequested, "writeAttempted": result.WriteAttempted,
				"writeSucceeded": result.WriteSucceeded, "targetAction": result.TargetAction,
				"targetError": result.TargetError, "writes": result.Writes,
				"published": result.Published,
			}); err != nil {
				return fmt.Errorf("write rule summary result: %w", err)
			}
		}
		return nil
	}
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	for _, result := range results {
		for _, group := range result.Evidence.Groups {
			if _, err := fmt.Fprintf(table, "GROUP\t%s\n", terminalSafe(group.Name)); err != nil {
				return err
			}
			for _, rule := range group.Rules {
				name := rule.Alert
				if name == "" {
					name = rule.Record
				}
				if _, err := fmt.Fprintf(table, "%s\t%s\t%s\n", verdictMark(rule.Verdict), terminalSafe(name), terminalSafe(strings.Join(rule.ReasonCodes, ","))); err != nil {
					return err
				}
			}
		}
		if _, err := fmt.Fprintf(
			table,
			"target\trequested=%t attempted=%t succeeded=%t\taction=%s\terror=%s\n",
			result.WriteRequested,
			result.WriteAttempted,
			result.WriteSucceeded,
			terminalSafe(result.TargetAction),
			terminalSafe(result.TargetError),
		); err != nil {
			return err
		}
		for _, write := range result.Writes {
			if _, err := fmt.Fprintf(
				table,
				"write\t%s\t%s\t%s\trequested=%t attempted=%t succeeded=%t\terror=%s\n",
				terminalSafe(write.Action),
				terminalSafe(write.Alert),
				terminalSafe(write.ID),
				write.Requested,
				write.Attempted,
				write.Succeeded,
				terminalSafe(write.Error),
			); err != nil {
				return err
			}
		}
		if result.Published {
			if _, err := fmt.Fprintf(
				table,
				"artifacts\t%s\t%s\t%s\temitted=%d review=%d\n",
				terminalSafe(result.RulesPath),
				terminalSafe(result.ReportPath),
				terminalSafe(result.HTMLPath),
				result.Summary.Emitted,
				result.Summary.NeedsReview,
			); err != nil {
				return err
			}
		} else {
			if _, err := fmt.Fprintf(table, "artifacts\t%s\n", "not written: artifact publication failed"); err != nil {
				return err
			}
		}
	}
	return table.Flush()
}

func verdictMark(verdict string) string {
	switch strings.ToLower(verdict) {
	case "native":
		return "✓"
	case "passthrough":
		return "≈"
	default:
		return "!"
	}
}

// terminalSafe renders untrusted artifact content as one printable terminal
// field. JSON output intentionally retains the original value for fidelity.
func terminalSafe(value string) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	var escaped strings.Builder
	for _, character := range value {
		if unicode.In(character, unicode.Cc, unicode.Cf) || character != ' ' && unicode.IsSpace(character) {
			if character <= 0xffff {
				_, _ = fmt.Fprintf(&escaped, `\u%04X`, character)
			} else {
				_, _ = fmt.Fprintf(&escaped, `\U%08X`, character)
			}
			continue
		}
		escaped.WriteRune(character)
	}
	return escaped.String()
}

func writeDifferentialSummary(writer io.Writer, summary app.DifferentialSummary, asJSON bool) error {
	if asJSON {
		return writeJSONLine(writer, summary)
	}
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	rows := []struct {
		label string
		value int
	}{
		{"queries", summary.Queries},
		{"compared", summary.Compared},
		{"equivalent", summary.Equivalent},
		{"value mismatch", summary.ValueMismatch},
		{"insufficient overlap", summary.InsufficientOverlap},
		{"no source data", summary.NoSourceData},
		{"no target data", summary.NoTargetData},
		{"both empty", summary.BothEmpty},
		{"target only data", summary.TargetOnlyData},
		{"no series match", summary.NoSeriesMatch},
		{"errors", summary.Errors},
		{"skipped", summary.Skipped},
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(table, "%s\t%d\n", row.label, row.value); err != nil {
			return err
		}
	}
	return table.Flush()
}
