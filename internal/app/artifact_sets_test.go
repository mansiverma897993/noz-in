package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mansiverma897993/noz-in/internal/artifactset"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
	"github.com/stretchr/testify/require"
)

func TestMigrationOutputsDeclareAndValidateCommittedArtifactSets(t *testing.T) {
	t.Parallel()

	dashboardResults, err := MigrateGrafana(
		context.Background(),
		[]string{"../source/grafana/testdata/modern.json"},
		GrafanaOptions{OutputDirectory: filepath.Join(t.TempDir(), "dashboards")},
	)
	require.NoError(t, err)
	require.Len(t, dashboardResults, 1)
	assertCommittedDashboardSet(t, dashboardResults[0])

	ruleResults, err := MigratePrometheusRules(
		context.Background(),
		[]string{"../source/prometheus/testdata/rules.yaml"},
		RuleOptions{OutputDirectory: filepath.Join(t.TempDir(), "rules")},
	)
	require.NoError(t, err)
	require.Len(t, ruleResults, 1)
	assertCommittedRuleSet(t, ruleResults[0])
}

func TestCommittedConsumersRejectOutOfBandReportReplacement(t *testing.T) {
	t.Parallel()

	dashboardResults, err := MigrateGrafana(
		context.Background(),
		[]string{"../source/grafana/testdata/modern.json"},
		GrafanaOptions{OutputDirectory: filepath.Join(t.TempDir(), "dashboards")},
	)
	require.NoError(t, err)
	dashboard := dashboardResults[0]
	dashboard.Evidence.Run.Flags["outOfBand"] = true
	require.NoError(t, writeJSON(dashboard.ReportPath, dashboard.Evidence))
	storedDashboardEvidence, reportData, err := readMigrationEvidence(dashboard.ReportPath)
	require.NoError(t, err)
	_, _, err = readBoundPrimaryDashboard(dashboard.ReportPath, reportData, storedDashboardEvidence)
	require.Error(t, err)
	require.ErrorContains(t, err, "report bytes do not match")

	ruleResults, err := MigratePrometheusRules(
		context.Background(),
		[]string{"../source/prometheus/testdata/rules.yaml"},
		RuleOptions{OutputDirectory: filepath.Join(t.TempDir(), "rules")},
	)
	require.NoError(t, err)
	rule := ruleResults[0]
	rule.Evidence.Run.Flags["outOfBand"] = true
	require.NoError(t, writeJSON(rule.ReportPath, rule.Evidence))
	changedReport, err := os.ReadFile(rule.ReportPath)
	require.NoError(t, err)
	var changedEvidence reporttypes.RuleReport
	require.NoError(t, json.Unmarshal(changedReport, &changedEvidence))
	err = ValidateStoredRuleArtifact(rule.ReportPath, changedEvidence)
	require.Error(t, err)
	require.ErrorContains(t, err, "report bytes do not match")
}

func TestCommittedConsumersRejectMissingManifest(t *testing.T) {
	t.Parallel()

	results, err := MigrateGrafana(
		context.Background(),
		[]string{"../source/grafana/testdata/modern.json"},
		GrafanaOptions{OutputDirectory: filepath.Join(t.TempDir(), "dashboards")},
	)
	require.NoError(t, err)
	result := results[0]
	require.NotNil(t, result.Evidence.ArtifactSet)
	require.NoError(t, os.Remove(filepath.Join(filepath.Dir(result.ReportPath), result.Evidence.ArtifactSet.Path)))
	evidence, reportData, err := readMigrationEvidence(result.ReportPath)
	require.NoError(t, err)
	_, _, err = readBoundPrimaryDashboard(result.ReportPath, reportData, evidence)
	require.Error(t, err)
	require.ErrorContains(t, err, "commit manifest")
}

func assertCommittedDashboardSet(t *testing.T, result GrafanaResult) {
	t.Helper()
	reportData, err := os.ReadFile(result.ReportPath)
	require.NoError(t, err)
	var evidence reporttypes.Report
	require.NoError(t, decodeStrictJSON(reportData, &evidence))
	require.NotNil(t, evidence.ArtifactSet)
	require.NotNil(t, evidence.PrimaryArtifact)
	_, err = artifactset.ReadCommitted(
		result.ReportPath,
		reportData,
		evidence.ArtifactSet,
		artifactset.KindDashboard,
		[]string{filepath.Base(result.DashboardPath), filepath.Base(result.HTMLPath)},
		maxMigrationReportSize,
	)
	require.NoError(t, err)
}

func assertCommittedRuleSet(t *testing.T, result RuleResult) {
	t.Helper()
	reportData, err := os.ReadFile(result.ReportPath)
	require.NoError(t, err)
	var evidence reporttypes.RuleReport
	require.NoError(t, decodeStrictJSON(reportData, &evidence))
	require.NotNil(t, evidence.ArtifactSet)
	require.NotNil(t, evidence.PrimaryArtifact)
	_, err = artifactset.ReadCommitted(
		result.ReportPath,
		reportData,
		evidence.ArtifactSet,
		artifactset.KindRules,
		[]string{filepath.Base(result.RulesPath), filepath.Base(result.HTMLPath)},
		maxMigrationReportSize,
	)
	require.NoError(t, err)
}
