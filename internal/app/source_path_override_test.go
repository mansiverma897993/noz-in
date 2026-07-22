package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGrafanaSourcePathOverrideIsPersistedInEvidence(t *testing.T) {
	t.Parallel()

	input := "../source/grafana/testdata/modern.json"
	virtualPath := filepath.Join(t.TempDir(), "source.grafana.json")
	results, err := MigrateGrafana(context.Background(), []string{input}, GrafanaOptions{
		OutputDirectory:     t.TempDir(),
		SourcePathOverrides: map[string]string{input: virtualPath},
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, virtualPath, results[0].Evidence.Source.Path)

	data, err := os.ReadFile(results[0].ReportPath)
	require.NoError(t, err)
	var persisted reporttypes.Report
	require.NoError(t, json.Unmarshal(data, &persisted))
	assert.Equal(t, virtualPath, persisted.Source.Path)
}
