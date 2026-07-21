package prometheus

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRuleFormats(t *testing.T) {
	t.Parallel()

	input := []byte(`groups:
- name: plain
  interval: 30s
  query_offset: 5m
  limit: 25
  labels:
    cluster: production
    severity: warning
  rules:
  - alert: AlwaysOn
    expr: 1
    for: 2m
    labels:
      severity: warning
---
apiVersion: v1
kind: List
items:
- apiVersion: monitoring.coreos.com/v1
  kind: PrometheusRule
  spec:
    groups:
    - name: operator
      rules:
      - record: job:http_requests:rate5m
        expr: sum(rate(http_requests_total[5m])) by (job)
`)

	rules, err := Parse(input, "rules.yaml")
	require.NoError(t, err)
	require.Len(t, rules.Groups, 2)
	assert.Equal(t, "plain", rules.Groups[0].Name)
	assert.Equal(t, "30s", rules.Groups[0].Interval)
	assert.Equal(t, "5m", rules.Groups[0].QueryOffset)
	assert.Equal(t, 25, rules.Groups[0].Limit)
	assert.Equal(t, map[string]string{"cluster": "production", "severity": "warning"}, rules.Groups[0].Labels)
	require.Len(t, rules.Groups[0].Rules, 1)
	assert.Equal(t, "1", rules.Groups[0].Rules[0].Expression)
	assert.True(t, rules.Groups[0].Rules[0].IsAlerting())
	assert.Equal(t, "/documents/0/groups/0/rules/0", rules.Groups[0].Rules[0].SourcePath)
	assert.True(t, rules.Groups[1].Rules[0].IsRecording())
}

func TestParsePreservesPrometheusNoopRuleFields(t *testing.T) {
	t.Parallel()

	rules, err := Parse([]byte(`groups:
- name: noops
  interval: 0s
  limit: -1
  rules:
  - alert: Immediate
    expr: up == 0
    for: 0s
    keep_firing_for: 0s
  - record: job:up:sum
    expr: sum(up)
    for: 0s
    keep_firing_for: 0s
`), "noops.yaml")
	require.NoError(t, err)
	require.Len(t, rules.Groups, 1)
	assert.Equal(t, "0s", rules.Groups[0].Interval)
	assert.Equal(t, -1, rules.Groups[0].Limit)
	require.Len(t, rules.Groups[0].Rules, 2)
	for _, rule := range rules.Groups[0].Rules {
		assert.Equal(t, "0s", rule.For)
		assert.Equal(t, "0s", rule.KeepFiringFor)
	}
}

func TestParseAcceptsUTF8PrometheusNamesBeforeTargetCompatibilityPreflight(t *testing.T) {
	t.Parallel()

	rules, err := Parse([]byte(`groups:
- name: utf8
  rules:
  - alert: UnicodeLabel
    expr: up == 0
    labels:
      地域: 東京
    annotations:
      説明: 稼働停止
`), "utf8.yaml")
	require.NoError(t, err)
	require.Len(t, rules.Groups, 1)
	assert.Equal(t, "東京", rules.Groups[0].Rules[0].Labels["地域"])
	assert.Equal(t, "稼働停止", rules.Groups[0].Rules[0].Annotations["説明"])
}

func TestParseAcceptsPrometheusTemplateFunctionsThatTargetPreflightMustSanitize(t *testing.T) {
	t.Parallel()

	rules, err := Parse([]byte(`groups:
- name: templates
  rules:
  - alert: PrometheusTemplates
    expr: up == 0
    labels:
      graph: '{{ graphLink "up" }}'
      route: '{{ index $labels "job" }}'
      query_braces: '{{ printf "{{" | query }}'
    annotations:
      query: '{{ query "up" | first | value }}'
      query_selector: '{{ query "up{job=\"api\"}" | first | value }}'
      table: '{{ tableLink "up" }}'
      graph_braces: '{{ printf "}}" | graphLink }}'
      duration: '{{ parseDuration "5m" }}'
      host: '{{ $labels.instance | stripPort }}'
      domain: '{{ $labels.instance | stripDomain }}'
      external: '{{ $externalURL }}'
`), "prometheus-templates.yaml")
	require.NoError(t, err)
	require.Len(t, rules.Groups, 1)
	assert.Equal(t, `{{ query "up" | first | value }}`, rules.Groups[0].Rules[0].Annotations["query"])
	assert.Equal(t, `{{ query "up{job=\"api\"}" | first | value }}`, rules.Groups[0].Rules[0].Annotations["query_selector"])
	assert.Equal(t, `{{ index $labels "job" }}`, rules.Groups[0].Rules[0].Labels["route"])
}

func TestParseRejectsUnknownGroupAndRuleFields(t *testing.T) {
	t.Parallel()

	_, err := Parse([]byte(`groups:
- name: unknown-group
  evaluation_delay: 5m
  rules: []
`), "unknown-group.yaml")
	require.ErrorContains(t, err, `field evaluation_delay not found in type rulefmt.RuleGroup`)

	_, err = Parse([]byte(`apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: unknown-rule
spec:
  groups:
  - name: guarded
    rules:
    - alert: Unknown
      expr: up == 0
      vendor_extension: enabled
`), "unknown-rule.yaml")
	require.ErrorContains(t, err, `field vendor_extension not found in type rulefmt.Rule`)
}

func TestParseAllowsKubernetesEnvelopeFieldsWhileGuardingRuleSemantics(t *testing.T) {
	t.Parallel()

	rules, err := Parse([]byte(`apiVersion: v1
kind: List
metadata:
  resourceVersion: "7"
  managedFields:
  - manager: kubectl
items:
- apiVersion: monitoring.coreos.com/v1
  kind: PrometheusRule
  metadata:
    name: guarded
    namespace: monitoring
    uid: 36b8fd82-01e6-48c3-85ea-dca42ea93e3b
    creationTimestamp: "2026-07-20T00:00:00Z"
    labels:
      owner: platform
    annotations:
      kubectl.kubernetes.io/last-applied-configuration: '{}'
  spec:
    groups:
    - name: exact
      query_offset: 0s
      limit: 0
      labels:
        cluster: prod
      rules:
      - alert: NodeDown
        expr: up == 0
  status:
    conditions:
    - type: Available
      status: "True"
`), "list.yaml")
	require.NoError(t, err)
	require.Len(t, rules.Groups, 1)
	assert.Equal(t, "0s", rules.Groups[0].QueryOffset)
	assert.Equal(t, map[string]string{"cluster": "prod"}, rules.Groups[0].Labels)
}

func TestParseRejectsNestedLabelValues(t *testing.T) {
	t.Parallel()

	_, err := Parse([]byte(`groups:
- name: invalid
  rules:
  - alert: Invalid
    expr: up == 0
    labels:
      nested:
        value: no
`), "invalid.yaml")
	require.ErrorContains(t, err, "cannot unmarshal !!map into string")
}

func TestParseAppliesPrometheusRulefmtSemanticContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		message string
	}{
		{
			name: "empty group name",
			input: `groups:
- name: ""
  rules: []
`,
			message: "Groupname must not be empty",
		},
		{
			name: "duplicate group name",
			input: `groups:
- name: duplicate
  rules: []
- name: duplicate
  rules: []
`,
			message: `groupname: "duplicate" is repeated in the same file`,
		},
		{
			name: "duplicate group name inside prometheus rule",
			input: `apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: duplicate
spec:
  groups:
  - name: duplicate
    rules: []
  - name: duplicate
    rules: []
`,
			message: `groupname: "duplicate" is repeated in the same file`,
		},
		{
			name: "alert and record",
			input: `groups:
- name: invalid
  rules:
  - alert: Both
    record: both_total
    expr: up
`,
			message: "only one of 'record' and 'alert' must be set",
		},
		{
			name: "neither alert nor record",
			input: `groups:
- name: invalid
  rules:
  - expr: up
`,
			message: "one of 'record' or 'alert' must be set",
		},
		{
			name: "missing expression",
			input: `groups:
- name: invalid
  rules:
  - alert: MissingExpr
`,
			message: "field 'expr' must be set in rule",
		},
		{
			name: "invalid expression",
			input: `groups:
- name: invalid
  rules:
  - alert: BadExpr
    expr: up ===== 0
`,
			message: "could not parse expression",
		},
		{
			name: "invalid group interval",
			input: `groups:
- name: invalid
  interval: definitely-not-a-duration
  rules: []
`,
			message: "not a valid duration string",
		},
		{
			name: "invalid alert duration",
			input: `groups:
- name: invalid
  rules:
  - alert: BadDuration
    expr: up
    for: definitely-not-a-duration
`,
			message: "not a valid duration string",
		},
		{
			name: "recording annotation",
			input: `groups:
- name: invalid
  rules:
  - record: valid_metric
    expr: up
    annotations:
      summary: unsupported
`,
			message: "invalid field 'annotations' in recording rule",
		},
		{
			name: "recording for",
			input: `groups:
- name: invalid
  rules:
  - record: valid_metric
    expr: up
    for: 5m
`,
			message: "invalid field 'for' in recording rule",
		},
		{
			name: "recording keep firing",
			input: `groups:
- name: invalid
  rules:
  - record: valid_metric
    expr: up
    keep_firing_for: 5m
`,
			message: "invalid field 'keep_firing_for' in recording rule",
		},
		{
			name: "invalid recording name",
			input: `groups:
- name: invalid
  rules:
  - record: 'metric{name="wrong"}'
    expr: up
`,
			message: "braces present in the recording rule name",
		},
		{
			name: "metric name label",
			input: `groups:
- name: invalid
  rules:
  - alert: InvalidLabel
    expr: up
    labels:
      __name__: forbidden
`,
			message: "invalid label name: __name__",
		},
		{
			name: "group metric name label",
			input: `groups:
- name: invalid
  labels:
    __name__: forbidden
  rules: []
`,
			message: "invalid label name: __name__",
		},
		{
			name: "invalid template",
			input: `groups:
- name: invalid
  rules:
  - alert: InvalidTemplate
    expr: up
    annotations:
      summary: '{{ $label.instance }}'
`,
			message: "undefined variable \"$label\"",
		},
		{
			name: "invalid annotation name",
			input: `groups:
- name: invalid
  rules:
  - alert: InvalidAnnotation
    expr: up
    annotations:
      "": empty-name
`,
			message: "invalid annotation name",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse([]byte(test.input), test.name+".yaml")
			require.ErrorContains(t, err, test.message)
		})
	}
}

func TestParseRejectsDuplicateRuleMapKeys(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		input string
		path  string
		key   string
	}{
		{
			name: "group labels",
			input: `groups:
- name: duplicate
  labels:
    team: platform
    team: operations
  rules: []
`,
			path: "/documents/0/groups/0/labels", key: "team",
		},
		{
			name: "rule labels",
			input: `groups:
- name: duplicate
  rules:
  - alert: Duplicate
    expr: up
    labels:
      severity: warning
      severity: critical
`,
			path: "/documents/0/groups/0/rules/0/labels", key: "severity",
		},
		{
			name: "annotations",
			input: `groups:
- name: duplicate
  rules:
  - alert: Duplicate
    expr: up
    annotations:
      summary: first
      summary: second
`,
			path: "/documents/0/groups/0/rules/0/annotations", key: "summary",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse([]byte(test.input), test.name+".yaml")
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.path)
			assert.Contains(t, err.Error(), `duplicate YAML map key "`+test.key+`"`)
		})
	}
}

func TestParseRejectsDuplicateLiteralRuleFields(t *testing.T) {
	t.Parallel()

	_, err := Parse([]byte(`groups:
- name: duplicate
  rules:
  - alert: First
    alert: Second
    expr: up
`), "duplicate-field.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `mapping key "alert" already defined`)
}

func TestParseRejectsSemanticallyEmptyAndTypoOnlyEnvelopes(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		input   string
		message string
	}{
		{name: "empty input", input: "", message: "input is semantically empty"},
		{name: "null document", input: "---\n", message: "object is semantically empty"},
		{name: "empty groups", input: "groups: []\n", message: "groups collection is semantically empty"},
		{name: "plain typo", input: "gropus: []\n", message: "no supported semantic content"},
		{
			name: "typo-only second document",
			input: `groups:
- name: valid
  rules: []
---
gropus:
- name: silently-lost
  rules: []
`,
			message: "/documents/1: object has no supported semantic content",
		},
		{name: "plain extra field", input: "groups: []\nmetdata: {}\n", message: "unsupported field"},
		{
			name: "spec typo",
			input: `apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: typo
spec:
  gropus: []
`,
			message: "PrometheusRule spec contains unsupported field",
		},
		{
			name: "metadata only list item",
			input: `apiVersion: v1
kind: List
items:
- apiVersion: monitoring.coreos.com/v1
  kind: PrometheusRule
  metadata:
    name: empty
`,
			message: "prometheusRule resource has no spec",
		},
		{
			name: "plain list item",
			input: `apiVersion: v1
kind: List
items:
- groups:
  - name: hidden
    rules: []
`,
			message: "must be a PrometheusRule resource",
		},
		{
			name: "empty list",
			input: `apiVersion: v1
kind: List
items: []
`,
			message: "List resource is semantically empty",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse([]byte(test.input), test.name+".yaml")
			require.ErrorContains(t, err, test.message)
		})
	}
}

func TestParseScopesDuplicateGroupNamesPerUnwrappedObject(t *testing.T) {
	t.Parallel()

	rules, err := Parse([]byte(`groups:
- name: shared
  rules: []
---
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRuleList
metadata:
  resourceVersion: "42"
items:
- apiVersion: monitoring.coreos.com/v1
  kind: PrometheusRule
  metadata:
    name: first
    labels:
      owner: platform
    annotations:
      note: retained-envelope-metadata
  spec:
    groups:
    - name: shared
      rules: []
- apiVersion: monitoring.coreos.com/v1
  kind: PrometheusRule
  metadata:
    name: second
    finalizers:
    - example.com/finalizer
  spec:
    groups:
    - name: shared
      rules: []
`), "scoped.yaml")
	require.NoError(t, err)
	require.Len(t, rules.Groups, 3)
	assert.Equal(t, "/documents/0/groups/0", rules.Groups[0].SourcePath)
	assert.Equal(t, "/documents/1/items/0/spec/groups/0", rules.Groups[1].SourcePath)
	assert.Equal(t, "/documents/1/items/1/spec/groups/0", rules.Groups[2].SourcePath)
}

func TestParsePreservesPrometheusYAMLAliases(t *testing.T) {
	t.Parallel()

	rules, err := Parse([]byte(`groups:
- name: aliases
  rules:
  - &base
    alert: Base
    expr: &expression up == 0
    for: 5m
    labels:
      severity: &severity warning
  - <<: *base
    alert: Derived
    expr: *expression
    labels:
      severity: *severity
`), "aliases.yaml")
	require.NoError(t, err)
	require.Len(t, rules.Groups, 1)
	require.Len(t, rules.Groups[0].Rules, 2)
	assert.Equal(t, "5m", rules.Groups[0].Rules[1].For)
	assert.Equal(t, "warning", rules.Groups[0].Rules[1].Labels["severity"])
}

func TestParseMaterializesDocumentScopedAliasesDeclaredOutsideGroups(t *testing.T) {
	t.Parallel()

	rules, err := Parse([]byte(`apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: &group_name external-anchors
  labels: &common_labels
    job: static-owner
    team: platform
  annotations:
    signoz.io/expression: &expression 'up{job="api"} == 0'
    signoz.io/summary: &summary 'Job {{ $labels.job }} is down'
spec:
  groups:
  - name: *group_name
    labels:
      <<: *common_labels
      cluster: production
    rules:
    - alert: JobDown
      expr: *expression
      labels: *common_labels
      annotations:
        summary: *summary
`), "external-aliases.yaml")
	require.NoError(t, err)
	require.Len(t, rules.Groups, 1)
	assert.Equal(t, "external-anchors", rules.Groups[0].Name)
	assert.Equal(t, map[string]string{
		"cluster": "production", "job": "static-owner", "team": "platform",
	}, rules.Groups[0].Labels)
	require.Len(t, rules.Groups[0].Rules, 1)
	assert.Equal(t, `up{job="api"} == 0`, rules.Groups[0].Rules[0].Expression)
	assert.Equal(t, map[string]string{"job": "static-owner", "team": "platform"}, rules.Groups[0].Rules[0].Labels)
	assert.Equal(t, `Job {{ $labels.job }} is down`, rules.Groups[0].Rules[0].Annotations["summary"])
}

func TestParseRejectsRecursiveExternalAlias(t *testing.T) {
	t.Parallel()

	_, err := Parse([]byte(`apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  labels: &recursive
    owner: *recursive
spec:
  groups:
  - name: recursive
    rules:
    - alert: Recursive
      expr: up
      labels: *recursive
`), "recursive-alias.yaml")
	require.ErrorContains(t, err, "recursive YAML alias")
}
