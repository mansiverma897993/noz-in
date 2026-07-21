package perses

import (
	"encoding/json"
	"testing"

	"github.com/mansiverma897993/signoz/internal/target/signoz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFromV5ProducesPersesShape(t *testing.T) {
	t.Parallel()
	v5 := signoz.DashboardV5{
		Title: "Demo", Description: "d", Tags: []string{"team:sre"}, UUID: "u",
		Layout: []signoz.Layout{{X: 0, Y: 0, W: 6, H: 6, I: "w1"}, {X: 6, Y: 0, W: 6, H: 6, I: "row1"}},
		Widgets: []signoz.Widget{
			{ID: "w1", Title: "CPU", PanelTypes: "graph", YAxisUnit: "short",
				Query: signoz.WidgetQuery{QueryType: "promql", PromQL: []signoz.PromQLQuery{{Name: "A", Query: "up"}}}},
			{ID: "v1", Title: "RAM", PanelTypes: "value",
				Query: signoz.WidgetQuery{QueryType: "builder", Builder: signoz.BuilderContainer{QueryData: []signoz.BuilderQueryData{{QueryName: "A"}}}}},
			{ID: "row1", Title: "A row", PanelTypes: "row"}, // rows carry no panel
		},
		Variables: map[string]signoz.VariableV5{
			"job": {Name: "job", Type: "DYNAMIC", DynamicVariablesAttribute: "service.name", MultiSelect: true},
		},
	}

	dashboard := FromV5(v5)

	assert.Equal(t, "v6", dashboard.SchemaVersion)
	assert.True(t, dashboard.GenerateName)
	assert.Equal(t, "Demo", dashboard.Spec.Display.Name)
	require.Len(t, dashboard.Tags, 1)
	assert.Equal(t, Tag{Key: "team", Value: "sre"}, dashboard.Tags[0])

	// Rows are layout-only; only the two real panels get Perses panels.
	require.Len(t, dashboard.Spec.Panels, 2)
	assert.Equal(t, "signoz/TimeSeriesPanel", dashboard.Spec.Panels["w1"].Spec.Plugin.Kind)
	assert.Equal(t, "signoz/StatChartPanel", dashboard.Spec.Panels["v1"].Spec.Plugin.Kind)
	assert.Equal(t, "Panel", dashboard.Spec.Panels["w1"].Kind)

	// Query kinds map correctly.
	assert.Equal(t, "signoz/PromQLQuery", dashboard.Spec.Panels["w1"].Spec.Queries[0].Spec.Plugin.Kind)
	assert.Equal(t, "signoz/BuilderQuery", dashboard.Spec.Panels["v1"].Spec.Queries[0].Spec.Plugin.Kind)

	// Layout is a single Grid; the row's layout entry is dropped (no panel).
	require.Len(t, dashboard.Spec.Layouts, 1)
	assert.Equal(t, "Grid", dashboard.Spec.Layouts[0].Kind)
	require.Len(t, dashboard.Spec.Layouts[0].Spec.Items, 1)
	assert.Equal(t, "#/spec/panels/w1", dashboard.Spec.Layouts[0].Spec.Items[0].Content.Ref)

	// Variable maps to a ListVariable with the dynamic plugin.
	require.Len(t, dashboard.Spec.Variables, 1)
	assert.Equal(t, "ListVariable", dashboard.Spec.Variables[0].Kind)
	require.NotNil(t, dashboard.Spec.Variables[0].Spec.Plugin)
	assert.Equal(t, "signoz/DynamicVariable", dashboard.Spec.Variables[0].Spec.Plugin.Kind)

	// The whole thing must be strict-JSON serializable.
	_, err := json.Marshal(dashboard)
	require.NoError(t, err)
}
