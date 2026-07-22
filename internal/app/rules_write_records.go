package app

// Rule write bookkeeping: mapping planned/attempted/completed target write
// outcomes onto the rule evidence report and its summary counters.

import (
	"fmt"
	"strings"

	"github.com/mansiverma897993/noz-in/internal/target/signoz"
	"github.com/mansiverma897993/noz-in/internal/validate"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
)

func applyRuleWritePlan(
	evidence *reporttypes.RuleReport,
	pending []validate.RuleCandidate,
	plan []signoz.AlertRuleWritePlan,
) error {
	if len(plan) != len(pending) {
		return fmt.Errorf("target planned %d alert rule outcomes for %d candidates", len(plan), len(pending))
	}
	for index, item := range plan {
		candidate := pending[index]
		record := &evidence.Groups[candidate.GroupIndex].Rules[candidate.RuleIndex]
		action := "pending_" + item.Action
		if item.Action == signoz.AlertRuleActionNotCreatedDisabled {
			action = item.Action
		}
		record.Write = &reporttypes.RuleWriteRecord{
			Requested: true, ID: item.ID, Action: action,
		}
	}
	refreshRuleWriteSummary(evidence)
	return nil
}

func plannedRuleWriteResults(plan []signoz.AlertRuleWritePlan) []signoz.AlertRuleWriteResult {
	results := make([]signoz.AlertRuleWriteResult, 0, len(plan))
	for _, item := range plan {
		action := "pending_" + item.Action
		if item.Action == signoz.AlertRuleActionNotCreatedDisabled {
			action = item.Action
		}
		results = append(results, signoz.AlertRuleWriteResult{
			ID: item.ID, Alert: item.Alert, Action: action, Requested: true,
		})
	}
	return results
}

func applyRuleMutationCheckpoint(
	evidence *reporttypes.RuleReport,
	pending []validate.RuleCandidate,
	checkpoint signoz.AlertRuleMutationCheckpoint,
) error {
	if checkpoint.Index < 0 || checkpoint.Index >= len(pending) {
		return fmt.Errorf("target checkpointed alert rule index %d for %d candidates", checkpoint.Index, len(pending))
	}
	if len(checkpoint.Completed) != checkpoint.Index {
		return fmt.Errorf(
			"target checkpointed %d completed alert rule outcomes before candidate %d",
			len(checkpoint.Completed), checkpoint.Index,
		)
	}
	candidate := pending[checkpoint.Index]
	if checkpoint.Alert != candidate.Payload.Alert {
		return fmt.Errorf(
			"target checkpointed alert rule %q at candidate %d; expected %q",
			checkpoint.Alert, checkpoint.Index, candidate.Payload.Alert,
		)
	}
	if checkpoint.Action != signoz.AlertRulePlanCreate && checkpoint.Action != signoz.AlertRulePlanUpdate {
		return fmt.Errorf("target checkpointed unsupported alert rule action %q", checkpoint.Action)
	}
	recordRuleWriteOutcomes(evidence, pending, checkpoint.Completed)
	record := &evidence.Groups[candidate.GroupIndex].Rules[candidate.RuleIndex]
	record.Write = &reporttypes.RuleWriteRecord{
		Requested: true, Attempted: true, ID: checkpoint.ID, Action: "attempting_" + checkpoint.Action,
	}
	refreshRuleWriteSummary(evidence)
	return nil
}

func mutationCheckpointWriteResults(
	checkpoint signoz.AlertRuleMutationCheckpoint,
) []signoz.AlertRuleWriteResult {
	results := append([]signoz.AlertRuleWriteResult(nil), checkpoint.Completed...)
	return append(results, signoz.AlertRuleWriteResult{
		ID: checkpoint.ID, Alert: checkpoint.Alert, Action: "attempting_" + checkpoint.Action,
		Requested: true, Attempted: true,
	})
}

func completeRuleWriteOutcomes(
	pending []validate.RuleCandidate,
	writes []signoz.AlertRuleWriteResult,
	writeErr error,
) []signoz.AlertRuleWriteResult {
	if len(writes) >= len(pending) {
		return writes
	}
	message := "rule write did not return an outcome"
	if writeErr != nil {
		message = writeErr.Error()
	}
	result := append([]signoz.AlertRuleWriteResult(nil), writes...)
	for index := len(writes); index < len(pending); index++ {
		result = append(result, signoz.AlertRuleWriteResult{
			Alert: pending[index].Payload.Alert, Action: signoz.AlertRuleActionNotAttempted,
			Requested: true, Error: message,
		})
	}
	return result
}

func recordRuleWriteOutcomes(
	evidence *reporttypes.RuleReport,
	pending []validate.RuleCandidate,
	writes []signoz.AlertRuleWriteResult,
) {
	for index, write := range writes {
		if index >= len(pending) {
			break
		}
		candidate := pending[index]
		record := &evidence.Groups[candidate.GroupIndex].Rules[candidate.RuleIndex]
		record.Write = &reporttypes.RuleWriteRecord{
			Requested: write.Requested, Attempted: write.Attempted, Succeeded: write.Succeeded,
			ID: write.ID, Action: write.Action, Error: write.Error,
		}
	}
	refreshRuleWriteSummary(evidence)
}

func refreshRuleWriteSummary(evidence *reporttypes.RuleReport) {
	evidence.Summary.Created = 0
	evidence.Summary.Updated = 0
	evidence.Summary.NotCreatedDisabled = 0
	for _, group := range evidence.Groups {
		for _, rule := range group.Rules {
			if rule.Write == nil {
				continue
			}
			switch {
			case rule.Write.Succeeded && strings.EqualFold(rule.Write.Action, "created"):
				evidence.Summary.Created++
			case rule.Write.Succeeded && strings.EqualFold(rule.Write.Action, "updated"):
				evidence.Summary.Updated++
			case rule.Write.Action == signoz.AlertRuleActionNotCreatedDisabled:
				evidence.Summary.NotCreatedDisabled++
			}
		}
	}
}

func anyRuleWriteAttempted(writes []signoz.AlertRuleWriteResult) bool {
	for _, write := range writes {
		if write.Attempted {
			return true
		}
	}
	return false
}

func allRuleWritesSucceeded(writes []signoz.AlertRuleWriteResult) bool {
	if len(writes) == 0 {
		return false
	}
	for _, write := range writes {
		if !write.Succeeded {
			return false
		}
	}
	return true
}

func prepareRuleWriteRecords(
	evidence *reporttypes.RuleReport,
	pending []validate.RuleCandidate,
	requested bool,
) {
	if !requested {
		return
	}
	for _, candidate := range pending {
		record := &evidence.Groups[candidate.GroupIndex].Rules[candidate.RuleIndex]
		record.Write = &reporttypes.RuleWriteRecord{Requested: true, Action: "pending"}
	}
}

func recordRuleTargetOutcome(evidence *reporttypes.RuleReport, result RuleResult) {
	if evidence.Run.Flags == nil {
		evidence.Run.Flags = make(map[string]any)
	}
	evidence.Run.Flags["writeRequested"] = result.WriteRequested
	evidence.Run.Flags["writeAttempted"] = result.WriteAttempted
	evidence.Run.Flags["writeSucceeded"] = result.WriteSucceeded
	evidence.Run.Flags["targetAction"] = result.TargetAction
	if result.TargetError == "" {
		delete(evidence.Run.Flags, "targetError")
	} else {
		evidence.Run.Flags["targetError"] = result.TargetError
	}
}
