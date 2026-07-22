package app

// Per-rule-file migration flow: evidence construction, artifact publication
// checkpoints, and the live alert-rule target write sequence.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/mansiverma897993/noz-in/internal/report"
	"github.com/mansiverma897993/noz-in/internal/rules"
	"github.com/mansiverma897993/noz-in/internal/target/signoz"
	"github.com/mansiverma897993/noz-in/internal/validate"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
)

func migratePrometheusRuleFile(
	ctx context.Context,
	path string,
	base string,
	migration rules.Migration,
	plan ruleMigrationPlan,
) (RuleResult, bool, error) {
	evidence, err := report.BuildRules(migration)
	if err != nil {
		return RuleResult{}, false, fmt.Errorf("build Prometheus rule evidence for %q: %w", path, err)
	}
	configureRuleEvidence(&evidence, plan)
	payloads := rulePayloads(migration)
	result := newRuleResult(base, plan.options, plan.client)
	primaryArtifact, err := artifactBindingFor(result.RulesPath, payloads)
	if err != nil {
		return result, false, err
	}
	evidence.PrimaryArtifact = primaryArtifact
	recordRuleTargetOutcome(&evidence, result)
	if err := ensureArtifactDestinations(result.RulesPath, result.ReportPath, result.HTMLPath); err != nil {
		return result, false, err
	}

	pending, operationErr := prepareRuleTargetOperation(ctx, path, migration, plan, &result, &evidence)
	recordRuleTargetOutcome(&evidence, result)
	if err := publishRuleArtifactSet(&result, &evidence, payloads); err != nil {
		return result, false, err
	}
	if err := checkpointRuleArtifacts(plan.options.ArtifactCheckpoint, result); err != nil {
		result.TargetAction = "failed"
		result.TargetError = err.Error()
		recordRuleTargetOutcome(&evidence, result)
		repairErr := publishRuleArtifactSet(&result, &evidence, payloads)
		return result, true, errors.Join(err, repairErr)
	}
	if err := ValidateStoredRuleArtifact(result.ReportPath, evidence); err != nil {
		return result, false, fmt.Errorf("validate published Prometheus rule artifact: %w", err)
	}
	if operationErr != nil {
		return result, true, operationErr
	}
	if !result.WriteRequested {
		return result, true, nil
	}
	return attemptRuleTargetWrite(
		ctx, path, plan.client, pending, payloads, result, evidence, plan.options.ArtifactCheckpoint,
	)
}

func configureRuleEvidence(evidence *reporttypes.RuleReport, plan ruleMigrationPlan) {
	evidence.Run = reporttypes.Run{
		StartedAt: plan.startedAt.Format(time.RFC3339Nano),
		Target:    plan.options.TargetURL,
		Flags: map[string]any{
			"dryRun":             plan.options.DryRun,
			"offline":            plan.client == nil,
			"alertOnAbsent":      plan.options.AlertOnAbsent,
			"validationEnabled":  plan.client != nil && plan.options.Validate,
			"validationWorkers":  effectiveWorkers(plan.options.ValidationWorkers),
			"metricNameMappings": len(plan.options.MetricNameMap),
			"allowInsecureHTTP":  plan.options.AllowInsecureHTTP,
		},
	}
}

func newRuleResult(base string, options RuleOptions, client *signoz.Client) RuleResult {
	return RuleResult{
		RulesPath:    filepath.Join(options.OutputDirectory, base+".signoz-rules.json"),
		ReportPath:   filepath.Join(options.OutputDirectory, base+".rules-report.json"),
		HTMLPath:     filepath.Join(options.OutputDirectory, base+".rules-report.html"),
		TargetAction: initialRuleTargetAction(client, options.DryRun),
	}
}

func prepareRuleTargetOperation(
	ctx context.Context,
	path string,
	migration rules.Migration,
	plan ruleMigrationPlan,
	result *RuleResult,
	evidence *reporttypes.RuleReport,
) ([]validate.RuleCandidate, error) {
	var pending []validate.RuleCandidate
	if plan.client != nil {
		var err error
		pending, err = validate.AlertRules(
			ctx,
			plan.client,
			migration,
			evidence,
			plan.options.Validate,
			validate.Options{Workers: plan.options.ValidationWorkers},
		)
		if err != nil {
			operationErr := targetError(fmt.Errorf("validate Prometheus rules %q: %w", path, err))
			result.TargetAction = "failed"
			result.TargetError = operationErr.Error()
			return pending, operationErr
		}
	}
	writeEnabled := plan.client != nil && !plan.options.DryRun
	prepareRuleWriteRecords(evidence, pending, writeEnabled)
	if writeEnabled && len(pending) > 0 {
		result.WriteRequested = true
		result.TargetAction = "ready"
	} else if writeEnabled {
		result.TargetAction = "skipped"
	}
	return pending, nil
}

func attemptRuleTargetWrite(
	ctx context.Context,
	path string,
	client *signoz.Client,
	pending []validate.RuleCandidate,
	payloads []signoz.AlertRuleV2,
	result RuleResult,
	evidence reporttypes.RuleReport,
	artifactCheckpoint func(RuleResult) error,
) (RuleResult, bool, error) {
	result.TargetAction = "planning"
	recordRuleTargetOutcome(&evidence, result)
	if err := publishRuleArtifactSet(&result, &evidence, payloads); err != nil {
		return result, false, err
	}

	pendingPayloads := make([]signoz.AlertRuleV2, 0, len(pending))
	for _, candidate := range pending {
		pendingPayloads = append(pendingPayloads, candidate.Payload)
	}
	var checkpointErr error
	writes, writeErr := client.UpsertAlertRulesWithCheckpoints(
		ctx,
		pendingPayloads,
		signoz.AlertRuleWriteCheckpoints{
			Planned: func(plan []signoz.AlertRuleWritePlan) error {
				if err := applyRuleWritePlan(&evidence, pending, plan); err != nil {
					checkpointErr = err
					return err
				}
				result.TargetAction = plannedRuleWriteAction(plan)
				result.Writes = plannedRuleWriteResults(plan)
				recordRuleTargetOutcome(&evidence, result)
				if err := publishRuleArtifactSet(&result, &evidence, payloads); err != nil {
					checkpointErr = err
					return err
				}
				checkpointErr = checkpointRuleArtifacts(artifactCheckpoint, result)
				return checkpointErr
			},
			BeforeMutation: func(checkpoint signoz.AlertRuleMutationCheckpoint) error {
				if err := applyRuleMutationCheckpoint(&evidence, pending, checkpoint); err != nil {
					checkpointErr = err
					return err
				}
				result.Writes = mutationCheckpointWriteResults(checkpoint)
				result.WriteAttempted = true
				result.WriteSucceeded = false
				result.TargetAction = "attempted"
				result.TargetError = ""
				recordRuleTargetOutcome(&evidence, result)
				if err := publishRuleArtifactSet(&result, &evidence, payloads); err != nil {
					checkpointErr = err
					return err
				}
				checkpointErr = checkpointRuleArtifacts(artifactCheckpoint, result)
				return checkpointErr
			},
		},
	)
	if checkpointErr != nil {
		writes = completeRuleWriteOutcomes(pending, writes, checkpointErr)
		recordRuleWriteOutcomes(&evidence, pending, writes)
		result.Writes = writes
		result.WriteAttempted = anyRuleWriteAttempted(writes)
		result.WriteSucceeded = false
		result.TargetAction = "failed"
		result.TargetError = checkpointErr.Error()
		recordRuleTargetOutcome(&evidence, result)
		repairErr := publishRuleArtifactSet(&result, &evidence, payloads)
		if repairErr != nil {
			return result, true, errors.Join(checkpointErr, fmt.Errorf("repair rule checkpoint failure: %w", repairErr))
		}
		return result, true, checkpointErr
	}
	writes = completeRuleWriteOutcomes(pending, writes, writeErr)
	recordRuleWriteOutcomes(&evidence, pending, writes)
	result.Writes = writes
	result.WriteAttempted = anyRuleWriteAttempted(writes)
	result.WriteSucceeded = allRuleWritesSucceeded(writes)
	result.TargetAction = ruleWriteAction(writes, writeErr)
	var operationErr error
	if writeErr != nil {
		operationErr = targetError(fmt.Errorf("migrate Prometheus rules %q: %w", path, writeErr))
		result.TargetError = operationErr.Error()
	}
	recordRuleTargetOutcome(&evidence, result)
	artifactErr := publishRuleArtifactSet(&result, &evidence, payloads)
	if artifactErr == nil {
		artifactErr = checkpointRuleArtifacts(artifactCheckpoint, result)
	}
	if operationErr != nil || artifactErr != nil {
		return result, true, finishRuleError(operationErr, artifactErr)
	}
	return result, true, nil
}

func ruleWriteAction(writes []signoz.AlertRuleWriteResult, writeErr error) string {
	succeeded := 0
	reviewOnly := 0
	for _, write := range writes {
		if write.Succeeded {
			succeeded++
		}
		if write.Action == signoz.AlertRuleActionNotCreatedDisabled {
			reviewOnly++
		}
	}
	if writeErr != nil {
		if succeeded > 0 {
			return "partial"
		}
		return "failed"
	}
	if reviewOnly == len(writes) && reviewOnly > 0 {
		return "review_only"
	}
	if reviewOnly > 0 {
		return "partial_review"
	}
	if succeeded == len(writes) && succeeded > 0 {
		return "succeeded"
	}
	return "skipped"
}

func plannedRuleWriteAction(plan []signoz.AlertRuleWritePlan) string {
	reviewOnly := 0
	for _, item := range plan {
		if item.Action == signoz.AlertRuleActionNotCreatedDisabled {
			reviewOnly++
		}
	}
	switch {
	case reviewOnly == len(plan) && reviewOnly > 0:
		return "review_only"
	case reviewOnly > 0:
		return "partial_review_planned"
	default:
		return "planned"
	}
}

func rulePayloads(migration rules.Migration) []signoz.AlertRuleV2 {
	payloads := make([]signoz.AlertRuleV2, 0)
	for _, group := range migration.Groups {
		for _, rule := range group.Rules {
			if rule.Payload != nil {
				payloads = append(payloads, *rule.Payload)
			}
		}
	}
	return payloads
}

func initialRuleTargetAction(client *signoz.Client, dryRun bool) string {
	if client == nil {
		return "offline"
	}
	if dryRun {
		return "dry_run"
	}
	return "pending"
}

func finishRuleError(operationErr, artifactErr error) error {
	switch {
	case operationErr == nil && artifactErr == nil:
		return nil
	case operationErr == nil:
		return artifactErr
	case artifactErr == nil:
		return operationErr
	default:
		return &Error{Kind: KindOf(operationErr), Err: errors.Join(
			operationErr,
			fmt.Errorf("publish rule migration outcome: %w", artifactErr),
		)}
	}
}

func checkpointRuleArtifacts(checkpoint func(RuleResult) error, result RuleResult) error {
	if checkpoint == nil {
		return nil
	}
	if err := checkpoint(result); err != nil {
		return fmt.Errorf("publish rule artifact checkpoint: %w", err)
	}
	return nil
}
