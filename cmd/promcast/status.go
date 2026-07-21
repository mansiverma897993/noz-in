package main

import (
	"errors"

	"github.com/mansiverma897993/signoz/internal/app"
	"github.com/mansiverma897993/signoz/internal/model"
)

type statusError struct {
	code int
}

func (err statusError) Error() string {
	return ""
}

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	var status statusError
	if errors.As(err, &status) {
		return status.code
	}
	switch app.KindOf(err) {
	case app.ErrorInput:
		return 3
	case app.ErrorTarget:
		return 4
	}
	return 1
}

func dashboardReviewStatus(results []app.GrafanaResult) error {
	for _, result := range results {
		if result.Summary.NeedsReview > 0 || result.Summary.PanelsNeedsReview > 0 || result.Summary.VariablesNeedsReview > 0 ||
			result.Summary.SourceFeaturesNeedsReview > 0 || !result.Summary.ReconciliationComplete ||
			result.Summary.PreviewInvalid > 0 || targetImportWasSkipped(result) ||
			hasUnvalidatedEmittedQuery(result) {
			return statusError{code: 2}
		}
	}
	return nil
}

func targetImportWasSkipped(result app.GrafanaResult) bool {
	if result.TargetSkipped == "" {
		return false
	}
	if result.ImportRequested {
		return true
	}
	dryRun, _ := result.Evidence.Run.Flags["dryRun"].(bool)
	return !dryRun
}

func hasUnvalidatedEmittedQuery(result app.GrafanaResult) bool {
	validationEnabled, _ := result.Evidence.Run.Flags["validationEnabled"].(bool)
	if !validationEnabled {
		return false
	}
	for _, panel := range result.Evidence.Panels {
		for _, query := range panel.Queries {
			if query.Disabled || query.EmittedKind == string(model.TranslationNone) {
				continue
			}
			validation := query.Validation
			if !validation.Previewed || !validation.PreviewOK || !validation.Executed || !validation.DataPresent ||
				(validation.MetricChecked && !validation.MetricFound) || validation.ErrorCode != "" || validation.Error != "" {
				return true
			}
		}
	}
	return false
}

func differentialReviewStatus(summary app.DifferentialSummary) error {
	if summary.Queries == 0 || summary.Equivalent != summary.Queries {
		return statusError{code: 2}
	}
	return nil
}

func ruleReviewStatus(results []app.RuleResult) error {
	for _, result := range results {
		if result.Summary.NeedsReview > 0 || result.Summary.PreviewInvalid > 0 ||
			hasUnvalidatedEmittedRule(result) || ruleTargetWriteWasSkipped(result) {
			return statusError{code: 2}
		}
	}
	return nil
}

func hasUnvalidatedEmittedRule(result app.RuleResult) bool {
	validationEnabled, _ := result.Evidence.Run.Flags["validationEnabled"].(bool)
	if !validationEnabled {
		return false
	}
	for _, group := range result.Evidence.Groups {
		for _, rule := range group.Rules {
			if rule.TargetMigrationID == "" {
				continue
			}
			validation := rule.Validation
			if !validation.Previewed || !validation.PreviewOK || !validation.Executed ||
				validation.ErrorCode != "" || validation.Error != "" {
				return true
			}
		}
	}
	return false
}

func ruleTargetWriteWasSkipped(result app.RuleResult) bool {
	dryRun, _ := result.Evidence.Run.Flags["dryRun"].(bool)
	if dryRun || result.Evidence.Run.Target == "" || result.Summary.Emitted == 0 {
		return false
	}
	return !result.WriteRequested || result.TargetAction == "skipped" ||
		result.TargetAction == "review_only" || result.TargetAction == "partial_review"
}
