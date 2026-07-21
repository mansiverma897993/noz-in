package mcpserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPWorkMetadataIsChargedExactlyToOutputQuota(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "out")
	service, err := New(Config{Root: root, OutputRoot: output, MaxOutputEntries: 6})
	require.NoError(t, err)
	operation, _, _, err := service.beginMigrationWork(
		[]byte(`{"title":"quota markers"}`),
		time.Unix(1, 0),
		false,
	)
	require.NoError(t, err)

	usage, err := service.measureOutputUsage()
	require.NoError(t, err)
	assert.Equal(t, int64(6), usage.entries)
	rootOwner, err := encodeWorkJSON(mcpWorkRootOwner{SchemaVersion: 1, Namespace: "promcast/mcp-work-v1"})
	require.NoError(t, err)
	owner, err := encodeWorkJSON(mcpWorkOwner{SchemaVersion: 1, Token: operation.token})
	require.NoError(t, err)
	plan, err := encodeWorkJSON(operation.plan)
	require.NoError(t, err)
	assert.Equal(t, int64(len(rootOwner)+len(owner)+len(plan)), usage.bytes)

	require.NoError(t, operation.cleanup())
	entries, err := os.ReadDir(output)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestMCPWorkMetadataQuotaFailureCleansOwnedPartialState(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "out")
	service, err := New(Config{Root: root, OutputRoot: output, MaxOutputEntries: 5})
	require.NoError(t, err)
	_, _, _, err = service.beginMigrationWork(
		[]byte(`{"title":"quota markers"}`),
		time.Unix(1, 0),
		false,
	)
	require.ErrorContains(t, err, "entry quota would be exceeded")
	entries, readErr := os.ReadDir(output)
	require.NoError(t, readErr)
	assert.Empty(t, entries)
}

func TestMCPRecoveryPreservesUnownedOrHostileWorkspace(t *testing.T) {
	t.Run("unowned reserved root", func(t *testing.T) {
		root := t.TempDir()
		output := filepath.Join(root, "out")
		work := filepath.Join(output, mcpWorkRootName)
		require.NoError(t, os.MkdirAll(work, 0o700))
		sentinel := filepath.Join(work, "operator-note.txt")
		require.NoError(t, os.WriteFile(sentinel, []byte("keep"), 0o600))

		_, err := New(Config{Root: root, OutputRoot: output})
		require.ErrorContains(t, err, "not owned")
		data, readErr := os.ReadFile(sentinel)
		require.NoError(t, readErr)
		assert.Equal(t, "keep", string(data))
	})

	t.Run("owned root with unrelated entry", func(t *testing.T) {
		root := t.TempDir()
		output := filepath.Join(root, "out")
		work := filepath.Join(output, mcpWorkRootName)
		require.NoError(t, os.MkdirAll(work, 0o700))
		owner, err := encodeWorkJSON(mcpWorkRootOwner{SchemaVersion: 1, Namespace: "promcast/mcp-work-v1"})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(work, mcpWorkRootOwnerName), owner, 0o600))
		sentinel := filepath.Join(work, "operator-note.txt")
		require.NoError(t, os.WriteFile(sentinel, []byte("keep"), 0o600))

		_, err = New(Config{Root: root, OutputRoot: output})
		require.ErrorContains(t, err, "unowned entry")
		assert.FileExists(t, sentinel)
	})

	t.Run("owned operation with injected entry", func(t *testing.T) {
		root := t.TempDir()
		output := filepath.Join(root, "out")
		service, err := New(Config{Root: root, OutputRoot: output})
		require.NoError(t, err)
		operation, _, _, err := service.beginMigrationWork(
			[]byte(`{"title":"hostile entry"}`),
			time.Unix(1, 0),
			false,
		)
		require.NoError(t, err)
		sentinel := filepath.Join(output, operation.relative, "operator-note.txt")
		require.NoError(t, os.WriteFile(sentinel, []byte("keep"), 0o600))

		_, err = New(Config{Root: root, OutputRoot: output})
		require.ErrorContains(t, err, "unowned entry")
		assert.FileExists(t, sentinel)
	})
}

func TestMCPRecoveryRootEnumerationIsBoundedAndNonDestructive(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "out")
	work := filepath.Join(output, mcpWorkRootName)
	require.NoError(t, os.MkdirAll(work, 0o700))
	owner, err := encodeWorkJSON(mcpWorkRootOwner{SchemaVersion: 1, Namespace: "promcast/mcp-work-v1"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(work, mcpWorkRootOwnerName), owner, 0o600))
	for index := range maxMCPWorkOperations + 1 {
		token := fmt.Sprintf("%032x", index+1)
		require.NoError(t, os.Mkdir(filepath.Join(work, mcpWorkOperationPrefix+token), 0o700))
	}

	_, err = New(Config{Root: root, OutputRoot: output})
	require.ErrorContains(t, err, "exceeds the recovery inventory limit")
	entries, readErr := os.ReadDir(work)
	require.NoError(t, readErr)
	assert.Len(t, entries, maxMCPWorkOperations+2)
}

func TestMCPRecoveryRejectsOperationDirectorySubstitution(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "out")
	service, err := New(Config{Root: root, OutputRoot: output})
	require.NoError(t, err)
	operation, _, _, err := service.beginMigrationWork(
		[]byte(`{"title":"operation identity"}`),
		time.Unix(1, 0),
		false,
	)
	require.NoError(t, err)

	work, err := service.openMCPWorkRoot()
	require.NoError(t, err)
	defer func() { _ = work.Close() }()
	name := filepath.Base(operation.relative)
	before, err := work.Lstat(name)
	require.NoError(t, err)

	original := filepath.Join(root, "displaced-operation")
	operationPath := filepath.Join(output, operation.relative)
	require.NoError(t, os.Rename(operationPath, original))
	require.NoError(t, os.Mkdir(operationPath, 0o700))
	for _, metadata := range []string{mcpWorkOwnerName, mcpWorkPlanName, operation.phase} {
		data, readErr := os.ReadFile(filepath.Join(original, metadata))
		require.NoError(t, readErr)
		require.NoError(t, os.WriteFile(filepath.Join(operationPath, metadata), data, 0o600))
	}
	sentinel := filepath.Join(operationPath, "replacement-sentinel")
	require.NoError(t, os.WriteFile(sentinel, []byte("preserve"), 0o600))

	err = service.recoverPinnedWorkOperation(work, name, before)
	require.ErrorContains(t, err, "changed while it was opened")
	assert.FileExists(t, sentinel)
	assert.DirExists(t, original)
}

func TestMCPRecoveryRejectsOperationSubstitutionBeforeRemoval(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "out")
	service, err := New(Config{Root: root, OutputRoot: output})
	require.NoError(t, err)
	operation, _, _, err := service.beginMigrationWork(
		[]byte(`{"title":"removal identity"}`), time.Unix(1, 0), false,
	)
	require.NoError(t, err)
	work, err := service.openMCPWorkRoot()
	require.NoError(t, err)
	defer func() { _ = work.Close() }()
	name := filepath.Base(operation.relative)
	before, err := work.Lstat(name)
	require.NoError(t, err)
	original := filepath.Join(root, "displaced-before-removal")
	operationPath := filepath.Join(output, operation.relative)
	require.NoError(t, os.Rename(operationPath, original))
	require.NoError(t, os.Mkdir(operationPath, 0o700))

	err = removePinnedRecoveryOperation(work, name, before)
	require.ErrorContains(t, err, "changed before removal")
	assert.DirExists(t, operationPath)
	assert.DirExists(t, original)
}

func TestMCPRecoveryRemovesOnlyRecognizedInterruptedAtomicTemporaries(t *testing.T) {
	const nonce = "aaaaaaaaaaaaaaaaaaaaaaaa"

	t.Run("work root owner", func(t *testing.T) {
		root := t.TempDir()
		output := filepath.Join(root, "out")
		work := filepath.Join(output, mcpWorkRootName)
		require.NoError(t, os.MkdirAll(work, 0o700))
		require.NoError(t, os.WriteFile(
			filepath.Join(work, "."+mcpWorkRootOwnerName+".tmp-"+nonce),
			[]byte("partial"),
			0o600,
		))

		_, err := New(Config{Root: root, OutputRoot: output})
		require.NoError(t, err)
		entries, readErr := os.ReadDir(output)
		require.NoError(t, readErr)
		assert.Empty(t, entries)
	})

	t.Run("operation owner", func(t *testing.T) {
		root := t.TempDir()
		output := filepath.Join(root, "out")
		work := filepath.Join(output, mcpWorkRootName)
		token := "11111111111111111111111111111111"
		operation := filepath.Join(work, mcpWorkOperationPrefix+token)
		require.NoError(t, os.MkdirAll(operation, 0o700))
		owner, err := encodeWorkJSON(mcpWorkRootOwner{SchemaVersion: 1, Namespace: "promcast/mcp-work-v1"})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(work, mcpWorkRootOwnerName), owner, 0o600))
		require.NoError(t, os.WriteFile(
			filepath.Join(operation, "."+mcpWorkOwnerName+".tmp-"+nonce),
			[]byte("partial"),
			0o600,
		))

		_, err = New(Config{Root: root, OutputRoot: output})
		require.NoError(t, err)
		entries, readErr := os.ReadDir(output)
		require.NoError(t, readErr)
		assert.Empty(t, entries)
	})

	t.Run("owned plan", func(t *testing.T) {
		root := t.TempDir()
		output := filepath.Join(root, "out")
		service, err := New(Config{Root: root, OutputRoot: output})
		require.NoError(t, err)
		operation, _, _, err := service.beginMigrationWork(
			[]byte(`{"title":"temporary plan"}`), time.Unix(1, 0), false,
		)
		require.NoError(t, err)
		require.NoError(t, os.Remove(filepath.Join(output, operation.relative, mcpWorkPlanName)))
		require.NoError(t, os.Remove(filepath.Join(output, operation.relative, operation.phase)))
		require.NoError(t, os.WriteFile(
			filepath.Join(output, operation.relative, "."+mcpWorkPlanName+".tmp-"+nonce),
			[]byte("partial"),
			0o600,
		))

		_, err = New(Config{Root: root, OutputRoot: output})
		require.NoError(t, err)
		entries, readErr := os.ReadDir(output)
		require.NoError(t, readErr)
		assert.Empty(t, entries)
	})

	t.Run("owned payload member", func(t *testing.T) {
		root := t.TempDir()
		output := filepath.Join(root, "out")
		service, err := New(Config{Root: root, OutputRoot: output})
		require.NoError(t, err)
		operation, _, _, err := service.beginMigrationWork(
			[]byte(`{"title":"temporary payload"}`), time.Unix(1, 0), false,
		)
		require.NoError(t, err)
		require.NoError(t, operation.writeInventory(mcpWorkInventoryInitial, mcpWorkInventory{
			Stage: "initial", Payload: mcpWorkPayloadInitial, Files: []string{"artifact.json"},
		}))
		require.NoError(t, operation.createPayload(mcpWorkPayloadInitial))
		require.NoError(t, os.WriteFile(
			filepath.Join(output, operation.relative, mcpWorkPayloadInitial, ".artifact.json.tmp-"+nonce),
			[]byte("partial"),
			0o600,
		))

		_, err = New(Config{Root: root, OutputRoot: output})
		require.NoError(t, err)
		entries, readErr := os.ReadDir(output)
		require.NoError(t, readErr)
		assert.Empty(t, entries)
	})
}

func TestMCPRecoveryAcceptsLegacyV1WorkPlanWithoutStagingLocation(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "out")
	service, err := New(Config{Root: root, OutputRoot: output})
	require.NoError(t, err)
	operation, _, _, err := service.beginMigrationWork(
		[]byte(`{"title":"legacy work plan"}`), time.Unix(1, 0), false,
	)
	require.NoError(t, err)
	legacy := operation.plan
	legacy.SchemaVersion = 1
	legacy.StagingParent = ""
	data, err := encodeWorkJSON(legacy)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(output, operation.relative, mcpWorkPlanName), data, 0o600,
	))

	_, err = New(Config{Root: root, OutputRoot: output})
	require.NoError(t, err)
	entries, err := os.ReadDir(output)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestMCPRecoveryRejectsCorruptInstalledResultWithoutHidingAttempt(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "out")
	target := newRecoveryTarget(t, func() {})
	defer target.Close()
	command := recoveryChildCommand(t, root, output, target.URL, "migration", "migration-result-installed", "")
	data, err := command.CombinedOutput()
	requireRecoveryChildExit(t, err, data)

	entries, err := os.ReadDir(output)
	require.NoError(t, err)
	var migrationDirectory string
	for _, entry := range entries {
		if entry.Name() != mcpWorkRootName {
			migrationDirectory = filepath.Join(output, entry.Name())
		}
	}
	require.NotEmpty(t, migrationDirectory)
	pointer := filepath.Join(migrationDirectory, resultGeneration, "migration-result.json")
	require.NoError(t, os.WriteFile(pointer, []byte("{not-json\n"), 0o600))

	_, err = New(Config{Root: root, OutputRoot: output})
	require.Error(t, err)
	assert.FileExists(t, filepath.Join(migrationDirectory, "migration.json"))
	assert.NoFileExists(t, filepath.Join(migrationDirectory, "migration-result.json"))
	assert.DirExists(t, filepath.Join(output, mcpWorkRootName))
}

func TestMCPRecoveryRejectsSymlinkedMigrationPointer(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "out")
	target := newRecoveryTarget(t, func() {})
	defer target.Close()
	command := recoveryChildCommand(t, root, output, target.URL, "migration", "migration-initial-installed", "")
	data, err := command.CombinedOutput()
	requireRecoveryChildExit(t, err, data)
	directory := recoveryMigrationDirectory(t, output)
	pointer := filepath.Join(directory, "migration.json")
	require.NoError(t, os.Remove(pointer))
	require.NoError(t, os.Symlink(filepath.Join(attemptGeneration, "migration.json"), pointer))

	_, err = New(Config{Root: root, OutputRoot: output})
	require.ErrorContains(t, err, "not a supported regular file")
	info, statErr := os.Lstat(pointer)
	require.NoError(t, statErr)
	assert.NotZero(t, info.Mode()&os.ModeSymlink)
	assert.DirExists(t, filepath.Join(output, mcpWorkRootName))
}

func TestMCPRecoveryRejectsInstalledResultWithoutEitherPointer(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "out")
	target := newRecoveryTarget(t, func() {})
	defer target.Close()
	command := recoveryChildCommand(t, root, output, target.URL, "migration", "migration-result-installed", "")
	data, err := command.CombinedOutput()
	requireRecoveryChildExit(t, err, data)
	directory := recoveryMigrationDirectory(t, output)
	embedded := filepath.Join(directory, resultGeneration, "migration-result.json")
	require.NoError(t, os.Remove(embedded))

	_, err = New(Config{Root: root, OutputRoot: output})
	require.ErrorContains(t, err, "exists without either owned result pointer")
	assert.DirExists(t, filepath.Join(directory, resultGeneration))
	assert.FileExists(t, filepath.Join(directory, "migration.json"))
	assert.DirExists(t, filepath.Join(output, mcpWorkRootName))
}

func TestMCPRecoveryRejectsChangedRawRuleInput(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "out")
	rule := "groups:\n- name: recovery\n  rules:\n  - record: recovery:up:sum\n    expr: sum(up)\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "recovery-rule.yaml"), []byte(rule), 0o600))
	target := newRecoveryTarget(t, func() {})
	defer target.Close()
	command := recoveryChildCommand(t, root, output, target.URL, "migration-with-rule", "migration-initial-installed", "")
	data, err := command.CombinedOutput()
	requireRecoveryChildExit(t, err, data)
	directory := recoveryMigrationDirectory(t, output)
	storedRule := filepath.Join(directory, attemptGeneration, "source.rules.001.yaml")
	require.NoError(t, os.WriteFile(storedRule, []byte("groups: []\n"), 0o600))

	_, err = New(Config{Root: root, OutputRoot: output})
	require.ErrorContains(t, err, "does not match its persisted digest and size")
	assert.FileExists(t, storedRule)
	assert.DirExists(t, filepath.Join(output, mcpWorkRootName))
}

func TestMCPRecoveryRejectsAttemptEvidenceAsFinalResult(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "out")
	target := newRecoveryTarget(t, func() {})
	defer target.Close()
	command := recoveryChildCommand(t, root, output, target.URL, "migration", "migration-result-installed", "")
	data, err := command.CombinedOutput()
	requireRecoveryChildExit(t, err, data)
	directory := recoveryMigrationDirectory(t, output)
	attempt := filepath.Join(directory, attemptGeneration)
	result := filepath.Join(directory, resultGeneration)
	require.NoError(t, os.RemoveAll(result))
	require.NoError(t, os.CopyFS(result, os.DirFS(attempt)))
	pointerData, err := os.ReadFile(filepath.Join(directory, "migration.json"))
	require.NoError(t, err)
	state, err := decodeManifest(pointerData)
	require.NoError(t, err)
	state.Generation = resultGeneration
	pointerData, err = encodeWorkJSON(state)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(result, "migration.json"), pointerData, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(result, "migration-result.json"), pointerData, 0o600))

	_, err = New(Config{Root: root, OutputRoot: output})
	require.ErrorContains(t, err, "does not carry a terminal target outcome")
	assert.DirExists(t, filepath.Join(output, mcpWorkRootName))
	assert.FileExists(t, filepath.Join(directory, "migration.json"))
}

func TestMCPRecoveryPinsValidationRunIdentity(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "out")
	service, err := New(Config{Root: root, OutputRoot: output})
	require.NoError(t, err)
	relative := filepath.Join("migration-validation-pin", "validations", "run-11111111111111111111111111111111")
	runPath := filepath.Join(output, relative)
	require.NoError(t, os.MkdirAll(runPath, 0o700))
	outputRoot, err := openVerifiedRoot(output, service.outputRootInfo)
	require.NoError(t, err)
	defer func() { _ = outputRoot.Close() }()
	before, err := outputRoot.Lstat(relative)
	require.NoError(t, err)
	displaced := filepath.Join(root, "displaced-validation")
	require.NoError(t, os.Rename(runPath, displaced))
	require.NoError(t, os.Mkdir(runPath, 0o700))
	sentinel := filepath.Join(runPath, "replacement-sentinel")
	require.NoError(t, os.WriteFile(sentinel, []byte("preserve"), 0o600))

	err = service.verifyPinnedValidationRun(outputRoot, relative, before)
	require.ErrorContains(t, err, "changed while it was opened")
	assert.FileExists(t, sentinel)
	assert.DirExists(t, displaced)
}

func TestMCPRejectsOversizedRecoveryStateBeforePublication(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "out")
	service, err := New(Config{Root: root, OutputRoot: output})
	require.NoError(t, err)
	result, err := service.handleMigrateDashboard(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{
			"grafana_json": `{"uid":"oversized-state","title":"Oversized state","panels":[]}`,
			"variables":    []string{"oversized=" + strings.Repeat("x", maxMCPWorkMetadataBytes)},
		}},
	})
	require.NoError(t, err)
	require.True(t, result.IsError)
	assert.Contains(t, fmt.Sprint(result.Content), "recovery metadata limit")
	entries, err := os.ReadDir(output)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestMCPRecoveryPreservesImpossiblePlanInventoryAndPhaseStates(t *testing.T) {
	t.Run("migration with validation inventory", func(t *testing.T) {
		root := t.TempDir()
		output := filepath.Join(root, "out")
		service, err := New(Config{Root: root, OutputRoot: output})
		require.NoError(t, err)
		operation, _, _, err := service.beginMigrationWork([]byte(`{"title":"shape"}`), time.Unix(1, 0), false)
		require.NoError(t, err)
		require.NoError(t, operation.writeInventory(mcpWorkInventoryValidate, mcpWorkInventory{
			Stage: "validation", Payload: mcpWorkPayloadValidation,
		}))

		_, err = New(Config{Root: root, OutputRoot: output})
		require.ErrorContains(t, err, "migration plan carries validation inventory")
		assert.DirExists(t, filepath.Join(output, operation.relative))
	})

	t.Run("non-import result without initial inventory", func(t *testing.T) {
		root := t.TempDir()
		output := filepath.Join(root, "out")
		service, err := New(Config{Root: root, OutputRoot: output})
		require.NoError(t, err)
		operation, _, _, err := service.beginMigrationWork([]byte(`{"title":"shape"}`), time.Unix(1, 0), false)
		require.NoError(t, err)
		require.NoError(t, operation.writeInventory(mcpWorkInventoryResult, mcpWorkInventory{
			Stage: "result", Payload: mcpWorkPayloadResult,
		}))

		_, err = New(Config{Root: root, OutputRoot: output})
		require.ErrorContains(t, err, "result inventory requires initial inventory and importRequested=true")
		assert.DirExists(t, filepath.Join(output, operation.relative))
	})

	t.Run("migration with validation phase", func(t *testing.T) {
		root := t.TempDir()
		output := filepath.Join(root, "out")
		service, err := New(Config{Root: root, OutputRoot: output})
		require.NoError(t, err)
		operation, _, _, err := service.beginMigrationWork([]byte(`{"title":"shape"}`), time.Unix(1, 0), false)
		require.NoError(t, err)
		require.NoError(t, operation.advancePhase(".phase-validation-prepared"))

		_, err = New(Config{Root: root, OutputRoot: output})
		require.ErrorContains(t, err, "migration plan carries phase")
		assert.DirExists(t, filepath.Join(output, operation.relative))
	})

	t.Run("validation with migration inventory", func(t *testing.T) {
		root := t.TempDir()
		output := filepath.Join(root, "out")
		service, err := New(Config{Root: root, OutputRoot: output})
		require.NoError(t, err)
		migrationID := "migration-shape-validation"
		validationRoot := filepath.Join(migrationID, "validations")
		operation, err := service.startMCPWork(mcpWorkPlan{
			Kind:             "validation",
			MigrationID:      migrationID,
			ValidationTarget: validationRoot,
			ValidationRun:    filepath.Join(validationRoot, "run-11111111111111111111111111111111"),
		})
		require.NoError(t, err)
		require.NoError(t, operation.writeInventory(mcpWorkInventoryInitial, mcpWorkInventory{
			Stage: "initial", Payload: mcpWorkPayloadInitial,
		}))

		_, err = New(Config{Root: root, OutputRoot: output})
		require.ErrorContains(t, err, "validation plan carries migration inventory")
		assert.DirExists(t, filepath.Join(output, operation.relative))
	})
}

func recoveryMigrationDirectory(t *testing.T, output string) string {
	t.Helper()
	entries, err := os.ReadDir(output)
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.Name() != mcpWorkRootName {
			return filepath.Join(output, entry.Name())
		}
	}
	t.Fatal("recovery output has no visible migration directory")
	return ""
}
