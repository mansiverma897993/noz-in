package rules

import (
	"bytes"
	"regexp"
	"testing"
	texttemplate "text/template"

	"github.com/mansiverma897993/signoz/internal/model"
	"github.com/mansiverma897993/signoz/internal/transpile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTranslateExtractsThresholdAndMapsAlertSemantics(t *testing.T) {
	t.Parallel()

	source := model.RuleSet{Groups: []model.RuleGroup{{
		Name: "node", Interval: "30s",
		Rules: []model.Rule{{
			Alert:      "NodeDown",
			Expression: `up{job="node-exporter",instance="$node"} == 0`,
			For:        "5m",
			Labels:     map[string]string{"severity": "warning", "team": "infra"},
			Annotations: map[string]string{
				"summary":     `Node {{ $labels.instance }} is down`,
				"description": `Value is {{ printf "%.2f" $value }}`,
			},
			SourcePath: "/groups/0/rules/0",
		}},
	}}}

	migration := Translate(source, transpile.NewAnalyzer(transpile.Options{}))
	rule := migration.Groups[0].Rules[0]
	require.NotNil(t, rule.Payload)
	assert.True(t, rule.ExtractedThreshold)
	assert.Equal(t, "equal", rule.Operator)
	assert.Zero(t, rule.Target)
	assert.Equal(t, `up{"service.instance.id"="$node","service.name"="node-exporter"}`, rule.Query)
	assert.Equal(t, "5m", rule.Payload.Evaluation.Spec.EvalWindow)
	assert.Equal(t, "30s", rule.Payload.Evaluation.Spec.Frequency)
	assert.Equal(t, "all_the_times", rule.Payload.Condition.Thresholds.Spec[0].MatchType)
	assert.True(t, rule.Payload.Condition.RequireMinPoints)
	assert.Equal(t, 3, rule.Payload.Condition.RequiredPoints)
	assert.True(t, rule.Payload.NotificationSettings.UsePolicy)
	assert.Equal(t, []string{"service.instance.id"}, rule.Payload.NotificationSettings.GroupBy)
	assert.Equal(t, `Node {{$service.instance.id}} is down`, rule.Payload.Annotations["summary"])
	assert.Equal(t, `Value is {{$value}}`, rule.Payload.Annotations["description"])
	assert.Contains(t, rule.Decision.Reasons, model.ReasonAnnotationFormatting)
	assert.True(t, rule.Payload.Disabled)
	assert.Equal(t, model.VerdictNeedsReview, rule.Decision.Verdict)
	assert.Contains(t, rule.Decision.Reasons, model.ReasonAlertForWindow)
	assert.Contains(t, rule.Decision.Reasons, model.ReasonTargetAlertRuntimeLabels)
}

func TestTranslateRewritesEverySupportedPrometheusLabelAccessForm(t *testing.T) {
	t.Parallel()

	source := model.RuleSet{Groups: []model.RuleGroup{{Name: "templates", Rules: []model.Rule{{
		Alert: "TemplateAccess", Expression: `up{job="api",instance="node-1"} == 0`,
		Labels: map[string]string{
			"severity":     "warning",
			"dollar_dot":   `{{ $labels.job }}`,
			"root_dot":     `{{ .Labels.instance }}`,
			"dollar_index": `{{ index $labels "job" }}`,
			"root_index":   `{{ index .Labels "instance" }}`,
			"root_value":   `{{ .Value }}`,
		},
		Annotations: map[string]string{
			"summary": `{{ $labels.job }} {{ .Labels.instance }} {{ index $labels "job" }} {{ index .Labels "instance" }}`,
		},
	}}}}}

	rule := Translate(source, nil).Groups[0].Rules[0]
	require.NotNil(t, rule.Payload)
	assert.Equal(t, `{{$service.name}}`, rule.Payload.Labels["dollar_dot"])
	assert.Equal(t, `{{$service.instance.id}}`, rule.Payload.Labels["root_dot"])
	assert.Equal(t, `{{$service.name}}`, rule.Payload.Labels["dollar_index"])
	assert.Equal(t, `{{$service.instance.id}}`, rule.Payload.Labels["root_index"])
	assert.Equal(t, `{{$value}}`, rule.Payload.Labels["root_value"])
	assert.Equal(t,
		`{{$service.name}} {{$service.instance.id}} {{$service.name}} {{$service.instance.id}}`,
		rule.Payload.Annotations["summary"],
	)
	assert.Equal(t, []string{"service.instance.id", "service.name"}, rule.Payload.NotificationSettings.GroupBy)
	assert.NotContains(t, rule.Decision.Reasons, model.ReasonAlertLabelFormatting)
	assert.NotContains(t, rule.Decision.Reasons, model.ReasonAnnotationFormatting)
	assert.Equal(t, `{{ index $labels "job" }}`, rule.Source.Labels["dollar_index"], "source evidence must stay byte-semantic")
	assertRuleTemplatesExecuteWithPinnedTargetSurface(t, rule)
}

func TestTranslateEscapesBareDollarTextFromSigNozTemplatePreprocessing(t *testing.T) {
	t.Parallel()

	source := model.RuleSet{Groups: []model.RuleGroup{{Name: "literal-dollar", Rules: []model.Rule{{
		Alert: "LiteralDollar", Expression: "up == 0",
		Labels: map[string]string{
			"severity": "warning",
			"owner":    `owner $job / $service.name / $5 / $$`,
		},
		Annotations: map[string]string{"summary": `literal $instance costs $5`},
	}}}}}

	rule := Translate(source, nil).Groups[0].Rules[0]
	require.NotNil(t, rule.Payload)
	assert.Equal(t, `owner {{"$"}}job / {{"$"}}service.name / {{"$"}}5 / {{"$"}}{{"$"}}`, rule.Payload.Labels["owner"])
	assert.Equal(t, `literal {{"$"}}instance costs {{"$"}}5`, rule.Payload.Annotations["summary"])
	assert.NotContains(t, rule.Decision.Reasons, model.ReasonAlertLabelFormatting)
	assert.NotContains(t, rule.Decision.Reasons, model.ReasonAnnotationFormatting)
	assertRuleTemplatesExecuteWithPinnedTargetSurface(t, rule)
}

func TestTranslateOmitsPrometheusOnlyTemplatesFromDisabledTargetCandidates(t *testing.T) {
	t.Parallel()

	source := model.RuleSet{Groups: []model.RuleGroup{{Name: "unsupported-templates", Rules: []model.Rule{{
		Alert: "UnsupportedTemplates", Expression: "up == 0",
		Labels: map[string]string{
			"severity":     "warning",
			"graph":        `{{ graphLink "up" }}`,
			"strip":        `{{ $labels.instance | stripPort }}`,
			"external":     `{{ $externalURL }}`,
			"path":         `{{ pathPrefix }}`,
			"query_braces": `{{ printf "{{" | query }}`,
			"quoted_label": `{{ printf "$labels.job" }}`,
			"comment":      `{{/* $labels.job */}}`,
		},
		Annotations: map[string]string{
			"query":          `{{ query "up" | first | value }}`,
			"query_selector": `{{ query "up{job=\"api\"}" | first | value }}`,
			"table":          `{{ tableLink "up" }}`,
			"duration":       `{{ parseDuration "5m" }}`,
			"domain":         `{{ $labels.instance | stripDomain }}`,
			"external":       `{{ .ExternalURL }}`,
			"graph_braces":   `{{ printf "}}" | graphLink }}`,
			"quoted_value":   `{{ printf "$value" }}`,
		},
	}}}}}

	rule := Translate(source, nil).Groups[0].Rules[0]
	require.NotNil(t, rule.Payload)
	assert.True(t, rule.Payload.Disabled)
	assert.Contains(t, rule.Decision.Reasons, model.ReasonAlertLabelFormatting)
	assert.Contains(t, rule.Decision.Reasons, model.ReasonAnnotationFormatting)
	for _, value := range rule.Payload.Labels {
		assert.NotContains(t, value, "graphLink")
		assert.NotContains(t, value, "stripPort")
		assert.NotContains(t, value, "$externalURL")
		assert.NotContains(t, value, "pathPrefix")
	}
	for _, value := range rule.Payload.Annotations {
		assert.NotContains(t, value, "query")
		assert.NotContains(t, value, "tableLink")
		assert.NotContains(t, value, "parseDuration")
		assert.NotContains(t, value, "stripDomain")
		assert.NotContains(t, value, ".ExternalURL")
	}
	assert.Equal(t, unsupportedPrometheusTemplateSentinel, rule.Payload.Labels["graph"])
	assert.Equal(t, unsupportedPrometheusTemplateSentinel, rule.Payload.Labels["query_braces"])
	assert.Equal(t, unsupportedPrometheusTemplateSentinel, rule.Payload.Labels["quoted_label"])
	assert.Empty(t, rule.Payload.Labels["comment"])
	assert.Equal(t, unsupportedPrometheusTemplateSentinel, rule.Payload.Annotations["query"])
	assert.Equal(t, unsupportedPrometheusTemplateSentinel, rule.Payload.Annotations["query_selector"])
	assert.Equal(t, unsupportedPrometheusTemplateSentinel, rule.Payload.Annotations["graph_braces"])
	assert.Equal(t, unsupportedPrometheusTemplateSentinel, rule.Payload.Annotations["quoted_value"])
	assertRuleTemplatesExecuteWithPinnedTargetSurface(t, rule)
}

func TestTranslateSanitizesTemplatesThatReadTargetMutatedRuntimeLabels(t *testing.T) {
	t.Parallel()

	source := model.RuleSet{Groups: []model.RuleGroup{{Name: "runtime-labels", Rules: []model.Rule{{
		Alert: "RuntimeLabels", Expression: `up{severity="page"} == 0`,
		Labels: map[string]string{
			"severity": "warning",
			"route":    `{{ $labels.severity }}`,
		},
		Annotations: map[string]string{
			"summary": `{{ index $labels "threshold.name" }}`,
			"details": `{{ $labels.alertname }}`,
		},
	}}}}}

	rule := Translate(source, nil).Groups[0].Rules[0]
	require.NotNil(t, rule.Payload)
	assert.Equal(t, unsupportedPrometheusTemplateSentinel, rule.Payload.Labels["route"])
	assert.Equal(t, unsupportedPrometheusTemplateSentinel, rule.Payload.Annotations["summary"])
	assert.Equal(t, `{{$alertname}}`, rule.Payload.Annotations["details"], "alertname is target-owned after expansion, not before it")
	assert.Contains(t, rule.Decision.Reasons, model.ReasonTargetAlertRuntimeLabels)
	assert.Contains(t, rule.Decision.Reasons, model.ReasonAlertLabelFormatting)
	assert.Contains(t, rule.Decision.Reasons, model.ReasonAnnotationFormatting)
	require.NotEmpty(t, rule.Decision.Notes)
	assert.Contains(t, rule.Decision.Notes[0], "severity")
}

func TestTranslateRemapsConfiguredJobAndInstanceLabelKeys(t *testing.T) {
	t.Parallel()

	source := model.RuleSet{Groups: []model.RuleGroup{{Name: "configured-remap", Rules: []model.Rule{{
		Alert: "ConfiguredRemap", Expression: "sum(up) == 0",
		Labels: map[string]string{"severity": "warning", "job": "api", "instance": "node-1"},
	}}}}}
	require.NoError(t, ValidateStableIdentities([]model.RuleSet{source}))

	rule := Translate(source, nil).Groups[0].Rules[0]
	require.NotNil(t, rule.Payload)
	assert.Equal(t, "api", rule.Payload.Labels["service.name"])
	assert.Equal(t, "node-1", rule.Payload.Labels["service.instance.id"])
	assert.NotContains(t, rule.Payload.Labels, "job")
	assert.NotContains(t, rule.Payload.Labels, "instance")
	assert.Equal(t, "api", rule.Source.Labels["job"])
}

func TestTranslateRetainsReviewRecordsWhenQueryCannotBeEmitted(t *testing.T) {
	t.Parallel()

	source := model.RuleSet{Groups: []model.RuleGroup{{Name: "mixed", Rules: []model.Rule{
		{Alert: "Complex", Expression: `(up == 0) or (errors_total > 5)`, Labels: map[string]string{"severity": "major"}, SourcePath: "/rules/0"},
		{Record: "job:up:sum", Expression: `sum by (job) (up)`, SourcePath: "/rules/1"},
	}}}}

	migration := Translate(source, transpile.NewAnalyzer(transpile.Options{}))
	alert := migration.Groups[0].Rules[0]
	assert.Nil(t, alert.Payload)
	assert.Equal(t, model.VerdictNeedsReview, alert.Decision.Verdict)
	assert.Contains(t, alert.Decision.Reasons, model.ReasonAlertThreshold)
	assert.Contains(t, alert.Decision.Reasons, model.ReasonTargetVectorMatchingUnresolved)

	recording := migration.Groups[0].Rules[1]
	assert.Nil(t, recording.Payload)
	assert.Contains(t, recording.Decision.Reasons, model.ReasonRecordingRule)
}

func TestTranslateDisambiguatesDuplicateAlertNames(t *testing.T) {
	t.Parallel()

	source := model.RuleSet{Groups: []model.RuleGroup{{Name: "node", Rules: []model.Rule{
		{Alert: "DiskFull", Expression: `disk_free < 10`, Labels: map[string]string{"severity": "warning"}, SourcePath: "/rules/0"},
		{Alert: "DiskFull", Expression: `disk_free < 5`, Labels: map[string]string{"severity": "critical"}, SourcePath: "/rules/1"},
	}}}}

	migration := Translate(source, transpile.NewAnalyzer(transpile.Options{}))
	assert.Equal(t, "DiskFull [warning]", migration.Groups[0].Rules[0].Payload.Alert)
	assert.Equal(t, "DiskFull [critical]", migration.Groups[0].Rules[1].Payload.Alert)
	assert.NotEqual(t, migration.Groups[0].Rules[0].Payload.Labels["promcast_id"], migration.Groups[0].Rules[1].Payload.Labels["promcast_id"])
}

func TestTranslateUsesStableNamespacedRuleIdentity(t *testing.T) {
	t.Parallel()

	source := model.RuleSet{
		Source: model.Source{Namespace: "prometheus:production", Identity: "rules/platform.yaml"},
		Groups: []model.RuleGroup{{
			Name: "availability", SourcePath: "/documents/0/groups/0",
			Rules: []model.Rule{{
				Alert: "NodeDown", Expression: "up == 0", SourcePath: "/documents/0/groups/0/rules/0",
			}},
		}},
	}
	first := Translate(source, nil).Groups[0].Rules[0].Payload.Labels["promcast_id"]

	edited := source
	edited.Groups = append([]model.RuleGroup(nil), source.Groups...)
	edited.Groups[0].Rules = append([]model.Rule(nil), source.Groups[0].Rules...)
	edited.Groups[0].Rules[0].Expression = "up < 1"
	second := Translate(edited, nil).Groups[0].Rules[0].Payload.Labels["promcast_id"]

	otherNamespace := source
	otherNamespace.Source.Namespace = "prometheus:staging"
	third := Translate(otherNamespace, nil).Groups[0].Rules[0].Payload.Labels["promcast_id"]

	otherFile := source
	otherFile.Source.Identity = "rules/edge.yaml"
	fourth := Translate(otherFile, nil).Groups[0].Rules[0].Payload.Labels["promcast_id"]

	unscoped := source
	unscoped.Source.Namespace = ""
	unscopedFirst := Translate(unscoped, nil).Groups[0].Rules[0].Payload.Labels["promcast_id"]
	unscoped.Source.Identity = "rules/edge.yaml"
	unscopedSecond := Translate(unscoped, nil).Groups[0].Rules[0].Payload.Labels["promcast_id"]

	assert.Equal(t, first, second, "editing a rule expression must update the existing SigNoz rule")
	assert.NotEqual(t, first, third, "different source estates must not share target identities")
	assert.Equal(t, first, fourth, "a supplied namespace must be stable across absolute, relative, and renamed source paths")
	assert.NotEqual(t, unscopedFirst, unscopedSecond, "unscoped runs retain file identity as a compatibility fallback")
}

func TestTranslateNamespacedRuleIdentitySurvivesGroupAndRuleReordering(t *testing.T) {
	t.Parallel()

	first := model.RuleSet{
		Source: model.Source{Namespace: "prometheus:production", Identity: "/checkout/one/rules.yaml"},
		Groups: []model.RuleGroup{
			{Name: "availability", SourcePath: "/groups/0", Rules: []model.Rule{
				{Alert: "NodeDown", Expression: "up == 0", SourcePath: "/groups/0/rules/0"},
				{Alert: "NodeFlapping", Expression: "changes(up[5m]) > 3", SourcePath: "/groups/0/rules/1"},
			}},
			{Name: "capacity", SourcePath: "/groups/1", Rules: []model.Rule{
				{Alert: "DiskFull", Expression: "disk_free < 10", SourcePath: "/groups/1/rules/0"},
			}},
		},
	}
	second := model.RuleSet{
		Source: model.Source{Namespace: "prometheus:production", Identity: "rules.yaml"},
		Groups: []model.RuleGroup{
			{Name: "capacity", SourcePath: "/groups/0", Rules: []model.Rule{
				{Alert: "DiskFull", Expression: "disk_free < 5", SourcePath: "/groups/0/rules/0"},
			}},
			{Name: "availability", SourcePath: "/groups/1", Rules: []model.Rule{
				{Alert: "NodeFlapping", Expression: "changes(up[10m]) > 5", SourcePath: "/groups/1/rules/0"},
				{Alert: "NodeDown", Expression: "up < 1", SourcePath: "/groups/1/rules/1"},
			}},
		},
	}

	assert.Equal(t, translatedRuleIDs(Translate(first, nil)), translatedRuleIDs(Translate(second, nil)))
}

func TestTranslateNamespacedDuplicateNamesUseExplicitStableSourceID(t *testing.T) {
	t.Parallel()

	source := model.RuleSet{
		Source: model.Source{Namespace: "prometheus:production"},
		Groups: []model.RuleGroup{{Name: "disk", Rules: []model.Rule{
			{Alert: "DiskFull", Expression: "disk_free < 10", Labels: map[string]string{StableSourceIDLabel: "warning"}},
			{Alert: "DiskFull", Expression: "disk_free < 5", Labels: map[string]string{StableSourceIDLabel: "critical"}},
		}}},
	}
	migration := Translate(source, nil)
	first := migration.Groups[0].Rules[0].Payload.Labels["promcast_id"]
	second := migration.Groups[0].Rules[1].Payload.Labels["promcast_id"]
	firstName := migration.Groups[0].Rules[0].Payload.Alert
	secondName := migration.Groups[0].Rules[1].Payload.Alert
	assert.NotEqual(t, first, second)

	reordered := source
	reordered.Groups = append([]model.RuleGroup(nil), source.Groups...)
	reordered.Groups[0].Rules = []model.Rule{source.Groups[0].Rules[1], source.Groups[0].Rules[0]}
	reorderedMigration := Translate(reordered, nil)
	assert.Equal(t, second, reorderedMigration.Groups[0].Rules[0].Payload.Labels["promcast_id"])
	assert.Equal(t, first, reorderedMigration.Groups[0].Rules[1].Payload.Labels["promcast_id"])
	assert.Equal(t, secondName, reorderedMigration.Groups[0].Rules[0].Payload.Alert)
	assert.Equal(t, firstName, reorderedMigration.Groups[0].Rules[1].Payload.Alert)
}

func TestTranslateCollectionInventoryDisambiguatesAcrossFiles(t *testing.T) {
	t.Parallel()

	first := model.RuleSet{
		Source: model.Source{Namespace: "prometheus:production", Identity: "rules/cpu.yaml"},
		Groups: []model.RuleGroup{{Name: "cpu", Rules: []model.Rule{{
			Alert: "Saturation", Expression: "cpu_usage > 0.9", Labels: map[string]string{"severity": "warning"},
		}}}},
	}
	second := model.RuleSet{
		Source: model.Source{Namespace: "prometheus:production", Identity: "rules/memory.yaml"},
		Groups: []model.RuleGroup{{Name: "memory", Rules: []model.Rule{{
			Alert: "Saturation", Expression: "memory_usage > 0.9", Labels: map[string]string{"severity": "warning"},
		}}}},
	}
	inventory := NewAlertNameInventory([]model.RuleSet{first, second})
	firstPayload := TranslateWithAlertNameInventory(first, nil, inventory).Groups[0].Rules[0].Payload
	secondPayload := TranslateWithAlertNameInventory(second, nil, inventory).Groups[0].Rules[0].Payload
	require.NotNil(t, firstPayload)
	require.NotNil(t, secondPayload)
	assert.NotEqual(t, firstPayload.Alert, secondPayload.Alert)

	reversed := NewAlertNameInventory([]model.RuleSet{second, first})
	firstAgain := TranslateWithAlertNameInventory(first, nil, reversed).Groups[0].Rules[0].Payload
	secondAgain := TranslateWithAlertNameInventory(second, nil, reversed).Groups[0].Rules[0].Payload
	assert.Equal(t, firstPayload.Alert, firstAgain.Alert)
	assert.Equal(t, secondPayload.Alert, secondAgain.Alert)
}

func TestTranslateDuplicateSuffixUsesCompleteStableMigrationID(t *testing.T) {
	t.Parallel()

	source := model.RuleSet{
		Source: model.Source{Namespace: "prometheus:production"},
		Groups: []model.RuleGroup{{Name: "disk", Rules: []model.Rule{
			{Alert: "DiskFull", Expression: "disk_free < 10", Labels: map[string]string{
				"severity": "warning", StableSourceIDLabel: "id-8926",
			}},
			{Alert: "DiskFull", Expression: "disk_free < 5", Labels: map[string]string{
				"severity": "warning", StableSourceIDLabel: "id-59803",
			}},
		}}},
	}
	migration := Translate(source, nil)
	first := migration.Groups[0].Rules[0].Payload
	second := migration.Groups[0].Rules[1].Payload
	require.NotNil(t, first)
	require.NotNil(t, second)
	require.NotEqual(t, first.Labels["promcast_id"], second.Labels["promcast_id"])
	assert.NotEqual(t, first.Alert, second.Alert)
	assert.Contains(t, first.Alert, first.Labels["promcast_id"])
	assert.Contains(t, second.Alert, second.Labels["promcast_id"])
}

func translatedRuleIDs(migration Migration) map[string]string {
	result := make(map[string]string)
	for _, group := range migration.Groups {
		for _, rule := range group.Rules {
			if rule.Payload != nil {
				result[group.Source.Name+"/"+rule.Source.Alert] = rule.Payload.Labels["promcast_id"]
			}
		}
	}
	return result
}

func TestValidateStableIdentitiesRejectsAmbiguousNamespacedRules(t *testing.T) {
	t.Parallel()

	sources := []model.RuleSet{{
		Source: model.Source{Namespace: "prometheus:production", Identity: "rules/disk.yaml"},
		Groups: []model.RuleGroup{{Name: "disk", Rules: []model.Rule{
			{Alert: "DiskFull", Expression: "disk_free < 10", SourcePath: "/groups/0/rules/0"},
			{Alert: "DiskFull", Expression: "disk_free < 5", SourcePath: "/groups/0/rules/1"},
		}}},
	}}

	err := ValidateStableIdentities(sources)
	require.Error(t, err)
	assert.Contains(t, err.Error(), StableSourceIDLabel)
	assert.Contains(t, err.Error(), "/groups/0/rules/0")
	assert.Contains(t, err.Error(), "/groups/0/rules/1")

	sources[0].Groups[0].Rules[0].Labels = map[string]string{StableSourceIDLabel: "warning"}
	sources[0].Groups[0].Rules[1].Labels = map[string]string{StableSourceIDLabel: "critical"}
	require.NoError(t, ValidateStableIdentities(sources))
}

func TestValidateStableIdentitiesRejectsDuplicateExplicitIDsAcrossFiles(t *testing.T) {
	t.Parallel()

	sources := []model.RuleSet{
		{
			Source: model.Source{Namespace: "prometheus:production", Identity: "rules/one.yaml"},
			Groups: []model.RuleGroup{{Name: "one", Rules: []model.Rule{{
				Alert: "First", Labels: map[string]string{StableSourceIDLabel: "shared"},
			}}}},
		},
		{
			Source: model.Source{Namespace: "prometheus:production", Identity: "rules/two.yaml"},
			Groups: []model.RuleGroup{{Name: "two", Rules: []model.Rule{{
				Alert: "Second", Labels: map[string]string{StableSourceIDLabel: "shared"},
			}}}},
		},
	}

	require.Error(t, ValidateStableIdentities(sources))
	sources[1].Source.Namespace = "prometheus:staging"
	require.NoError(t, ValidateStableIdentities(sources))
}

func TestValidateStableIdentitiesRejectsUnsafeOrUnboundedComponents(t *testing.T) {
	t.Parallel()

	source := model.RuleSet{
		Source: model.Source{Namespace: "prometheus:production"},
		Groups: []model.RuleGroup{{Name: "availability", Rules: []model.Rule{{
			Alert: "NodeDown", Expression: "up == 0",
			Labels: map[string]string{StableSourceIDLabel: "node\x00primary"},
		}}}},
	}
	require.ErrorContains(t, ValidateStableIdentities([]model.RuleSet{source}), "control or formatting")

	source.Groups[0].Rules[0].Labels[StableSourceIDLabel] = "node-primary"
	source.Source.Namespace = string(make([]byte, 513))
	require.ErrorContains(t, ValidateStableIdentities([]model.RuleSet{source}), "exceeds 512 bytes")

	source.Source.Namespace = "prometheus:production"
	source.Groups[0].Name = "availability\n"
	require.ErrorContains(t, ValidateStableIdentities([]model.RuleSet{source}), "control or formatting")
	source.Groups[0].Name = "availability"
	source.Groups[0].Rules[0].Alert = "NodeDown\n"
	require.ErrorContains(t, ValidateStableIdentities([]model.RuleSet{source}), "control or formatting")
	source.Groups[0].Rules[0].Alert = "NodeDown"
	source.Groups[0].Rules[0].Labels[StableSourceIDLabel] = "node-primary\n"
	require.ErrorContains(t, ValidateStableIdentities([]model.RuleSet{source}), "control or formatting")
}

func TestValidateStableIdentitiesRejectsGeneratedAlertLabelCollisions(t *testing.T) {
	t.Parallel()

	for _, label := range []string{"prometheus_alertname", "prometheus_rule_group", "promcast_id"} {
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			source := model.RuleSet{Groups: []model.RuleGroup{{Name: "ownership", Rules: []model.Rule{{
				Alert: "Collision", Expression: "up == 0", SourcePath: "/groups/0/rules/0",
				Labels: map[string]string{label: "source-value"},
			}}}}}
			err := ValidateStableIdentities([]model.RuleSet{source})
			require.ErrorContains(t, err, `reserved target label "`+label+`"`)
		})
	}

	t.Run("inherited group label", func(t *testing.T) {
		t.Parallel()
		source := model.RuleSet{Groups: []model.RuleGroup{{
			Name: "ownership", Labels: map[string]string{"prometheus_rule_group": "source-value"},
			Rules: []model.Rule{{Alert: "Collision", Expression: "up == 0", SourcePath: "/groups/0/rules/0"}},
		}}}
		require.ErrorContains(t, ValidateStableIdentities([]model.RuleSet{source}), `reserved target label "prometheus_rule_group"`)
	})
}

func TestValidateStableIdentitiesRejectsPinnedSigNozRuntimeLabelOwnership(t *testing.T) {
	t.Parallel()

	for _, label := range []string{"threshold.name", "ruleId", "ruleSource", "nodata", "alertname"} {
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			source := model.RuleSet{Groups: []model.RuleGroup{{Name: "runtime", Rules: []model.Rule{{
				Alert: "Collision", Expression: "sum(up) == 0",
				Labels: map[string]string{label: "source-value"},
			}}}}}
			err := ValidateStableIdentities([]model.RuleSet{source})
			require.ErrorContains(t, err, `reserved target label "`+label+`"`)
		})
	}

	source := model.RuleSet{Groups: []model.RuleGroup{{Name: "runtime", Rules: []model.Rule{{
		Alert: "ThresholdIsNotOwned", Expression: "sum(up) == 0",
		Labels: map[string]string{"threshold": "source-value"},
	}}}}}
	require.NoError(t, ValidateStableIdentities([]model.RuleSet{source}), "plain threshold is not the v0.133 runtime label")
}

func TestValidateStableIdentitiesRejectsConfiguredLabelNamespaceCollisions(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		expression string
		labels     map[string]string
		want       string
	}{
		{
			name: "both configured spellings", expression: "sum(up) == 0",
			labels: map[string]string{"job": "source", "service.name": "target"}, want: "would collide",
		},
		{
			name: "explicit selector", expression: `up{job="api"} == 0`,
			labels: map[string]string{"service.name": "static"}, want: "may retain source label \"job\"",
		},
		{
			name: "unknown selector labels fail closed", expression: "up == 0",
			labels: map[string]string{"service.name": "static"}, want: "may retain source label \"job\"",
		},
		{
			name: "grouping retains alias", expression: "sum by (job) (up) == 0",
			labels: map[string]string{"service.name": "static"}, want: "may retain source label \"job\"",
		},
		{
			name: "without retains alias", expression: "sum without (cluster) (up) == 0",
			labels: map[string]string{"service.name": "static"}, want: "may retain source label \"job\"",
		},
		{
			name: "unknown label function", expression: `label_replace(sum(up), "job", "api", "cluster", "(.*)") == 0`,
			labels: map[string]string{"service.name": "static"}, want: "may retain source label \"job\"",
		},
		{
			name: "selection aggregation preserves input labels", expression: `topk(1, up{job=~".+"}) > 0`,
			labels: map[string]string{"service.name": "static"}, want: "may retain source label \"job\"",
		},
		{
			name: "count values creates alias", expression: `count_values("job", up) > 0`,
			labels: map[string]string{"service.name": "static"}, want: "may retain source label \"job\"",
		},
		{
			name: "instance alias", expression: `up{instance="node"} == 0`,
			labels: map[string]string{"service.instance.id": "static"}, want: "may retain source label \"instance\"",
		},
		{
			name: "configured source with explicit target selector", expression: `up{"service.name"="dynamic"} == 0`,
			labels: map[string]string{"job": "static"}, want: "may retain target label \"service.name\"",
		},
		{
			name: "configured source with target grouping", expression: `sum by ("service.name") (up) == 0`,
			labels: map[string]string{"job": "static"}, want: "may retain target label \"service.name\"",
		},
		{
			name: "configured source with selection aggregation", expression: `topk(1, up{"service.name"=~".+"}) > 0`,
			labels: map[string]string{"job": "static"}, want: "may retain target label \"service.name\"",
		},
		{
			name: "configured source with count values target", expression: `count_values("service.name", up) > 0`,
			labels: map[string]string{"job": "static"}, want: "may retain target label \"service.name\"",
		},
		{
			name: "configured instance with explicit target selector", expression: `up{"service.instance.id"="dynamic"} == 0`,
			labels: map[string]string{"instance": "static"}, want: "may retain target label \"service.instance.id\"",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := model.RuleSet{Groups: []model.RuleGroup{{Name: "collision", Rules: []model.Rule{{
				Alert: "Collision", Expression: test.expression, Labels: test.labels,
			}}}}}
			err := ValidateStableIdentities([]model.RuleSet{source})
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestValidateStableIdentitiesAllowsOnlyProvenAliasDroppingExpressions(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		expression string
		labels     map[string]string
	}{
		{expression: "sum(up) == 0", labels: map[string]string{"service.name": "static"}},
		{expression: "sum by (cluster) (up) == 0", labels: map[string]string{"service.name": "static"}},
		{expression: "sum without (job) (up) == 0", labels: map[string]string{"service.name": "static"}},
		{expression: "sum(up) == 0", labels: map[string]string{"job": "static"}},
		{expression: `sum by (cluster) (up) == 0`, labels: map[string]string{"job": "static"}},
		{expression: `sum without ("service.name") (up) == 0`, labels: map[string]string{"job": "static"}},
	} {
		source := model.RuleSet{Groups: []model.RuleGroup{{Name: "safe", Rules: []model.Rule{{
			Alert: "Safe", Expression: test.expression, Labels: test.labels,
		}}}}}
		require.NoError(t, ValidateStableIdentities([]model.RuleSet{source}), test.expression)
	}
}

func TestValidateStableIdentitiesEnforcesPinnedSigNozRuleKeyNames(t *testing.T) {
	t.Parallel()

	source := model.RuleSet{Groups: []model.RuleGroup{{Name: "target-names", Rules: []model.Rule{{
		Alert: "TargetNames", Expression: "sum(up) == 0",
	}}}}}
	for _, invalid := range []string{".leading", "9leading", "地域"} {
		source.Groups[0].Rules[0].Labels = map[string]string{invalid: "value"}
		source.Groups[0].Rules[0].Annotations = nil
		require.ErrorContains(t, ValidateStableIdentities([]model.RuleSet{source}), `label name "`+invalid+`"`)

		source.Groups[0].Rules[0].Labels = nil
		source.Groups[0].Rules[0].Annotations = map[string]string{invalid: "value"}
		require.ErrorContains(t, ValidateStableIdentities([]model.RuleSet{source}), `annotation name "`+invalid+`"`)
	}

	source.Groups[0].Rules[0].Labels = map[string]string{"team.owner": "platform", "_ok": "true"}
	source.Groups[0].Rules[0].Annotations = map[string]string{"runbook.url": "https://example.invalid", "_ok": "true"}
	require.NoError(t, ValidateStableIdentities([]model.RuleSet{source}), "ASCII dotted target keys are accepted")
}

func TestValidateStableIdentitiesGuardsConditionalSeverityPreservationLabel(t *testing.T) {
	t.Parallel()

	source := model.RuleSet{Groups: []model.RuleGroup{{Name: "severity", Rules: []model.Rule{{
		Alert: "Normalized", Expression: "up == 0", SourcePath: "/groups/0/rules/0",
		Labels: map[string]string{"severity": "WARN", "prometheus_severity": "source-owned"},
	}}}}}
	err := ValidateStableIdentities([]model.RuleSet{source})
	require.ErrorContains(t, err, `reserved target label "prometheus_severity"`)
	assert.Contains(t, err.Error(), `requires normalization to "warning"`)

	source.Groups[0].Rules[0].Labels["severity"] = "warning"
	require.NoError(t, ValidateStableIdentities([]model.RuleSet{source}), "no preservation label is generated for canonical severity")

	source.Groups[0].Rules[0].Record = "recorded_metric"
	source.Groups[0].Rules[0].Alert = ""
	require.NoError(t, ValidateStableIdentities([]model.RuleSet{source}), "recording rules do not generate alert provenance labels")
}

func TestValidateStableIdentitiesRequiresCanonicalExplicitSourceID(t *testing.T) {
	t.Parallel()

	source := model.RuleSet{Groups: []model.RuleGroup{{Name: "identity", Rules: []model.Rule{{
		Alert: "NodeDown", Expression: "up == 0", SourcePath: "/groups/0/rules/0",
		Labels: map[string]string{StableSourceIDLabel: " node-primary "},
	}}}}}
	require.ErrorContains(t, ValidateStableIdentities([]model.RuleSet{source}), "must not contain surrounding whitespace")

	source.Groups[0].Rules[0].Labels[StableSourceIDLabel] = "   "
	require.ErrorContains(t, ValidateStableIdentities([]model.RuleSet{source}), "must be nonempty when present")

	source.Groups[0].Rules[0].Labels[StableSourceIDLabel] = "node-primary"
	require.NoError(t, ValidateStableIdentities([]model.RuleSet{source}))

	source.Groups[0].Labels = map[string]string{StableSourceIDLabel: " inherited "}
	delete(source.Groups[0].Rules[0].Labels, StableSourceIDLabel)
	require.ErrorContains(t, ValidateStableIdentities([]model.RuleSet{source}), "must not contain surrounding whitespace")
}

func TestExtractThresholdHandlesReversedAndComplexComparisons(t *testing.T) {
	t.Parallel()

	query, operator, target, extracted, err := extractThreshold(`0.75 < (used_bytes / capacity_bytes)`)
	require.NoError(t, err)
	assert.True(t, extracted)
	assert.Equal(t, "above", operator)
	assert.Equal(t, 0.75, target)
	assert.Contains(t, query, "used_bytes / capacity_bytes")

	_, _, _, extracted, err = extractThreshold(`(up == 0) and (maintenance == 0)`)
	require.NoError(t, err)
	assert.False(t, extracted)
}

func TestTranslateUsesMetadataDrivenTargetVectorMatching(t *testing.T) {
	t.Parallel()

	attributes := []string{"mountpoint", "server.address", "service.instance.id", "service.name", "url.scheme"}
	analyzer := transpile.NewAnalyzer(transpile.Options{Metrics: map[string]model.TargetMetric{
		"available_bytes": {Type: "gauge", Attributes: attributes},
		"capacity_bytes":  {Type: "gauge", Attributes: attributes},
	}})
	source := model.RuleSet{Groups: []model.RuleGroup{{Name: "storage", Rules: []model.Rule{{
		Alert: "DiskFull", Expression: `available_bytes{instance="$node"} / capacity_bytes{instance="$node"} < 0.1`,
		Labels: map[string]string{"severity": "critical"},
	}}}}}

	migration := Translate(source, analyzer)
	rule := migration.Groups[0].Rules[0]
	require.NotNil(t, rule.Payload)
	assert.Contains(t, rule.Query, `on (mountpoint, "service.instance.id", "service.name")`)
	assert.NotContains(t, rule.Query, `"server.address"`)
	assert.Contains(t, rule.Decision.Reasons, model.ReasonTargetVectorMatching)
}

func TestTranslateKeepsBuilderCandidateRisksOutOfPromQLAlerts(t *testing.T) {
	t.Parallel()

	options := transpile.Options{Metrics: map[string]model.TargetMetric{
		"requests_total": {Type: "sum", Temporality: "cumulative", IsMonotonic: true},
		"up":             {Type: "gauge"},
		"errors":         {Type: "gauge"},
		"latency_bucket": {Type: "histogram", Temporality: "cumulative"},
	}}
	tests := []struct {
		name       string
		expression string
	}{
		{name: "rate", expression: `rate(requests_total[1m]) > 0`},
		{name: "latest", expression: `up == 0`},
		{name: "histogram percentile", expression: `histogram_quantile(0.95, sum by (le) (rate(latency_bucket[1m]))) > 1`},
		{name: "formula", expression: `sum(errors) / 2 > 0.1`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := model.RuleSet{Groups: []model.RuleGroup{{Name: "candidate", Rules: []model.Rule{{
				Alert: "PromQLExecution", Expression: test.expression,
				Labels: map[string]string{"severity": "warning"},
			}}}}}
			migration := Translate(source, transpile.NewAnalyzer(options))
			rule := migration.Groups[0].Rules[0]

			require.NotNil(t, rule.Payload)
			assert.True(t, rule.Payload.Disabled, "reasons: %v", rule.Decision.Reasons)
			assert.Equal(t, model.VerdictNeedsReview, rule.Decision.Verdict, "reasons: %v", rule.Decision.Reasons)
			assert.Contains(t, rule.Decision.Reasons, model.ReasonAlertForDefault)
			assert.Equal(t, "promql", rule.Payload.Condition.CompositeQuery.QueryType)
			require.Len(t, rule.Payload.Condition.CompositeQuery.Queries, 1)
			assert.Equal(t, "promql", rule.Payload.Condition.CompositeQuery.Queries[0].Type)
			assert.Equal(t, rule.Query, rule.Payload.Condition.CompositeQuery.Queries[0].Spec.Query)
			for _, reason := range rule.Decision.Reasons {
				assert.False(t, model.IsBuilderCandidateSemanticReason(reason))
			}
		})
	}
}

func TestTranslateMergesGroupLabelsBeforeRuleLabels(t *testing.T) {
	t.Parallel()

	source := model.RuleSet{Groups: []model.RuleGroup{{
		Name: "merged",
		Labels: map[string]string{
			"cluster": "production", "owner": "platform", "severity": "info",
		},
		Rules: []model.Rule{{
			Alert: "MergeLabels", Expression: "up == 0",
			Labels: map[string]string{"owner": "payments", "severity": "critical", "runbook": "node-down"},
		}},
	}}}

	rule := Translate(source, nil).Groups[0].Rules[0]
	require.NotNil(t, rule.Payload)
	assert.Equal(t, "production", rule.Payload.Labels["cluster"])
	assert.Equal(t, "payments", rule.Payload.Labels["owner"])
	assert.Equal(t, "critical", rule.Payload.Labels["severity"])
	assert.Equal(t, "node-down", rule.Payload.Labels["runbook"])
	assert.Equal(t, "platform", source.Groups[0].Labels["owner"], "source group evidence must not be mutated")
	assert.Equal(t, "payments", rule.Source.Labels["owner"], "source rule evidence must remain rule-local")
}

func TestTranslateDisablesEveryRuleAffectedByGroupOffsetAndLimit(t *testing.T) {
	t.Parallel()

	source := model.RuleSet{Groups: []model.RuleGroup{{
		Name: "guarded", QueryOffset: "15m", Limit: 100,
		Rules: []model.Rule{
			{Alert: "OffsetAlert", Expression: "up == 0", Labels: map[string]string{"severity": "warning"}},
			{Record: "job:up:sum", Expression: "sum by (job) (up)"},
		},
	}}}

	migration := Translate(source, nil)
	require.Len(t, migration.Groups[0].Rules, 2)
	for _, rule := range migration.Groups[0].Rules {
		assert.Equal(t, model.VerdictNeedsReview, rule.Decision.Verdict)
		assert.Contains(t, rule.Decision.Reasons, model.ReasonRuleGroupQueryOffset)
		assert.Contains(t, rule.Decision.Reasons, model.ReasonRuleGroupLimit)
	}
	require.NotNil(t, migration.Groups[0].Rules[0].Payload)
	assert.True(t, migration.Groups[0].Rules[0].Payload.Disabled)
}

func TestTranslateTreatsZeroQueryOffsetAndNonpositiveLimitsAsNoops(t *testing.T) {
	t.Parallel()

	for _, limit := range []int{0, -1, -100} {
		source := model.RuleSet{Groups: []model.RuleGroup{{
			Name: "zero", QueryOffset: "0s", Limit: limit,
			Rules: []model.Rule{{Alert: "Zero", Expression: "up == 0", Labels: map[string]string{"severity": "warning"}}},
		}}}

		rule := Translate(source, nil).Groups[0].Rules[0]
		assert.NotContains(t, rule.Decision.Reasons, model.ReasonRuleGroupQueryOffset)
		assert.NotContains(t, rule.Decision.Reasons, model.ReasonRuleGroupLimit)
	}
}

func TestTranslateReportsUnrepresentableGroupIntervalsWithoutAcceleratingSilently(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		interval  string
		forValue  string
		frequency string
	}{
		{name: "longer than for window", interval: "10m", forValue: "5m", frequency: "1m"},
		{name: "invalid", interval: "not-a-duration", forValue: "5m", frequency: "1m"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := model.RuleSet{Groups: []model.RuleGroup{{
				Name: "interval", Interval: test.interval,
				Rules: []model.Rule{{
					Alert: "Interval", Expression: "up == 0", For: test.forValue,
					Labels: map[string]string{"severity": "warning"},
				}},
			}}}

			rule := Translate(source, nil).Groups[0].Rules[0]
			require.NotNil(t, rule.Payload)
			assert.Equal(t, test.frequency, rule.Payload.Evaluation.Spec.Frequency)
			assert.True(t, rule.Payload.Disabled)
			assert.Contains(t, rule.Decision.Reasons, model.ReasonRuleGroupInterval)
		})
	}
}

func TestTranslateTreatsZeroGroupIntervalAndKeepFiringForAsNoops(t *testing.T) {
	t.Parallel()

	source := model.RuleSet{Groups: []model.RuleGroup{{
		Name: "global-default", Interval: "0s",
		Rules: []model.Rule{{
			Alert: "Noops", Expression: "up == 0", For: "5m", KeepFiringFor: "0s",
			Labels: map[string]string{"severity": "warning"},
		}},
	}}}

	rule := Translate(source, nil).Groups[0].Rules[0]
	require.NotNil(t, rule.Payload)
	assert.Equal(t, "1m", rule.Payload.Evaluation.Spec.Frequency)
	assert.NotContains(t, rule.Decision.Reasons, model.ReasonRuleGroupInterval)
	assert.NotContains(t, rule.Decision.Reasons, model.ReasonKeepFiringFor)

	source.Groups[0].Rules[0].KeepFiringFor = "5m"
	positive := Translate(source, nil).Groups[0].Rules[0]
	assert.Contains(t, positive.Decision.Reasons, model.ReasonKeepFiringFor)
}

func TestTranslateUsesPinnedPromQLStepForCandidateMinimumPoints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		forValue string
		require  bool
		points   int
	}{
		{forValue: "2m", require: false, points: 0},
		{forValue: "3m", require: true, points: 1},
		{forValue: "15m", require: true, points: 13},
		{forValue: "15m59s", require: true, points: 13},
	}
	for _, test := range tests {
		t.Run(test.forValue, func(t *testing.T) {
			t.Parallel()
			source := model.RuleSet{Groups: []model.RuleGroup{{Name: "points", Rules: []model.Rule{{
				Alert: "Points", Expression: "up == 0", For: test.forValue,
				Labels: map[string]string{"severity": "warning"},
			}}}}}

			rule := Translate(source, nil).Groups[0].Rules[0]
			require.NotNil(t, rule.Payload)
			assert.Equal(t, test.require, rule.Payload.Condition.RequireMinPoints)
			assert.Equal(t, test.points, rule.Payload.Condition.RequiredPoints)
			assert.True(t, rule.Payload.Disabled, "minimum points are candidate hardening, not equivalence proof")
			assert.Contains(t, rule.Decision.Reasons, model.ReasonAlertForWindow)
		})
	}
}

func TestTranslateTreatsExplicitZeroForAsImmediateApproximation(t *testing.T) {
	t.Parallel()

	for _, forValue := range []string{"0", "0s", "0m"} {
		t.Run(forValue, func(t *testing.T) {
			t.Parallel()
			source := model.RuleSet{Groups: []model.RuleGroup{{Name: "immediate", Rules: []model.Rule{{
				Alert: "Immediate", Expression: "up == 0", For: forValue,
				Labels: map[string]string{"severity": "warning"},
			}}}}}

			rule := Translate(source, nil).Groups[0].Rules[0]
			require.NotNil(t, rule.Payload)
			assert.Equal(t, "at_least_once", rule.Payload.Condition.Thresholds.Spec[0].MatchType)
			assert.Contains(t, rule.Decision.Reasons, model.ReasonAlertForDefault)
			assert.NotContains(t, rule.Decision.Reasons, model.ReasonAlertForInvalid)
			assert.True(t, rule.Payload.Disabled)
		})
	}
}

func TestBuilderCandidateOnlyReviewRejectsIndependentRisk(t *testing.T) {
	t.Parallel()

	translation := model.Translation{
		Kind: model.TranslationBuilder,
		Decision: model.Decision{Verdict: model.VerdictNeedsReview, Reasons: []model.ReasonCode{
			model.ReasonBuilderLatestLookback,
			model.ReasonUnsupportedModifier,
		}},
	}

	assert.False(t, builderCandidateOnlyReview(translation))
	assert.Equal(t, []model.ReasonCode{model.ReasonUnsupportedModifier}, promQLAlertReasons(translation.Decision.Reasons))
}

func TestTranslateDisablesAlertWhenAnalyzerRequiresReview(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		expr    string
		options transpile.Options
		reason  model.ReasonCode
	}{
		{
			name: "missing target metric", expr: `sum(missing_total) > 0`,
			options: transpile.Options{MissingMetrics: map[string]bool{"missing_total": true}},
			reason:  model.ReasonMissingMetric,
		},
		{
			name: "metadata unavailable", expr: `sum(unavailable_total) > 0`,
			options: transpile.Options{MetadataErrors: map[string]bool{"unavailable_total": true}},
			reason:  model.ReasonMetricMetadataUnavailable,
		},
		{
			name: "recording rule series", expr: `sum(instance:requests:rate5m) > 0`,
			reason: model.ReasonRecordingRuleMetric,
		},
		{
			name: "offset semantics", expr: `sum(requests_total offset 30m) > 0`,
			reason: model.ReasonUnsupportedModifier,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := model.RuleSet{Groups: []model.RuleGroup{{Name: "review", Rules: []model.Rule{{
				Alert: "ReviewRequired", Expression: test.expr,
				Labels: map[string]string{"severity": "warning"},
			}}}}}

			migration := Translate(source, transpile.NewAnalyzer(test.options))
			rule := migration.Groups[0].Rules[0]

			require.NotNil(t, rule.Payload)
			assert.True(t, rule.Payload.Disabled)
			assert.Equal(t, model.VerdictNeedsReview, rule.Decision.Verdict)
			assert.Contains(t, rule.Decision.Reasons, test.reason)
		})
	}
}

var emittedTargetLabelTemplate = regexp.MustCompile(`{{\$([A-Za-z_][A-Za-z0-9_.]*)}}`)

func assertRuleTemplatesExecuteWithPinnedTargetSurface(t *testing.T, rule RuleMigration) {
	t.Helper()
	require.NotNil(t, rule.Payload)
	values := make([]string, 0, len(rule.Payload.Labels)+len(rule.Payload.Annotations))
	for _, value := range rule.Payload.Labels {
		values = append(values, value)
	}
	for _, value := range rule.Payload.Annotations {
		values = append(values, value)
	}
	for _, value := range values {
		processed := emittedTargetLabelTemplate.ReplaceAllStringFunc(value, func(action string) string {
			match := emittedTargetLabelTemplate.FindStringSubmatch(action)
			if match[1] == "value" || match[1] == "threshold" {
				return action
			}
			return `{{index .Labels "` + match[1] + `"}}`
		})
		template, err := texttemplate.New("target").Parse(
			`{{$labels := .Labels}}{{$value := .Value}}{{$threshold := .Threshold}}` + processed,
		)
		require.NoError(t, err, value)
		var rendered bytes.Buffer
		require.NoError(t, template.Execute(&rendered, struct {
			Labels    map[string]string
			Value     float64
			Threshold float64
		}{
			Labels: map[string]string{
				"service.name": "api", "service.instance.id": "node-1",
			},
			Value: 1, Threshold: 1,
		}), value)
	}
}
