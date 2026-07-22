package app

import (
	"path/filepath"
	"testing"

	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadRuleSetsBuildsRecordingRuleIndex(t *testing.T) {
	t.Parallel()

	sets, recordings, err := loadRuleSets([]string{
		filepath.Join("..", "source", "prometheus", "testdata", "rules.yaml"),
	})

	require.NoError(t, err)
	assert.NotEmpty(t, sets)
	assert.NotEmpty(t, recordings)
}

func TestRecordingRuleIndexAppliesGroupLabelsBeforeInliningChecks(t *testing.T) {
	t.Parallel()

	sets := []model.RuleSet{{Groups: []model.RuleGroup{{
		Name: "recordings", Labels: map[string]string{"cluster": "production", "owner": "platform"},
		Rules: []model.Rule{{
			Record: "job:requests:rate5m", Expression: "sum(rate(requests_total[5m]))",
			Labels: map[string]string{"owner": "service-team"},
		}},
	}}}}

	index, err := recordingRuleIndex(sets, []bool{true})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"cluster": "production", "owner": "service-team",
	}, index["job:requests:rate5m"].Labels)
	assert.Equal(t, "platform", sets[0].Groups[0].Labels["owner"])
}
