package transpile

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/mansiverma897993/signoz/internal/model"
	"github.com/mansiverma897993/signoz/internal/source/grafana"
	"github.com/stretchr/testify/require"
)

func TestCorpusAccountsForEveryTarget(t *testing.T) {
	root := os.Getenv("PROMCAST_RESEARCH_ROOT")
	if root == "" {
		t.Skip("PROMCAST_RESEARCH_ROOT is not set")
	}

	analyzer := NewAnalyzer(Options{})
	counts := map[model.Verdict]int{}
	var dashboards int
	var targets int
	var parseErrors int
	var parseErrorSamples []string
	reasonCounts := make(map[model.ReasonCode]int)
	roots := []string{
		filepath.Join(root, "corpus", "top"),
		filepath.Join(root, "corpus", "mixin"),
		filepath.Join(root, "corpus-complex"),
	}
	for _, corpusRoot := range roots {
		err := filepath.WalkDir(corpusRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			require.NoError(t, walkErr)
			if entry.IsDir() || filepath.Ext(path) != ".json" || strings.Contains(path, string(filepath.Separator)+"_census"+string(filepath.Separator)) {
				return nil
			}

			dashboard, err := grafana.ParseFile(path)
			require.NoError(t, err, path)
			dashboards++
			for _, panel := range dashboard.Panels {
				for _, query := range panel.Queries {
					translation := analyzer.Analyze(query)
					require.NotEmpty(t, translation.Decision.Verdict, "%s %s", path, query.SourcePath)
					counts[translation.Decision.Verdict]++
					for _, reason := range translation.Decision.Reasons {
						reasonCounts[reason]++
					}
					targets++
					if len(translation.ParseErrors) > 0 {
						parseErrors++
						if len(parseErrorSamples) < 10 {
							parseErrorSamples = append(parseErrorSamples, query.Expression+" :: "+translation.ParseErrors[0].Message)
						}
					}
				}
			}
			return nil
		})
		require.NoError(t, err)
	}

	t.Logf("accounted for %d targets: native=%d passthrough=%d needs_review=%d parse_errors=%d", targets, counts[model.VerdictNative], counts[model.VerdictPassthrough], counts[model.VerdictNeedsReview], parseErrors)
	reasons := make([]string, 0, len(reasonCounts))
	for reason := range reasonCounts {
		reasons = append(reasons, string(reason))
	}
	sort.Strings(reasons)
	for _, reason := range reasons {
		t.Logf("query reason %s=%d", reason, reasonCounts[model.ReasonCode(reason)])
	}
	for _, sample := range parseErrorSamples {
		t.Log(sample)
	}
	require.Equal(t, 151, dashboards)
	require.Equal(t, 4973, targets)
	require.Equal(t, 0, counts[model.VerdictNative])
	require.Equal(t, 73, counts[model.VerdictPassthrough])
	require.Equal(t, 4900, counts[model.VerdictNeedsReview])
	require.Equal(t, map[model.ReasonCode]int{
		model.ReasonDynamicRewriteConflict:         879,
		model.ReasonDynamicStructure:               913,
		model.ReasonEmptyExpression:                41,
		model.ReasonGrafanaExpression:              2,
		model.ReasonGrafanaIntervalControl:         2108,
		model.ReasonGrafanaQueryFormat:             359,
		model.ReasonGrafanaVariableLegend:          1970,
		model.ReasonHiddenTarget:                   41,
		model.ReasonInstantQuery:                   497,
		model.ReasonMetricTypeRequired:             803,
		model.ReasonNonstandardQuantile:            25,
		model.ReasonNonPromDatasource:              252,
		model.ReasonResourceLabelRemap:             1563,
		model.ReasonRefIDNormalized:                1820,
		model.ReasonRateIntervalRewrite:            2046,
		model.ReasonRecordingRuleMetric:            274,
		model.ReasonRegexVariable:                  2915,
		model.ReasonSubquery:                       5,
		model.ReasonTargetOnlyLabelSemanticUse:     21,
		model.ReasonTargetVectorMatching:           281,
		model.ReasonTargetVectorMatchingUnresolved: 128,
		model.ReasonTopKSemantics:                  45,
		model.ReasonUnmappedQueryConfig:            2429,
		model.ReasonUnsupportedFunction:            41,
		model.ReasonUnsupportedModifier:            22,
		model.ReasonUnsupportedOperator:            1,
		model.ReasonVectorMatching:                 1005,
		model.ReasonWithoutClause:                  11,
	}, reasonCounts)
	require.Zero(t, parseErrors)
	require.Equal(t, targets, counts[model.VerdictNative]+counts[model.VerdictPassthrough]+counts[model.VerdictNeedsReview])
}
