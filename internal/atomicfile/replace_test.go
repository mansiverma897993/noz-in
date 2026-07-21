package atomicfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplaceOverwritesExistingDestination(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	destination := filepath.Join(directory, "destination")
	require.NoError(t, os.WriteFile(source, []byte("new"), 0o600))
	require.NoError(t, os.WriteFile(destination, []byte("old"), 0o600))

	require.NoError(t, Replace(source, destination))
	contents, err := os.ReadFile(destination)
	require.NoError(t, err)
	assert.Equal(t, "new", string(contents))
	_, err = os.Stat(source)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestReplaceRootOverwritesExistingDestination(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "source"), []byte("new"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "destination"), []byte("old"), 0o600))
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()

	require.NoError(t, ReplaceRoot(root, "source", "destination"))
	contents, err := os.ReadFile(filepath.Join(directory, "destination"))
	require.NoError(t, err)
	assert.Equal(t, "new", string(contents))
}
