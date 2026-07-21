package grafana

import (
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/mansiverma897993/signoz/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseModernDashboard(t *testing.T) {
	t.Parallel()

	dashboard, err := ParseFile("testdata/modern.json")
	require.NoError(t, err)

	assert.Equal(t, "Service overview", dashboard.Title)
	assert.Equal(t, 41, dashboard.Source.SchemaVersion)
	require.Len(t, dashboard.Panels, 3)
	assert.Equal(t, model.PanelKindGraph, dashboard.Panels[0].Kind)
	assert.Equal(t, "percentunit", dashboard.Panels[0].Unit)
	assert.Equal(t, "/panels/1/panels/0", dashboard.Panels[2].SourcePath)
	assert.Equal(t, model.PanelKindValue, dashboard.Panels[2].Kind)
	require.Len(t, dashboard.Panels[0].Queries, 2)
	assert.Equal(t, "prometheus", dashboard.Panels[0].Queries[0].Datasource.Type)

	require.Len(t, dashboard.Variables, 2)
	assert.Equal(t, "label_values(up, job)", dashboard.Variables[1].Query)
	assert.Equal(t, []string{"api", "worker"}, dashboard.Variables[1].Current)
}

func TestParseRecordsExactSourceBytesSHA256(t *testing.T) {
	t.Parallel()

	contents := " \n{\"title\":\"Exact bytes\",\"schemaVersion\":41}\n\t"
	dashboard, err := Parse(strings.NewReader(contents), "exact.json")
	require.NoError(t, err)

	digest := sha256.Sum256([]byte(contents))
	assert.Equal(t, fmt.Sprintf("%x", digest[:]), dashboard.Source.SHA256)
	assert.NotEqual(t, dashboard.Source.SHA256, func() string {
		normalized := sha256.Sum256([]byte(strings.TrimSpace(contents)))
		return fmt.Sprintf("%x", normalized[:])
	}())
}

func TestParseRejectsAmbiguousJSONKeysAtEveryDepth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		wantError string
	}{
		{
			name:      "dashboard duplicate before type error",
			input:     `{"title":"first","title":7}`,
			wantError: `duplicate JSON object key "title" at /title`,
		},
		{
			name:      "dashboard case variant",
			input:     `{"title":"first","TITLE":"second"}`,
			wantError: `case-insensitive JSON object key collision "TITLE" with "title" at /TITLE`,
		},
		{
			name:      "panel case variant",
			input:     `{"title":"Panel","panels":[{"type":"timeseries","TYPE":"stat"}]}`,
			wantError: `case-insensitive JSON object key collision "TYPE" with "type" at /panels/0/TYPE`,
		},
		{
			name:      "target duplicate",
			input:     `{"title":"Target","panels":[{"targets":[{"refId":"A","refId":false}]}]}`,
			wantError: `duplicate JSON object key "refId" at /panels/0/targets/0/refId`,
		},
		{
			name:      "target case variant",
			input:     `{"title":"Target","panels":[{"targets":[{"refId":"A","REFID":"B"}]}]}`,
			wantError: `case-insensitive JSON object key collision "REFID" with "refId" at /panels/0/targets/0/REFID`,
		},
		{
			name:      "nested typed case variant",
			input:     `{"title":"Defaults","panels":[{"fieldConfig":{"defaults":{"unit":"s","UNIT":"ms"}}}]}`,
			wantError: `case-insensitive JSON object key collision "UNIT" with "unit" at /panels/0/fieldConfig/defaults/UNIT`,
		},
		{
			name:      "unknown nested duplicate with escaped pointer",
			input:     `{"title":"Nested","panels":[{"transformations":[{"id":"organize","options":{"mode/name~":"one","mode/name~":"two"}}]}]}`,
			wantError: `duplicate JSON object key "mode/name~" at /panels/0/transformations/0/options/mode~1name~0`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(strings.NewReader(test.input), "ambiguous.json")
			require.ErrorContains(t, err, test.wantError)
		})
	}
}

func TestParseAllowsCaseDistinctKeysWhereJSONIsDecodedAsAMap(t *testing.T) {
	t.Parallel()

	dashboard, err := Parse(strings.NewReader(`{
		"title":"Series names","panels":[{"transformations":[{
			"id":"filterFieldsByName","options":{"excludeByName":{"Line":true,"line":false}}
		}]}]
	}`), "series-names.json")
	require.NoError(t, err)
	require.Len(t, dashboard.Panels, 1)
	require.Len(t, dashboard.Panels[0].SourceFeatures, 1)
	assert.Equal(t, `{"excludeByName":{"Line":true,"line":false}}`, dashboard.Panels[0].SourceFeatures[0].Detail)
}

func TestParseRetainsDashboardSizeCap(t *testing.T) {
	t.Parallel()

	reader := &fixedByteReader{remaining: maxDashboardSize + 1, value: ' '}
	_, err := Parse(reader, "oversized.json")
	require.ErrorContains(t, err, fmt.Sprintf("exceeds %d bytes", maxDashboardSize))
}

type fixedByteReader struct {
	remaining int
	value     byte
}

func (reader *fixedByteReader) Read(destination []byte) (int, error) {
	if reader.remaining == 0 {
		return 0, io.EOF
	}
	count := min(len(destination), reader.remaining)
	for index := range destination[:count] {
		destination[index] = reader.value
	}
	reader.remaining -= count
	return count, nil
}

func TestParsePreservesVariableAllValueAndCurrent(t *testing.T) {
	t.Parallel()

	dashboard, err := Parse(strings.NewReader(`{
		"title":"Variable evidence","templating":{"list":[{
			"name":"instance","type":"query","query":"label_values(instance)",
			"includeAll":true,"allValue":" .+ ",
			"current":{"text":"All","value":["$__all"]}
		}]}
	}`), "variable-evidence.json")
	require.NoError(t, err)
	require.Len(t, dashboard.Variables, 1)

	assert.Equal(t, " .+ ", dashboard.Variables[0].AllValue)
	assert.Equal(t, []string{"$__all"}, dashboard.Variables[0].Current)
}

func TestParseLegacyRows(t *testing.T) {
	t.Parallel()

	dashboard, err := ParseFile("testdata/legacy.json")
	require.NoError(t, err)

	require.Len(t, dashboard.Panels, 3)
	assert.Equal(t, model.PanelKindRow, dashboard.Panels[0].Kind)
	assert.Equal(t, "Headlines", dashboard.Panels[0].Title)
	assert.Equal(t, model.Grid{X: 0, Y: 0, W: 24, H: 1}, dashboard.Panels[0].Grid)
	assert.Equal(t, model.Grid{X: 0, Y: 1, W: 12, H: 9}, dashboard.Panels[1].Grid)
	assert.Equal(t, model.Grid{X: 0, Y: 10, W: 24, H: 9}, dashboard.Panels[2].Grid)
	assert.Equal(t, "/rows/0/panels/1", dashboard.Panels[2].SourcePath)
}

func TestParseModernGrafanaExpressionTarget(t *testing.T) {
	t.Parallel()

	dashboard, err := Parse(strings.NewReader(`{
		"title":"Expressions","schemaVersion":41,"panels":[{
			"id":1,"title":"Ratio","type":"timeseries","targets":[
				{"refId":"A","expr":"sum(errors_total)","hide":true,"datasource":{"type":"prometheus","uid":"prom"}},
				{"refId":"B","expr":"sum(requests_total)","hide":true,"datasource":{"type":"prometheus","uid":"prom"}},
				{"refId":"C","type":"math","expression":"$A / $B","datasource":{"type":"__expr__","uid":"__expr__"}}
			]
		}]
	}`), "expressions.json")
	require.NoError(t, err)
	require.Len(t, dashboard.Panels, 1)
	require.Len(t, dashboard.Panels[0].Queries, 3)
	expression := dashboard.Panels[0].Queries[2]
	assert.Equal(t, "$A / $B", expression.Expression)
	assert.Equal(t, "math", expression.QueryType)
	assert.Equal(t, "__expr__", expression.Datasource.Type)
	assert.Equal(t, model.SourceInventory{
		Captured: true, Panels: 1, Queries: 3,
	}, dashboard.SourceInventory)
}

func TestParsePreservesTargetQueryFormat(t *testing.T) {
	t.Parallel()

	dashboard, err := Parse(strings.NewReader(`{
		"title":"Format","panels":[{
			"title":"Heatmap","type":"heatmap",
			"targets":[{"refId":"A","expr":"rate(requests_total[5m])","format":" heatmap "}]
		}]
	}`), "format.json")
	require.NoError(t, err)
	require.Len(t, dashboard.Panels, 1)
	require.Len(t, dashboard.Panels[0].Queries, 1)
	assert.Equal(t, " heatmap ", dashboard.Panels[0].Queries[0].Format)
}

func TestParsePreservesPanelAndTargetIntervalControls(t *testing.T) {
	t.Parallel()

	dashboard, err := Parse(strings.NewReader(`{
		"title":"Cadence","panels":[{
			"title":"Rate","type":"timeseries","interval":"15m","maxDataPoints":12,
			"targets":[{"refId":"A","expr":"sum(rate(requests_total[$__interval]))","interval":"30m","intervalFactor":2,"maxDataPoints":1}]
		}]
	}`), "cadence.json")
	require.NoError(t, err)
	require.Len(t, dashboard.Panels[0].Queries, 1)

	query := dashboard.Panels[0].Queries[0]
	assert.Equal(t, "30m", query.Interval)
	assert.Equal(t, 2, query.IntervalFactor)
	assert.Equal(t, 1, query.MaxDataPoints)
}

func TestParseInventoriesUnmappedVisualizationSemantics(t *testing.T) {
	t.Parallel()

	dashboard, err := Parse(strings.NewReader(`{
		"title":"Visual semantics","panels":[{
			"id":1,"title":"Gauge","type":"gauge","stack":true,
			"fieldConfig":{"defaults":{
				"unit":"percent","min":0,"max":100,"decimals":1,
				"mappings":[{"type":"value"}],"custom":{"stacking":{"mode":"normal"}}
			}},
			"options":{"orientation":"horizontal"},
			"targets":[{"refId":"A","expr":"up"}]
		}]
	}`), "visual.json")
	require.NoError(t, err)
	require.Len(t, dashboard.Panels, 1)
	panel := dashboard.Panels[0]
	assert.Equal(t, "gauge", panel.SourceType)
	assert.Equal(t, model.PanelKindValue, panel.Kind)
	require.Len(t, panel.SourceFeatures, 8)
	assert.Equal(t, model.ReasonVisualizationDowngrade, panel.SourceFeatures[0].Reason)
	for _, feature := range panel.SourceFeatures[1:] {
		assert.Equal(t, model.ReasonUnmappedVisualization, feature.Reason)
	}
	assert.Equal(t, model.SourceInventory{
		Captured: true, Panels: 1, Queries: 1, SourceFeatures: 8,
	}, dashboard.SourceInventory)
	assert.Equal(t, []string{
		"/panels/0/type",
		"/panels/0/fieldConfig/defaults/custom",
		"/panels/0/fieldConfig/defaults/decimals",
		"/panels/0/fieldConfig/defaults/mappings",
		"/panels/0/fieldConfig/defaults/max",
		"/panels/0/fieldConfig/defaults/min",
		"/panels/0/options/orientation",
		"/panels/0/stack",
	}, featurePaths(panel.SourceFeatures))
}

func TestParseAcceptsGrafanaFlexibleNumericKnobs(t *testing.T) {
	t.Parallel()

	dashboard, err := Parse(strings.NewReader(`{
		"title":"Numbers","panels":[{
			"title":"Queries","type":"timeseries","span":"6","maxDataPoints":"",
			"targets":[
				{"refId":"A","expr":"up","maxDataPoints":null},
				{"refId":"B","expr":"up","maxDataPoints":"123"},
				{"refId":"C","expr":"up","maxDataPoints":456,"intervalFactor":"2"},
				{"refId":"D","expr":"up","maxDataPoints":""}
			]
		}]
	}`), "numbers.json")
	require.NoError(t, err)
	queries := dashboard.Panels[0].Queries
	require.Len(t, queries, 4)
	assert.Equal(t, []int{0, 123, 456, 0}, []int{
		queries[0].MaxDataPoints, queries[1].MaxDataPoints, queries[2].MaxDataPoints, queries[3].MaxDataPoints,
	})
	assert.Equal(t, 2, queries[2].IntervalFactor)

	_, err = Parse(strings.NewReader(`{
		"title":"Invalid number","panels":[{"type":"timeseries","targets":[{"maxDataPoints":"many"}]}]
	}`), "bad-number.json")
	require.ErrorContains(t, err, "expected a finite number")
}

func TestParseAcceptsAndNormalizesGrafanaTargetStep(t *testing.T) {
	t.Parallel()

	dashboard, err := Parse(strings.NewReader(`{
		"title":"Steps","panels":[{"type":"timeseries","targets":[
			{"refId":"A","expr":"up","step":null},
			{"refId":"B","expr":"up","step":"15"},
			{"refId":"C","expr":"up","step":15.25},
			{"refId":"D","expr":"up","step":""},
			{"refId":"E","expr":"up","step":-4},
			{"refId":"F","expr":"up","step":0}
		]}]
	}`), "steps.json")
	require.NoError(t, err)
	require.Len(t, dashboard.Panels[0].Queries, 6)
	assert.Equal(t, []int{0, 15, 16, 0, 0, 0}, []int{
		dashboard.Panels[0].Queries[0].Step,
		dashboard.Panels[0].Queries[1].Step,
		dashboard.Panels[0].Queries[2].Step,
		dashboard.Panels[0].Queries[3].Step,
		dashboard.Panels[0].Queries[4].Step,
		dashboard.Panels[0].Queries[5].Step,
	})
	assert.Equal(t, []string{"null", `"15"`, "15.25", `""`, "-4", "0"}, []string{
		dashboard.Panels[0].Queries[0].SourceFeatures[0].Detail,
		dashboard.Panels[0].Queries[1].SourceFeatures[0].Detail,
		dashboard.Panels[0].Queries[2].SourceFeatures[0].Detail,
		dashboard.Panels[0].Queries[3].SourceFeatures[0].Detail,
		dashboard.Panels[0].Queries[4].SourceFeatures[0].Detail,
		dashboard.Panels[0].Queries[5].SourceFeatures[0].Detail,
	})
	assert.Equal(t, []model.ReasonCode{
		model.ReasonUnmappedQueryConfig,
		model.ReasonGrafanaIntervalControl,
		model.ReasonGrafanaIntervalControl,
		model.ReasonUnmappedQueryConfig,
		model.ReasonUnmappedQueryConfig,
		model.ReasonUnmappedQueryConfig,
	}, []model.ReasonCode{
		dashboard.Panels[0].Queries[0].SourceFeatures[0].Reason,
		dashboard.Panels[0].Queries[1].SourceFeatures[0].Reason,
		dashboard.Panels[0].Queries[2].SourceFeatures[0].Reason,
		dashboard.Panels[0].Queries[3].SourceFeatures[0].Reason,
		dashboard.Panels[0].Queries[4].SourceFeatures[0].Reason,
		dashboard.Panels[0].Queries[5].SourceFeatures[0].Reason,
	})
	assert.Equal(t, 6, dashboard.SourceInventory.SourceFeatures)
	assert.Equal(t, dashboard.SourceInventory, normalizedInventory(dashboard))

	for _, value := range []string{`"many"`, `"NaN"`, `"Inf"`, `{}`} {
		_, err := Parse(strings.NewReader(fmt.Sprintf(`{
			"title":"Invalid step","panels":[{"type":"timeseries","targets":[{"expr":"up","step":%s}]}]
		}`, value)), "bad-step.json")
		require.ErrorContains(t, err, "expected a finite number", value)
	}
}

func TestParseAccountsForTargetRangeAndExemplarConfiguration(t *testing.T) {
	t.Parallel()

	dashboard, err := Parse(strings.NewReader(`{
		"title":"Target config","panels":[{"type":"timeseries","targets":[
			{"expr":"up","instant":true,"range":false,"exemplar":true,"step":"30","intervalFactor":2},
			{"expr":"up","range":true,"exemplar":false},
			{"expr":"up"}
		]}]
	}`), "target-config.json")
	require.NoError(t, err)
	require.Len(t, dashboard.Panels, 1)
	require.Len(t, dashboard.Panels[0].Queries, 3)
	queries := dashboard.Panels[0].Queries

	assert.Equal(t, []string{"A", "B", "C"}, []string{queries[0].RefID, queries[1].RefID, queries[2].RefID})
	assert.True(t, queries[0].RefIDNormalized)
	assert.True(t, queries[1].RefIDNormalized)
	assert.True(t, queries[2].RefIDNormalized)
	assert.True(t, queries[0].Instant)
	assert.Equal(t, 30, queries[0].Step)
	assert.Equal(t, 2, queries[0].IntervalFactor)

	require.Len(t, queries[0].SourceFeatures, 3)
	assert.Equal(t, []string{
		"/panels/0/targets/0/step",
		"/panels/0/targets/0/range",
		"/panels/0/targets/0/exemplar",
	}, featurePaths(queries[0].SourceFeatures))
	assert.Equal(t, []string{`"30"`, "false", "true"}, []string{
		queries[0].SourceFeatures[0].Detail,
		queries[0].SourceFeatures[1].Detail,
		queries[0].SourceFeatures[2].Detail,
	})
	require.Len(t, queries[1].SourceFeatures, 2)
	assert.Equal(t, []string{
		"/panels/0/targets/1/range",
		"/panels/0/targets/1/exemplar",
	}, featurePaths(queries[1].SourceFeatures))
	assert.Equal(t, []string{"true", "false"}, []string{
		queries[1].SourceFeatures[0].Detail,
		queries[1].SourceFeatures[1].Detail,
	})
	assert.Empty(t, queries[2].SourceFeatures)
	assert.Equal(t, model.ReasonGrafanaIntervalControl, queries[0].SourceFeatures[0].Reason)
	for _, feature := range queries[0].SourceFeatures[1:] {
		assert.Equal(t, model.ReasonUnmappedQueryConfig, feature.Reason)
	}
	for _, feature := range queries[1].SourceFeatures {
		assert.Equal(t, model.ReasonUnmappedQueryConfig, feature.Reason)
	}
	assert.Equal(t, model.SourceInventory{
		Captured: true, Panels: 1, Queries: 3, SourceFeatures: 5,
	}, dashboard.SourceInventory)
	assert.Equal(t, dashboard.SourceInventory, normalizedInventory(dashboard))
}

func featurePaths(features []model.SourceFeature) []string {
	paths := make([]string, 0, len(features))
	for _, feature := range features {
		paths = append(paths, feature.SourcePath)
	}
	return paths
}

func TestParseNormalizesMissingAndDuplicateRefIDsWithoutStealingExplicitNames(t *testing.T) {
	t.Parallel()

	dashboard, err := Parse(strings.NewReader(`{
		"title":"Refs","panels":[{"title":"Queries","type":"timeseries","targets":[
			{"expr":"up"},{"refId":"A","expr":"process_start_time_seconds"},{"refId":"A","expr":"node_load1"},{"refId":"CPU load","expr":"node_load5"}
		]}]
	}`), "refs.json")
	require.NoError(t, err)
	queries := dashboard.Panels[0].Queries
	require.Len(t, queries, 4)
	assert.Equal(t, []string{"B", "A", "C", "D"}, []string{queries[0].RefID, queries[1].RefID, queries[2].RefID, queries[3].RefID})
	assert.True(t, queries[0].RefIDNormalized)
	assert.False(t, queries[1].RefIDNormalized)
	assert.True(t, queries[2].RefIDNormalized)
	assert.True(t, queries[3].RefIDNormalized)
}

func TestParseAccountsForDashboardVariablesInDisplayText(t *testing.T) {
	t.Parallel()

	dashboard, err := Parse(strings.NewReader(`{
		"title":"Display variables",
		"panels":[{
			"title":"CPU for ${host.value}","description":"**Runbook** for $environment","type":"timeseries",
			"targets":[
				{"refId":"A","expr":"up","legendFormat":"env=[[environment:regex]]"},
				{"refId":"B","expr":"up","legendFormat":"__auto"},
				{"refId":"C","expr":"up","legendFormat":"{{optional_label}}"}
			]
		}]
	}`), "display-variables.json")
	require.NoError(t, err)
	require.Len(t, dashboard.Panels, 1)
	require.Len(t, dashboard.Panels[0].Queries, 3)

	assertFeatureEvidence(t, dashboard.Panels[0].SourceFeatures, map[string]struct {
		detail string
		reason model.ReasonCode
	}{
		"/panels/0/title":       {detail: "CPU for ${host.value}", reason: model.ReasonGrafanaVariablePanelTitle},
		"/panels/0/description": {detail: "**Runbook** for $environment", reason: model.ReasonGrafanaPanelDescription},
	})
	assertFeatureEvidence(t, dashboard.Panels[0].Queries[0].SourceFeatures, map[string]struct {
		detail string
		reason model.ReasonCode
	}{
		"/panels/0/targets/0/legendFormat": {detail: "env=[[environment:regex]]", reason: model.ReasonGrafanaVariableLegend},
	})
	assertFeatureEvidence(t, dashboard.Panels[0].Queries[1].SourceFeatures, map[string]struct {
		detail string
		reason model.ReasonCode
	}{
		"/panels/0/targets/1/legendFormat": {detail: "__auto", reason: model.ReasonGrafanaVariableLegend},
	})
	assertFeatureEvidence(t, dashboard.Panels[0].Queries[2].SourceFeatures, map[string]struct {
		detail string
		reason model.ReasonCode
	}{
		"/panels/0/targets/2/legendFormat": {detail: "{{optional_label}}", reason: model.ReasonGrafanaVariableLegend},
	})
	assert.Equal(t, 5, dashboard.SourceInventory.SourceFeatures)
	assert.Equal(t, dashboard.SourceInventory, normalizedInventory(dashboard))
}

func TestParseDoesNotLetNonCanonicalRefIDsStealLaterExactRefs(t *testing.T) {
	t.Parallel()

	dashboard, err := Parse(strings.NewReader(`{
		"title":"Exact refs win","panels":[{"targets":[
			{"refId":" A ","expr":"up"},
			{"refId":"A","expr":"node_load1"},
			{"refId":" B ","expr":"node_load5"},
			{"refId":"B","expr":"process_start_time_seconds"}
		]}]
	}`), "exact-refs.json")
	require.NoError(t, err)
	queries := dashboard.Panels[0].Queries
	require.Len(t, queries, 4)
	assert.Equal(t, []string{"C", "A", "D", "B"}, []string{
		queries[0].RefID, queries[1].RefID, queries[2].RefID, queries[3].RefID,
	})
	assert.Equal(t, []bool{true, false, true, false}, []bool{
		queries[0].RefIDNormalized, queries[1].RefIDNormalized,
		queries[2].RefIDNormalized, queries[3].RefIDNormalized,
	})
}

func TestParseAccountsForUnsupportedSourceFeatures(t *testing.T) {
	t.Parallel()

	dashboard, err := Parse(strings.NewReader(`{
		"title":"Features","links":[{"title":"runbook"}],
		"annotations":{"list":[{"name":"deploys","expr":"changes(deploy_total[5m])"}]},
		"panels":[{"id":1,"title":"Shared","type":"timeseries",
			"alert":{"name":"legacy"},"links":[{"title":"panel runbook"}],
			"fieldConfig":{"defaults":{"thresholds":{"steps":[{"value":80}]}},"overrides":[{"matcher":{"id":"byName"}}]},
			"libraryPanel":{"uid":"shared-cpu"}
		}]
	}`), "features.json")
	require.NoError(t, err)
	require.Len(t, dashboard.SourceFeatures, 2)
	assert.Equal(t, "/annotations/list/0", dashboard.SourceFeatures[0].SourcePath)
	assert.Equal(t, "/links/0", dashboard.SourceFeatures[1].SourcePath)
	require.Len(t, dashboard.Panels, 1)
	require.Len(t, dashboard.Panels[0].SourceFeatures, 5)
	assert.Equal(t, model.ReasonLibraryPanel, dashboard.Panels[0].SourceFeatures[4].Reason)
	assert.Equal(t, model.SourceInventory{
		Captured: true, Panels: 1, SourceFeatures: 7,
	}, dashboard.SourceInventory)
}

func TestParseAccountsForExplicitEmptyKnownUnsupportedSourceFeatures(t *testing.T) {
	t.Parallel()

	dashboard, err := Parse(strings.NewReader(`{
		"title":"Explicit empty features",
		"panels":[{"title":"Panel","type":"timeseries",
			"alert":null,
			"fieldConfig":{"defaults":{"thresholds":{}}},
			"libraryPanel":[]
		}]
	}`), "empty-known-features.json")
	require.NoError(t, err)
	require.Len(t, dashboard.Panels, 1)
	require.Len(t, dashboard.Panels[0].SourceFeatures, 3)
	assert.Equal(t, []string{
		"/panels/0/alert",
		"/panels/0/fieldConfig/defaults/thresholds",
		"/panels/0/libraryPanel",
	}, []string{
		dashboard.Panels[0].SourceFeatures[0].SourcePath,
		dashboard.Panels[0].SourceFeatures[1].SourcePath,
		dashboard.Panels[0].SourceFeatures[2].SourcePath,
	})
	assert.Equal(t, []string{"null", "{}", "[]"}, []string{
		dashboard.Panels[0].SourceFeatures[0].Detail,
		dashboard.Panels[0].SourceFeatures[1].Detail,
		dashboard.Panels[0].SourceFeatures[2].Detail,
	})
	assert.Equal(t, 3, dashboard.SourceInventory.SourceFeatures)
}

func TestParseInventoriesDashboardConfigurationByPresence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, value, detail string
	}{
		{name: "time", value: `{"from":"now-1h","to":"now"}`, detail: `{"from":"now-1h","to":"now"}`},
		{name: "timezone", value: `"utc"`, detail: `"utc"`},
		{name: "refresh", value: `""`, detail: `""`},
		{name: "timepicker", value: `{}`, detail: `{}`},
		{name: "editable", value: `false`, detail: `false`},
		{name: "fiscalYearStartMonth", value: `null`, detail: `null`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dashboard, err := Parse(strings.NewReader(fmt.Sprintf(`{"title":"Config",%q:%s}`, test.name, test.value)), "config.json")
			require.NoError(t, err)
			require.Len(t, dashboard.SourceFeatures, 1)
			feature := dashboard.SourceFeatures[0]
			assert.Equal(t, "/"+test.name, feature.SourcePath)
			assert.Equal(t, test.detail, feature.Detail)
			assert.Equal(t, model.ReasonUnmappedDashboardConfig, feature.Reason)
			assert.Equal(t, 1, dashboard.SourceInventory.SourceFeatures)
		})
	}

	dashboard, err := Parse(strings.NewReader(`{
		"title":"Metadata","id":1,"version":2,"gnetId":3,"iteration":4,"__requires":[{"type":"panel"}]
	}`), "metadata.json")
	require.NoError(t, err)
	assert.Empty(t, dashboard.SourceFeatures)
}

func TestParseEscapesUnmappedPropertyNamesAsJSONPointerSegments(t *testing.T) {
	t.Parallel()

	dashboard, err := Parse(strings.NewReader(`{
		"title":"Escaped paths","plugin/field~name":false,
		"panels":[{"title":"Panel","type":"timeseries","targets":[{"refId":"A","expr":"up","vendor/query~mode":null}]}]
	}`), "escaped-paths.json")
	require.NoError(t, err)
	require.Len(t, dashboard.SourceFeatures, 1)
	assert.Equal(t, "/plugin~1field~0name", dashboard.SourceFeatures[0].SourcePath)
	require.Len(t, dashboard.Panels[0].Queries[0].SourceFeatures, 1)
	assert.Equal(t, "/panels/0/targets/0/vendor~1query~0mode", dashboard.Panels[0].Queries[0].SourceFeatures[0].SourcePath)
}

func TestParseInventoriesVariableConfigurationByPresence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, value, detail string
	}{
		{name: "hide", value: `0`, detail: `0`},
		{name: "refresh", value: `0`, detail: `0`},
		{name: "options", value: `[]`, detail: `[]`},
		{name: "sort", value: `0`, detail: `0`},
		{name: "skipUrlSync", value: `false`, detail: `false`},
		{name: "allFormat", value: `"regex"`, detail: `"regex"`},
		{name: "multiFormat", value: `"pipe"`, detail: `"pipe"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := fmt.Sprintf(`{"title":"Variables","templating":{"list":[{"name":"job","type":"custom","query":"api",%q:%s}]}}`, test.name, test.value)
			dashboard, err := Parse(strings.NewReader(input), "variables.json")
			require.NoError(t, err)
			require.Len(t, dashboard.Variables, 1)
			require.Len(t, dashboard.Variables[0].SourceFeatures, 1)
			feature := dashboard.Variables[0].SourceFeatures[0]
			assert.Equal(t, "/templating/list/0/"+test.name, feature.SourcePath)
			assert.Equal(t, test.detail, feature.Detail)
			assert.Equal(t, model.ReasonUnmappedVariableConfig, feature.Reason)
			assert.Equal(t, 1, dashboard.SourceInventory.SourceFeatures)
		})
	}
}

func TestParseInventoriesTargetAndTransparentConfigurationByPresence(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, value string
	}{
		{name: "legendLink", value: `"https://example.test"`},
		{name: "dimensions", value: `{}`},
		{name: "metricName", value: `"CPUUtilization"`},
		{name: "namespace", value: `"AWS/EC2"`},
		{name: "region", value: `"us-east-1"`},
		{name: "editorMode", value: `"code"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := fmt.Sprintf(`{"title":"Targets","panels":[{"id":1,"title":"One","type":"timeseries","targets":[{"refId":"A","expr":"up",%q:%s}]}]}`, test.name, test.value)
			dashboard, err := Parse(strings.NewReader(input), "targets.json")
			require.NoError(t, err)
			require.Len(t, dashboard.Panels[0].Queries[0].SourceFeatures, 1)
			feature := dashboard.Panels[0].Queries[0].SourceFeatures[0]
			assert.Equal(t, "/panels/0/targets/0/"+test.name, feature.SourcePath)
			assert.Equal(t, test.value, feature.Detail)
			assert.Equal(t, model.ReasonUnmappedQueryConfig, feature.Reason)
		})
	}

	dashboard, err := Parse(strings.NewReader(`{
		"title":"Transparent","panels":[{"id":1,"title":"One","type":"timeseries","pluginVersion":"11.1.0","transparent":false}]
	}`), "transparent.json")
	require.NoError(t, err)
	require.Len(t, dashboard.Panels[0].SourceFeatures, 1)
	assert.Equal(t, "/panels/0/transparent", dashboard.Panels[0].SourceFeatures[0].SourcePath)
	assert.Equal(t, "false", dashboard.Panels[0].SourceFeatures[0].Detail)
	assert.Equal(t, model.ReasonUnmappedVisualization, dashboard.Panels[0].SourceFeatures[0].Reason)
}

func TestParsePreservesEmptyAndNullUnknownVisualizationProperties(t *testing.T) {
	t.Parallel()

	dashboard, err := Parse(strings.NewReader(`{
		"title":"Presence traps","panels":[{
			"title":"Panel","type":"timeseries",
			"panelArray":[],"panelNull":null,"panelObject":{},
			"fieldConfig":{
				"defaults":{"unit":"none","defaultArray":[],"defaultNull":null,"defaultObject":{}},
				"overrides":[],"rootArray":[],"rootNull":null,"rootObject":{}
			},
			"options":{"optionArray":[],"optionNull":null,"optionObject":{}}
		}]
	}`), "presence-traps.json")
	require.NoError(t, err)
	require.Len(t, dashboard.Panels, 1)

	want := map[string]string{
		"/panels/0/fieldConfig/rootArray":              "[]",
		"/panels/0/fieldConfig/rootNull":               "null",
		"/panels/0/fieldConfig/rootObject":             "{}",
		"/panels/0/fieldConfig/defaults/defaultArray":  "[]",
		"/panels/0/fieldConfig/defaults/defaultNull":   "null",
		"/panels/0/fieldConfig/defaults/defaultObject": "{}",
		"/panels/0/options/optionArray":                "[]",
		"/panels/0/options/optionNull":                 "null",
		"/panels/0/options/optionObject":               "{}",
		"/panels/0/panelArray":                         "[]",
		"/panels/0/panelNull":                          "null",
		"/panels/0/panelObject":                        "{}",
	}
	features := dashboard.Panels[0].SourceFeatures
	require.Len(t, features, len(want))
	for _, feature := range features {
		assert.Equal(t, model.ReasonUnmappedVisualization, feature.Reason)
		detail, exists := want[feature.SourcePath]
		assert.True(t, exists, feature.SourcePath)
		assert.Equal(t, detail, feature.Detail, feature.SourcePath)
		delete(want, feature.SourcePath)
	}
	assert.Empty(t, want)
	assert.Equal(t, 12, dashboard.SourceInventory.SourceFeatures)
	assert.Equal(t, dashboard.SourceInventory, normalizedInventory(dashboard))
}

func TestParseAccountsForYAxesAndTransformationConfiguration(t *testing.T) {
	t.Parallel()

	dashboard, err := Parse(strings.NewReader(`{
		"title":"Visual execution","panels":[{
			"title":"Panel","type":"timeseries",
			"yaxes":[
				{"format":"bytes","custom":{},"label":"Left","min":null,"show":false},
				{"format":"percent","logBase":2}
			],
			"transformations":[
				{"id":"calculateField","options":{"mode":"binary","reduce":{"reducer":"sum"}},"disabled":false},
				{"id":"organize","options":null}
			]
		}]
	}`), "visual-execution.json")
	require.NoError(t, err)
	panel := dashboard.Panels[0]
	assert.Equal(t, "bytes", panel.Unit)
	assert.Equal(t, []string{"calculateField", "organize"}, panel.Transforms)

	want := map[string]string{
		"/panels/0/yaxes/0/custom":             "{}",
		"/panels/0/yaxes/0/label":              `"Left"`,
		"/panels/0/yaxes/0/min":                "null",
		"/panels/0/yaxes/0/show":               "false",
		"/panels/0/yaxes/1/format":             `"percent"`,
		"/panels/0/yaxes/1/logBase":            "2",
		"/panels/0/transformations/0/options":  `{"mode":"binary","reduce":{"reducer":"sum"}}`,
		"/panels/0/transformations/0/disabled": "false",
		"/panels/0/transformations/1/options":  "null",
	}
	require.Len(t, panel.SourceFeatures, len(want))
	for _, feature := range panel.SourceFeatures {
		assert.Equal(t, model.ReasonUnmappedVisualization, feature.Reason)
		detail, exists := want[feature.SourcePath]
		assert.True(t, exists, feature.SourcePath)
		assert.Equal(t, detail, feature.Detail, feature.SourcePath)
		delete(want, feature.SourcePath)
	}
	assert.Empty(t, want)
	assert.Equal(t, 9, dashboard.SourceInventory.SourceFeatures)
	assert.Equal(t, dashboard.SourceInventory, normalizedInventory(dashboard))
}

func TestParseInventoriesNestedContainerAndDatasourceProperties(t *testing.T) {
	t.Parallel()

	dashboard, err := Parse(strings.NewReader(`{
		"title":"Nested evidence",
		"annotations":{"vendor":{},"list":[{
			"name":"Modern annotation","enable":false,
			"target":{"expr":"changes(deploy_total[5m])"},
			"datasource":{"type":"prometheus","uid":"prom","vendor":null}
		}]},
		"templating":{"vendor":[],"list":[{
			"name":"job","type":"query",
			"query":{"query":"label_values(up, job)","refId":"StandardVariableQuery","vendor":null},
			"current":{"value":"api","text":"API","selected":false},
			"datasource":{"type":"prometheus","uid":"prom","vendor":{}}
		}]},
		"__inputs":[{"name":"DS_PROMETHEUS","type":"datasource","pluginId":"prometheus","vendor":null}],
		"panels":[{
			"title":"Up","type":"timeseries",
			"gridPos":{"x":0,"y":0,"w":12,"h":8,"vendor":null},
			"datasource":{"type":"prometheus","uid":"prom","vendor":[]},
			"targets":[{"refId":"A","expr":"up","datasource":{"type":"prometheus","uid":"prom","vendor":{}}}]
		}]
	}`), "nested-evidence.json")
	require.NoError(t, err)
	require.Len(t, dashboard.Panels, 1)
	require.Len(t, dashboard.Variables, 1)

	dashboardWant := map[string]struct {
		detail string
		reason model.ReasonCode
	}{
		"/annotations/list/0":                   {detail: `{"name":"Modern annotation","enable":false,"target":{"expr":"changes(deploy_total[5m])"},"datasource":{"type":"prometheus","uid":"prom","vendor":null}}`, reason: model.ReasonAnnotationQuery},
		"/annotations/list/0/enable":            {detail: "false", reason: model.ReasonAnnotationQuery},
		"/annotations/list/0/target":            {detail: `{"expr":"changes(deploy_total[5m])"}`, reason: model.ReasonAnnotationQuery},
		"/annotations/list/0/datasource/vendor": {detail: "null", reason: model.ReasonAnnotationQuery},
		"/annotations/vendor":                   {detail: "{}", reason: model.ReasonUnmappedDashboardConfig},
		"/templating/vendor":                    {detail: "[]", reason: model.ReasonUnmappedDashboardConfig},
		"/__inputs/0/vendor":                    {detail: "null", reason: model.ReasonUnmappedDashboardConfig},
	}
	assertFeatureEvidence(t, dashboard.SourceFeatures, dashboardWant)

	panelWant := map[string]struct {
		detail string
		reason model.ReasonCode
	}{
		"/panels/0/gridPos/vendor":    {detail: "null", reason: model.ReasonUnmappedVisualization},
		"/panels/0/datasource/vendor": {detail: "[]", reason: model.ReasonUnmappedVisualization},
	}
	assertFeatureEvidence(t, dashboard.Panels[0].SourceFeatures, panelWant)
	queryWant := map[string]struct {
		detail string
		reason model.ReasonCode
	}{
		"/panels/0/targets/0/datasource/vendor": {detail: "{}", reason: model.ReasonUnmappedQueryConfig},
	}
	assertFeatureEvidence(t, dashboard.Panels[0].Queries[0].SourceFeatures, queryWant)

	variable := dashboard.Variables[0]
	assert.Equal(t, "label_values(up, job)", variable.Query)
	assert.Equal(t, []string{"api"}, variable.Current)
	variableWant := map[string]struct {
		detail string
		reason model.ReasonCode
	}{
		"/templating/list/0/query/refId":       {detail: `"StandardVariableQuery"`, reason: model.ReasonUnmappedVariableConfig},
		"/templating/list/0/query/vendor":      {detail: "null", reason: model.ReasonUnmappedVariableConfig},
		"/templating/list/0/current/selected":  {detail: "false", reason: model.ReasonUnmappedVariableConfig},
		"/templating/list/0/current/text":      {detail: `"API"`, reason: model.ReasonUnmappedVariableConfig},
		"/templating/list/0/datasource/vendor": {detail: "{}", reason: model.ReasonUnmappedVariableConfig},
	}
	assertFeatureEvidence(t, variable.SourceFeatures, variableWant)

	assert.Equal(t, model.SourceInventory{
		Captured: true, Panels: 1, Queries: 1, Variables: 1, SourceFeatures: 15,
	}, dashboard.SourceInventory)
	assert.Equal(t, dashboard.SourceInventory, normalizedInventory(dashboard))
}

func assertFeatureEvidence(
	t *testing.T,
	features []model.SourceFeature,
	want map[string]struct {
		detail string
		reason model.ReasonCode
	},
) {
	t.Helper()
	require.Len(t, features, len(want))
	for _, feature := range features {
		expected, exists := want[feature.SourcePath]
		assert.True(t, exists, feature.SourcePath)
		assert.Equal(t, expected.detail, feature.Detail, feature.SourcePath)
		assert.Equal(t, expected.reason, feature.Reason, feature.SourcePath)
		delete(want, feature.SourcePath)
	}
	assert.Empty(t, want)
}

func TestParseInventoriesLegacyRowConfigurationByPresence(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, value string
	}{
		{name: "repeat", value: `"cluster"`},
		{name: "repeatIteration", value: `7`},
		{name: "repeatRowId", value: `2`},
		{name: "showTitle", value: `false`},
		{name: "titleSize", value: `"h5"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := fmt.Sprintf(`{"title":"Rows","schemaVersion":14,"rows":[{"title":"Overview",%q:%s,"panels":[]}]}`, test.name, test.value)
			dashboard, err := Parse(strings.NewReader(input), "rows.json")
			require.NoError(t, err)
			require.Len(t, dashboard.Panels, 1)
			require.Len(t, dashboard.Panels[0].SourceFeatures, 1)
			feature := dashboard.Panels[0].SourceFeatures[0]
			assert.Equal(t, "/rows/0/"+test.name, feature.SourcePath)
			assert.Equal(t, test.value, feature.Detail)
			assert.Equal(t, model.ReasonUnmappedDashboardConfig, feature.Reason)
			assert.Equal(t, 1, dashboard.SourceInventory.SourceFeatures)
		})
	}
}

func TestParsePacksLegacySpanPanelsOnOneRow(t *testing.T) {
	t.Parallel()

	dashboard, err := Parse(strings.NewReader(`{
		"title":"Legacy","schemaVersion":14,"rows":[{"title":"Overview","height":"240px","panels":[
			{"id":1,"title":"One","type":"grafana-singlestat-panel","span":4},
			{"id":2,"title":"Two","type":"grafana-piechart-panel","span":4},
			{"id":3,"title":"Three","type":"graph","span":4}
		]}]
	}`), "legacy-spans.json")
	require.NoError(t, err)
	require.Len(t, dashboard.Panels, 4)
	assert.Equal(t, model.PanelKindRow, dashboard.Panels[0].Kind)
	assert.Equal(t, model.PanelKindValue, dashboard.Panels[1].Kind)
	assert.Equal(t, model.PanelKindPie, dashboard.Panels[2].Kind)
	assert.Equal(t, []int{0, 8, 16}, []int{dashboard.Panels[1].Grid.X, dashboard.Panels[2].Grid.X, dashboard.Panels[3].Grid.X})
	assert.Equal(t, []int{1, 1, 1}, []int{dashboard.Panels[1].Grid.Y, dashboard.Panels[2].Grid.Y, dashboard.Panels[3].Grid.Y})
	assert.Equal(t, 8, dashboard.Panels[1].Grid.H)
}

func TestParseLegacyRowsReserveChildHeightOnlyWhenExpanded(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name          string
		collapsed     bool
		nextRowY      int
		firstChildY   int
		firstChildEnd int
	}{
		{name: "collapsed row compacts the following header", collapsed: true, nextRowY: 1, firstChildY: 1, firstChildEnd: 4},
		{name: "expanded row reserves visible child height", collapsed: false, nextRowY: 4, firstChildY: 1, firstChildEnd: 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := fmt.Sprintf(`{
				"title":"Legacy rows","schemaVersion":14,"rows":[
					{"title":"First","collapsed":%t,"height":"90px","panels":[{"title":"Child","type":"graph","span":12}]},
					{"title":"Second","panels":[]}
				]
			}`, test.collapsed)
			dashboard, err := Parse(strings.NewReader(input), "legacy-row-collapse.json")
			require.NoError(t, err)
			require.Len(t, dashboard.Panels, 3)
			assert.Equal(t, 0, dashboard.Panels[0].Grid.Y)
			assert.Equal(t, test.firstChildY, dashboard.Panels[1].Grid.Y)
			assert.Equal(t, test.firstChildEnd, dashboard.Panels[1].Grid.Y+dashboard.Panels[1].Grid.H)
			assert.Equal(t, test.nextRowY, dashboard.Panels[2].Grid.Y)
		})
	}
}

func TestParseRejectsMissingTitle(t *testing.T) {
	t.Parallel()

	_, err := ParseFile("testdata/missing-title.json")
	require.ErrorContains(t, err, "title is required")
}

func TestParseRejectsDuplicateVariableNames(t *testing.T) {
	t.Parallel()

	_, err := Parse(strings.NewReader(`{
		"title":"Duplicate variables",
		"templating":{"list":[
			{"name":"env","type":"custom","query":"prod"},
			{"name":"env","type":"custom","query":"staging"}
		]}
	}`), "duplicate-variables.json")
	require.ErrorContains(t, err, `duplicate variable name "env"`)
	require.ErrorContains(t, err, "/templating/list/1")
	require.ErrorContains(t, err, "/templating/list/0")
}

func TestParseRejectsVariableNamesThatCannotBeRepresentedSafely(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", "123", "env-name"} {
		t.Run(fmt.Sprintf("name_%q", name), func(t *testing.T) {
			t.Parallel()
			_, err := Parse(strings.NewReader(fmt.Sprintf(`{
				"title":"Unsafe variable","templating":{"list":[{"name":%q,"type":"custom","query":"prod"}]}
			}`, name)), "unsafe-variable.json")
			require.ErrorContains(t, err, "cannot be represented safely")
			require.ErrorContains(t, err, "/templating/list/0")
		})
	}
}

func TestParseRejectsVariableNamesReservedBySigNozRuntime(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"SIGNOZ_START_TIME", "SIGNOZ_END_TIME", "start_timestamp", "end_datetime"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(strings.NewReader(fmt.Sprintf(`{
				"title":"Reserved variable","templating":{"list":[{"name":%q,"type":"custom","query":"prod"}]}
			}`, name)), "reserved-variable.json")
			require.ErrorContains(t, err, "collides with a reserved SigNoz runtime variable")
		})
	}
}

func TestParseAccountsForInputsTransformationsAndText(t *testing.T) {
	t.Parallel()

	dashboard, err := ParseFile("testdata/inputs.json")
	require.NoError(t, err)
	require.Len(t, dashboard.Panels, 2)

	panel := dashboard.Panels[0]
	assert.Equal(t, "prometheus", panel.Datasource.Type)
	assert.Equal(t, []string{"joinByField"}, panel.Transforms)
	assert.Equal(t, "2h", panel.TimeFrom)
	require.Len(t, panel.Queries, 1)
	assert.True(t, panel.Queries[0].Instant)
	assert.Equal(t, "table", panel.Queries[0].Format)
	assert.Equal(t, "Check the service runbook.", dashboard.Panels[1].Content)
	require.Len(t, dashboard.Variables, 1)
	assert.Equal(t, "/source-.+/", dashboard.Variables[0].Regex)
}

func TestParseRejectsTrailingJSON(t *testing.T) {
	t.Parallel()

	_, err := Parse(strings.NewReader(`{"title":"one"}{"title":"two"}`), "inline.json")
	require.ErrorContains(t, err, "trailing JSON value")
}
