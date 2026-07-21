package app

import (
	"testing"

	"github.com/mansiverma897993/signoz/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSourceAndTargetVariableResolutionStayDistinct(t *testing.T) {
	t.Parallel()

	dashboard := model.Dashboard{Variables: []model.Variable{
		{Name: "single", Current: []string{"api"}},
		{Name: "single_multi", Multi: true, Current: []string{"api"}},
		{Name: "all_custom", IncludeAll: true, Current: []string{"All"}, AllValue: ".+"},
		{Name: "all_unset", IncludeAll: true, Current: []string{"$__all"}},
		{Name: "literal_all", Current: []string{"all"}},
		{Name: "multiple", Multi: true, Current: []string{"api", "worker"}},
		{Name: "empty", Current: []string{""}},
		{Name: "unset"},
	}}

	source := resolveSourceVariables(dashboard, nil)
	target := resolveTargetVariables(dashboard, nil, map[string]string{
		"all_custom": "dynamic",
		"all_unset":  "dynamic",
	})

	assert.Equal(t, "api", source.Values["single"])
	assert.Equal(t, "api", target.Values["single"])
	assert.Equal(t, []string{"api"}, source.Multi["single_multi"])
	assert.Equal(t, []string{"api"}, target.Values["single_multi"])
	assert.Equal(t, ".+", source.Values["all_custom"])
	assert.Equal(t, "__all__", target.Values["all_custom"])
	assert.NotContains(t, source.Values, "all_unset")
	assert.Equal(t, "__all__", target.Values["all_unset"])
	assert.Contains(t, source.Issues["all_unset"].Reasons, model.ReasonVariableAllValue)
	assert.Equal(t, "all", source.Values["literal_all"])
	assert.Equal(t, "all", target.Values["literal_all"])
	assert.Equal(t, []string{"api", "worker"}, source.Multi["multiple"])
	assert.Equal(t, []string{"api", "worker"}, target.Values["multiple"])

	for _, name := range []string{"empty", "unset"} {
		assert.NotContains(t, source.Values, name)
		assert.NotContains(t, target.Values, name)
		assert.Contains(t, source.Issues[name].Reasons, model.ReasonMissingVariableValue)
		assert.Contains(t, target.Issues[name].Reasons, model.ReasonMissingVariableValue)
	}
	assert.NotContains(t, source.Values, "multiple")
	assert.Contains(t, source.Issues["multiple"].Reasons, model.ReasonMultiVariableValue)
	assert.NotContains(t, target.Issues, "multiple")
}

func TestVariableOverridesResolveOnlyTheirOwnSide(t *testing.T) {
	t.Parallel()

	dashboard := model.Dashboard{Variables: []model.Variable{{
		Name: "job", Multi: true, IncludeAll: true, Current: []string{"api", "worker"},
	}}}

	source := resolveSourceVariables(dashboard, map[string]string{"job": "api|worker"})
	target := resolveTargetVariables(
		dashboard,
		map[string]string{"job": "$__all"},
		map[string]string{"job": "dynamic"},
	)

	require.Empty(t, source.Issues)
	require.Empty(t, target.Issues)
	assert.Equal(t, "api|worker", source.Values["job"])
	assert.NotContains(t, source.Multi, "job")
	assert.Equal(t, "__all__", target.Values["job"])
	assert.NotEqual(t, source.Values["job"], target.Values["job"])

	scalarSelection := resolveTargetVariables(
		dashboard,
		map[string]string{"job": "api"},
		map[string]string{"job": "dynamic"},
	)
	require.Empty(t, scalarSelection.Issues)
	assert.Equal(t, []string{"api"}, scalarSelection.Values["job"])
}

func TestSourceVariableResolutionFollowsPinnedPrometheusEscapingModes(t *testing.T) {
	t.Parallel()

	dashboard := model.Dashboard{Variables: []model.Variable{
		{Name: "plain", Current: []string{"api.prod"}},
		{Name: "backslash", Current: []string{`api\west`}},
		{Name: "quoted", Current: []string{`api"west`}},
		{Name: "all_enabled", IncludeAll: true, Current: []string{"api"}},
	}}
	resolution := resolveSourceVariables(dashboard, nil)

	assert.Equal(t, "api.prod", resolution.Values["plain"])
	assert.Equal(t, `api\\west`, resolution.Values["backslash"])
	assert.NotContains(t, resolution.Values, "quoted")
	require.Contains(t, resolution.Issues, "quoted")
	assert.Contains(t, resolution.Issues["quoted"].Reasons, model.ReasonMissingVariableValue)
	assert.Contains(t, resolution.Issues["quoted"].Reasons, model.ReasonVariableValueEscaping)
	assert.Equal(t, []string{"api"}, resolution.Multi["all_enabled"])
	assert.NotContains(t, resolution.Values, "all_enabled")

	overridden := resolveSourceVariables(dashboard, map[string]string{"quoted": `api\"west`})
	assert.Equal(t, `api\"west`, overridden.Values["quoted"])
	assert.NotContains(t, overridden.Issues, "quoted")
	assert.NotContains(t, overridden.Multi, "quoted")
}

func TestTargetAllFailsClosedForNonDynamicVariable(t *testing.T) {
	t.Parallel()

	dashboard := model.Dashboard{Variables: []model.Variable{{
		Name: "environment", Multi: true, IncludeAll: true, Current: []string{"All"},
	}}}
	resolution := resolveTargetVariables(
		dashboard,
		nil,
		map[string]string{"environment": "custom"},
	)

	assert.NotContains(t, resolution.Values, "environment")
	require.Contains(t, resolution.Issues, "environment")
	assert.Contains(t, resolution.Issues["environment"].Reasons, model.ReasonMissingVariableValue)
	assert.Contains(t, resolution.Issues["environment"].Reasons, model.ReasonVariableAllValue)
	assert.Contains(t, resolution.Issues["environment"].Detail, `target variable type "custom"`)
}

func TestTargetVariableResolutionRejectsArrayWhenMultiIsDisabled(t *testing.T) {
	t.Parallel()

	dashboard := model.Dashboard{Variables: []model.Variable{{
		Name: "job", Current: []string{"api", "worker"}, Multi: false,
	}}}
	resolution := resolveTargetVariables(
		dashboard,
		nil,
		map[string]string{"job": "dynamic"},
	)

	assert.NotContains(t, resolution.Values, "job")
	require.Contains(t, resolution.Issues, "job")
	assert.Contains(t, resolution.Issues["job"].Reasons, model.ReasonMissingVariableValue)
	assert.Contains(t, resolution.Issues["job"].Reasons, model.ReasonMultiVariableValue)
	assert.Contains(t, resolution.Issues["job"].Detail, "2 current values while multi is disabled")
}

func TestTargetOverrideCannotInventUnpersistedVariable(t *testing.T) {
	t.Parallel()

	resolution := resolveTargetVariables(
		model.Dashboard{},
		map[string]string{"ghost": "api"},
		nil,
	)

	assert.NotContains(t, resolution.Values, "ghost")
	require.Contains(t, resolution.Issues, "ghost")
	assert.Contains(t, resolution.Issues["ghost"].Reasons, model.ReasonMissingVariableValue)
	assert.Contains(t, resolution.Issues["ghost"].Detail, "no persisted dashboard variable definition")
}

func TestScopedVariableAliasesRequireUseByBothExactQueries(t *testing.T) {
	t.Parallel()

	dashboard := model.Dashboard{Variables: []model.Variable{{
		Name: "cluster", Query: "label_values(up, cluster)",
	}}}
	source := sourceVariableValues{"cluster": "prod"}
	target := targetVariableValues{"cluster": "production"}

	aliases, bindings, err := scopedVariableAliases(
		dashboard,
		source,
		target,
		map[string]struct{}{"cluster": {}},
		map[string]struct{}{"cluster": {}},
		func(_, _ string) bool { return true },
	)
	require.NoError(t, err)
	assert.Equal(t, map[string]map[string]string{
		"cluster": {"production": "prod"},
	}, aliases)
	assert.Equal(t, []DifferentialLabelValueAliasBinding{{
		VariableName: "cluster",
		SourceLabel:  "cluster",
		TargetLabel:  "cluster",
		SourceValue:  "prod",
		TargetValue:  "production",
	}}, bindings)

	for _, test := range []struct {
		name        string
		sourceNames map[string]struct{}
		targetNames map[string]struct{}
	}{
		{name: "unrelated source query", targetNames: map[string]struct{}{"cluster": {}}},
		{name: "unrelated target query", sourceNames: map[string]struct{}{"cluster": {}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			aliases, bindings, aliasErr := scopedVariableAliases(
				dashboard, source, target, test.sourceNames, test.targetNames,
				func(_, _ string) bool { return true },
			)
			require.NoError(t, aliasErr)
			assert.Empty(t, aliases)
			assert.Empty(t, bindings)
		})
	}
}

func TestScopedVariableAliasesRejectConflictingVariablesForOneLabel(t *testing.T) {
	t.Parallel()

	dashboard := model.Dashboard{Variables: []model.Variable{
		{Name: "primary", Query: "label_values(up, cluster)"},
		{Name: "secondary", Query: "label_values(other_metric, cluster)"},
	}}
	used := map[string]struct{}{"primary": {}, "secondary": {}}

	aliases, bindings, err := scopedVariableAliases(
		dashboard,
		sourceVariableValues{"primary": "prod", "secondary": "staging"},
		targetVariableValues{"primary": "production", "secondary": "production"},
		used,
		used,
		func(_, _ string) bool { return true },
	)

	require.Error(t, err)
	assert.Nil(t, aliases)
	assert.Nil(t, bindings)
	assert.Contains(t, err.Error(), `variables "primary" and "secondary"`)
	assert.Contains(t, err.Error(), `label "cluster"`)
}

func TestScopedVariableAliasesMergeNonConflictingVariablesForOneLabel(t *testing.T) {
	t.Parallel()

	dashboard := model.Dashboard{Variables: []model.Variable{
		{Name: "primary", Query: "label_values(up, cluster)"},
		{Name: "secondary", Query: "label_values(other_metric, cluster)"},
	}}
	used := map[string]struct{}{"primary": {}, "secondary": {}}

	aliases, bindings, err := scopedVariableAliases(
		dashboard,
		sourceVariableValues{"primary": "prod", "secondary": "staging"},
		targetVariableValues{"primary": "production", "secondary": "stage"},
		used,
		used,
		func(_, _ string) bool { return true },
	)

	require.NoError(t, err)
	assert.Equal(t, map[string]map[string]string{
		"cluster": {"production": "prod", "stage": "staging"},
	}, aliases)
	assert.Len(t, bindings, 2)
}

func TestScopedVariableAliasesRejectTargetAllSentinel(t *testing.T) {
	t.Parallel()

	dashboard := model.Dashboard{Variables: []model.Variable{{
		Name: "cluster", Query: "label_values(up, cluster)",
	}}}

	aliases, bindings, err := scopedVariableAliases(
		dashboard,
		sourceVariableValues{"cluster": "prod"},
		targetVariableValues{"cluster": "__all__"},
		map[string]struct{}{"cluster": {}},
		map[string]struct{}{"cluster": {}},
		func(_, _ string) bool { return true },
	)

	require.NoError(t, err)
	assert.Empty(t, aliases)
	assert.Empty(t, bindings)
}
