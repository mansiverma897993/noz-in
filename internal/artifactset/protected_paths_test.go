package artifactset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProtectedPathsIncludesPointerBoundGenerationsAndClassifiedOrphan(t *testing.T) {
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "hosts.report.json")
	previous, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(
		reportPath,
		previous,
		KindDashboard,
		dashboardArtifacts(t, directory, previous, "previous"),
	))
	current, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(
		reportPath,
		current,
		KindDashboard,
		dashboardArtifacts(t, directory, current, "current"),
	))
	orphan, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	publishOrphanGeneration(t, reportPath, orphan, "orphan")

	protected, err := ProtectedPathsForReport(reportPath, current, KindDashboard)
	require.NoError(t, err)
	container := generationContainerName(current.Path)
	for _, generation := range []string{previous.Generation, current.Generation, orphan.Generation} {
		generationPath := filepath.Join(directory, container, generation)
		assert.Contains(t, protected, generationPath)
		for _, name := range []string{
			stageOwnerName,
			"hosts.signoz.json",
			"hosts.report.json",
			"hosts.report.html",
			current.Path,
		} {
			assert.Contains(t, protected, filepath.Join(generationPath, name))
		}
	}
	assert.Contains(t, protected, filepath.Join(directory, "hosts.candidate.signoz.json"))
	assert.Contains(t, protected, filepath.Join(directory, lockName(current.Path)))
}

func TestProtectedPathsRejectsHostileGenerationEntryCounts(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string, string)
		limit  string
	}{
		{
			name: "generation container",
			mutate: func(t *testing.T, directory string, manifestName string) {
				container := filepath.Join(directory, generationContainerName(manifestName))
				for _, digit := range []string{"a", "b", "c"} {
					require.NoError(t, os.Mkdir(filepath.Join(container, strings.Repeat(digit, 32)), 0o700))
				}
			},
			limit: "more than 3 entries",
		},
		{
			name: "generation members",
			mutate: func(t *testing.T, directory string, manifestName string) {
				root, err := os.OpenRoot(directory)
				require.NoError(t, err)
				pointer, found, err := readGenerationPointer(root, manifestName)
				require.NoError(t, err)
				require.True(t, found)
				require.NoError(t, root.Close())
				generation := filepath.Join(directory, generationContainerName(manifestName), pointer.Generation)
				require.NoError(t, os.WriteFile(filepath.Join(generation, "unmanifested"), []byte("x"), 0o600))
			},
			limit: "more than 5 entries",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			reportPath := filepath.Join(directory, "hosts.report.json")
			binding, err := NewBindingForReport(reportPath, KindDashboard)
			require.NoError(t, err)
			require.NoError(t, Commit(
				reportPath,
				binding,
				KindDashboard,
				dashboardArtifacts(t, directory, binding, "stable"),
			))
			test.mutate(t, directory, binding.Path)

			_, err = ProtectedPathsForReport(reportPath, binding, KindDashboard)
			require.ErrorContains(t, err, test.limit)
		})
	}
}

func TestProtectedPathsRejectsSymlinkedGenerationMember(t *testing.T) {
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "hosts.report.json")
	binding, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(
		reportPath,
		binding,
		KindDashboard,
		dashboardArtifacts(t, directory, binding, "stable"),
	))
	primary := filepath.Join(
		directory,
		generationContainerName(binding.Path),
		binding.Generation,
		"hosts.signoz.json",
	)
	require.NoError(t, os.Remove(primary))
	if err := os.Symlink("hosts.report.html", primary); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}

	_, err = ProtectedPathsForReport(reportPath, binding, KindDashboard)
	require.ErrorContains(t, err, "not a regular file")
}
