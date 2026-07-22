package rules

import (
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	projectmodel "github.com/mansiverma897993/noz-in/internal/model"
	"github.com/mansiverma897993/noz-in/internal/stableidentity"
	"github.com/prometheus/prometheus/promql/parser"
)

// StableSourceIDLabel disambiguates rules that intentionally share a group
// and alert name inside one namespaced source collection. Unlike source
// positions and expressions, its value must remain stable across edits and
// reordering.
const StableSourceIDLabel = "promcast_source_id"

var generatedAlertLabels = []string{
	"prometheus_alertname",
	"prometheus_rule_group",
	"promcast_id",
}

var targetRuntimeAlertLabels = []string{
	"threshold.name",
	"ruleId",
	"ruleSource",
	"nodata",
	"alertname",
}

var configuredAlertLabelRemaps = []struct {
	source string
	target string
}{
	{source: "job", target: "service.name"},
	{source: "instance", target: "service.instance.id"},
}

// ValidateStableIdentities validates canonical explicit source IDs, rejects
// namespace-backed alerts that would resolve to the same SigNoz upsert key,
// and enforces ownership of generated target labels. The check is deliberately
// separate from Translate so callers can fail before metadata requests,
// artifact publication, or target mutation.
func ValidateStableIdentities(sources []projectmodel.RuleSet) error {
	seen := make(map[string]string)
	for _, source := range sources {
		if err := stableidentity.ValidateComponent("rule source namespace", source.Source.Namespace, 512); err != nil {
			return err
		}
		if err := stableidentity.ValidateComponent("rule source identity", source.Source.Identity, 4096); err != nil {
			return err
		}
		if err := stableidentity.ValidateComponent("rule source path", source.Source.Path, 4096); err != nil {
			return err
		}
		namespace := strings.TrimSpace(source.Source.Namespace)
		for _, group := range source.Groups {
			if err := stableidentity.ValidateComponent("rule group name", group.Name, 4096); err != nil {
				return err
			}
			if err := stableidentity.ValidateComponent("rule group source path", group.SourcePath, 4096); err != nil {
				return err
			}
			if err := validateStableSourceIDLabel(group.Labels, "rule group "+group.SourcePath); err != nil {
				return err
			}
			for _, rule := range group.Rules {
				if err := stableidentity.ValidateComponent("alert name", rule.Alert, 4096); err != nil {
					return err
				}
				if err := stableidentity.ValidateComponent("rule source path", rule.SourcePath, 4096); err != nil {
					return err
				}
				if err := validateStableSourceIDLabel(rule.Labels, "rule "+rule.SourcePath); err != nil {
					return err
				}
				if err := validateGeneratedAlertLabelOwnership(group, rule); err != nil {
					return err
				}
				effectiveLabels := group.EffectiveLabels(rule)
				rawSourceID := effectiveLabels[StableSourceIDLabel]
				sourceID := rawSourceID
				if !rule.IsAlerting() {
					continue
				}
				if namespace == "" {
					continue
				}
				keyParts := []string{namespace}
				if sourceID != "" {
					keyParts = append(keyParts, "source-id", sourceID)
				} else {
					keyParts = append(keyParts, "named-rule", group.Name, rule.Alert)
				}
				keyDigest := stableidentity.Sum256(keyParts...)
				key := string(keyDigest[:])
				location := stableRuleLocation(source.Source, group, rule)
				if previous, exists := seen[key]; exists {
					return fmt.Errorf(
						"namespaced Prometheus rules %s and %s have the same stable identity in source namespace %q; add distinct %s labels or use separate source namespaces",
						previous, location, namespace, StableSourceIDLabel,
					)
				}
				seen[key] = location
			}
		}
	}
	return nil
}

func validateStableSourceIDLabel(labels map[string]string, location string) error {
	raw, exists := labels[StableSourceIDLabel]
	if !exists {
		return nil
	}
	if err := stableidentity.ValidateComponent(StableSourceIDLabel, raw, 1024); err != nil {
		return fmt.Errorf("%s: %w", location, err)
	}
	canonical := strings.TrimSpace(raw)
	if canonical == "" {
		return fmt.Errorf("%s: %s must be nonempty when present", location, StableSourceIDLabel)
	}
	if canonical != raw {
		return fmt.Errorf("%s: %s must not contain surrounding whitespace", location, StableSourceIDLabel)
	}
	if strings.Contains(raw, "{{") {
		return fmt.Errorf("%s: %s must be a literal value, not an alert template", location, StableSourceIDLabel)
	}
	return nil
}

func validateGeneratedAlertLabelOwnership(group projectmodel.RuleGroup, rule projectmodel.Rule) error {
	if !rule.IsAlerting() {
		return nil
	}
	labels := group.EffectiveLabels(rule)
	location := fmt.Sprintf("Prometheus alert %q in group %q%s", rule.Alert, group.Name, rule.SourcePath)
	for _, label := range generatedAlertLabels {
		if _, exists := labels[label]; exists {
			return fmt.Errorf(
				"%s defines reserved target label %q; promcast owns this generated label",
				location, label,
			)
		}
	}
	for _, label := range targetRuntimeAlertLabels {
		if _, exists := labels[label]; exists {
			return fmt.Errorf(
				"%s defines reserved target label %q; pinned SigNoz v0.133 owns this runtime label",
				location, label,
			)
		}
	}
	for _, remap := range configuredAlertLabelRemaps {
		_, hasSource := labels[remap.source]
		_, hasTarget := labels[remap.target]
		if hasSource && hasTarget {
			return fmt.Errorf(
				"%s defines both %q and %q; configured label remapping %q to %q would collide",
				location, remap.source, remap.target, remap.source, remap.target,
			)
		}
		if hasTarget && alertExpressionMayRetainLabel(rule.Expression, remap.source) {
			return fmt.Errorf(
				"%s configures target label %q while its PromQL result may retain source label %q; remapping %q to %q would overwrite a dynamic source label",
				location, remap.target, remap.source, remap.source, remap.target,
			)
		}
		if hasSource && alertExpressionMayRetainLabel(rule.Expression, remap.target) {
			return fmt.Errorf(
				"%s configures source label %q, which maps to %q, while its PromQL result may retain target label %q; configured label remapping would overwrite a dynamic target label",
				location, remap.source, remap.target, remap.target,
			)
		}
	}
	for name := range labels {
		targetName := targetLabel(name)
		if !validSigNozRuleLabelName(targetName) {
			return fmt.Errorf(
				"%s label name %q maps to %q, which pinned SigNoz v0.133 rejects; target rule names allow only ASCII letters, digits after the first byte, underscore, and dot",
				location, name, targetName,
			)
		}
	}
	for name := range rule.Annotations {
		if !validSigNozRuleLabelName(name) {
			return fmt.Errorf(
				"%s annotation name %q is rejected by pinned SigNoz v0.133; target rule names allow only ASCII letters, digits after the first byte, underscore, and dot",
				location, name,
			)
		}
	}
	originalSeverity := labels["severity"]
	normalizedSeverity, _, _ := normalizeSeverity(originalSeverity)
	if originalSeverity != "" && originalSeverity != normalizedSeverity {
		if _, exists := labels["prometheus_severity"]; exists {
			return fmt.Errorf(
				"%s defines reserved target label %q while severity %q requires normalization to %q; promcast owns the generated preservation label",
				location, "prometheus_severity", originalSeverity, normalizedSeverity,
			)
		}
	}
	return nil
}

// alertExpressionMayRetainLabel is deliberately conservative. A configured
// target-namespace label must not overwrite the remapped value of a dynamic
// Prometheus job or instance label. Only scalar results and a root aggregation
// that positively drops the label are exempt; unknown functions, subqueries,
// and vector-to-vector binary expressions remain possible collisions.
func alertExpressionMayRetainLabel(expression, label string) bool {
	query := expression
	if extractedQuery, _, _, extracted, err := extractThreshold(expression); err == nil && extracted {
		query = extractedQuery
	}
	expr, err := parser.NewParser(parser.Options{}).ParseExpr(query)
	if err != nil {
		return true
	}
	expr = unwrap(expr)
	if expr.Type() == parser.ValueTypeScalar || expr.Type() == parser.ValueTypeString {
		return false
	}
	aggregation, ok := expr.(*parser.AggregateExpr)
	if !ok {
		return true
	}
	switch aggregation.Op {
	case parser.SUM, parser.AVG, parser.COUNT, parser.MIN, parser.MAX,
		parser.GROUP, parser.STDDEV, parser.STDVAR, parser.QUANTILE:
		// These operators emit only their grouping labels.
	default:
		// Selection aggregators such as topk/bottomk/limitk retain input
		// series labels. count_values also creates a label named by Param.
		return true
	}
	grouped := slices.Contains(aggregation.Grouping, label)
	if aggregation.Without {
		return !grouped
	}
	return grouped
}

func stableRuleLocation(source projectmodel.Source, group projectmodel.RuleGroup, rule projectmodel.Rule) string {
	identity := strings.TrimSpace(source.Identity)
	if identity == "" {
		identity = strings.TrimSpace(source.Path)
	}
	if identity == "" {
		identity = "<in-memory>"
	}
	return fmt.Sprintf("%q (%s%s, group %q)", rule.Alert, identity, rule.SourcePath, group.Name)
}

func migrationID(source projectmodel.Source, group projectmodel.RuleGroup, rule projectmodel.Rule) string {
	namespace := strings.TrimSpace(source.Namespace)
	parts := []string{"prometheus"}
	if namespace != "" {
		parts = append(parts, namespace)
		if sourceID := group.EffectiveLabels(rule)[StableSourceIDLabel]; sourceID != "" {
			parts = append(parts, "source-id", sourceID)
		} else {
			parts = append(parts, "named-rule", group.Name, rule.Alert)
		}
	} else {
		identity := strings.TrimSpace(source.Identity)
		if identity == "" {
			identity = strings.TrimSpace(source.Path)
		}
		parts = append(parts, identity, group.Name, group.SourcePath, rule.Alert, rule.SourcePath)
	}
	digest := stableidentity.Sum256(parts...)
	return hex.EncodeToString(digest[:12])
}
