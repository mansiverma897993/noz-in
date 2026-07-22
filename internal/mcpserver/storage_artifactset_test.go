package mcpserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mansiverma897993/noz-in/internal/artifactset"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrivateStagingPublishesBoundImmutableTreeAndCountsQuota(t *testing.T) {
	service, output := newQuotaTestService(t, Config{})
	staging, binding := stagedDashboardArtifactSet(t)
	relative := preparePublishedStorageDestination(t, service)

	require.NoError(t, service.publishPrivateStagingDirectory(
		staging,
		relative,
		binding,
		nil,
	))
	layout, err := artifactset.StorageLayoutForBinding(*binding)
	require.NoError(t, err)
	assert.DirExists(t, filepath.Join(output, relative, layout.Generations, binding.Generation))
	assert.FileExists(t, filepath.Join(output, relative, layout.Pointer))
	assert.NoFileExists(t, filepath.Join(output, relative, layout.Lock))

	usage, err := service.measureOutputUsage()
	require.NoError(t, err)
	// Parent + destination, four stable facades, pointer, generation
	// container + directory, four immutable generation files, and the bounded
	// generation ownership marker used for crash-safe pruning.
	assert.Equal(t, int64(14), usage.entries)
}

func TestPrivateStagingQuotaAccountsForHiddenImmutableTree(t *testing.T) {
	service, _ := newQuotaTestService(t, Config{MaxOutputEntries: 7})
	staging, binding := stagedDashboardArtifactSet(t)
	relative := preparePublishedStorageDestination(t, service)

	err := service.publishPrivateStagingDirectory(
		staging,
		relative,
		binding,
		nil,
	)
	require.ErrorContains(t, err, "entry quota would be exceeded")
	usage, measureErr := service.measureOutputUsage()
	require.NoError(t, measureErr)
	assert.Equal(t, int64(2), usage.entries, "full-tree quota admission must precede child publication")
}

func TestPrivateStagingRetainsAndAccountsForOnlyCurrentAndPreviousGenerations(t *testing.T) {
	for _, test := range []struct {
		name               string
		maxEntries         int
		wantError          bool
		wantPublishedUsage int64
	}{
		{name: "exact quota succeeds", maxEntries: 20, wantPublishedUsage: 20},
		{name: "one entry below fails before copy", maxEntries: 19, wantError: true, wantPublishedUsage: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, output := newQuotaTestService(t, Config{MaxOutputEntries: test.maxEntries})
			staging, binding, generations := stagedDashboardArtifactSetGenerations(t, 3)
			layout, err := artifactset.StorageLayoutForBinding(*binding)
			require.NoError(t, err)
			assert.NoDirExists(t, filepath.Join(staging, layout.Generations, generations[0]))
			assert.DirExists(t, filepath.Join(staging, layout.Generations, generations[1]))
			assert.DirExists(t, filepath.Join(staging, layout.Generations, generations[2]))
			relative := preparePublishedStorageDestination(t, service)

			err = service.publishPrivateStagingDirectory(staging, relative, binding, nil)
			if test.wantError {
				require.ErrorContains(t, err, "entry quota would be exceeded")
			} else {
				require.NoError(t, err)
				assert.NoDirExists(t, filepath.Join(output, relative, layout.Generations, generations[0]))
				assert.DirExists(t, filepath.Join(output, relative, layout.Generations, generations[1]))
				assert.DirExists(t, filepath.Join(output, relative, layout.Generations, generations[2]))
			}
			usage, measureErr := service.measureOutputUsage()
			require.NoError(t, measureErr)
			assert.Equal(t, test.wantPublishedUsage, usage.entries)
		})
	}
}

func TestPrivateStagingRejectsValidShapedUnboundHiddenTree(t *testing.T) {
	service, _ := newQuotaTestService(t, Config{})
	staging, binding := stagedDashboardArtifactSet(t)
	layout, err := artifactset.StorageLayoutForBinding(*binding)
	require.NoError(t, err)
	unbound := ".promcast-generations-" + strings.Repeat("a", 64)
	require.NotEqual(t, layout.Generations, unbound)
	require.NoError(t, os.Mkdir(filepath.Join(staging, unbound), 0o700))
	relative := preparePublishedStorageDestination(t, service)

	err = service.publishPrivateStagingDirectory(
		staging,
		relative,
		binding,
		nil,
	)
	require.ErrorContains(t, err, "unrecognized hidden entry")
}

func TestPrivateStagingRejectsExtraVisibleRootEntries(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{
			name: "regular file",
			mutate: func(t *testing.T, staging string) {
				require.NoError(t, os.WriteFile(filepath.Join(staging, "extra.txt"), []byte("smuggled"), 0o600))
			},
		},
		{
			name: "directory",
			mutate: func(t *testing.T, staging string) {
				require.NoError(t, os.Mkdir(filepath.Join(staging, "extra-dir"), 0o700))
			},
		},
		{
			name: "symlink",
			mutate: func(t *testing.T, staging string) {
				if err := os.Symlink("validated.report.json", filepath.Join(staging, "extra-link")); err != nil {
					t.Skipf("symlinks are unavailable: %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _ := newQuotaTestService(t, Config{})
			staging, binding := stagedDashboardArtifactSet(t)
			test.mutate(t, staging)
			relative := preparePublishedStorageDestination(t, service)

			err := service.publishPrivateStagingDirectory(
				staging,
				relative,
				binding,
				nil,
			)
			require.ErrorContains(t, err, "unrecognized root entry")
		})
	}
}

func TestPrivateStagingRejectsUnmanifestedGenerationMembers(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string, reporttypes.ArtifactSetBinding)
	}{
		{
			name: "regular file",
			mutate: func(t *testing.T, generation string, _ reporttypes.ArtifactSetBinding) {
				require.NoError(t, os.WriteFile(filepath.Join(generation, "extra.txt"), []byte("smuggled"), 0o600))
			},
		},
		{
			name: "nested directory",
			mutate: func(t *testing.T, generation string, _ reporttypes.ArtifactSetBinding) {
				require.NoError(t, os.Mkdir(filepath.Join(generation, "nested"), 0o700))
			},
		},
		{
			name: "symlink member",
			mutate: func(t *testing.T, generation string, _ reporttypes.ArtifactSetBinding) {
				primary := filepath.Join(generation, "validated.signoz.json")
				require.NoError(t, os.Remove(primary))
				if err := os.Symlink("validated.report.html", primary); err != nil {
					t.Skipf("symlinks are unavailable: %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _ := newQuotaTestService(t, Config{})
			staging, binding := stagedDashboardArtifactSet(t)
			layout, err := artifactset.StorageLayoutForBinding(*binding)
			require.NoError(t, err)
			generation := filepath.Join(staging, layout.Generations, binding.Generation)
			test.mutate(t, generation, *binding)
			relative := preparePublishedStorageDestination(t, service)

			err = service.publishPrivateStagingDirectory(
				staging,
				relative,
				binding,
				nil,
			)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "retained")
		})
	}
}

func stagedDashboardArtifactSet(t *testing.T) (string, *reporttypes.ArtifactSetBinding) {
	t.Helper()
	directory := t.TempDir()
	evidence := reporttypes.Report{
		SchemaVersion: "1",
		Dashboard:     reporttypes.DashboardInfo{Title: "Retained storage"},
	}
	_, err := stageValidationArtifactSet(
		directory,
		&evidence,
		[]byte("{\"schemaVersion\":\"v5\",\"title\":\"Retained storage\"}\n"),
	)
	require.NoError(t, err)
	require.NotNil(t, evidence.ArtifactSet)
	return directory, evidence.ArtifactSet
}

func stagedDashboardArtifactSetGenerations(
	t *testing.T,
	count int,
) (string, *reporttypes.ArtifactSetBinding, []string) {
	t.Helper()
	directory := t.TempDir()
	evidence := reporttypes.Report{
		SchemaVersion: "1",
		Dashboard:     reporttypes.DashboardInfo{Title: "Bounded retained storage"},
	}
	generations := make([]string, 0, count)
	for range count {
		_, err := stageValidationArtifactSet(
			directory,
			&evidence,
			[]byte("{\"schemaVersion\":\"v5\",\"title\":\"Bounded retained storage\"}\n"),
		)
		require.NoError(t, err)
		require.NotNil(t, evidence.ArtifactSet)
		generations = append(generations, evidence.ArtifactSet.Generation)
	}
	return directory, evidence.ArtifactSet, generations
}

func preparePublishedStorageDestination(t *testing.T, service *Service) string {
	t.Helper()
	require.NoError(t, service.createOutputDirectory("migration"))
	relative := filepath.Join("migration", resultGeneration)
	require.NoError(t, service.createOutputDirectory(relative))
	return relative
}
