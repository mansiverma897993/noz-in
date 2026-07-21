package transpile

import (
	"testing"
	"time"

	"github.com/mansiverma897993/signoz/internal/model"

	"github.com/stretchr/testify/assert"
)

func TestMaterializeSourceExpression(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{RateInterval: 5 * time.Minute})
	expression, missing := analyzer.MaterializeSourceExpression(
		`sum(rate(node_cpu_seconds_total{job=~"$job",instance="$node"}[$__rate_interval])) by ([[group]])`,
		map[string]string{"job": "node-exporter", "node": "source:9100", "group": "cpu"},
	)
	assert.Equal(t, `sum(rate(node_cpu_seconds_total{job=~"node-exporter",instance="source:9100"}[5m])) by (cpu)`, expression)
	assert.Empty(t, missing)
}

func TestMaterializeSourceQueryUsesQueryIntervalControls(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{Interval: time.Minute, Range: time.Hour})
	expression, missing := analyzer.MaterializeSourceQuery(model.Query{
		Expression: `sum(increase(requests_total[$__interval]))`,
		Interval:   "30m", IntervalFactor: 2, MaxDataPoints: 1,
	}, nil)

	assert.Equal(t, `sum(increase(requests_total[2h]))`, expression)
	assert.Empty(t, missing)
}

func TestMaterializeSourceQueryUsesExactGrafanaTimeGlobalsForWindow(t *testing.T) {
	t.Parallel()

	analyzer := NewAnalyzer(Options{})
	expression, missing := analyzer.MaterializeSourceQueryForWindow(
		model.Query{Expression: `vector($__from) + vector(${__to}) + vector([[__from]])`},
		nil,
		nil,
		time.UnixMilli(1_234),
		time.UnixMilli(9_876),
	)

	assert.Equal(t, `vector(1234) + vector(9876) + vector(1234)`, expression)
	assert.Empty(t, missing)
}

func TestMaterializeSourceQueryDoesNotGuessTimeGlobalsWithoutWindow(t *testing.T) {
	t.Parallel()

	expression, missing := NewAnalyzer(Options{}).MaterializeSourceQuery(
		model.Query{Expression: `vector($__from) + vector(${__to})`}, nil,
	)

	assert.Equal(t, `vector($__from) + vector(${__to})`, expression)
	assert.Equal(t, []string{"__from", "__to"}, missing)
}

func TestMaterializeVariablesReportsMissingNames(t *testing.T) {
	t.Parallel()

	expression, missing := MaterializeVariables(`up{job="$job",instance="${node:regex}"} + $factor`, map[string]string{"job": "api"})
	assert.Equal(t, `up{job="api",instance="${node:regex}"} + $factor`, expression)
	assert.Equal(t, []string{"factor", "node"}, missing)
}

func TestMaterializeVariablesUsesPinnedPrometheusMultiValueForms(t *testing.T) {
	t.Parallel()

	expression, missing := MaterializeVariablesWithMulti(
		`up{a=~"$job",b=~"${job:regex}",c=~"${job:pipe}",d=~"[[job]]"}`,
		nil,
		map[string][]string{"job": {"api", "worker"}},
	)
	assert.Equal(t, `up{a=~"(api|worker)",b=~"(api|worker)",c=~"api|worker",d=~"(api|worker)"}`, expression)
	assert.Empty(t, missing)

	expression, missing = MaterializeVariablesWithMulti(
		`up{job=~"$job"}`,
		nil,
		map[string][]string{"job": {"api"}},
	)
	assert.Equal(t, `up{job=~"api"}`, expression)
	assert.Empty(t, missing)
}

func TestMaterializeVariablesLeavesFeatureToggleDependentMultiValuesUnresolved(t *testing.T) {
	t.Parallel()

	expression, missing := MaterializeVariablesWithMulti(
		`up{job=~"$job"}`,
		nil,
		map[string][]string{"job": {"api's", "worker"}},
	)
	assert.Equal(t, `up{job=~"$job"}`, expression)
	assert.Equal(t, []string{"job"}, missing)

	expression, missing = MaterializeVariablesWithMulti(
		`up{job=~"${job:pipe}"}`,
		nil,
		map[string][]string{"job": {"api.prod", "worker|canary"}},
	)
	assert.Equal(t, `up{job=~"api.prod|worker|canary"}`, expression)
	assert.Empty(t, missing, "pipe is a deliberate raw join in both pinned runtimes")
}

func TestTargetRawVariableSubstitutionExactMatchesPinnedRuntimeBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		expression string
		values     []string
		multi      bool
		want       bool
	}{
		{name: "plain scalar", expression: `up{host=~"$host"}`, values: []string{"api.prod"}, want: true},
		{name: "scalar backslash", expression: `up{host=~"$host"}`, values: []string{`api\west`}},
		{name: "scalar quote", expression: `up{host=~"${host}"}`, values: []string{`api"west`}},
		{name: "safe multi", expression: `up{host=~"$host"}`, values: []string{"api", "worker"}, multi: true, want: true},
		{name: "multi equality changes literal", expression: `up{host="$host"}`, values: []string{"api", "worker"}, multi: true},
		{name: "multi non-matcher changes grammar", expression: `scalar($host)`, values: []string{"api", "worker"}, multi: true},
		{name: "multi regex metachar", expression: `up{host=~"$host"}`, values: []string{"api.prod", "worker"}, multi: true},
		{name: "explicit regex metachar", expression: `up{host=~"${host:regex}"}`, values: []string{"api.prod"}},
		{name: "raw pipe metachar", expression: `up{host=~"${host:pipe}"}`, values: []string{"api.prod", "worker|canary"}, multi: true, want: true},
		{name: "pipe outside matcher unsupported", expression: `sum(${host:pipe})`, values: []string{"api.prod"}},
		{name: "other variable ignored", expression: `up{host=~"$other"}`, values: []string{`api\west`}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, test.want, TargetRawVariableSubstitutionExact(
				test.expression, "host", test.values, test.multi, []string{"host", "other"},
			))
		})
	}
}

func TestTargetRawVariableSubstitutionExactRejectsSecondPassRendering(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		vars  []string
		exact bool
	}{
		{name: "go template action", value: `{{.SIGNOZ_START_TIME}}`, vars: []string{"env"}},
		{name: "go template close delimiter", value: `literal }}`, vars: []string{"env"}},
		{name: "shorter peer recursively replaced", value: `$x`, vars: []string{"longer", "x"}},
		{name: "peer prefix recursively replaced", value: `$x_suffix`, vars: []string{"longer", "x"}},
		{name: "same length peer ordering unproven", value: `$xy`, vars: []string{"ab", "xy"}},
		{name: "longer peer already processed", value: `$longer`, vars: []string{"x", "longer"}, exact: true},
		{name: "self reference already processed", value: `$env`, vars: []string{"env"}, exact: true},
		{name: "legacy peer recursively replaced", value: `[[x]]`, vars: []string{"env", "x"}},
		{name: "longer legacy peer already processed", value: `[[longer]]`, vars: []string{"x", "longer"}, exact: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, test.exact, TargetRawVariableSubstitutionExact(
				`up{job="$env"}`, "env", []string{test.value}, false, test.vars,
			))
		})
	}
}

func TestVariableNamesIsSyntaxAware(t *testing.T) {
	t.Parallel()

	names := VariableNames(`label_replace(up{job="${job}",instance="[[node:regex]]",zone="${host.value}"}, "dst", "${1}", "src", "$pattern") + ${factor:csv} + vector($SIGNOZ_START_TIME)`)

	assert.Equal(t, []string{"factor", "host", "job", "node", "pattern"}, names)
}

func TestTargetPromQLRuntimeSubstitutionExactMatchesPinnedRenderer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		source    string
		target    string
		variables []string
		wantExact bool
	}{
		{name: "exact dashboard variable", source: `up{env="$env"}`, target: `up{env="$env"}`, variables: []string{"env"}, wantExact: true},
		{name: "longer exact key wins before prefix", source: `up{env="$environment"}`, target: `up{env="$environment"}`, variables: []string{"env", "environment"}, wantExact: true},
		{name: "short key corrupts unknown longer token", source: `up{env="$environment"}`, target: `up{env="$environment"}`, variables: []string{"env"}},
		{name: "go template action parse", source: `up{env="{{literal}}"}`, target: `up{env="{{literal}}"}`},
		{name: "reserved source literal", source: `label_replace(up,"dst","$start_timestamp","src","(.*)")`, target: `label_replace(up,"dst","$start_timestamp","src","(.*)")`},
		{name: "reserved key corrupts longer token", source: `up{env="$start_timestamp_suffix"}`, target: `up{env="$start_timestamp_suffix"}`},
		{name: "mapped start global", source: `vector($__from)`, target: `vector($SIGNOZ_START_TIME)`, wantExact: true},
		{name: "mapped end global legacy form", source: `vector([[__to]])`, target: `vector($SIGNOZ_END_TIME)`, wantExact: true},
		{name: "mapped and raw reserved occurrence", source: `vector($__from) + label_replace(up,"dst","$SIGNOZ_START_TIME","src","(.*)")`, target: `vector($SIGNOZ_START_TIME) + label_replace(up,"dst","$SIGNOZ_START_TIME","src","(.*)")`},
		{name: "PromQL numeric capture remains literal", source: `label_replace(up,"dst","$1","src","(.*)")`, target: `label_replace(up,"dst","$1","src","(.*)")`, wantExact: true},
		{name: "undefined plain dollar remains literal", source: `up{env="$missing"}`, target: `up{env="$missing"}`, wantExact: true},
		{name: "undefined braced reference was normalized", source: `up{env="${missing}"}`, target: `up{env="$missing"}`},
		{name: "undefined legacy reference was normalized", source: `up{env="[[missing]]"}`, target: `up{env="$missing"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, test.wantExact, TargetPromQLRuntimeSubstitutionExact(
				test.source, test.target, test.variables,
			))
		})
	}
}

func TestTargetDynamicAllMatcherRemovalExactRequiresPositiveRegexMatchers(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		expression string
		want       bool
	}{
		{expression: `up{job=~"$job"}`, want: true},
		{expression: `up{job=~"${job:regex}",instance=~"[[job]]"}`, want: true},
		{expression: `up{job!~"$job"}`},
		{expression: `up{job="$job"}`},
		{expression: `up{job=~"prefix-$job"}`},
		{expression: `scalar($job)`},
		{expression: `up`},
	} {
		assert.Equal(t, test.want, TargetDynamicAllMatcherRemovalExact(test.expression, "job"), test.expression)
	}
}
