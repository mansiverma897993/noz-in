package main

import (
	"fmt"
	"testing"

	"github.com/mansiverma897993/signoz/internal/app"
	"github.com/mansiverma897993/signoz/pkg/reporttypes"
	"github.com/stretchr/testify/assert"
)

func TestCommandExitCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "clean", want: 0},
		{name: "review", err: statusError{code: 2}, want: 2},
		{name: "input", err: &app.Error{Kind: app.ErrorInput, Err: fmt.Errorf("bad JSON")}, want: 3},
		{name: "target", err: &app.Error{Kind: app.ErrorTarget, Err: fmt.Errorf("unauthorized")}, want: 4},
		{name: "internal", err: fmt.Errorf("unexpected"), want: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, test.want, commandExitCode(test.err))
		})
	}
}

func TestDashboardReviewStatusRejectsFailedNativeValidation(t *testing.T) {
	t.Parallel()

	result := app.GrafanaResult{
		TargetSkipped: "one or more emitted queries failed target validation",
		Summary:       reporttypes.Summary{Panels: 1, PanelsAccounted: 1, Queries: 1, QueriesAccounted: 1, ReconciliationComplete: true},
		Evidence: reporttypes.Report{
			Run: reporttypes.Run{Flags: map[string]any{"validationEnabled": true}},
			Panels: []reporttypes.PanelRecord{{Queries: []reporttypes.QueryRecord{{
				RefID: "A", EmittedKind: "builder", Verdict: "native",
				Validation: reporttypes.Validation{Previewed: true, PreviewOK: false, ErrorCode: "widget_rejected"},
			}}}},
		},
	}

	assert.Equal(t, 2, commandExitCode(dashboardReviewStatus([]app.GrafanaResult{result})))
}

func TestDashboardReviewStatusReportsNonDryRunTargetSkip(t *testing.T) {
	t.Parallel()

	clean := reporttypes.Summary{
		Panels: 1, PanelsAccounted: 1, Queries: 1, QueriesAccounted: 1, ReconciliationComplete: true,
	}
	result := app.GrafanaResult{
		TargetSkipped: "no executable widgets were emitted; dashboard was not imported",
		Summary:       clean,
		Evidence: reporttypes.Report{Run: reporttypes.Run{Flags: map[string]any{
			"dryRun": false,
		}}},
	}

	assert.Equal(t, 2, commandExitCode(dashboardReviewStatus([]app.GrafanaResult{result})))
}

func TestDashboardReviewStatusDoesNotTreatDocumentedDryRunSkipAsFailure(t *testing.T) {
	t.Parallel()

	result := app.GrafanaResult{
		TargetSkipped: "dry run: target validation passed; dashboard was not imported",
		Summary: reporttypes.Summary{
			Panels: 1, PanelsAccounted: 1, Queries: 1, QueriesAccounted: 1, ReconciliationComplete: true,
		},
		Evidence: reporttypes.Report{
			Run: reporttypes.Run{Flags: map[string]any{"dryRun": true, "validationEnabled": true}},
			Panels: []reporttypes.PanelRecord{{Queries: []reporttypes.QueryRecord{{
				RefID: "A", EmittedKind: "builder", Verdict: "native",
				Validation: reporttypes.Validation{
					Previewed: true, PreviewOK: true, Executed: true, DataPresent: true,
				},
			}}}},
		},
	}

	assert.Equal(t, 0, commandExitCode(dashboardReviewStatus([]app.GrafanaResult{result})))
}

func TestDashboardReviewStatusRequiresDataWhenValidationIsEnabled(t *testing.T) {
	t.Parallel()

	result := app.GrafanaResult{
		Summary: reporttypes.Summary{
			Panels: 1, PanelsAccounted: 1, Queries: 1, QueriesAccounted: 1, ReconciliationComplete: true,
		},
		Evidence: reporttypes.Report{
			Run: reporttypes.Run{Flags: map[string]any{"validationEnabled": true}},
			Panels: []reporttypes.PanelRecord{{Queries: []reporttypes.QueryRecord{{
				RefID: "A", EmittedKind: "builder", Verdict: "native",
				Validation: reporttypes.Validation{
					Previewed: true, PreviewOK: true, Executed: true, DataPresent: false,
				},
			}}}},
		},
	}

	assert.Equal(t, 2, commandExitCode(dashboardReviewStatus([]app.GrafanaResult{result})))
}

func TestDifferentialReviewStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		summary app.DifferentialSummary
		code    int
	}{
		{name: "all equivalent", summary: app.DifferentialSummary{Queries: 2, Compared: 2, Equivalent: 2}, code: 0},
		{name: "value mismatch", summary: app.DifferentialSummary{Queries: 1, Compared: 1, ValueMismatch: 1}, code: 2},
		{name: "target only", summary: app.DifferentialSummary{Queries: 1, Compared: 1, TargetOnlyData: 1}, code: 2},
		{name: "error", summary: app.DifferentialSummary{Queries: 1, Errors: 1}, code: 2},
		{name: "skipped", summary: app.DifferentialSummary{Queries: 1, Skipped: 1}, code: 2},
		{name: "empty", summary: app.DifferentialSummary{}, code: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.code, commandExitCode(differentialReviewStatus(test.summary)))
		})
	}
}

func TestRuleReviewStatusRejectsInvalidOrUnverifiedLiveRules(t *testing.T) {
	t.Parallel()

	validRecord := reporttypes.RuleRecord{
		TargetMigrationID: "managed-rule",
		Validation:        reporttypes.Validation{Previewed: true, PreviewOK: true, Executed: true},
	}
	for _, test := range []struct {
		name   string
		result app.RuleResult
		code   int
	}{
		{
			name: "clean validated write",
			result: app.RuleResult{
				Summary: reporttypes.RuleSummary{Emitted: 1}, WriteRequested: true, WriteSucceeded: true,
				TargetAction: "succeeded",
				Evidence: reporttypes.RuleReport{
					Run:    reporttypes.Run{Target: "https://signoz.example", Flags: map[string]any{"validationEnabled": true}},
					Groups: []reporttypes.RuleGroupRecord{{Rules: []reporttypes.RuleRecord{validRecord}}},
				},
			},
			code: 0,
		},
		{
			name: "invalid preview and skipped write",
			result: app.RuleResult{
				Summary: reporttypes.RuleSummary{Emitted: 1, PreviewInvalid: 1}, TargetAction: "skipped",
				Evidence: reporttypes.RuleReport{
					Run: reporttypes.Run{Target: "https://signoz.example", Flags: map[string]any{"validationEnabled": true}},
					Groups: []reporttypes.RuleGroupRecord{{Rules: []reporttypes.RuleRecord{{
						TargetMigrationID: "managed-rule",
						Validation:        reporttypes.Validation{Previewed: true, PreviewOK: false},
					}}}},
				},
			},
			code: 2,
		},
		{
			name: "emitted rule never executed",
			result: app.RuleResult{
				Summary: reporttypes.RuleSummary{Emitted: 1}, WriteRequested: true,
				Evidence: reporttypes.RuleReport{
					Run: reporttypes.Run{Target: "https://signoz.example", Flags: map[string]any{"validationEnabled": true}},
					Groups: []reporttypes.RuleGroupRecord{{Rules: []reporttypes.RuleRecord{{
						TargetMigrationID: "managed-rule",
						Validation:        reporttypes.Validation{Previewed: true, PreviewOK: true},
					}}}},
				},
			},
			code: 2,
		},
		{
			name: "missing disabled candidate retained for review",
			result: app.RuleResult{
				Summary:        reporttypes.RuleSummary{Emitted: 1, NotCreatedDisabled: 1},
				WriteRequested: true, TargetAction: "review_only",
				Evidence: reporttypes.RuleReport{Run: reporttypes.Run{
					Target: "https://signoz.example", Flags: map[string]any{"validationEnabled": false},
				}},
			},
			code: 2,
		},
		{
			name: "mixed update and review-only candidate",
			result: app.RuleResult{
				Summary:        reporttypes.RuleSummary{Emitted: 2, Updated: 1, NotCreatedDisabled: 1},
				WriteRequested: true, WriteAttempted: true, TargetAction: "partial_review",
				Evidence: reporttypes.RuleReport{Run: reporttypes.Run{
					Target: "https://signoz.example", Flags: map[string]any{"validationEnabled": false},
				}},
			},
			code: 2,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, test.code, commandExitCode(ruleReviewStatus([]app.RuleResult{test.result})))
		})
	}
}
