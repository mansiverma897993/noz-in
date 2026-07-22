package report

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mansiverma897993/noz-in/internal/migrate"
	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/mansiverma897993/noz-in/internal/source/grafana"
	"github.com/mansiverma897993/noz-in/internal/transpile"
	"github.com/stretchr/testify/require"
)

func TestCorpusReportsReconcileEverySourceObject(t *testing.T) {
	root := os.Getenv("PROMCAST_RESEARCH_ROOT")
	if root == "" {
		t.Skip("PROMCAST_RESEARCH_ROOT is not set")
	}

	var dashboards int
	var panelsNeedsReview int
	var panelsOmitted int
	var sourceFeatures int
	var dynamicConflictPanels int
	var dynamicConflictQueries int
	var unresolvedAllPanels int
	var unresolvedAllQueries int
	var unresolvedAllVariables int
	var overlappingPanels int
	var overlappingQueries int
	var omittedNeither int
	var vectorMatchingQueries int
	var unresolvedVectorMatchingQueries int
	hasReason := func(reasons []string, reason model.ReasonCode) bool {
		return slices.Contains(reasons, string(reason))
	}
	for _, corpusRoot := range []string{
		filepath.Join(root, "corpus", "top"),
		filepath.Join(root, "corpus", "mixin"),
		filepath.Join(root, "corpus-complex"),
	} {
		err := filepath.WalkDir(corpusRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			require.NoError(t, walkErr)
			if entry.IsDir() || filepath.Ext(path) != ".json" || strings.Contains(path, string(filepath.Separator)+"_census"+string(filepath.Separator)) {
				return nil
			}
			dashboard, err := grafana.ParseFile(path)
			require.NoError(t, err, path)
			evidence := Build(migrate.Dashboard(dashboard, transpile.NewAnalyzer(transpile.Options{})))
			require.True(t, evidence.Summary.ReconciliationComplete, path)
			dashboards++
			panelsNeedsReview += evidence.Summary.PanelsNeedsReview
			panelsOmitted += evidence.Summary.PanelsOmitted
			sourceFeatures += evidence.Summary.SourceFeatures
			for _, panel := range evidence.Panels {
				panelDynamic := false
				panelAll := false
				for _, query := range panel.Queries {
					dynamic := hasReason(query.ReasonCodes, model.ReasonDynamicRewriteConflict)
					all := hasReason(query.ReasonCodes, model.ReasonMissingVariableValue) &&
						hasReason(query.ReasonCodes, model.ReasonVariableAllValue)
					if dynamic {
						dynamicConflictQueries++
						panelDynamic = true
					}
					if all {
						unresolvedAllQueries++
						panelAll = true
					}
					if dynamic && all {
						overlappingQueries++
					}
					if hasReason(query.ReasonCodes, model.ReasonTargetVectorMatching) {
						vectorMatchingQueries++
					}
					if hasReason(query.ReasonCodes, model.ReasonTargetVectorMatchingUnresolved) {
						unresolvedVectorMatchingQueries++
					}
				}
				if panel.EmittedKind != "omitted" {
					continue
				}
				if panelDynamic {
					dynamicConflictPanels++
				}
				if panelAll {
					unresolvedAllPanels++
				}
				if panelDynamic && panelAll {
					overlappingPanels++
				}
				if !panelDynamic && !panelAll {
					omittedNeither++
				}
			}
			for _, variable := range evidence.Variables {
				if hasReason(variable.ReasonCodes, model.ReasonMissingVariableValue) &&
					hasReason(variable.ReasonCodes, model.ReasonVariableAllValue) {
					unresolvedAllVariables++
				}
			}
			return nil
		})
		require.NoError(t, err)
	}

	t.Logf("reconciled dashboards=%d panels_needs_review=%d panels_omitted=%d source_features=%d dynamic_panels=%d dynamic_queries=%d all_panels=%d all_queries=%d all_variables=%d vector_rewrites=%d",
		dashboards, panelsNeedsReview, panelsOmitted, sourceFeatures,
		dynamicConflictPanels, dynamicConflictQueries, unresolvedAllPanels,
		unresolvedAllQueries, unresolvedAllVariables, vectorMatchingQueries)
	require.Equal(t, 151, dashboards)
	require.Equal(t, 3007, panelsNeedsReview)
	require.Equal(t, 1978, panelsOmitted)
	require.Equal(t, 51745, sourceFeatures)
	require.Equal(t, 297, dynamicConflictPanels)
	require.Equal(t, 879, dynamicConflictQueries)
	require.Equal(t, 384, unresolvedAllPanels)
	require.Equal(t, 811, unresolvedAllQueries)
	require.Equal(t, 30, unresolvedAllVariables)
	require.Equal(t, 2, overlappingPanels)
	require.Zero(t, overlappingQueries)
	require.Equal(t, 1299, omittedNeither)
	require.Equal(t, 281, vectorMatchingQueries)
	require.Equal(t, 128, unresolvedVectorMatchingQueries)
}
