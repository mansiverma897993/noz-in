package artifactset

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/mansiverma897993/signoz/pkg/reporttypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommitPublishesOneVerifiableGeneration(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	reportPath := filepath.Join(directory, "hosts.report.json")
	binding, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	artifacts := dashboardArtifacts(t, directory, binding, "one")

	require.NoError(t, Commit(reportPath, binding, KindDashboard, artifacts))

	reportData, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	snapshot, err := ReadCommitted(
		reportPath,
		reportData,
		&binding,
		KindDashboard,
		[]string{"hosts.signoz.json", "hosts.report.html"},
		1<<20,
	)
	require.NoError(t, err)
	assert.Equal(t, binding.Generation, snapshot.Manifest.Generation)
	assert.Equal(t, "primary-one", string(snapshot.Data["hosts.signoz.json"]))
	assert.Equal(t, "html-one", string(snapshot.Data["hosts.report.html"]))
	assert.FileExists(t, filepath.Join(directory, binding.Path))
	assertMode(t, directory, 0o700)
	for _, name := range []string{
		"hosts.signoz.json", "hosts.report.json", "hosts.report.html", binding.Path,
	} {
		assertMode(t, filepath.Join(directory, name), 0o600)
	}
}

func TestConcurrentCommitsNeverPublishMixedGeneration(t *testing.T) {
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "hosts.report.json")
	const writers = 24

	start := make(chan struct{})
	errors := make(chan error, writers)
	var wait sync.WaitGroup
	for index := range writers {
		wait.Go(func() {
			<-start
			binding, err := NewBindingForReport(reportPath, KindDashboard)
			if err != nil {
				errors <- err
				return
			}
			marker := fmt.Sprintf("writer-%02d", index)
			artifacts := dashboardArtifacts(t, directory, binding, marker)
			errors <- Commit(reportPath, binding, KindDashboard, artifacts)
		})
	}
	close(start)
	wait.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}

	reportData, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	var report struct {
		Marker      string                          `json:"marker"`
		ArtifactSet *reporttypes.ArtifactSetBinding `json:"artifactSet"`
	}
	require.NoError(t, json.Unmarshal(reportData, &report))
	require.NotNil(t, report.ArtifactSet)
	snapshot, err := ReadCommitted(
		reportPath,
		reportData,
		report.ArtifactSet,
		KindDashboard,
		[]string{"hosts.signoz.json", "hosts.report.html"},
		1<<20,
	)
	require.NoError(t, err)
	assert.Equal(t, "primary-"+report.Marker, string(snapshot.Data["hosts.signoz.json"]))
	assert.Equal(t, "html-"+report.Marker, string(snapshot.Data["hosts.report.html"]))
	assertPointerBoundGenerationSet(t, directory, report.ArtifactSet.Path)
}

func TestConcurrentProcessCommitsNeverPublishMixedGeneration(t *testing.T) {
	directory := t.TempDir()
	const writers = 8
	type process struct {
		command *exec.Cmd
		marker  string
	}
	processes := make([]process, 0, writers)
	for index := range writers {
		marker := fmt.Sprintf("process-%02d", index)
		command := exec.Command(os.Args[0], "-test.run=^TestArtifactSetCommitProcessHelper$")
		command.Env = append(os.Environ(),
			"PROMCAST_ARTIFACTSET_HELPER_DIR="+directory,
			"PROMCAST_ARTIFACTSET_HELPER_MARKER="+marker,
		)
		require.NoError(t, command.Start())
		processes = append(processes, process{command: command, marker: marker})
	}
	for _, child := range processes {
		require.NoError(t, child.command.Wait(), child.marker)
	}

	reportPath := filepath.Join(directory, "hosts.report.json")
	reportData, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	var report struct {
		Marker      string                          `json:"marker"`
		ArtifactSet *reporttypes.ArtifactSetBinding `json:"artifactSet"`
	}
	require.NoError(t, json.Unmarshal(reportData, &report))
	require.NotNil(t, report.ArtifactSet)
	snapshot, err := ReadCommitted(
		reportPath,
		reportData,
		report.ArtifactSet,
		KindDashboard,
		[]string{"hosts.signoz.json", "hosts.report.html"},
		1<<20,
	)
	require.NoError(t, err)
	assert.Equal(t, "primary-"+report.Marker, string(snapshot.Data["hosts.signoz.json"]))
	assert.Equal(t, "html-"+report.Marker, string(snapshot.Data["hosts.report.html"]))
	assertPointerBoundGenerationSet(t, directory, report.ArtifactSet.Path)
}

func TestArtifactSetCommitProcessHelper(t *testing.T) {
	directory := os.Getenv("PROMCAST_ARTIFACTSET_HELPER_DIR")
	if directory == "" {
		return
	}
	marker := os.Getenv("PROMCAST_ARTIFACTSET_HELPER_MARKER")
	require.NotEmpty(t, marker)
	reportPath := filepath.Join(directory, "hosts.report.json")
	binding, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(reportPath, binding, KindDashboard, dashboardArtifacts(t, directory, binding, marker)))
}

func TestStableFacadeTamperingFailsClosedAndPrefixCollisionIsPreserved(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	reportPath := filepath.Join(directory, "hosts.report.json")
	first, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(reportPath, first, KindDashboard, dashboardArtifacts(t, directory, first, "first")))

	staleStage := filepath.Join(directory, stagePrefix(first.Path)+"interrupted")
	require.NoError(t, os.Mkdir(staleStage, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(staleStage, "unpublished"), []byte("stale"), 0o600))
	// External facade tampering while the report and current pointer agree is
	// not mistaken for an interrupted publication.
	require.NoError(t, os.WriteFile(filepath.Join(directory, "hosts.signoz.json"), []byte("primary-interrupted"), 0o600))
	reportData, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	_, err = ReadCommitted(
		reportPath,
		reportData,
		&first,
		KindDashboard,
		[]string{"hosts.signoz.json"},
		1<<20,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match commit manifest")

	second, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(reportPath, second, KindDashboard, dashboardArtifacts(t, directory, second, "second")))
	assert.DirExists(t, staleStage)
	staleData, err := os.ReadFile(filepath.Join(staleStage, "unpublished"))
	require.NoError(t, err)
	assert.Equal(t, "stale", string(staleData))
	reportData, err = os.ReadFile(reportPath)
	require.NoError(t, err)
	_, err = ReadCommitted(reportPath, reportData, &second, KindDashboard, nil, 1<<20)
	require.NoError(t, err)
}

func TestStageCleanupPreservesStrictPrefixCollisionsWithoutOwnership(t *testing.T) {
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "hosts.report.json")
	stable, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(reportPath, stable, KindDashboard, dashboardArtifacts(t, directory, stable, "stable")))

	generation := strings.Repeat("a", 32)
	nonce := strings.Repeat("b", stageNonceBytes*2)
	hostileDirectory := filepath.Join(directory, ownedStageName(stable.Path, generation, "generation", nonce))
	require.NoError(t, os.Mkdir(hostileDirectory, 0o700))
	protected := filepath.Join(hostileDirectory, "protected-input")
	require.NoError(t, os.WriteFile(protected, []byte("do not delete"), 0o600))
	hostileFile := filepath.Join(directory, ownedStageName(stable.Path, generation, "facade:report", nonce))
	require.NoError(t, os.WriteFile(hostileFile, []byte("also protected"), 0o600))

	next, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(reportPath, next, KindDashboard, dashboardArtifacts(t, directory, next, "next")))

	data, err := os.ReadFile(protected)
	require.NoError(t, err)
	assert.Equal(t, "do not delete", string(data))
	data, err = os.ReadFile(hostileFile)
	require.NoError(t, err)
	assert.Equal(t, "also protected", string(data))
}

func TestStageCleanupRemovesOnlyMarkerBoundPartialStage(t *testing.T) {
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "hosts.report.json")
	stable, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(reportPath, stable, KindDashboard, dashboardArtifacts(t, directory, stable, "stable")))

	pinned, lock, err := openLockedPinnedDirectory(directory, lockName(stable.Path))
	require.NoError(t, err)
	interrupted, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	stage, err := createOwnedStage(
		pinned,
		stable.Path,
		interrupted.Generation,
		"generation",
		[]string{"partial"},
	)
	require.NoError(t, err)
	require.NoError(t, writeStageFile(pinned.root, filepath.Join(stage, "partial"), []byte("owned")))
	require.NoError(t, lock.Close())
	require.NoError(t, pinned.Close())

	next, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(reportPath, next, KindDashboard, dashboardArtifacts(t, directory, next, "next")))
	assert.NoDirExists(t, filepath.Join(directory, stage))
}

func TestEveryPublicationBarrierLeavesPriorOrNewCheckpointReadable(t *testing.T) {
	barriers := []commitBarrier{
		{phase: barrierGenerationMember, role: RoleHTML},
		{phase: barrierGenerationMember, role: RoleReport},
		{phase: barrierGenerationMember, role: RolePrimary},
		{phase: barrierGenerationManifest},
		{phase: barrierGenerationPublish},
		{phase: barrierGenerationPointer},
		{phase: barrierFacadeMember, role: RoleHTML},
		{phase: barrierFacadeMember, role: RolePrimary},
		{phase: barrierFacadeManifest},
		{phase: barrierFacadeReport, role: RoleReport},
	}

	for _, barrier := range barriers {
		t.Run(barrier.phase+"-"+string(barrier.role), func(t *testing.T) {
			directory := t.TempDir()
			reportPath := filepath.Join(directory, "hosts.report.json")
			first, err := NewBindingForReport(reportPath, KindDashboard)
			require.NoError(t, err)
			require.NoError(t, Commit(reportPath, first, KindDashboard, dashboardArtifacts(t, directory, first, "attempted")))

			final, err := NextBinding(first)
			require.NoError(t, err)
			injected := errors.New("simulated crash")
			err = commitWithBarrier(
				reportPath,
				final,
				KindDashboard,
				dashboardArtifacts(t, directory, final, "final"),
				func(reached commitBarrier) error {
					if reached == barrier {
						return injected
					}
					return nil
				},
			)
			require.ErrorIs(t, err, injected)

			reportData, err := os.ReadFile(reportPath)
			require.NoError(t, err)
			var current struct {
				Marker      string                          `json:"marker"`
				ArtifactSet *reporttypes.ArtifactSetBinding `json:"artifactSet"`
			}
			require.NoError(t, json.Unmarshal(reportData, &current))
			require.Contains(t, []string{"attempted", "final"}, current.Marker)
			require.NotNil(t, current.ArtifactSet)
			snapshot, err := ReadCommitted(
				reportPath,
				reportData,
				current.ArtifactSet,
				KindDashboard,
				[]string{"hosts.signoz.json", "hosts.report.html"},
				1<<20,
			)
			require.NoError(t, err)
			assert.Equal(t, "primary-"+current.Marker, string(snapshot.Data["hosts.signoz.json"]))
			assert.Equal(t, "html-"+current.Marker, string(snapshot.Data["hosts.report.html"]))

			recovered, err := NewBindingForReport(reportPath, KindDashboard)
			require.NoError(t, err)
			require.NoError(t, Commit(
				reportPath,
				recovered,
				KindDashboard,
				dashboardArtifacts(t, directory, recovered, "recovered"),
			))
			recoveredData, err := os.ReadFile(reportPath)
			require.NoError(t, err)
			_, err = ReadCommitted(reportPath, recoveredData, &recovered, KindDashboard, nil, 1<<20)
			require.NoError(t, err)
		})
	}
}

func TestPointerRejectsRollbackPastPreviousGeneration(t *testing.T) {
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "hosts.report.json")
	first, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	firstArtifacts := dashboardArtifacts(t, directory, first, "first")
	require.NoError(t, Commit(reportPath, first, KindDashboard, firstArtifacts))
	firstReport := normalizedArtifact(firstArtifacts, RoleReport).Data

	second, err := NextBinding(first)
	require.NoError(t, err)
	require.NoError(t, Commit(reportPath, second, KindDashboard, dashboardArtifacts(t, directory, second, "second")))
	third, err := NextBinding(second)
	require.NoError(t, err)
	require.NoError(t, Commit(reportPath, third, KindDashboard, dashboardArtifacts(t, directory, third, "third")))

	_, err = ReadCommitted(reportPath, firstReport, &first, KindDashboard, nil, 1<<20)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "neither current nor the recoverable previous generation")
}

func TestSuccessfulCommitsRetainOnlyCurrentAndPreviousGenerations(t *testing.T) {
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "hosts.report.json")
	bindings := make([]reporttypes.ArtifactSetBinding, 0, 8)
	reports := make([][]byte, 0, 8)

	for index := range 8 {
		binding, err := NewBindingForReport(reportPath, KindDashboard)
		require.NoError(t, err)
		artifacts := dashboardArtifacts(t, directory, binding, fmt.Sprintf("generation-%d", index))
		require.NoError(t, Commit(reportPath, binding, KindDashboard, artifacts))
		bindings = append(bindings, binding)
		reports = append(reports, append([]byte(nil), normalizedArtifact(artifacts, RoleReport).Data...))
		assertPointerBoundGenerationSet(t, directory, binding.Path)
	}

	current := bindings[len(bindings)-1]
	previous := bindings[len(bindings)-2]
	_, err := ReadCommitted(reportPath, reports[len(reports)-1], &current, KindDashboard, nil, 1<<20)
	require.NoError(t, err)
	_, err = ReadCommitted(reportPath, reports[len(reports)-2], &previous, KindDashboard, nil, 1<<20)
	require.NoError(t, err)
	_, err = ReadCommitted(reportPath, reports[0], &bindings[0], KindDashboard, nil, 1<<20)
	require.ErrorContains(t, err, "neither current nor the recoverable previous generation")
}

func TestInterruptedFirstPublishOrphanIsRemovedBeforeRetry(t *testing.T) {
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "hosts.report.json")
	interrupted, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	injected := errors.New("simulated crash before pointer")
	err = commitWithBarrier(
		reportPath,
		interrupted,
		KindDashboard,
		dashboardArtifacts(t, directory, interrupted, "interrupted"),
		func(reached commitBarrier) error {
			if reached.phase == barrierGenerationPublish {
				return injected
			}
			return nil
		},
	)
	require.ErrorIs(t, err, injected)
	assert.Equal(t, []string{interrupted.Generation}, generationNames(t, directory, interrupted.Path))
	assert.NoFileExists(t, filepath.Join(directory, currentPointerName(interrupted.Path)))

	recovered, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(
		reportPath,
		recovered,
		KindDashboard,
		dashboardArtifacts(t, directory, recovered, "recovered"),
	))
	assert.Equal(t, []string{recovered.Generation}, generationNames(t, directory, recovered.Path))
	assertPointerBoundGenerationSet(t, directory, recovered.Path)
}

func TestInterruptedLegacyUpgradePreservesFlatGenerationAndPrunesOnlyOrphan(t *testing.T) {
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "hosts.report.json")
	legacy, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(reportPath, legacy, KindDashboard, dashboardArtifacts(t, directory, legacy, "legacy")))
	require.NoError(t, os.RemoveAll(filepath.Join(directory, generationContainerName(legacy.Path))))
	require.NoError(t, os.Remove(filepath.Join(directory, currentPointerName(legacy.Path))))

	interrupted, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	injected := errors.New("simulated legacy upgrade crash before pointer")
	err = commitWithBarrier(
		reportPath,
		interrupted,
		KindDashboard,
		dashboardArtifacts(t, directory, interrupted, "interrupted"),
		func(reached commitBarrier) error {
			if reached.phase == barrierGenerationPublish {
				return injected
			}
			return nil
		},
	)
	require.ErrorIs(t, err, injected)
	assert.ElementsMatch(
		t,
		[]string{legacy.Generation, interrupted.Generation},
		generationNames(t, directory, legacy.Path),
	)
	assert.NoFileExists(t, filepath.Join(directory, currentPointerName(legacy.Path)))

	recovered, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(
		reportPath,
		recovered,
		KindDashboard,
		dashboardArtifacts(t, directory, recovered, "recovered"),
	))
	assertPointerBoundGenerationSet(t, directory, recovered.Path)
	assert.ElementsMatch(
		t,
		[]string{legacy.Generation, recovered.Generation},
		generationNames(t, directory, recovered.Path),
	)
}

func TestUnpointedStableManifestRequiresValidReportBeforeAnyPrune(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string, reporttypes.ArtifactSetBinding)
	}{
		{
			name: "missing report",
			mutate: func(t *testing.T, reportPath string, _ reporttypes.ArtifactSetBinding) {
				require.NoError(t, os.Remove(reportPath))
			},
		},
		{
			name: "malformed report",
			mutate: func(t *testing.T, reportPath string, _ reporttypes.ArtifactSetBinding) {
				require.NoError(t, os.WriteFile(reportPath, []byte(`{"artifactSet":`), 0o600))
			},
		},
		{
			name: "report without binding",
			mutate: func(t *testing.T, reportPath string, _ reporttypes.ArtifactSetBinding) {
				require.NoError(t, os.WriteFile(reportPath, []byte(`{"marker":"tampered"}`), 0o600))
			},
		},
		{
			name: "mismatched report generation",
			mutate: func(t *testing.T, reportPath string, binding reporttypes.ArtifactSetBinding) {
				binding.Generation = strings.Repeat("e", 32)
				require.NoError(t, os.WriteFile(
					reportPath,
					encodedReport(t, binding, "tampered", "hosts.signoz.json", []byte("primary-stable")),
					0o600,
				))
			},
		},
		{
			name: "report bytes mismatch stable manifest",
			mutate: func(t *testing.T, reportPath string, binding reporttypes.ArtifactSetBinding) {
				require.NoError(t, os.WriteFile(
					reportPath,
					encodedReport(t, binding, "tampered", "hosts.signoz.json", []byte("primary-stable")),
					0o600,
				))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			reportPath := filepath.Join(directory, "hosts.report.json")
			stable, err := NewBindingForReport(reportPath, KindDashboard)
			require.NoError(t, err)
			require.NoError(t, Commit(
				reportPath,
				stable,
				KindDashboard,
				dashboardArtifacts(t, directory, stable, "stable"),
			))
			require.NoError(t, os.Remove(filepath.Join(directory, currentPointerName(stable.Path))))
			test.mutate(t, reportPath, stable)

			next, err := NewBindingForReport(reportPath, KindDashboard)
			require.NoError(t, err)
			err = Commit(reportPath, next, KindDashboard, dashboardArtifacts(t, directory, next, "next"))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "before publication")
			assert.Equal(t, []string{stable.Generation}, generationNames(t, directory, stable.Path))
			assert.DirExists(t, filepath.Join(directory, generationContainerName(stable.Path), stable.Generation))
			assert.NoDirExists(t, filepath.Join(directory, generationContainerName(stable.Path), next.Generation))
		})
	}
}

func TestRepeatedPrePointerInterruptionsRemainBounded(t *testing.T) {
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "hosts.report.json")
	previous, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(reportPath, previous, KindDashboard, dashboardArtifacts(t, directory, previous, "previous")))
	stable, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(reportPath, stable, KindDashboard, dashboardArtifacts(t, directory, stable, "stable")))

	for index := range 8 {
		interrupted, err := NewBindingForReport(reportPath, KindDashboard)
		require.NoError(t, err)
		injected := errors.New("simulated crash before pointer")
		err = commitWithBarrier(
			reportPath,
			interrupted,
			KindDashboard,
			dashboardArtifacts(t, directory, interrupted, fmt.Sprintf("interrupted-%d", index)),
			func(reached commitBarrier) error {
				if reached.phase == barrierGenerationPublish {
					return injected
				}
				return nil
			},
		)
		require.ErrorIs(t, err, injected)
		assert.ElementsMatch(
			t,
			[]string{previous.Generation, stable.Generation, interrupted.Generation},
			generationNames(t, directory, stable.Path),
		)
	}

	recovered, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(
		reportPath,
		recovered,
		KindDashboard,
		dashboardArtifacts(t, directory, recovered, "recovered"),
	))
	assertPointerBoundGenerationSet(t, directory, recovered.Path)
}

func TestInterruptedPreflightPruneIsDurableAndRetryable(t *testing.T) {
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "hosts.report.json")
	stable, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(reportPath, stable, KindDashboard, dashboardArtifacts(t, directory, stable, "stable")))

	orphan, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	injected := errors.New("simulated crash before pointer")
	err = commitWithBarrier(
		reportPath,
		orphan,
		KindDashboard,
		dashboardArtifacts(t, directory, orphan, "orphan"),
		func(reached commitBarrier) error {
			if reached.phase == barrierGenerationPublish {
				return injected
			}
			return nil
		},
	)
	require.ErrorIs(t, err, injected)

	retry, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	injected = errors.New("simulated crash after preflight prune")
	err = commitWithBarrier(
		reportPath,
		retry,
		KindDashboard,
		dashboardArtifacts(t, directory, retry, "retry"),
		func(reached commitBarrier) error {
			if reached.phase == barrierGenerationPreflightPrune {
				return injected
			}
			return nil
		},
	)
	require.ErrorIs(t, err, injected)
	assert.Equal(t, []string{stable.Generation}, generationNames(t, directory, stable.Path))
	assert.NoDirExists(t, filepath.Join(directory, generationContainerName(stable.Path), retry.Generation))

	require.NoError(t, Commit(
		reportPath,
		retry,
		KindDashboard,
		dashboardArtifacts(t, directory, retry, "retry"),
	))
	assertPointerBoundGenerationSet(t, directory, retry.Path)
}

func TestInterruptedPostCommitPruneLeavesCurrentAndPreviousReadable(t *testing.T) {
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "hosts.report.json")
	first, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	firstArtifacts := dashboardArtifacts(t, directory, first, "first")
	require.NoError(t, Commit(reportPath, first, KindDashboard, firstArtifacts))
	second, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	secondArtifacts := dashboardArtifacts(t, directory, second, "second")
	require.NoError(t, Commit(reportPath, second, KindDashboard, secondArtifacts))
	third, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	thirdArtifacts := dashboardArtifacts(t, directory, third, "third")
	injected := errors.New("simulated crash after committed prune")
	err = commitWithBarrier(
		reportPath,
		third,
		KindDashboard,
		thirdArtifacts,
		func(reached commitBarrier) error {
			if reached.phase == barrierGenerationPrune {
				return injected
			}
			return nil
		},
	)
	require.ErrorIs(t, err, injected)
	assert.ElementsMatch(
		t,
		[]string{second.Generation, third.Generation},
		generationNames(t, directory, third.Path),
	)
	_, err = ReadCommitted(
		reportPath,
		normalizedArtifact(thirdArtifacts, RoleReport).Data,
		&third,
		KindDashboard,
		nil,
		1<<20,
	)
	require.NoError(t, err)
	_, err = ReadCommitted(
		reportPath,
		normalizedArtifact(secondArtifacts, RoleReport).Data,
		&second,
		KindDashboard,
		nil,
		1<<20,
	)
	require.NoError(t, err)
	assertPointerBoundGenerationSet(t, directory, third.Path)
}

func TestInterruptedPruneRenameRecoversTombstoneOnRetry(t *testing.T) {
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "hosts.report.json")
	first, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(reportPath, first, KindDashboard, dashboardArtifacts(t, directory, first, "first")))
	second, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(reportPath, second, KindDashboard, dashboardArtifacts(t, directory, second, "second")))
	third, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	injected := errors.New("simulated crash after prune rename")
	err = commitWithBarrier(
		reportPath,
		third,
		KindDashboard,
		dashboardArtifacts(t, directory, third, "third"),
		func(reached commitBarrier) error {
			if reached.phase == barrierGenerationPruneRename {
				return injected
			}
			return nil
		},
	)
	require.ErrorIs(t, err, injected)
	assert.Len(t, pruneTombstones(t, directory, third.Path), 1)

	retry, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(reportPath, retry, KindDashboard, dashboardArtifacts(t, directory, retry, "retry")))
	assert.Empty(t, pruneTombstones(t, directory, retry.Path))
	assertPointerBoundGenerationSet(t, directory, retry.Path)
}

func TestInterruptedPruneDeleteRecoversPartialTombstoneOnRetry(t *testing.T) {
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "hosts.report.json")
	first, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(reportPath, first, KindDashboard, dashboardArtifacts(t, directory, first, "first")))
	second, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(reportPath, second, KindDashboard, dashboardArtifacts(t, directory, second, "second")))
	third, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	injected := errors.New("simulated crash during tombstone deletion")
	err = commitWithBarrier(
		reportPath,
		third,
		KindDashboard,
		dashboardArtifacts(t, directory, third, "third"),
		func(reached commitBarrier) error {
			if reached.phase == barrierGenerationPruneDelete {
				return injected
			}
			return nil
		},
	)
	require.ErrorIs(t, err, injected)
	tombstones := pruneTombstones(t, directory, third.Path)
	require.Len(t, tombstones, 1)
	assert.FileExists(t, filepath.Join(
		directory,
		generationContainerName(third.Path),
		tombstones[0],
		stageOwnerName,
	))

	retry, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(reportPath, retry, KindDashboard, dashboardArtifacts(t, directory, retry, "retry")))
	assert.Empty(t, pruneTombstones(t, directory, retry.Path))
	assertPointerBoundGenerationSet(t, directory, retry.Path)
}

func TestMalformedOrUnknownPointerDisablesPruningAndPublication(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string, generationPointer) []byte
	}{
		{
			name: "malformed",
			mutate: func(_ *testing.T, _ string, _ generationPointer) []byte {
				return []byte(`{"schemaVersion":1,"unknown":true}`)
			},
		},
		{
			name: "unknown current generation",
			mutate: func(t *testing.T, _ string, pointer generationPointer) []byte {
				pointer.Generation = strings.Repeat("f", 32)
				data, err := json.Marshal(pointer)
				require.NoError(t, err)
				return data
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			reportPath := filepath.Join(directory, "hosts.report.json")
			stable, err := NewBindingForReport(reportPath, KindDashboard)
			require.NoError(t, err)
			require.NoError(t, Commit(reportPath, stable, KindDashboard, dashboardArtifacts(t, directory, stable, "stable")))
			root, err := os.OpenRoot(directory)
			require.NoError(t, err)
			pointer, found, err := readGenerationPointer(root, stable.Path)
			require.NoError(t, err)
			require.True(t, found)
			require.NoError(t, root.Close())
			pointerPath := filepath.Join(directory, currentPointerName(stable.Path))
			require.NoError(t, os.WriteFile(pointerPath, test.mutate(t, stable.Path, pointer), 0o600))

			next, err := NewBindingForReport(reportPath, KindDashboard)
			require.NoError(t, err)
			err = Commit(reportPath, next, KindDashboard, dashboardArtifacts(t, directory, next, "next"))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "pointer")
			assert.Equal(t, []string{stable.Generation}, generationNames(t, directory, stable.Path))
			assert.NoDirExists(t, filepath.Join(directory, generationContainerName(stable.Path), next.Generation))
		})
	}
}

func TestMissingStableReportCannotCollapseTwoPointerReferences(t *testing.T) {
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "hosts.report.json")
	first, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(reportPath, first, KindDashboard, dashboardArtifacts(t, directory, first, "first")))
	second, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(reportPath, second, KindDashboard, dashboardArtifacts(t, directory, second, "second")))
	require.NoError(t, os.Remove(reportPath))

	next, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	err = Commit(reportPath, next, KindDashboard, dashboardArtifacts(t, directory, next, "next"))
	require.ErrorContains(t, err, "stable report has no valid artifact-set binding")
	assert.ElementsMatch(
		t,
		[]string{first.Generation, second.Generation},
		generationNames(t, directory, second.Path),
	)
	assert.NoDirExists(t, filepath.Join(directory, generationContainerName(second.Path), next.Generation))
}

func TestMultipleUnreferencedGenerationsFailClosed(t *testing.T) {
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "hosts.report.json")
	stable, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(reportPath, stable, KindDashboard, dashboardArtifacts(t, directory, stable, "stable")))
	orphanOne, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	publishOrphanGeneration(t, reportPath, orphanOne, "orphan-one")
	orphanTwo, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	publishOrphanGeneration(t, reportPath, orphanTwo, "orphan-two")

	next, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	err = Commit(reportPath, next, KindDashboard, dashboardArtifacts(t, directory, next, "next"))
	require.ErrorContains(t, err, "2 unreferenced generations")
	assert.ElementsMatch(
		t,
		[]string{stable.Generation, orphanOne.Generation, orphanTwo.Generation},
		generationNames(t, directory, stable.Path),
	)
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()
	_, err = InspectRetainedStorage(root, stable, KindDashboard)
	require.ErrorContains(t, err, "multiple unreferenced generations")
}

func TestInspectClassifiesButDoesNotExportSingleOrphan(t *testing.T) {
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "hosts.report.json")
	previous, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(reportPath, previous, KindDashboard, dashboardArtifacts(t, directory, previous, "previous")))
	stable, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(reportPath, stable, KindDashboard, dashboardArtifacts(t, directory, stable, "stable")))
	orphan, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	publishOrphanGeneration(t, reportPath, orphan, "orphan")

	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()
	tree, err := InspectRetainedStorage(root, stable, KindDashboard)
	require.NoError(t, err)
	assert.Equal(t, orphan.Generation, tree.OrphanGeneration)
	assert.Equal(t, filepath.Join(tree.Layout.Generations, orphan.Generation), tree.OrphanDirectory)
	require.Len(t, tree.OrphanFiles, 5)
	assert.Contains(t, tree.Directories, filepath.Join(tree.Layout.Generations, stable.Generation))
	assert.Contains(t, tree.Directories, filepath.Join(tree.Layout.Generations, previous.Generation))
	for _, path := range append(append([]string(nil), tree.Directories...), tree.Files...) {
		assert.NotContains(t, path, orphan.Generation)
	}
}

func TestPresentCandidateFacadeBarrierLeavesAttemptedCheckpointReadable(t *testing.T) {
	for _, target := range []commitBarrier{
		{phase: barrierGenerationMember, role: RoleCandidate},
		{phase: barrierFacadeMember, role: RoleCandidate},
	} {
		t.Run(target.phase, func(t *testing.T) {
			directory := t.TempDir()
			reportPath := filepath.Join(directory, "hosts.report.json")
			first, err := NewBindingForReport(reportPath, KindDashboard)
			require.NoError(t, err)
			require.NoError(t, Commit(reportPath, first, KindDashboard, dashboardArtifacts(t, directory, first, "attempted")))

			final, err := NextBinding(first)
			require.NoError(t, err)
			finalArtifacts := dashboardArtifacts(t, directory, final, "final")
			finalArtifacts = append(finalArtifacts, Artifact{
				Role: RoleCandidate, Path: filepath.Join(directory, "hosts.candidate.signoz.json"), Data: []byte("candidate-final"),
			})
			injected := errors.New("simulated crash")
			err = commitWithBarrier(
				reportPath,
				final,
				KindDashboard,
				finalArtifacts,
				func(reached commitBarrier) error {
					if reached == target {
						return injected
					}
					return nil
				},
			)
			require.ErrorIs(t, err, injected)
			reportData, err := os.ReadFile(reportPath)
			require.NoError(t, err)
			snapshot, err := ReadCommitted(
				reportPath,
				reportData,
				&first,
				KindDashboard,
				[]string{"hosts.signoz.json"},
				1<<20,
			)
			require.NoError(t, err)
			assert.Equal(t, "primary-attempted", string(snapshot.Data["hosts.signoz.json"]))
		})
	}
}

func TestCandidateRemovalBarrierLeavesAttemptedCheckpointReadable(t *testing.T) {
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "hosts.report.json")
	first, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	firstArtifacts := dashboardArtifacts(t, directory, first, "attempted")
	firstArtifacts = append(firstArtifacts, Artifact{
		Role: RoleCandidate, Path: filepath.Join(directory, "hosts.candidate.signoz.json"), Data: []byte("candidate-attempted"),
	})
	require.NoError(t, Commit(reportPath, first, KindDashboard, firstArtifacts))

	final, err := NextBinding(first)
	require.NoError(t, err)
	injected := errors.New("simulated crash")
	err = commitWithBarrier(
		reportPath,
		final,
		KindDashboard,
		dashboardArtifacts(t, directory, final, "final"),
		func(reached commitBarrier) error {
			if reached == (commitBarrier{phase: barrierFacadeMember, role: RoleCandidate}) {
				return injected
			}
			return nil
		},
	)
	require.ErrorIs(t, err, injected)
	reportData, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	snapshot, err := ReadCommitted(
		reportPath,
		reportData,
		&first,
		KindDashboard,
		[]string{"hosts.candidate.signoz.json"},
		1<<20,
	)
	require.NoError(t, err)
	assert.Equal(t, "candidate-attempted", string(snapshot.Data["hosts.candidate.signoz.json"]))
}

func TestFlatGenerationIsPreservedBeforeFacadeTransition(t *testing.T) {
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "hosts.report.json")
	first, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)
	require.NoError(t, Commit(reportPath, first, KindDashboard, dashboardArtifacts(t, directory, first, "legacy")))
	require.NoError(t, os.RemoveAll(filepath.Join(directory, generationContainerName(first.Path))))
	require.NoError(t, os.Remove(filepath.Join(directory, currentPointerName(first.Path))))

	final, err := NextBinding(first)
	require.NoError(t, err)
	injected := errors.New("simulated crash")
	err = commitWithBarrier(
		reportPath,
		final,
		KindDashboard,
		dashboardArtifacts(t, directory, final, "final"),
		func(reached commitBarrier) error {
			if reached == (commitBarrier{phase: barrierFacadeMember, role: RolePrimary}) {
				return injected
			}
			return nil
		},
	)
	require.ErrorIs(t, err, injected)
	reportData, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	snapshot, err := ReadCommitted(
		reportPath,
		reportData,
		&first,
		KindDashboard,
		[]string{"hosts.signoz.json"},
		1<<20,
	)
	require.NoError(t, err)
	assert.Equal(t, "primary-legacy", string(snapshot.Data["hosts.signoz.json"]))
}

func TestUpdatePreservesUntouchedMembersAndAdvancesGeneration(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	reportPath := filepath.Join(directory, "alerts.rules-report.json")
	first, err := NewBindingForReport(reportPath, KindRules)
	require.NoError(t, err)
	primary := []byte("rules")
	firstReport := encodedReport(t, first, "first", "alerts.signoz-rules.json", primary)
	require.NoError(t, Commit(reportPath, first, KindRules, []Artifact{
		{Role: RolePrimary, Path: filepath.Join(directory, "alerts.signoz-rules.json"), Data: primary},
		{Role: RoleReport, Path: reportPath, Data: firstReport},
		{Role: RoleHTML, Path: filepath.Join(directory, "alerts.rules-report.html"), Data: []byte("old-html")},
	}))

	second, err := NextBinding(first)
	require.NoError(t, err)
	secondReport := encodedReport(t, second, "second", "alerts.signoz-rules.json", primary)
	require.NoError(t, Update(reportPath, first, second, KindRules, []Artifact{
		{Role: RoleReport, Path: reportPath, Data: secondReport},
		{Role: RoleHTML, Path: filepath.Join(directory, "alerts.rules-report.html"), Data: []byte("new-html")},
	}))

	snapshot, err := ReadCommitted(
		reportPath,
		secondReport,
		&second,
		KindRules,
		[]string{"alerts.signoz-rules.json", "alerts.rules-report.html"},
		1<<20,
	)
	require.NoError(t, err)
	assert.Equal(t, "rules", string(snapshot.Data["alerts.signoz-rules.json"]))
	assert.Equal(t, "new-html", string(snapshot.Data["alerts.rules-report.html"]))
}

func TestNewBindingRejectsWindowsInvalidReportNamesOnEveryPlatform(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"CON.report.json",
		"NUL.rules-report.json",
		"bad:name.report.json",
		"bad\x01name.report.json",
	} {
		kind := KindDashboard
		if strings.Contains(name, ".rules-report.json") {
			kind = KindRules
		}
		_, err := NewBindingForReport(filepath.Join(t.TempDir(), name), kind)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "not portable")
	}
}

func TestCommitRejectsMissingOrMismatchedPrimaryReportBinding(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		report func(reporttypes.ArtifactSetBinding) []byte
	}{
		{
			name: "missing",
			report: func(binding reporttypes.ArtifactSetBinding) []byte {
				data, err := json.Marshal(map[string]any{"artifactSet": binding})
				require.NoError(t, err)
				return data
			},
		},
		{
			name: "mismatched",
			report: func(binding reporttypes.ArtifactSetBinding) []byte {
				data, err := json.Marshal(map[string]any{
					"artifactSet": binding,
					"primaryArtifact": reporttypes.ArtifactBinding{
						Path: "hosts.signoz.json", SHA256: strings.Repeat("0", 64), SizeBytes: 7,
					},
				})
				require.NoError(t, err)
				return data
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			reportPath := filepath.Join(directory, "hosts.report.json")
			binding, err := NewBindingForReport(reportPath, KindDashboard)
			require.NoError(t, err)
			err = Commit(reportPath, binding, KindDashboard, []Artifact{
				{Role: RolePrimary, Path: filepath.Join(directory, "hosts.signoz.json"), Data: []byte("primary")},
				{Role: RoleReport, Path: reportPath, Data: test.report(binding)},
				{Role: RoleHTML, Path: filepath.Join(directory, "hosts.report.html"), Data: []byte("html")},
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "primary artifact binding")
			assert.NoFileExists(t, filepath.Join(directory, binding.Path))
		})
	}
}

func TestCommitRejectsArtifactLargerThanSharedReaderLimit(t *testing.T) {
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "hosts.report.json")
	binding, err := NewBindingForReport(reportPath, KindDashboard)
	require.NoError(t, err)

	oversized := make([]byte, int(MaxMemberSize)+1)
	err = Commit(reportPath, binding, KindDashboard, []Artifact{
		{Role: RolePrimary, Path: filepath.Join(directory, "hosts.signoz.json"), Data: oversized},
		{Role: RoleReport, Path: reportPath, Data: []byte(`{"unused":true}`)},
		{Role: RoleHTML, Path: filepath.Join(directory, "hosts.report.html"), Data: []byte("html")},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), fmt.Sprintf("exceeds %d bytes", MaxMemberSize))
	assert.NoFileExists(t, filepath.Join(directory, binding.Path))
}

func dashboardArtifacts(
	t *testing.T,
	directory string,
	binding reporttypes.ArtifactSetBinding,
	marker string,
) []Artifact {
	t.Helper()
	primary := []byte("primary-" + marker)
	return []Artifact{
		{Role: RolePrimary, Path: filepath.Join(directory, "hosts.signoz.json"), Data: primary},
		{Role: RoleReport, Path: filepath.Join(directory, "hosts.report.json"), Data: encodedReport(t, binding, marker, "hosts.signoz.json", primary)},
		{Role: RoleHTML, Path: filepath.Join(directory, "hosts.report.html"), Data: []byte("html-" + marker)},
	}
}

func encodedReport(
	t *testing.T,
	binding reporttypes.ArtifactSetBinding,
	marker string,
	primaryName string,
	primary []byte,
) []byte {
	t.Helper()
	digest := sha256.Sum256(primary)
	data, err := json.Marshal(map[string]any{
		"artifactSet": binding,
		"marker":      marker,
		"primaryArtifact": reporttypes.ArtifactBinding{
			Path: primaryName, SHA256: fmt.Sprintf("%x", digest[:]), SizeBytes: int64(len(primary)),
		},
	})
	require.NoError(t, err)
	return data
}

func generationNames(t *testing.T, directory string, manifestName string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(directory, generationContainerName(manifestName)))
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

func pruneTombstones(t *testing.T, directory string, manifestName string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(directory, generationContainerName(manifestName)))
	require.NoError(t, err)
	var names []string
	for _, entry := range entries {
		if _, _, ok := parsePruneTombstoneName(entry.Name(), manifestName); ok {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

func assertPointerBoundGenerationSet(t *testing.T, directory string, manifestName string) {
	t.Helper()
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	pointer, found, err := readGenerationPointer(root, manifestName)
	require.NoError(t, err)
	require.True(t, found)
	require.NoError(t, root.Close())
	expected := []string{pointer.Generation}
	if pointer.PreviousGeneration != "" {
		expected = append(expected, pointer.PreviousGeneration)
	}
	assert.ElementsMatch(t, expected, generationNames(t, directory, manifestName))
}

func publishOrphanGeneration(
	t *testing.T,
	reportPath string,
	binding reporttypes.ArtifactSetBinding,
	marker string,
) {
	t.Helper()
	artifacts, err := normalizeArtifacts(
		reportPath,
		binding,
		KindDashboard,
		dashboardArtifacts(t, filepath.Dir(reportPath), binding, marker),
	)
	require.NoError(t, err)
	directory, lock, err := openLockedPinnedDirectory(filepath.Dir(reportPath), lockName(binding.Path))
	require.NoError(t, err)
	defer func() { require.NoError(t, directory.Close()) }()
	defer func() { require.NoError(t, lock.Close()) }()
	_, err = publishImmutableGenerationLocked(directory, binding, KindDashboard, artifacts, nil)
	require.NoError(t, err)
}

func assertMode(t *testing.T, path string, expected os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, expected, info.Mode().Perm(), path)
}
