package metricmap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "metrics.yaml")
	require.NoError(t, os.WriteFile(path, []byte("http_requests_total: http.server.request.count\n"), 0o600))
	result, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "http.server.request.count", result["http_requests_total"])
}

func TestLoadRejectsDuplicateAndTrailingDocuments(t *testing.T) {
	t.Parallel()

	for name, contents := range map[string]string{
		"duplicate": "source: first\nsource: second\n",
		"documents": "source: first\n---\nsource: second\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "metrics.yaml")
			require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
			_, err := Load(path)
			require.Error(t, err)
		})
	}
}
