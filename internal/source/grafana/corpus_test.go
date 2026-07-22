package grafana

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/stretchr/testify/require"
)

func TestCorpusParsesEveryDashboard(t *testing.T) {
	root := os.Getenv("PROMCAST_RESEARCH_ROOT")
	if root == "" {
		t.Skip("PROMCAST_RESEARCH_ROOT is not set")
	}

	roots := []string{
		filepath.Join(root, "corpus", "top"),
		filepath.Join(root, "corpus", "mixin"),
		filepath.Join(root, "corpus-complex"),
	}

	var dashboards int
	var panels int
	var queries int
	var sourceFeatures int
	var expressionTargets int
	var instantTargets int
	var intervalControlledTargets int
	reasonCounts := make(map[model.ReasonCode]int)
	for _, corpusRoot := range roots {
		err := filepath.WalkDir(corpusRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			require.NoError(t, walkErr)
			if entry.IsDir() || filepath.Ext(path) != ".json" || strings.Contains(path, string(filepath.Separator)+"_census"+string(filepath.Separator)) {
				return nil
			}

			dashboard, err := ParseFile(path)
			require.NoError(t, err, path)
			dashboards++
			panels += len(dashboard.Panels)
			for _, feature := range dashboard.SourceFeatures {
				sourceFeatures++
				reasonCounts[feature.Reason]++
			}
			for _, variable := range dashboard.Variables {
				for _, feature := range variable.SourceFeatures {
					sourceFeatures++
					reasonCounts[feature.Reason]++
				}
			}
			for _, panel := range dashboard.Panels {
				for _, feature := range panel.SourceFeatures {
					sourceFeatures++
					reasonCounts[feature.Reason]++
				}
				queries += len(panel.Queries)
				for _, query := range panel.Queries {
					for _, feature := range query.SourceFeatures {
						sourceFeatures++
						reasonCounts[feature.Reason]++
					}
					if query.Datasource.Type == "__expr__" || query.Datasource.UID == "__expr__" {
						expressionTargets++
					}
					if query.Instant {
						instantTargets++
					}
					if query.Step > 0 || query.Interval != "" || query.IntervalFactor > 0 || query.MaxDataPoints > 0 {
						intervalControlledTargets++
					}
				}
			}
			return nil
		})
		require.NoError(t, err)
	}

	t.Logf("parsed dashboards=%d panels=%d queries=%d source_features=%d expressions=%d instant=%d interval_controlled=%d",
		dashboards, panels, queries, sourceFeatures, expressionTargets, instantTargets, intervalControlledTargets)
	reasons := make([]string, 0, len(reasonCounts))
	for reason := range reasonCounts {
		reasons = append(reasons, string(reason))
	}
	sort.Strings(reasons)
	for _, reason := range reasons {
		t.Logf("source reason %s=%d", reason, reasonCounts[model.ReasonCode(reason)])
	}
	require.Equal(t, 151, dashboards)
	require.Equal(t, 3186, panels)
	require.Equal(t, 4973, queries)
	require.Equal(t, 51745, sourceFeatures)
	require.Equal(t, 2, expressionTargets)
	require.Equal(t, 502, instantTargets)
	require.Equal(t, 2108, intervalControlledTargets)
	require.Equal(t, map[model.ReasonCode]int{
		model.ReasonAnnotationQuery:           446,
		model.ReasonDashboardLink:             78,
		model.ReasonFieldOverrides:            1776,
		model.ReasonFieldThresholds:           1558,
		model.ReasonGrafanaIntervalControl:    1059,
		model.ReasonGrafanaPanelDescription:   765,
		model.ReasonGrafanaVariableLegend:     1970,
		model.ReasonGrafanaVariablePanelTitle: 66,
		model.ReasonLegacyPanelAlert:          12,
		model.ReasonPanelLink:                 20,
		model.ReasonUnmappedDashboardConfig:   2827,
		model.ReasonUnmappedQueryConfig:       5309,
		model.ReasonUnmappedVariableConfig:    3629,
		model.ReasonUnmappedVisualization:     32141,
		model.ReasonVisualizationDowngrade:    89,
	}, reasonCounts)
}
