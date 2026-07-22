package validate

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mansiverma897993/noz-in/internal/rules"
	"github.com/mansiverma897993/noz-in/internal/target/signoz"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
)

// RuleCandidate identifies an alert that passed target preflight.
type RuleCandidate struct {
	GroupIndex int
	RuleIndex  int
	Payload    signoz.AlertRuleV2
}

type ruleResult struct {
	candidate  RuleCandidate
	validation reporttypes.Validation
	accepted   bool
	err        error
}

// AlertRules previews and executes emitted alerts, returning only safe writes.
func AlertRules(
	ctx context.Context,
	client *signoz.Client,
	migration rules.Migration,
	evidence *reporttypes.RuleReport,
	preflight bool,
	options Options,
) ([]RuleCandidate, error) {
	candidates := ruleCandidates(migration)
	if !preflight || len(candidates) == 0 {
		return candidates, nil
	}
	workers := options.Workers
	if workers <= 0 {
		workers = 4
	}
	workers = min(workers, len(candidates))
	now := time.Now()
	if options.Now != nil {
		now = options.Now()
	}

	jobs := make(chan RuleCandidate)
	results := make(chan ruleResult, len(candidates))
	var waitGroup sync.WaitGroup
	for range workers {
		waitGroup.Go(func() {
			for candidate := range jobs {
				validation, accepted, err := validateRule(ctx, client, candidate.Payload, now)
				results <- ruleResult{candidate: candidate, validation: validation, accepted: accepted, err: err}
			}
		})
	}
	go func() {
		defer close(jobs)
		for _, candidate := range candidates {
			jobs <- candidate
		}
	}()
	go func() {
		waitGroup.Wait()
		close(results)
	}()

	byPosition := make(map[[2]int]ruleResult, len(candidates))
	var firstError error
	for result := range results {
		key := [2]int{result.candidate.GroupIndex, result.candidate.RuleIndex}
		byPosition[key] = result
		if result.err != nil && firstError == nil {
			firstError = result.err
		}
	}
	if firstError != nil {
		return nil, firstError
	}

	resetRuleValidationSummary(&evidence.Summary)
	accepted := make([]RuleCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		result := byPosition[[2]int{candidate.GroupIndex, candidate.RuleIndex}]
		evidence.Groups[candidate.GroupIndex].Rules[candidate.RuleIndex].Validation = result.validation
		addRuleValidationSummary(&evidence.Summary, result.validation)
		if result.accepted {
			accepted = append(accepted, candidate)
		}
	}
	return accepted, nil
}

func ruleCandidates(migration rules.Migration) []RuleCandidate {
	var candidates []RuleCandidate
	for groupIndex, group := range migration.Groups {
		for ruleIndex, rule := range group.Rules {
			if rule.Payload == nil {
				continue
			}
			candidates = append(candidates, RuleCandidate{
				GroupIndex: groupIndex,
				RuleIndex:  ruleIndex,
				Payload:    *rule.Payload,
			})
		}
	}
	return candidates
}

func validateRule(
	ctx context.Context,
	client *signoz.Client,
	payload signoz.AlertRuleV2,
	now time.Time,
) (reporttypes.Validation, bool, error) {
	request := signoz.QueryRequestForAlert(payload, now)
	previews, err := client.Preview(ctx, request)
	if err != nil {
		return reporttypes.Validation{}, false, fmt.Errorf("preview alert %q: %w", payload.Alert, err)
	}
	validation := reporttypes.Validation{
		Previewed: true,
		CheckedAt: now.UTC().Format(time.RFC3339Nano),
	}
	preview, found := previews["A"]
	if !found {
		validation.ErrorCode = "PREVIEW_RESULT_MISSING"
		validation.Error = "SigNoz preview response did not include query A"
		return validation, false, nil
	}
	validation.PreviewOK = preview.Valid
	validation.PreviewStatements = preview.Statements
	validation.PreviewWarnings = preview.Warnings
	if preview.Valid && previewErrorPresent(preview.Error) {
		validation.PreviewOK = false
		validation.ErrorCode = "PREVIEW_RESPONSE_INCONSISTENT"
		validation.Error = "SigNoz preview marked query A valid while also returning an error"
		return validation, false, nil
	}
	if !preview.Valid {
		validation.ErrorCode, validation.Error = previewFailure(preview.Error)
		return validation, false, nil
	}

	executions, err := client.QueryRange(ctx, request)
	if err != nil {
		return reporttypes.Validation{}, false, fmt.Errorf("execute alert %q: %w", payload.Alert, err)
	}
	execution, found := executions["A"]
	if !found {
		validation.ErrorCode = "EXECUTION_RESULT_MISSING"
		validation.Error = "SigNoz query response did not include query A"
		return validation, false, nil
	}
	validation.Executed = true
	validation.DataPresent = execution.HasData()
	validation.Series = execution.Series
	validation.Points = execution.Points
	validation.Rows = execution.Rows
	validation.Samples = sampleSeries(execution.Sample)
	return validation, true, nil
}

func resetRuleValidationSummary(summary *reporttypes.RuleSummary) {
	summary.Previewed = 0
	summary.PreviewValid = 0
	summary.PreviewInvalid = 0
	summary.Executed = 0
	summary.DataPresent = 0
	summary.DataAbsent = 0
}

func addRuleValidationSummary(summary *reporttypes.RuleSummary, validation reporttypes.Validation) {
	if validation.Previewed {
		summary.Previewed++
		if validation.PreviewOK {
			summary.PreviewValid++
		} else {
			summary.PreviewInvalid++
		}
	}
	if validation.Executed {
		summary.Executed++
		if validation.DataPresent {
			summary.DataPresent++
		} else {
			summary.DataAbsent++
		}
	}
}
