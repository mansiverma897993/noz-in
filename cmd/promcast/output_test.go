package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/mansiverma897993/signoz/internal/app"
	"github.com/mansiverma897993/signoz/internal/target/signoz"
	"github.com/mansiverma897993/signoz/pkg/reporttypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteGrafanaJSONSummaryIncludesTargetOutcomeContract(t *testing.T) {
	t.Parallel()

	result := app.GrafanaResult{
		ImportRequested: true, ImportAttempted: true, ImportSucceeded: false,
		TargetAction: "failed", TargetDashboardID: "dashboard-1", TargetError: "target rejected import",
		TargetSkipped: "dashboard outcome is unknown", PartialImportEligible: true, PartialImportPerformed: false,
	}
	var output bytes.Buffer
	require.NoError(t, writeGrafanaResults(&output, []app.GrafanaResult{result}, true))

	var summary map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(output.Bytes()), &summary))
	assert.Equal(t, "summary", summary["type"])
	assert.Equal(t, true, summary["importRequested"])
	assert.Equal(t, true, summary["importAttempted"])
	assert.Equal(t, false, summary["importSucceeded"])
	assert.Equal(t, "failed", summary["targetAction"])
	assert.Equal(t, "dashboard-1", summary["targetDashboardId"])
	assert.Equal(t, "target rejected import", summary["targetError"])
	assert.Equal(t, "dashboard outcome is unknown", summary["targetSkipped"])
	assert.Equal(t, true, summary["partialImportEligible"])
	assert.Equal(t, false, summary["partialImportPerformed"])
}

func TestWriteGrafanaHumanSummaryDoesNotCallFailedAttemptSkipped(t *testing.T) {
	t.Parallel()

	for _, result := range []app.GrafanaResult{
		{
			ImportRequested: true, ImportAttempted: true, TargetAction: "failed",
			TargetError: "target rejected import", TargetSkipped: "target outcome is unchanged or unknown",
		},
		{
			ImportRequested: true, TargetAction: "failed",
			TargetError: "target validation failed before the write result was known",
		},
	} {
		var output bytes.Buffer
		require.NoError(t, writeGrafanaResults(&output, []app.GrafanaResult{result}, false))
		assert.Contains(t, output.String(), "failed or outcome unknown")
		assert.NotContains(t, output.String(), "target skipped")
	}
}

func TestWriteGrafanaResultsDoesNotClaimUnpublishedArtifacts(t *testing.T) {
	t.Parallel()

	unpublished := app.GrafanaResult{
		DashboardPath: "out/dash.signoz.json",
		ReportPath:    "out/dash.report.json",
		HTMLPath:      "out/dash.report.html",
		Summary:       reporttypes.Summary{Headline: "0 native"},
		Published:     false,
	}

	var human bytes.Buffer
	require.NoError(t, writeGrafanaResults(&human, []app.GrafanaResult{unpublished}, false))
	assert.NotContains(t, human.String(), "out/dash.signoz.json")
	assert.NotContains(t, human.String(), "offline artifacts written")
	assert.Contains(t, human.String(), "not written")

	var jsonBuf bytes.Buffer
	require.NoError(t, writeGrafanaResults(&jsonBuf, []app.GrafanaResult{unpublished}, true))
	var summary map[string]any
	// The summary line is the last JSON object emitted for the dashboard.
	lines := bytes.Split(bytes.TrimSpace(jsonBuf.Bytes()), []byte("\n"))
	require.NoError(t, json.Unmarshal(lines[len(lines)-1], &summary))
	assert.Equal(t, false, summary["published"])

	published := unpublished
	published.Published = true
	var okBuf bytes.Buffer
	require.NoError(t, writeGrafanaResults(&okBuf, []app.GrafanaResult{published}, false))
	assert.Contains(t, okBuf.String(), "out/dash.signoz.json")
	assert.Contains(t, okBuf.String(), "offline artifacts written")
}

func TestWriteRuleResultsExposePartialWriteOutcomeInJSONAndHumanOutput(t *testing.T) {
	t.Parallel()

	result := app.RuleResult{
		Summary:        reporttypes.RuleSummary{Rules: 2, Emitted: 2},
		WriteRequested: true,
		WriteAttempted: true,
		WriteSucceeded: false,
		TargetAction:   "partial",
		TargetError:    "second rule was rejected",
		Writes: []signoz.AlertRuleWriteResult{
			{
				ID: "first-id", Alert: "FirstDown", Action: "created",
				Requested: true, Attempted: true, Succeeded: true,
			},
		},
	}

	var jsonOutput bytes.Buffer
	require.NoError(t, writeRuleResults(&jsonOutput, []app.RuleResult{result}, true))
	var summary map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(jsonOutput.Bytes()), &summary))
	assert.Equal(t, true, summary["writeRequested"])
	assert.Equal(t, true, summary["writeAttempted"])
	assert.Equal(t, false, summary["writeSucceeded"])
	assert.Equal(t, "partial", summary["targetAction"])
	assert.Equal(t, "second rule was rejected", summary["targetError"])
	writes, ok := summary["writes"].([]any)
	require.True(t, ok)
	require.Len(t, writes, 1)
	assert.Equal(t, "first-id", writes[0].(map[string]any)["id"])
	assert.Equal(t, true, writes[0].(map[string]any)["requested"])
	assert.Equal(t, true, writes[0].(map[string]any)["attempted"])
	assert.Equal(t, true, writes[0].(map[string]any)["succeeded"])

	var humanOutput bytes.Buffer
	require.NoError(t, writeRuleResults(&humanOutput, []app.RuleResult{result}, false))
	for _, detail := range []string{
		"requested=true", "attempted=true", "succeeded=false", "partial",
		"second rule was rejected", "created", "FirstDown", "first-id",
	} {
		assert.Contains(t, humanOutput.String(), detail)
	}
}

func TestWriteRuleResultsExplainsReviewOnlyDisabledCandidate(t *testing.T) {
	t.Parallel()

	result := app.RuleResult{
		Summary:        reporttypes.RuleSummary{Rules: 1, Emitted: 1, NotCreatedDisabled: 1},
		WriteRequested: true, TargetAction: "review_only",
		Writes: []signoz.AlertRuleWriteResult{{
			Alert: "NodeDown", Action: signoz.AlertRuleActionNotCreatedDisabled, Requested: true,
		}},
	}
	var output bytes.Buffer
	require.NoError(t, writeRuleResults(&output, []app.RuleResult{result}, false))
	for _, detail := range []string{
		"review_only", signoz.AlertRuleActionNotCreatedDisabled, "NodeDown",
		"requested=true attempted=false succeeded=false",
	} {
		assert.Contains(t, output.String(), detail)
	}
}

func TestWriteDifferentialSummaryIncludesEveryOutcome(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	err := writeDifferentialSummary(&output, app.DifferentialSummary{
		Queries: 12, Compared: 11, Equivalent: 1, ValueMismatch: 1,
		InsufficientOverlap: 2, NoSourceData: 3, NoTargetData: 4,
		BothEmpty: 5, TargetOnlyData: 6, NoSeriesMatch: 7, Errors: 8, Skipped: 9,
	}, false)
	require.NoError(t, err)

	for _, label := range []string{
		"queries", "compared", "equivalent", "value mismatch", "insufficient overlap",
		"no source data", "no target data", "both empty", "target only data",
		"no series match", "errors", "skipped",
	} {
		assert.Contains(t, output.String(), label)
	}
}

func TestHumanOutputEscapesTerminalControlAndFormatCharacters(t *testing.T) {
	t.Parallel()

	result := app.GrafanaResult{
		Evidence: reporttypes.Report{
			Dashboard: reporttypes.DashboardInfo{Title: "safe\x1b[2J\nforged\u2028split"},
			Panels: []reporttypes.PanelRecord{{
				Title: "panel\tname\u202E", Verdict: "native",
			}},
		},
		Summary:      reporttypes.Summary{Headline: "ok\rnot-ok"},
		TargetError:  "failed\u009B31m",
		TargetAction: "failed",
	}
	var output bytes.Buffer
	require.NoError(t, writeGrafanaResults(&output, []app.GrafanaResult{result}, false))

	assert.NotContains(t, output.String(), "\x1b")
	assert.NotContains(t, output.String(), "\nforged")
	assert.NotContains(t, output.String(), "\tname")
	assert.NotContains(t, output.String(), "\u202E")
	for _, escaped := range []string{`\u001B`, `\u000A`, `\u0009`, `\u202E`, `\u000D`, `\u009B`, `\u2028`} {
		assert.Contains(t, output.String(), escaped)
	}
}
