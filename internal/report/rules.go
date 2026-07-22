package report

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/mansiverma897993/noz-in/internal/rules"
	"github.com/mansiverma897993/noz-in/internal/target/signoz"
	"github.com/mansiverma897993/noz-in/internal/version"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
)

// BuildRules creates an evidence report that includes non-emittable recording rules.
func BuildRules(migration rules.Migration) (reporttypes.RuleReport, error) {
	result := reporttypes.RuleReport{
		SchemaVersion: "1",
		Tool:          reporttypes.Tool{Name: "promcast", Version: version.Version(), Commit: version.Commit()},
		Groups:        make([]reporttypes.RuleGroupRecord, 0, len(migration.Groups)),
		Source: reporttypes.Source{
			Kind:      migration.Source.Source.Kind,
			Path:      migration.Source.Source.Path,
			Namespace: migration.Source.Source.Namespace,
			Identity:  migration.Source.Source.Identity,
			SHA256:    migration.Source.Source.SHA256,
		},
		ReasonCodes: reasonCodeIndex(),
	}
	for _, group := range migration.Groups {
		groupRecord := reporttypes.RuleGroupRecord{
			Name: group.Source.Name, Interval: group.Source.Interval, QueryOffset: group.Source.QueryOffset,
			Limit: group.Source.Limit, Labels: group.Source.Labels, SourcePath: group.Source.SourcePath,
			Rules: make([]reporttypes.RuleRecord, 0, len(group.Rules)),
		}
		result.Summary.Groups++
		for _, rule := range group.Rules {
			record := reporttypes.RuleRecord{
				SourcePath:         rule.Source.SourcePath,
				Alert:              rule.Source.Alert,
				Record:             rule.Source.Record,
				Original:           rule.Source.Expression,
				For:                rule.Source.For,
				KeepFiringFor:      rule.Source.KeepFiringFor,
				Labels:             rule.Source.Labels,
				Annotations:        rule.Source.Annotations,
				Verdict:            string(rule.Decision.Verdict),
				ReasonCodes:        stringReasons(rule.Decision.Reasons),
				Notes:              append([]string(nil), rule.Decision.Notes...),
				PromQL:             rule.Query,
				ExtractedThreshold: rule.ExtractedThreshold,
				Operator:           rule.Operator,
				Target:             rule.Target,
			}
			result.Summary.Rules++
			if rule.Source.IsAlerting() {
				result.Summary.Alerting++
			}
			if rule.Source.IsRecording() {
				result.Summary.Recording++
			}
			switch rule.Decision.Verdict {
			case model.VerdictPassthrough:
				result.Summary.Passthrough++
			case model.VerdictNeedsReview:
				result.Summary.NeedsReview++
			}
			if rule.Payload != nil {
				payloadSHA256, err := rulePayloadSHA256(*rule.Payload)
				if err != nil {
					return result, fmt.Errorf("hash emitted alert rule %q: %w", rule.Payload.Alert, err)
				}
				result.Summary.Emitted++
				record.TargetAlert = rule.Payload.Alert
				record.TargetMigrationID = rule.Payload.Labels["promcast_id"]
				record.EmittedSpecSHA256 = payloadSHA256
				record.EvalWindow = rule.Payload.Evaluation.Spec.EvalWindow
				record.Frequency = rule.Payload.Evaluation.Spec.Frequency
				record.RequireMinPoints = rule.Payload.Condition.RequireMinPoints
				record.RequiredNumPoints = rule.Payload.Condition.RequiredPoints
				record.Disabled = rule.Payload.Disabled
				if rule.Payload.Disabled {
					result.Summary.Disabled++
				} else {
					result.Summary.Enabled++
				}
			}
			groupRecord.Rules = append(groupRecord.Rules, record)
		}
		result.Groups = append(result.Groups, groupRecord)
	}
	return result, nil
}

func rulePayloadSHA256(payload signoz.AlertRuleV2) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal emitted alert rule: %w", err)
	}
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest[:]), nil
}
