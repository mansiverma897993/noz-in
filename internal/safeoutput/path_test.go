package safeoutput

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRejectAliasesCoversLexicalSymlinkHardlinkAndMissingPaths(t *testing.T) {
	directory := t.TempDir()
	protected := filepath.Join(directory, "protected.json")
	require.NoError(t, os.WriteFile(protected, []byte("evidence"), 0o600))

	hardlink := filepath.Join(directory, "hardlink.json")
	require.NoError(t, os.Link(protected, hardlink))
	symlink := filepath.Join(directory, "symlink.json")
	require.NoError(t, os.Symlink(protected, symlink))

	linkedDirectory := filepath.Join(directory, "linked-directory")
	realDirectory := filepath.Join(directory, "real-directory")
	require.NoError(t, os.Mkdir(realDirectory, 0o700))
	require.NoError(t, os.Symlink(realDirectory, linkedDirectory))
	missingProtected := filepath.Join(realDirectory, "future.json")

	for name, destination := range map[string]string{
		"lexical":              filepath.Join(directory, ".", "protected.json"),
		"hardlink":             hardlink,
		"symlink":              symlink,
		"missing through link": filepath.Join(linkedDirectory, "future.json"),
	} {
		t.Run(name, func(t *testing.T) {
			candidate := protected
			if name == "missing through link" {
				candidate = missingProtected
			}
			err := RejectAliases(destination, ProtectedPath{Path: candidate, Purpose: "test input"})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "aliases protected test input")
		})
	}

	require.NoError(t, RejectAliases(
		filepath.Join(directory, "review.html"),
		ProtectedPath{Path: protected, Purpose: "test input"},
	))
}

func TestLexicallyEqualDoesNotTreatHardlinksAsTheDesignatedPath(t *testing.T) {
	directory := t.TempDir()
	left := filepath.Join(directory, "left")
	right := filepath.Join(directory, "right")
	require.NoError(t, os.WriteFile(left, []byte("same inode"), 0o600))
	require.NoError(t, os.Link(left, right))

	assert.True(t, LexicallyEqual(left, filepath.Join(directory, ".", "left")))
	assert.False(t, LexicallyEqual(left, right))
	aliases, err := Aliases(left, right)
	require.NoError(t, err)
	assert.True(t, aliases)
}

func TestOpenOrCreateDirectoryPinsNestedOutputAndRejectsFinalSymlink(t *testing.T) {
	directory := t.TempDir()
	output := filepath.Join(directory, "missing", "nested")
	pinned, err := OpenOrCreateDirectory(output, 0o700)
	require.NoError(t, err)
	info, err := pinned.Root().Stat(".")
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	require.NoError(t, pinned.Close())
	assert.DirExists(t, output)

	victim := filepath.Join(directory, "victim")
	require.NoError(t, os.Mkdir(victim, 0o700))
	linked := filepath.Join(directory, "linked")
	require.NoError(t, os.Symlink(victim, linked))
	_, err = OpenOrCreateDirectory(linked, 0o700)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a real directory")
}

func TestOpenOrCreateDirectoryStopsAfterParentSyncFailure(t *testing.T) {
	directory := t.TempDir()
	output := filepath.Join(directory, "first", "second")
	injected := errors.New("simulated directory sync failure")
	_, err := openDirectoryWithSync(output, true, 0o700, func(*os.Root) error {
		return injected
	})

	require.ErrorIs(t, err, injected)
	assert.DirExists(t, filepath.Join(directory, "first"))
	assert.NoDirExists(t, output, "creation continued beyond an unpersisted parent entry")
}

func TestWriteFileAtomicStaysInPinnedParentAfterSymlinkSubstitution(t *testing.T) {
	parent := t.TempDir()
	output := filepath.Join(parent, "output")
	moved := filepath.Join(parent, "moved")
	victim := filepath.Join(parent, "victim")
	require.NoError(t, os.Mkdir(output, 0o700))
	require.NoError(t, os.Mkdir(victim, 0o700))
	destination := filepath.Join(output, "report.json")

	err := writeFileAtomic(destination, []byte("pinned\n"), 0o600, func(*PinnedDirectory) error {
		if err := os.Rename(output, moved); err != nil {
			return err
		}
		return os.Symlink(victim, output)
	})

	require.NoError(t, err)
	contents, err := os.ReadFile(filepath.Join(moved, "report.json"))
	require.NoError(t, err)
	assert.Equal(t, "pinned\n", string(contents))
	entries, err := os.ReadDir(victim)
	require.NoError(t, err)
	assert.Empty(t, entries, "atomic writer followed the substituted parent symlink")
}

func TestWriteFileAtomicRejectsSymlinkDestinationWithoutTouchingTarget(t *testing.T) {
	directory := t.TempDir()
	victim := filepath.Join(directory, "victim")
	require.NoError(t, os.WriteFile(victim, []byte("unchanged"), 0o644))
	destination := filepath.Join(directory, "report.json")
	require.NoError(t, os.Symlink(victim, destination))

	err := WriteFileAtomic(destination, []byte("replacement"), 0o600)

	require.Error(t, err)
	contents, readErr := os.ReadFile(victim)
	require.NoError(t, readErr)
	assert.Equal(t, "unchanged", string(contents))
}
