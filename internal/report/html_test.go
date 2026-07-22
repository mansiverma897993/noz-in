package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteHTMLPutsReviewPanelsFirstAndEscapesSource(t *testing.T) {
	t.Parallel()

	evidence := reporttypes.Report{
		SchemaVersion: "1",
		Dashboard:     reporttypes.DashboardInfo{Title: "Hosts <prod>"},
		Summary: reporttypes.Summary{
			Panels: 2, PanelsAccounted: 2, SourceFeaturesNeedsReview: 2, Headline: "accounted",
		},
		ReasonCodes: map[string]string{
			"TEST_REASON": "Needs attention", "UNMAPPED_QUERY_CONFIG": "Unmapped query configuration",
			"UNMAPPED_DASHBOARD_CONFIG": "Unmapped dashboard configuration",
			"UNMAPPED_VARIABLE_CONFIG":  "Unmapped variable configuration",
		},
		SourceFeatures: []reporttypes.SourceFeatureRecord{{
			Kind: "dashboard_property", SourcePath: "/timezone", Detail: `"browser"`,
			Verdict: "needs_review", ReasonCode: "UNMAPPED_DASHBOARD_CONFIG",
		}},
		Panels: []reporttypes.PanelRecord{
			{Title: "Native panel", Verdict: "NATIVE", Kind: "graph", EmittedKind: "graph", EmittedMode: "BUILDER"},
			{Title: "Review panel", Verdict: "NEEDS_REVIEW", Kind: "table", EmittedKind: "graph", EmittedMode: "PROMQL", Queries: []reporttypes.QueryRecord{{
				RefID: "A", Original: `<script>alert("x")</script>`, Format: `<format>table`, Step: 30, Verdict: "needs_review",
				ReasonCodes: []string{"TEST_REASON", "UNMAPPED_QUERY_CONFIG"},
				SourceFeatures: []reporttypes.SourceFeatureRecord{{
					Kind: "query_range", SourcePath: "/panels/0/targets/0/range", Detail: "false",
					Verdict: "needs_review", ReasonCode: "UNMAPPED_QUERY_CONFIG",
				}},
			}}},
		},
		Variables: []reporttypes.VariableRecord{{
			Name: "instance", Label: "Host <selector>", SourcePath: "/templating/list/0", SourceKind: "query", EmittedKind: "dynamic",
			Current: []string{"$__all"}, AllValue: `<unsafe>.+`, Verdict: "needs_review",
			ReasonCodes: []string{"VARIABLE_ALL_VALUE_SEMANTICS", "UNMAPPED_VARIABLE_CONFIG"}, Notes: []string{"All matcher semantics differ."},
			SourceFeatures: []reporttypes.SourceFeatureRecord{{
				Kind: "variable_property", SourcePath: "/templating/list/0/hide", Detail: "0",
				Verdict: "needs_review", ReasonCode: "UNMAPPED_VARIABLE_CONFIG",
			}},
		}},
	}
	path := filepath.Join(t.TempDir(), "report.html")

	require.NoError(t, WriteHTML(path, evidence))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	html := string(data)
	assert.Less(t, strings.Index(html, "Review panel"), strings.Index(html, "Native panel"))
	assert.Contains(t, html, "&lt;script&gt;alert")
	assert.NotContains(t, html, `<script>alert`)
	assert.Contains(t, html, "Grafana query format")
	assert.Contains(t, html, "&lt;format&gt;table")
	assert.Contains(t, html, "Grafana target step")
	assert.Contains(t, html, "Unmapped query configuration")
	assert.Contains(t, html, "/panels/0/targets/0/range")
	assert.GreaterOrEqual(t, strings.Count(html, "UNMAPPED_QUERY_CONFIG"), 2)
	panelsStart := strings.Index(html, "<h2>Panels</h2>")
	require.Positive(t, panelsStart)
	assert.Contains(t, html[:panelsStart], "UNMAPPED_QUERY_CONFIG")
	assert.Contains(t, html, "Grafana All value")
	assert.Contains(t, html, "&lt;unsafe&gt;.&#43;")
	assert.Contains(t, html, "Current:")
	assert.Contains(t, html, "$__all")
	assert.Contains(t, html, "All matcher semantics differ.")
	assert.Contains(t, html, "Dashboard source features")
	assert.Contains(t, html, "/timezone")
	assert.Contains(t, html, "UNMAPPED_DASHBOARD_CONFIG")
	assert.Contains(t, html, "Host &lt;selector&gt;")
	assert.Contains(t, html, "Unmapped variable configuration")
	assert.Contains(t, html, "/templating/list/0/hide")
	assert.Contains(t, html, "UNMAPPED_VARIABLE_CONFIG")
	assert.Contains(t, html, "Content-Security-Policy")
}
