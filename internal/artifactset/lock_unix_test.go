//go:build !windows

package artifactset

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommitRefusesSymlinkLockWithoutTouchingTarget(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	reportPath := filepath.Join(directory, "hosts.report.json")
	binding, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	victim := filepath.Join(t.TempDir(), "victim")
	require.NoError(t, os.WriteFile(victim, []byte("unchanged"), 0o644))
	require.NoError(t, os.Symlink(victim, filepath.Join(directory, lockName(binding.Path))))

	err = Commit(reportPath, binding, KindDashboard, dashboardArtifacts(t, directory, binding, "blocked"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a regular file")
	data, readErr := os.ReadFile(victim)
	require.NoError(t, readErr)
	assert.Equal(t, "unchanged", string(data))
	info, statErr := os.Stat(victim)
	require.NoError(t, statErr)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

func TestPinnedCommitCannotBeRedirectedByDirectorySymlinkSwap(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	directory := filepath.Join(parent, "output")
	moved := filepath.Join(parent, "moved")
	outside := filepath.Join(parent, "outside")
	require.NoError(t, os.Mkdir(directory, 0o700))
	require.NoError(t, os.Mkdir(outside, 0o700))
	reportPath := filepath.Join(directory, "hosts.report.json")
	binding, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	artifacts, err := normalizeArtifacts(reportPath, binding, KindDashboard, dashboardArtifacts(t, directory, binding, "pinned"))
	require.NoError(t, err)
	pinned, lock, err := openLockedPinnedDirectory(directory, lockName(binding.Path))
	require.NoError(t, err)
	defer func() { require.NoError(t, pinned.Close()) }()
	defer func() { require.NoError(t, lock.Close()) }()

	require.NoError(t, os.Rename(directory, moved))
	require.NoError(t, os.Symlink(outside, directory))
	require.NoError(t, commitLocked(pinned, filepath.Base(reportPath), binding, KindDashboard, artifacts))

	assert.FileExists(t, filepath.Join(moved, "hosts.signoz.json"))
	assert.FileExists(t, filepath.Join(moved, "hosts.report.json"))
	assert.FileExists(t, filepath.Join(moved, binding.Path))
	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	assert.Empty(t, entries, "rooted publication wrote through the substituted symlink")
}

func TestCommitRejectsDirectorySymlinkSubstitutedBeforePin(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	directory := filepath.Join(parent, "output")
	moved := filepath.Join(parent, "moved")
	outside := filepath.Join(parent, "outside")
	require.NoError(t, os.Mkdir(directory, 0o700))
	require.NoError(t, os.Mkdir(outside, 0o700))
	reportPath := filepath.Join(directory, "hosts.report.json")
	binding, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	artifacts := dashboardArtifacts(t, directory, binding, "blocked")

	require.NoError(t, os.Rename(directory, moved))
	require.NoError(t, os.Symlink(outside, directory))
	err = Commit(reportPath, binding, KindDashboard, artifacts)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a real directory")
	entries, readErr := os.ReadDir(outside)
	require.NoError(t, readErr)
	assert.Empty(t, entries, "commit created a lock or artifact through the substituted output symlink")
}
