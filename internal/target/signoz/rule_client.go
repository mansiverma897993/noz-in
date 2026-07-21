package signoz

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const migrationIDLabel = "promcast_id"

// StoredAlertRule is the compact rule shape returned by the list/create API.
type StoredAlertRule struct {
	ID       string            `json:"id"`
	Alert    string            `json:"alert"`
	Labels   map[string]string `json:"labels"`
	Disabled bool              `json:"disabled"`
}

// AlertRuleWriteResult describes the outcome of one requested rule write.
type AlertRuleWriteResult struct {
	ID        string `json:"id,omitempty"`
	Alert     string `json:"alert"`
	Action    string `json:"action"`
	Requested bool   `json:"requested"`
	Attempted bool   `json:"attempted"`
	Succeeded bool   `json:"succeeded"`
	Error     string `json:"error,omitempty"`
}

// AlertRuleWritePlan is the mutation decision made from one locked target
// inventory. A checkpoint callback sees the complete plan before the first
// POST or PUT is sent.
type AlertRuleWritePlan struct {
	ID     string `json:"id,omitempty"`
	Alert  string `json:"alert"`
	Action string `json:"action"`
}

// AlertRuleMutationCheckpoint describes the next mutation and every completed
// outcome that precedes it. The callback runs immediately before the first
// HTTP request for that mutation. Returning an error prevents the request.
type AlertRuleMutationCheckpoint struct {
	Index     int                    `json:"index"`
	ID        string                 `json:"id,omitempty"`
	Alert     string                 `json:"alert"`
	Action    string                 `json:"action"`
	Completed []AlertRuleWriteResult `json:"completed"`
}

// AlertRuleWriteCheckpoints lets callers persist both the complete plan and
// the outcome-unknown state that must precede each POST or PUT.
type AlertRuleWriteCheckpoints struct {
	Planned        func([]AlertRuleWritePlan) error
	BeforeMutation func(AlertRuleMutationCheckpoint) error
}

const (
	AlertRulePlanCreate               = "create"
	AlertRulePlanUpdate               = "update"
	AlertRuleActionNotCreatedDisabled = "not_created_disabled"
	AlertRuleActionFailed             = "failed"
	AlertRuleActionNotAttempted       = "not_attempted"
)

type plannedAlertRuleWrite struct {
	rule   AlertRuleV2
	id     string
	action string
}

// ListAlertRules returns rules visible to the service account.
func (client *Client) ListAlertRules(ctx context.Context) ([]StoredAlertRule, error) {
	var response struct {
		Data *[]StoredAlertRule `json:"data"`
	}
	if err := client.do(ctx, http.MethodGet, "/api/v2/rules", nil, nil, &response); err != nil {
		return nil, err
	}
	if response.Data == nil {
		return nil, fmt.Errorf("decode SigNoz alert rule inventory response: missing data array")
	}
	return *response.Data, nil
}

// UpsertAlertRule creates or updates a rule using its promcast_id label.
func (client *Client) UpsertAlertRule(ctx context.Context, rule AlertRuleV2) (AlertRuleWriteResult, error) {
	results, err := client.UpsertAlertRules(ctx, []AlertRuleV2{rule})
	if len(results) == 0 {
		return AlertRuleWriteResult{}, err
	}
	return results[0], err
}

// UpsertAlertRules writes a batch using one target inventory lookup.
func (client *Client) UpsertAlertRules(ctx context.Context, pending []AlertRuleV2) ([]AlertRuleWriteResult, error) {
	return client.UpsertAlertRulesWithCheckpoints(ctx, pending, AlertRuleWriteCheckpoints{})
}

// UpsertAlertRulesWithCheckpoint writes a batch using one target inventory
// lookup. When checkpoint is non-nil, it is called with the complete mutation
// plan while the in-process upsert locks are still held and before any POST or
// PUT. Returning an error from checkpoint aborts the batch without mutation.
//
// SigNoz v0.133.0 starts an executor on create even when disabled is true. A
// disabled candidate that is absent from inventory is therefore never POSTed;
// it is returned as not_created_disabled. An already-owned rule can safely be
// disabled through the update path, which honors disabled atomically.
func (client *Client) UpsertAlertRulesWithCheckpoint(
	ctx context.Context,
	pending []AlertRuleV2,
	checkpoint func([]AlertRuleWritePlan) error,
) ([]AlertRuleWriteResult, error) {
	return client.UpsertAlertRulesWithCheckpoints(ctx, pending, AlertRuleWriteCheckpoints{Planned: checkpoint})
}

// UpsertAlertRulesWithCheckpoints writes a batch while exposing durability
// barriers for the complete plan and every mutating request.
func (client *Client) UpsertAlertRulesWithCheckpoints(
	ctx context.Context,
	pending []AlertRuleV2,
	checkpoints AlertRuleWriteCheckpoints,
) ([]AlertRuleWriteResult, error) {
	if len(pending) == 0 {
		return []AlertRuleWriteResult{}, nil
	}
	lockKeys, err := client.alertRuleLockKeys(pending)
	if err != nil {
		return nil, err
	}
	release, err := acquireUpsertLocks(ctx, lockKeys...)
	if err != nil {
		return nil, err
	}
	defer release()

	return client.upsertAlertRulesLocked(ctx, pending, checkpoints)
}

func (client *Client) alertRuleLockKeys(pending []AlertRuleV2) ([]string, error) {
	keys := make([]string, 0, len(pending)*2)
	byMigrationID := make(map[string]string, len(pending))
	byName := make(map[string]string, len(pending))
	for _, rule := range pending {
		migrationID := strings.TrimSpace(rule.Labels[migrationIDLabel])
		if migrationID == "" {
			return nil, fmt.Errorf("alert rule %q has no %s label", rule.Alert, migrationIDLabel)
		}
		if strings.TrimSpace(rule.Alert) == "" {
			return nil, fmt.Errorf("alert rule with migration id %q has no name", migrationID)
		}
		if previous, exists := byMigrationID[migrationID]; exists {
			return nil, fmt.Errorf("pending alert rules %q and %q share %s %q", previous, rule.Alert, migrationIDLabel, migrationID)
		}
		if previousID, exists := byName[rule.Alert]; exists {
			return nil, fmt.Errorf("pending alert rule name %q belongs to migration ids %q and %q", rule.Alert, previousID, migrationID)
		}
		byMigrationID[migrationID] = rule.Alert
		byName[rule.Alert] = migrationID
		keys = append(keys,
			client.upsertLockKey("alert-migration-id", migrationID),
			client.upsertLockKey("alert-name", rule.Alert),
		)
	}
	return keys, nil
}

func (client *Client) upsertAlertRulesLocked(
	ctx context.Context,
	pending []AlertRuleV2,
	checkpoints AlertRuleWriteCheckpoints,
) ([]AlertRuleWriteResult, error) {
	rules, err := client.ListAlertRules(ctx)
	if err != nil {
		return nil, err
	}
	byMigrationID, byName, err := indexAlertRules(rules)
	if err != nil {
		return nil, err
	}

	plan := make([]plannedAlertRuleWrite, 0, len(pending))
	for _, rule := range pending {
		migrationID := strings.TrimSpace(rule.Labels[migrationIDLabel])
		if existing, ok := byMigrationID[migrationID]; ok {
			if collision, exists := byName[rule.Alert]; exists && collision.ID != existing.ID {
				return nil, alertRuleNameCollision(rule.Alert)
			}
			delete(byName, existing.Alert)
			existing.Alert = rule.Alert
			existing.Labels = rule.Labels
			byMigrationID[migrationID] = existing
			byName[rule.Alert] = existing
			plan = append(plan, plannedAlertRuleWrite{rule: rule, id: existing.ID, action: AlertRulePlanUpdate})
			continue
		}
		if _, collision := byName[rule.Alert]; collision {
			return nil, alertRuleNameCollision(rule.Alert)
		}
		if rule.Disabled {
			plan = append(plan, plannedAlertRuleWrite{rule: rule, action: AlertRuleActionNotCreatedDisabled})
			continue
		}
		plan = append(plan, plannedAlertRuleWrite{rule: rule, action: AlertRulePlanCreate})
		planned := StoredAlertRule{
			ID: "planned:" + migrationID, Alert: rule.Alert, Labels: rule.Labels,
		}
		byMigrationID[migrationID] = planned
		byName[rule.Alert] = planned
	}

	if checkpoints.Planned != nil {
		publicPlan := make([]AlertRuleWritePlan, 0, len(plan))
		for _, item := range plan {
			publicPlan = append(publicPlan, AlertRuleWritePlan{ID: item.id, Alert: item.rule.Alert, Action: item.action})
		}
		if err := checkpoints.Planned(publicPlan); err != nil {
			return nil, err
		}
	}

	results := make([]AlertRuleWriteResult, 0, len(plan))
	for index, item := range plan {
		result, err := client.executeAlertRuleWrite(ctx, item, index, results, checkpoints.BeforeMutation)
		results = append(results, result)
		if err == nil {
			continue
		}
		for _, skipped := range plan[index+1:] {
			results = append(results, AlertRuleWriteResult{
				ID: skipped.id, Alert: skipped.rule.Alert, Action: AlertRuleActionNotAttempted,
				Requested: true, Error: "rule batch stopped after an earlier write failure",
			})
		}
		return results, err
	}
	return results, nil
}

func (client *Client) executeAlertRuleWrite(
	ctx context.Context,
	item plannedAlertRuleWrite,
	index int,
	completed []AlertRuleWriteResult,
	checkpoint func(AlertRuleMutationCheckpoint) error,
) (AlertRuleWriteResult, error) {
	switch item.action {
	case AlertRuleActionNotCreatedDisabled:
		return AlertRuleWriteResult{
			Alert: item.rule.Alert, Action: AlertRuleActionNotCreatedDisabled, Requested: true,
		}, nil
	case AlertRulePlanUpdate:
		if err := checkpointAlertRuleMutation(checkpoint, index, item.id, item.rule.Alert, AlertRulePlanUpdate, completed); err != nil {
			return notAttemptedAlertRuleWrite(item, err), err
		}
		path := "/api/v2/rules/" + url.PathEscape(item.id)
		if err := client.do(ctx, http.MethodPut, path, nil, item.rule, nil); err != nil {
			writeErr := fmt.Errorf("update alert rule %q: %w", item.rule.Alert, err)
			return failedAlertRuleWrite(item, true, writeErr), writeErr
		}
		return successfulAlertRuleWrite(item.id, item.rule.Alert, "updated"), nil
	case AlertRulePlanCreate:
		if err := checkpointAlertRuleMutation(checkpoint, index, "", item.rule.Alert, AlertRulePlanCreate, completed); err != nil {
			return notAttemptedAlertRuleWrite(item, err), err
		}
		created, err := client.createAlertRule(ctx, item.rule)
		if err == nil {
			return successfulAlertRuleWrite(created.ID, item.rule.Alert, "created"), nil
		}
		reconciled, reconcileErr := client.reconcileAlertRuleCreate(
			ctx, item.rule, err, index, completed, checkpoint,
		)
		if reconcileErr != nil {
			return failedAlertRuleWrite(item, true, reconcileErr), reconcileErr
		}
		return reconciled, nil
	default:
		writeErr := fmt.Errorf("unsupported alert rule write plan action %q", item.action)
		return failedAlertRuleWrite(item, false, writeErr), writeErr
	}
}

func checkpointAlertRuleMutation(
	checkpoint func(AlertRuleMutationCheckpoint) error,
	index int,
	id string,
	alert string,
	action string,
	completed []AlertRuleWriteResult,
) error {
	if checkpoint == nil {
		return nil
	}
	snapshot := make([]AlertRuleWriteResult, len(completed))
	copy(snapshot, completed)
	if err := checkpoint(AlertRuleMutationCheckpoint{
		Index: index, ID: id, Alert: alert, Action: action, Completed: snapshot,
	}); err != nil {
		return fmt.Errorf("checkpoint alert rule %q before %s: %w", alert, action, err)
	}
	return nil
}

func successfulAlertRuleWrite(id, alert, action string) AlertRuleWriteResult {
	return AlertRuleWriteResult{
		ID: id, Alert: alert, Action: action, Requested: true, Attempted: true, Succeeded: true,
	}
}

func failedAlertRuleWrite(item plannedAlertRuleWrite, attempted bool, err error) AlertRuleWriteResult {
	return AlertRuleWriteResult{
		ID: item.id, Alert: item.rule.Alert, Action: AlertRuleActionFailed,
		Requested: true, Attempted: attempted, Error: err.Error(),
	}
}

func notAttemptedAlertRuleWrite(item plannedAlertRuleWrite, err error) AlertRuleWriteResult {
	return AlertRuleWriteResult{
		ID: item.id, Alert: item.rule.Alert, Action: AlertRuleActionNotAttempted,
		Requested: true, Error: err.Error(),
	}
}

func indexAlertRules(rules []StoredAlertRule) (map[string]StoredAlertRule, map[string]StoredAlertRule, error) {
	byMigrationID := make(map[string]StoredAlertRule, len(rules))
	byName := make(map[string]StoredAlertRule, len(rules))
	for _, rule := range rules {
		if strings.TrimSpace(rule.ID) == "" {
			return nil, nil, fmt.Errorf("SigNoz alert rule inventory contains a rule with no id")
		}
		migrationID := strings.TrimSpace(rule.Labels[migrationIDLabel])
		if migrationID != "" {
			if previous, exists := byMigrationID[migrationID]; exists && previous.ID != rule.ID {
				return nil, nil, fmt.Errorf("multiple SigNoz alert rules use %s %q", migrationIDLabel, migrationID)
			}
			byMigrationID[migrationID] = rule
		}
		if rule.Alert != "" {
			if previous, exists := byName[rule.Alert]; exists && previous.ID != rule.ID {
				return nil, nil, fmt.Errorf("multiple SigNoz alert rules use name %q", rule.Alert)
			}
			byName[rule.Alert] = rule
		}
	}
	return byMigrationID, byName, nil
}

func (client *Client) createAlertRule(ctx context.Context, rule AlertRuleV2) (StoredAlertRule, error) {
	var response struct {
		Data StoredAlertRule `json:"data"`
	}
	if err := client.do(ctx, http.MethodPost, "/api/v2/rules", nil, rule, &response); err != nil {
		return StoredAlertRule{}, fmt.Errorf("create alert rule %q: %w", rule.Alert, err)
	}
	if response.Data.ID == "" {
		return StoredAlertRule{}, fmt.Errorf("SigNoz create alert rule %q response did not include an id", rule.Alert)
	}
	response.Data.Alert = rule.Alert
	response.Data.Labels = rule.Labels
	return response.Data, nil
}

func (client *Client) reconcileAlertRuleCreate(
	ctx context.Context,
	rule AlertRuleV2,
	createErr error,
	index int,
	completed []AlertRuleWriteResult,
	checkpoint func(AlertRuleMutationCheckpoint) error,
) (AlertRuleWriteResult, error) {
	rules, err := client.ListAlertRules(ctx)
	if err != nil {
		return AlertRuleWriteResult{}, fmt.Errorf("%w; reconcile target inventory: %v", createErr, err)
	}
	byMigrationID, byName, err := indexAlertRules(rules)
	if err != nil {
		return AlertRuleWriteResult{}, errors.Join(createErr, err)
	}
	migrationID := strings.TrimSpace(rule.Labels[migrationIDLabel])
	if existing, found := byMigrationID[migrationID]; found {
		if collision, exists := byName[rule.Alert]; exists && collision.ID != existing.ID {
			return AlertRuleWriteResult{}, errors.Join(createErr, alertRuleNameCollision(rule.Alert))
		}
		if err := checkpointAlertRuleMutation(
			checkpoint, index, existing.ID, rule.Alert, AlertRulePlanUpdate, completed,
		); err != nil {
			return AlertRuleWriteResult{
				ID: existing.ID, Alert: rule.Alert, Action: AlertRuleActionFailed,
				Requested: true, Attempted: true, Error: errors.Join(createErr, err).Error(),
			}, errors.Join(createErr, err)
		}
		path := "/api/v2/rules/" + url.PathEscape(existing.ID)
		if err := client.do(ctx, http.MethodPut, path, nil, rule, nil); err != nil {
			return AlertRuleWriteResult{}, errors.Join(createErr, fmt.Errorf("reconcile alert rule %q: %w", rule.Alert, err))
		}
		return successfulAlertRuleWrite(existing.ID, rule.Alert, "updated"), nil
	}
	if _, collision := byName[rule.Alert]; collision {
		return AlertRuleWriteResult{}, errors.Join(createErr, alertRuleNameCollision(rule.Alert))
	}
	return AlertRuleWriteResult{}, createErr
}

func alertRuleNameCollision(name string) error {
	return fmt.Errorf(
		"alert rule name %q already belongs to a different rule; rename it or remove the collision before importing",
		name,
	)
}
