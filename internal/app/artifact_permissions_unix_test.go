//go:build !windows

package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeneratedArtifactsAreOwnerOnly(t *testing.T) {
	t.Parallel()

	dashboardOutput := filepath.Join(t.TempDir(), "dashboards")
	dashboardResults, err := MigrateGrafana(
		context.Background(),
		[]string{"../source/grafana/testdata/modern.json"},
		GrafanaOptions{OutputDirectory: dashboardOutput},
	)
	require.NoError(t, err)
	require.Len(t, dashboardResults, 1)
	assertOwnerOnly(t, dashboardResults[0].DashboardPath)
	assertOwnerOnly(t, dashboardResults[0].ReportPath)
	assertOwnerOnly(t, dashboardResults[0].HTMLPath)
	require.NotNil(t, dashboardResults[0].Evidence.ArtifactSet)
	assertOwnerOnly(t, filepath.Join(dashboardOutput, dashboardResults[0].Evidence.ArtifactSet.Path))
	assertArtifactTreeOwnerOnly(t, dashboardOutput)

	ruleOutput := filepath.Join(t.TempDir(), "rules")
	ruleResults, err := MigratePrometheusRules(
		context.Background(),
		[]string{"../source/prometheus/testdata/rules.yaml"},
		RuleOptions{OutputDirectory: ruleOutput},
	)
	require.NoError(t, err)
	require.Len(t, ruleResults, 1)
	assertOwnerOnly(t, ruleResults[0].RulesPath)
	assertOwnerOnly(t, ruleResults[0].ReportPath)
	assertOwnerOnly(t, ruleResults[0].HTMLPath)
	require.NotNil(t, ruleResults[0].Evidence.ArtifactSet)
	assertOwnerOnly(t, filepath.Join(ruleOutput, ruleResults[0].Evidence.ArtifactSet.Path))
	assertArtifactTreeOwnerOnly(t, ruleOutput)
}

func assertArtifactTreeOwnerOnly(t *testing.T, root string) {
	t.Helper()
	require.NoError(t, filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), path)
			return nil
		}
		assert.True(t, info.Mode().IsRegular(), path)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), path)
		return nil
	}))
}

func assertOwnerOnly(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), path)
}
