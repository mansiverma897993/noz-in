package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mansiverma897993/signoz/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoadOverridesBindsCamelCaseYAML guards the YAML->JSON round-trip: the
// builder fields use camelCase JSON names, and must populate from a YAML file
// written exactly as the skill instructs an agent to write it.
func TestLoadOverridesBindsCamelCaseYAML(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "overrides.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`overrides:
  - sourcePath: /panels/0/targets/0
    builder:
      name: A
      metricName: node_memory_MemTotal_bytes
      timeAggregation: latest
      spaceAggregation: sum
      stepSeconds: 60
`), 0o600))

	loaded, err := loadOverrides(path)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.NotNil(t, loaded[0].Builder)
	assert.Equal(t, "node_memory_MemTotal_bytes", loaded[0].Builder.MetricName)
	assert.Equal(t, "latest", loaded[0].Builder.TimeAggregation)
	assert.Equal(t, "sum", loaded[0].Builder.SpaceAggregation)
	assert.Equal(t, 60, loaded[0].Builder.StepSeconds)
}

func TestApplyOverridesReplacesTranslationAsCandidate(t *testing.T) {
	t.Parallel()
	migration := model.Migration{Translations: map[string]model.Translation{
		"/panels/0/targets/0": {
			Kind:     model.TranslationPromQL,
			PromQL:   `sum(rate(node_cpu_seconds_total[5m]))`,
			Decision: model.Decision{Verdict: model.VerdictPassthrough, Reasons: []model.ReasonCode{model.ReasonMetricTypeRequired}},
		},
	}}
	overrides := []DashboardOverride{{
		SourcePath: "/panels/0/targets/0",
		Builder: &model.BuilderQuery{
			Name: "A", MetricName: "node_cpu_seconds_total",
			TimeAggregation: "rate", SpaceAggregation: "sum", StepSeconds: 300,
		},
	}}

	applied := applyOverrides(migration, overrides)
	require.Equal(t, []string{"/panels/0/targets/0"}, applied)

	got := migration.Translations["/panels/0/targets/0"]
	assert.Equal(t, model.TranslationBuilder, got.Kind)
	require.NotNil(t, got.Builder)
	assert.Equal(t, "node_cpu_seconds_total", got.Builder.MetricName)
	// The original PromQL is preserved as the differential's source of truth.
	assert.Equal(t, `sum(rate(node_cpu_seconds_total[5m]))`, got.PromQL)
	// Marked operator-override + a builder-candidate reason, held at review until
	// the live promotion gate verifies it (offline it never becomes native).
	assert.Contains(t, got.Decision.Reasons, model.ReasonOperatorOverride)
	assert.Contains(t, got.Decision.Reasons, model.ReasonBuilderRateIncrease)
	assert.Equal(t, model.VerdictNeedsReview, got.Decision.Verdict)
}

func TestApplyOverridesIgnoresUnknownPath(t *testing.T) {
	t.Parallel()
	migration := model.Migration{Translations: map[string]model.Translation{}}
	applied := applyOverrides(migration, []DashboardOverride{{
		SourcePath: "/nope", Builder: &model.BuilderQuery{Name: "A", MetricName: "m"},
	}})
	assert.Empty(t, applied)
}
