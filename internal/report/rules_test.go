package report

import (
	"math"
	"testing"

	"github.com/mansiverma897993/signoz/internal/model"
	"github.com/mansiverma897993/signoz/internal/rules"
	"github.com/mansiverma897993/signoz/internal/target/signoz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildRulesAccountsForRecordingAndDisabledRules(t *testing.T) {
	t.Parallel()

	migration := rules.Migration{Groups: []rules.GroupMigration{{
		Source: model.RuleGroup{
			Name: "node", Interval: "30s", QueryOffset: "5m", Limit: 20,
			Labels: map[string]string{"cluster": "production"},
		},
		Rules: []rules.RuleMigration{
			{
				Source:   model.Rule{Alert: "NodeDown", Expression: "up == 0"},
				Decision: model.Decision{Verdict: model.VerdictPassthrough},
				Payload: &signoz.AlertRuleV2{
					Alert: "NodeDown", Disabled: false,
					Condition: signoz.AlertConditionV2{RequireMinPoints: true, RequiredPoints: 3},
				},
			},
			{
				Source:   model.Rule{Record: "job:up:sum", Expression: "sum(up)"},
				Decision: model.Decision{Verdict: model.VerdictNeedsReview, Reasons: []model.ReasonCode{model.ReasonRecordingRule}},
			},
		},
	}}}

	report, err := BuildRules(migration)
	require.NoError(t, err)
	report.Summary.NotCreatedDisabled = 2
	assert.Equal(t, 2, report.Summary.Rules)
	assert.Equal(t, 1, report.Summary.Alerting)
	assert.Equal(t, 1, report.Summary.Recording)
	assert.Equal(t, 1, report.Summary.Emitted)
	assert.Equal(t, 1, report.Summary.Enabled)
	require.Len(t, report.Groups, 1)
	assert.Equal(t, "30s", report.Groups[0].Interval)
	assert.Equal(t, "5m", report.Groups[0].QueryOffset)
	assert.Equal(t, 20, report.Groups[0].Limit)
	assert.Equal(t, map[string]string{"cluster": "production"}, report.Groups[0].Labels)
	assert.True(t, report.Groups[0].Rules[0].RequireMinPoints)
	assert.Equal(t, 3, report.Groups[0].Rules[0].RequiredNumPoints)
	assert.Contains(t, report.Groups[0].Rules[1].ReasonCodes, string(model.ReasonRecordingRule))

	html, err := RulesHTMLBytes(report)
	require.NoError(t, err)
	assert.Contains(t, string(html), "query offset 5m")
	assert.Contains(t, string(html), "limit 20")
	assert.Contains(t, string(html), "minimum points 3")
	assert.Contains(t, string(html), "production")
	assert.Contains(t, string(html), "Disabled candidates not created")
}

func TestBuildRulesUsesArraysForEmptyCollections(t *testing.T) {
	t.Parallel()

	evidence, err := BuildRules(rules.Migration{Groups: []rules.GroupMigration{{Source: model.RuleGroup{Name: "empty"}}}})
	require.NoError(t, err)
	require.NotNil(t, evidence.Groups)
	require.NotNil(t, evidence.Groups[0].Rules)
}

func TestBuildRulesReturnsPayloadEncodingError(t *testing.T) {
	t.Parallel()

	migration := rules.Migration{Groups: []rules.GroupMigration{{
		Source: model.RuleGroup{Name: "invalid"},
		Rules: []rules.RuleMigration{{
			Source: model.Rule{Alert: "InvalidThreshold"},
			Payload: &signoz.AlertRuleV2{
				Alert: "InvalidThreshold",
				Condition: signoz.AlertConditionV2{Thresholds: signoz.AlertThresholds{
					Spec: []signoz.AlertThreshold{{Target: math.NaN()}},
				}},
			},
		}},
	}}}

	_, err := BuildRules(migration)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `hash emitted alert rule "InvalidThreshold"`)
	assert.Contains(t, err.Error(), "unsupported value")
}
