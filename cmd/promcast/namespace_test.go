package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mansiverma897993/signoz/pkg/reporttypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSourceNamespaceFlagsReachMigrationEvidence(t *testing.T) {
	t.Run("grafana", func(t *testing.T) {
		outputDirectory := t.TempDir()
		_, err := runCommand(t,
			"grafana", "../../internal/source/grafana/testdata/modern.json",
			"--offline", "--source-namespace", "grafana:production",
			"--source-identity", "dashboards/service-overview", "--out", outputDirectory,
		)
		assert.Equal(t, 2, commandExitCode(err))

		data, readErr := os.ReadFile(filepath.Join(outputDirectory, "modern.report.json"))
		require.NoError(t, readErr)
		var evidence reporttypes.Report
		require.NoError(t, json.Unmarshal(data, &evidence))
		assert.Equal(t, "grafana:production", evidence.Source.Namespace)
		assert.Equal(t, "dashboards/service-overview", evidence.Source.Identity)
	})

	t.Run("prometheus rules", func(t *testing.T) {
		outputDirectory := t.TempDir()
		_, err := runCommand(t,
			"rules", "../../internal/source/prometheus/testdata/rules.yaml",
			"--offline", "--source-namespace", "prometheus:production", "--out", outputDirectory,
		)
		assert.Equal(t, 2, commandExitCode(err))

		data, readErr := os.ReadFile(filepath.Join(outputDirectory, "rules.rules-report.json"))
		require.NoError(t, readErr)
		var evidence reporttypes.RuleReport
		require.NoError(t, json.Unmarshal(data, &evidence))
		assert.Equal(t, "prometheus:production", evidence.Source.Namespace)
	})
}
