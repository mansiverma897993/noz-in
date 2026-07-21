package prometheus

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCorpusParsesEveryRuleFile(t *testing.T) {
	root := os.Getenv("PROMCAST_RESEARCH_ROOT")
	if root == "" {
		t.Skip("PROMCAST_RESEARCH_ROOT is not set")
	}

	var files int
	var groups int
	var alerting int
	var recording int
	err := filepath.WalkDir(filepath.Join(root, "corpus-complex"), func(path string, entry fs.DirEntry, walkErr error) error {
		require.NoError(t, walkErr)
		if entry.IsDir() || filepath.Ext(path) != ".yaml" && filepath.Ext(path) != ".yml" {
			return nil
		}
		rules, err := ParseFile(path)
		require.NoError(t, err, path)
		if len(rules.Groups) == 0 {
			return nil
		}
		files++
		groups += len(rules.Groups)
		for _, group := range rules.Groups {
			for _, rule := range group.Rules {
				if rule.IsAlerting() {
					alerting++
				}
				if rule.IsRecording() {
					recording++
				}
			}
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 17, files)
	require.Equal(t, 84, groups)
	require.Equal(t, 295, alerting)
	require.Equal(t, 250, recording)
	t.Logf("parsed %d rule files, %d groups, %d alerting rules, and %d recording rules", files, groups, alerting, recording)
}
