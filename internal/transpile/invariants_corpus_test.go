package transpile

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mansiverma897993/signoz/internal/model"
	"github.com/mansiverma897993/signoz/internal/source/grafana"
	"github.com/stretchr/testify/require"
)

// TestCorpusEmitsNoSentinelLeakOrUnprovenNative enforces two safety invariants
// over every corpus query, the class of bug the hostile audit found:
//
//  1. No sentinel placeholder ever survives into an emitted query. The parser
//     probe substitutes variables with placeholder literals ("1", "5m",
//     "sm_var_*"); none of these may appear in a shipped PromQL string, builder
//     metric name, or filter value.
//  2. Native is never claimed offline. Without a live differential the analyzer
//     cannot prove equivalence, so no translation may carry a native verdict.
func TestCorpusEmitsNoSentinelLeakOrUnprovenNative(t *testing.T) {
	root := os.Getenv("PROMCAST_RESEARCH_ROOT")
	if root == "" {
		t.Skip("PROMCAST_RESEARCH_ROOT is not set")
	}
	analyzer := NewAnalyzer(Options{})
	roots := []string{
		filepath.Join(root, "corpus", "top"),
		filepath.Join(root, "corpus", "mixin"),
		filepath.Join(root, "corpus-complex"),
	}
	for _, corpusRoot := range roots {
		require.NoError(t, filepath.WalkDir(corpusRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			require.NoError(t, walkErr)
			if entry.IsDir() || filepath.Ext(path) != ".json" ||
				strings.Contains(path, string(filepath.Separator)+"_census"+string(filepath.Separator)) {
				return nil
			}
			dashboard, err := grafana.ParseFile(path)
			require.NoError(t, err, path)
			for _, panel := range dashboard.Panels {
				for _, query := range panel.Queries {
					translation := analyzer.Analyze(query)
					assertNoSentinelLeak(t, path, query.SourcePath, translation)
					require.NotEqual(t, model.VerdictNative, translation.Decision.Verdict,
						"offline analysis must never claim native: %s %s", path, query.SourcePath)
				}
			}
			return nil
		}))
	}
}

func assertNoSentinelLeak(t *testing.T, path, sourcePath string, translation model.Translation) {
	t.Helper()
	require.NotContains(t, translation.PromQL, "sm_var_",
		"sentinel identifier leaked into emitted PromQL: %s %s", path, sourcePath)
	check := func(builder *model.BuilderQuery) {
		if builder == nil {
			return
		}
		require.NotContains(t, builder.MetricName, "sm_var_", "%s %s", path, sourcePath)
		for _, filter := range builder.Filters {
			require.NotContains(t, filter.Value, "sm_var_",
				"sentinel leaked into builder filter: %s %s", path, sourcePath)
			require.NotContains(t, filter.Label, "sm_var_", "%s %s", path, sourcePath)
		}
	}
	check(translation.Builder)
	if translation.Formula != nil {
		require.NotContains(t, translation.Formula.Expression, "sm_var_", "%s %s", path, sourcePath)
		for index := range translation.Formula.Queries {
			check(&translation.Formula.Queries[index])
		}
	}
}
