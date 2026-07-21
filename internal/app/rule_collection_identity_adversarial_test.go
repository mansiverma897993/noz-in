package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mansiverma897993/signoz/internal/rules"
	"github.com/mansiverma897993/signoz/internal/target/signoz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigratePrometheusRulesDisambiguatesSameAlertAcrossFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	first := filepath.Join(root, "cpu.yaml")
	second := filepath.Join(root, "memory.yaml")
	require.NoError(t, os.WriteFile(first, []byte(`groups:
  - name: cpu
    rules:
      - alert: Saturation
        expr: cpu_usage > 0.9
        labels:
          severity: warning
`), 0o600))
	require.NoError(t, os.WriteFile(second, []byte(`groups:
  - name: memory
    rules:
      - alert: Saturation
        expr: memory_usage > 0.9
        labels:
          severity: warning
`), 0o600))

	migrate := func(paths []string) map[string]string {
		t.Helper()
		results, err := MigratePrometheusRules(context.Background(), paths, RuleOptions{
			OutputDirectory: t.TempDir(), SourceNamespace: "prometheus:production",
		})
		require.NoError(t, err)
		require.Len(t, results, 2)
		byID := make(map[string]string)
		for _, result := range results {
			data, readErr := os.ReadFile(result.RulesPath)
			require.NoError(t, readErr)
			var payloads []signoz.AlertRuleV2
			require.NoError(t, json.Unmarshal(data, &payloads))
			require.Len(t, payloads, 1)
			byID[payloads[0].Labels["promcast_id"]] = payloads[0].Alert
		}
		return byID
	}

	forward := migrate([]string{first, second})
	reverse := migrate([]string{second, first})
	assert.Equal(t, forward, reverse)
	require.Len(t, forward, 2)
	names := make(map[string]bool)
	for _, name := range forward {
		names[name] = true
	}
	assert.Len(t, names, 2)
}

func TestValidateTargetRuleNamesRejectsCollisionBeforePublication(t *testing.T) {
	t.Parallel()

	migrations := []rules.Migration{{Groups: []rules.GroupMigration{{Rules: []rules.RuleMigration{
		{Payload: &signoz.AlertRuleV2{Alert: "Collision", Labels: map[string]string{"promcast_id": "id-one"}}},
		{Payload: &signoz.AlertRuleV2{Alert: "Collision", Labels: map[string]string{"promcast_id": "id-two"}}},
	}}}}}
	err := validateTargetRuleNames(migrations)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "share target alert name")
}
