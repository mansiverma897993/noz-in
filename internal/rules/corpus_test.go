package rules

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/mansiverma897993/noz-in/internal/model"
	sourceprometheus "github.com/mansiverma897993/noz-in/internal/source/prometheus"
	"github.com/mansiverma897993/noz-in/internal/transpile"
	"github.com/stretchr/testify/require"
)

func TestCorpusAccountsForEveryRule(t *testing.T) {
	root := os.Getenv("PROMCAST_RESEARCH_ROOT")
	if root == "" {
		t.Skip("PROMCAST_RESEARCH_ROOT is not set")
	}

	analyzer := transpile.NewAnalyzer(transpile.Options{})
	counts := map[model.Verdict]int{}
	var alerting int
	var recording int
	var payloads int
	var enabled int
	var parseErrors int
	reasonCounts := make(map[model.ReasonCode]int)
	err := filepath.WalkDir(filepath.Join(root, "corpus-complex"), func(path string, entry fs.DirEntry, walkErr error) error {
		require.NoError(t, walkErr)
		if entry.IsDir() || filepath.Ext(path) != ".yaml" && filepath.Ext(path) != ".yml" {
			return nil
		}
		source, err := sourceprometheus.ParseFile(path)
		require.NoError(t, err, path)
		migration := Translate(source, analyzer)
		for _, group := range migration.Groups {
			for _, rule := range group.Rules {
				counts[rule.Decision.Verdict]++
				if rule.Source.IsAlerting() {
					alerting++
				}
				if rule.Source.IsRecording() {
					recording++
				}
				if rule.Payload != nil {
					payloads++
					if !rule.Payload.Disabled {
						enabled++
					}
				}
				for _, reason := range rule.Decision.Reasons {
					reasonCounts[reason]++
					if reason == model.ReasonParseError {
						parseErrors++
					}
				}
			}
		}
		return nil
	})
	require.NoError(t, err)
	t.Logf("accounted for %d alerting and %d recording rules: payloads=%d enabled=%d passthrough=%d needs_review=%d parse_errors=%d",
		alerting, recording, payloads, enabled, counts[model.VerdictPassthrough], counts[model.VerdictNeedsReview], parseErrors)
	reasons := make([]string, 0, len(reasonCounts))
	for reason := range reasonCounts {
		reasons = append(reasons, string(reason))
	}
	sort.Strings(reasons)
	for _, reason := range reasons {
		t.Logf("rule reason %s=%d", reason, reasonCounts[model.ReasonCode(reason)])
	}
	require.Equal(t, 295, alerting)
	require.Equal(t, 250, recording)
	require.Equal(t, 244, payloads)
	require.Zero(t, enabled)
	require.Zero(t, counts[model.VerdictPassthrough])
	require.Equal(t, 545, counts[model.VerdictNeedsReview])
	require.Equal(t, map[model.ReasonCode]int{
		model.ReasonAlertForDefault:                31,
		model.ReasonAlertForWindow:                 264,
		model.ReasonAlertNameDisambiguated:         50,
		model.ReasonAlertThreshold:                 118,
		model.ReasonAnnotationFormatting:           64,
		model.ReasonMetricTypeRequired:             119,
		model.ReasonNonstandardQuantile:            2,
		model.ReasonResourceLabelRemap:             113,
		model.ReasonRecordingRule:                  250,
		model.ReasonRecordingRuleMetric:            11,
		model.ReasonSeverityNormalized:             11,
		model.ReasonSubquery:                       8,
		model.ReasonTargetAlertRuntimeLabels:       295,
		model.ReasonTargetOnlyLabelSemanticUse:     1,
		model.ReasonTargetVectorMatching:           83,
		model.ReasonTargetVectorMatchingUnresolved: 50,
		model.ReasonTopKSemantics:                  6,
		model.ReasonUnsupportedFunction:            64,
		model.ReasonUnsupportedModifier:            2,
		model.ReasonVectorMatching:                 60,
		model.ReasonWithoutClause:                  13,
	}, reasonCounts)
	require.Zero(t, parseErrors)
	require.Equal(t, alerting+recording, counts[model.VerdictPassthrough]+counts[model.VerdictNeedsReview]+counts[model.VerdictNative])
}
