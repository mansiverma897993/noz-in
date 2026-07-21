package rules

import (
	"fmt"
	"strings"

	projectmodel "github.com/mansiverma897993/signoz/internal/model"
	"github.com/mansiverma897993/signoz/internal/target/signoz"
	"github.com/mansiverma897993/signoz/internal/transpile"
)

const queryName = "A"

// Migration contains source rules and their target translations.
type Migration struct {
	Source projectmodel.RuleSet `json:"source"`
	Groups []GroupMigration     `json:"groups"`
}

// AlertNameInventory carries collection-wide duplicate counts so target alert
// names remain unique and stable when one migration run spans multiple files.
// Its maps are immutable after construction.
type AlertNameInventory struct {
	names      map[string]int
	severities map[string]int
}

// GroupMigration preserves source grouping for the evidence report.
type GroupMigration struct {
	Source projectmodel.RuleGroup `json:"source"`
	Rules  []RuleMigration        `json:"rules"`
}

// RuleMigration is one source rule, decision, and optional SigNoz payload.
type RuleMigration struct {
	Source             projectmodel.Rule     `json:"source"`
	Decision           projectmodel.Decision `json:"decision"`
	Payload            *signoz.AlertRuleV2   `json:"payload,omitempty"`
	Query              string                `json:"query,omitempty"`
	Operator           string                `json:"operator,omitempty"`
	Target             float64               `json:"target,omitempty"`
	ExtractedThreshold bool                  `json:"extractedThreshold"`
}

// Translate converts every source rule without dropping recording rules.
func Translate(source projectmodel.RuleSet, analyzer *transpile.Analyzer) Migration {
	return TranslateWithAlertNameInventory(
		source,
		analyzer,
		NewAlertNameInventory([]projectmodel.RuleSet{source}),
	)
}

// TranslateWithAlertNameInventory translates one source using counts from the
// entire input collection. Callers migrating several files must share one
// inventory across every call.
func TranslateWithAlertNameInventory(
	source projectmodel.RuleSet,
	analyzer *transpile.Analyzer,
	inventory AlertNameInventory,
) Migration {
	if analyzer == nil {
		analyzer = transpile.NewAnalyzer(transpile.Options{})
	}
	if inventory.names == nil || inventory.severities == nil {
		inventory = NewAlertNameInventory([]projectmodel.RuleSet{source})
	}
	result := Migration{Source: source}
	for _, group := range source.Groups {
		groupResult := GroupMigration{Source: group}
		for _, rule := range group.Rules {
			groupResult.Rules = append(groupResult.Rules, translateRule(
				source.Source, group, rule, analyzer, inventory.names, inventory.severities,
			))
		}
		result.Groups = append(result.Groups, groupResult)
	}
	return result
}

func translateRule(
	source projectmodel.Source,
	group projectmodel.RuleGroup,
	rule projectmodel.Rule,
	analyzer *transpile.Analyzer,
	nameCounts map[string]int,
	severityCounts map[string]int,
) RuleMigration {
	baseReasons, baseNotes, schedule := ruleTranslationContext(group, rule)
	effectiveLabels := group.EffectiveLabels(rule)
	if rule.IsRecording() && !rule.IsAlerting() {
		return RuleMigration{
			Source: rule,
			Decision: projectmodel.Decision{
				Verdict: projectmodel.VerdictNeedsReview,
				Reasons: uniqueReasons(append(baseReasons, projectmodel.ReasonRecordingRule)),
				Notes:   baseNotes,
			},
		}
	}
	if !rule.IsAlerting() || strings.TrimSpace(rule.Expression) == "" {
		return RuleMigration{
			Source: rule,
			Decision: projectmodel.Decision{
				Verdict: projectmodel.VerdictNeedsReview,
				Reasons: uniqueReasons(append(baseReasons, projectmodel.ReasonEmptyExpression)),
				Notes:   baseNotes,
			},
		}
	}

	query, operator, target, extracted, err := extractThreshold(rule.Expression)
	if err != nil {
		return RuleMigration{
			Source: rule,
			Decision: projectmodel.Decision{
				Verdict: projectmodel.VerdictNeedsReview,
				Reasons: uniqueReasons(append(baseReasons, projectmodel.ReasonParseError)),
				Notes:   append(baseNotes, err.Error()),
			},
		}
	}
	reasons := append([]projectmodel.ReasonCode(nil), baseReasons...)
	needsReview := len(baseReasons) > 0 || !schedule.Safe
	if !extracted {
		query = rule.Expression
		operator = "above"
		target = 0
		reasons = append(reasons, projectmodel.ReasonAlertThreshold)
		needsReview = true
	}

	translation := analyzer.Analyze(projectmodel.Query{RefID: queryName, Expression: query})
	if translation.Kind == projectmodel.TranslationNone || strings.TrimSpace(translation.PromQL) == "" {
		translationReasons := append([]projectmodel.ReasonCode(nil), translation.Decision.Reasons...)
		if len(translationReasons) == 0 {
			translationReasons = append(translationReasons, projectmodel.ReasonParseError)
		}
		notes := append([]string(nil), baseNotes...)
		notes = append(notes, translation.Decision.Notes...)
		notes = append(notes, parseErrorNotes(translation.ParseErrors)...)
		return RuleMigration{
			Source: rule,
			Decision: projectmodel.Decision{
				Verdict: projectmodel.VerdictNeedsReview,
				Reasons: uniqueReasons(append(reasons, translationReasons...)),
				Notes:   notes,
			},
			Query:              query,
			Operator:           operator,
			Target:             target,
			ExtractedThreshold: extracted,
		}
	}
	query = translation.PromQL
	reasons = append(reasons, promQLAlertReasons(translation.Decision.Reasons)...)
	if translation.Decision.Verdict == projectmodel.VerdictNeedsReview && !builderCandidateOnlyReview(translation) {
		needsReview = true
	}

	severity, severitySafe, severityChanged := normalizeSeverity(effectiveLabels["severity"])
	if severityChanged {
		reasons = append(reasons, projectmodel.ReasonSeverityNormalized)
	}
	if !severitySafe {
		needsReview = true
	}

	annotations, annotationGroupBy, formattingDropped, unsupportedTemplate := rewriteAlertTemplates(rule.Annotations)
	if formattingDropped || unsupportedTemplate {
		reasons = append(reasons, projectmodel.ReasonAnnotationFormatting)
	}
	if unsupportedTemplate {
		needsReview = true
	}
	rewrittenLabels, labelGroupBy, labelFormattingDropped, unsupportedLabelTemplate := rewriteAlertTemplates(effectiveLabels)
	if labelFormattingDropped || unsupportedLabelTemplate {
		reasons = append(reasons, projectmodel.ReasonAlertLabelFormatting)
	}
	if unsupportedLabelTemplate {
		needsReview = true
	}
	groupBy := mergeSortedStrings(annotationGroupBy, labelGroupBy)

	ruleMigrationID := migrationID(source, group, rule)
	alertName := rule.Alert
	severityKey := rule.Alert + "\x00" + severity
	if nameCounts[rule.Alert] > 1 {
		if severityCounts[severityKey] == 1 {
			alertName = fmt.Sprintf("%s [%s]", rule.Alert, severity)
		} else {
			alertName = fmt.Sprintf("%s [%s/%s/%s]", rule.Alert, group.Name, severity, ruleMigrationID)
		}
		reasons = append(reasons, projectmodel.ReasonAlertNameDisambiguated)
	}

	matchType := "all_the_times"
	if schedule.Immediate {
		matchType = "at_least_once"
	}
	if !isZeroDuration(rule.KeepFiringFor) {
		reasons = append(reasons, projectmodel.ReasonKeepFiringFor)
		needsReview = true
	}

	labels := remapConfiguredAlertLabels(rewrittenLabels)
	if labels == nil {
		labels = make(map[string]string)
	}
	if original := effectiveLabels["severity"]; original != "" && original != severity {
		labels["prometheus_severity"] = rewrittenLabels["severity"]
	}
	labels["severity"] = severity
	labels["prometheus_alertname"] = rule.Alert
	labels["prometheus_rule_group"] = group.Name
	labels["promcast_id"] = ruleMigrationID

	description := annotations["description"]
	if description == "" {
		description = annotations["summary"]
	}
	payload := signoz.AlertRuleV2{
		Alert:         alertName,
		AlertType:     "METRIC_BASED_ALERT",
		Description:   description,
		RuleType:      "promql_rule",
		Version:       "v5",
		SchemaVersion: "v2alpha1",
		Condition: signoz.AlertConditionV2{
			CompositeQuery: signoz.AlertCompositeQuery{
				QueryType: "promql",
				PanelType: "graph",
				Queries: []signoz.AlertQueryEnvelope{{
					Type: "promql",
					Spec: signoz.AlertQuerySpec{Name: queryName, Query: query},
				}},
			},
			SelectedQuery:    queryName,
			RequireMinPoints: schedule.RequireMinPoints,
			RequiredPoints:   schedule.RequiredPoints,
			Thresholds: signoz.AlertThresholds{Kind: "basic", Spec: []signoz.AlertThreshold{{
				Name: severity, Operator: operator, MatchType: matchType, Target: target,
			}}},
		},
		Evaluation: signoz.AlertEvaluation{Kind: "rolling", Spec: signoz.AlertEvaluationSpec{
			EvalWindow: schedule.EvalWindow,
			Frequency:  schedule.Frequency,
		}},
		NotificationSettings: signoz.AlertNotificationSettings{
			GroupBy:   groupBy,
			Renotify:  signoz.AlertRenotify{Enabled: false, Interval: "30m"},
			UsePolicy: true,
		},
		Labels:      labels,
		Annotations: annotations,
		Disabled:    needsReview,
	}
	verdict := projectmodel.VerdictPassthrough
	if needsReview {
		verdict = projectmodel.VerdictNeedsReview
	}
	return RuleMigration{
		Source: rule,
		Decision: projectmodel.Decision{
			Verdict: verdict, Reasons: uniqueReasons(reasons),
			Notes: append(baseNotes, translation.Decision.Notes...),
		},
		Payload:            &payload,
		Query:              query,
		Operator:           operator,
		Target:             target,
		ExtractedThreshold: extracted,
	}
}

func ruleTranslationContext(
	group projectmodel.RuleGroup,
	rule projectmodel.Rule,
) ([]projectmodel.ReasonCode, []string, evaluationPlan) {
	reasons := ruleGroupReasons(group)
	if !rule.IsAlerting() {
		return reasons, nil, evaluationPlan{}
	}
	schedule := evaluationSchedule(rule.For, group.Interval)
	reasons = uniqueReasons(append(reasons, schedule.Reasons...))
	reasons = append(reasons, projectmodel.ReasonTargetAlertRuntimeLabels)
	labels := explicitTargetRuntimeLabels(rule.Expression)
	if len(labels) == 0 {
		return reasons, nil, schedule
	}
	note := fmt.Sprintf(
		"source PromQL explicitly references target-owned runtime label(s) %s; pinned SigNoz v0.133 owns or mutates those labels during rule evaluation, routing, or fingerprinting",
		strings.Join(labels, ", "),
	)
	return reasons, []string{note}, schedule
}

// promQLAlertReasons removes only risks that belong to a Builder execution
// engine. Alert rules always execute the canonical PromQL carried by the
// translation, so reporting a Builder-only mismatch against that payload would
// be misleading. Every other reason is retained.
func promQLAlertReasons(reasons []projectmodel.ReasonCode) []projectmodel.ReasonCode {
	result := make([]projectmodel.ReasonCode, 0, len(reasons))
	for _, reason := range reasons {
		if !projectmodel.IsBuilderCandidateSemanticReason(reason) {
			result = append(result, reason)
		}
	}
	return result
}

// builderCandidateOnlyReview is deliberately closed to new reason codes. It
// recognizes the exact case where analysis selected a structurally valid
// Builder candidate and changed the verdict solely because that engine is not
// PromQL-equivalent. An unknown or independently unsafe reason still disables
// the alert.
func builderCandidateOnlyReview(translation projectmodel.Translation) bool {
	if translation.Kind != projectmodel.TranslationBuilder && translation.Kind != projectmodel.TranslationFormula {
		return false
	}
	foundCandidateRisk := false
	for _, reason := range translation.Decision.Reasons {
		if projectmodel.IsBuilderCandidateSemanticReason(reason) {
			foundCandidateRisk = true
			continue
		}
		switch reason {
		case projectmodel.ReasonMetricNameRemap,
			projectmodel.ReasonResourceLabelRemap,
			projectmodel.ReasonRecordingRuleInlined,
			projectmodel.ReasonRefIDNormalized:
			// These are provenance qualifications, not review gates.
		default:
			return false
		}
	}
	return foundCandidateRisk
}

// NewAlertNameInventory counts alert names and normalized severities across a
// complete source collection before any artifact or target mutation.
func NewAlertNameInventory(sources []projectmodel.RuleSet) AlertNameInventory {
	names := make(map[string]int)
	severities := make(map[string]int)
	for _, source := range sources {
		for _, group := range source.Groups {
			for _, rule := range group.Rules {
				if !rule.IsAlerting() {
					continue
				}
				effectiveLabels := group.EffectiveLabels(rule)
				severity, _, _ := normalizeSeverity(effectiveLabels["severity"])
				names[rule.Alert]++
				severities[rule.Alert+"\x00"+severity]++
			}
		}
	}
	return AlertNameInventory{names: names, severities: severities}
}

func uniqueReasons(reasons []projectmodel.ReasonCode) []projectmodel.ReasonCode {
	seen := make(map[projectmodel.ReasonCode]struct{}, len(reasons))
	result := make([]projectmodel.ReasonCode, 0, len(reasons))
	for _, reason := range reasons {
		if _, ok := seen[reason]; ok {
			continue
		}
		seen[reason] = struct{}{}
		result = append(result, reason)
	}
	return result
}

func parseErrorNotes(parseErrors []projectmodel.ParseError) []string {
	notes := make([]string, 0, len(parseErrors))
	for _, parseError := range parseErrors {
		notes = append(notes, parseError.Message)
	}
	return notes
}
