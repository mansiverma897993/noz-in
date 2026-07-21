package artifactbind

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/mansiverma897993/signoz/pkg/reporttypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateAdjacentRejectsChangedOrSymlinkedArtifact(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	reportPath := filepath.Join(directory, "dash.report.json")
	artifactPath := filepath.Join(directory, "dash.signoz.json")
	data := []byte("{}\n")
	require.NoError(t, os.WriteFile(artifactPath, data, 0o600))
	digest := sha256.Sum256(data)
	binding := &reporttypes.ArtifactBinding{
		Path: filepath.Base(artifactPath), SHA256: fmt.Sprintf("%x", digest[:]), SizeBytes: int64(len(data)),
	}
	require.NoError(t, ValidateAdjacent(reportPath, binding, ".signoz.json", 1024))

	require.NoError(t, os.WriteFile(artifactPath, []byte("[]\n"), 0o600))
	err := ValidateAdjacent(reportPath, binding, ".signoz.json", 1024)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match report")

	require.NoError(t, os.Remove(artifactPath))
	require.NoError(t, os.Symlink(filepath.Join(directory, "outside"), artifactPath))
	err = ValidateAdjacent(reportPath, binding, ".signoz.json", 1024)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a regular file")
}
