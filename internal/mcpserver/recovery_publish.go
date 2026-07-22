package mcpserver

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/mansiverma897993/noz-in/internal/app"
	"github.com/mansiverma897993/noz-in/internal/artifactset"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
)

func (service *Service) allocateMigrationID(data []byte, now time.Time) (string, string, error) {
	digest := sha256Digest(data)
	base := fmt.Sprintf("dashboard-%s-%s", now.Format("20060102-150405"), digest[:8])
	root, err := openVerifiedRoot(service.config.OutputRoot, service.outputRootInfo)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = root.Close() }()
	for sequence := 1; sequence <= 100; sequence++ {
		id := base
		if sequence > 1 {
			id = fmt.Sprintf("%s-%d", base, sequence)
		}
		directory, err := service.migrationDirectory(id)
		if err != nil {
			return "", "", err
		}
		if _, err := root.Lstat(id); errors.Is(err, os.ErrNotExist) {
			return directory, id, nil
		} else if err != nil {
			return "", "", fmt.Errorf("inspect migration destination %q: %w", id, err)
		}
	}
	return "", "", fmt.Errorf("could not allocate a unique migration directory")
}

func sha256Digest(data []byte) string {
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest[:])
}

func (service *Service) beginMigrationWork(
	data []byte,
	now time.Time,
	importRequested bool,
) (*mcpWorkOperation, string, string, error) {
	directory, id, err := service.allocateMigrationID(data, now)
	if err != nil {
		return nil, "", "", err
	}
	operation, err := service.startMCPWork(mcpWorkPlan{
		Kind: "migration", MigrationID: id, ImportRequested: importRequested,
	})
	if err != nil {
		return nil, "", "", err
	}
	return operation, directory, id, nil
}

func (service *Service) beginValidationWork(
	migrationID string,
) (*mcpWorkOperation, string, string, error) {
	if err := service.ensureMigrationDirectoryStable(migrationID); err != nil {
		return nil, "", "", err
	}
	root, err := openVerifiedRoot(service.config.OutputRoot, service.outputRootInfo)
	if err != nil {
		return nil, "", "", err
	}
	defer func() { _ = root.Close() }()
	validationRoot := filepath.Join(migrationID, "validations")
	rootInfo, err := root.Lstat(validationRoot)
	rootMissing := errors.Is(err, os.ErrNotExist)
	if err != nil && !rootMissing {
		return nil, "", "", fmt.Errorf("inspect validation publication root: %w", err)
	}
	if !rootMissing && !rootInfo.IsDir() {
		return nil, "", "", fmt.Errorf("validation publication root is not a real directory")
	}
	for range 100 {
		var nonce [12]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return nil, "", "", fmt.Errorf("generate validation publication id: %w", err)
		}
		run := filepath.Join(validationRoot, "run-"+hex.EncodeToString(nonce[:]))
		if _, err := root.Lstat(run); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, "", "", fmt.Errorf("inspect validation publication destination: %w", err)
		}
		target := run
		if rootMissing {
			target = validationRoot
		}
		operation, err := service.startMCPWork(mcpWorkPlan{
			Kind: "validation", MigrationID: migrationID,
			ValidationTarget: target, ValidationRun: run,
		})
		if err != nil {
			return nil, "", "", err
		}
		return operation, target, run, nil
	}
	return nil, "", "", fmt.Errorf("could not allocate a unique validation publication id")
}

func (service *Service) publishValidationWork(
	operation *mcpWorkOperation,
	stagingDirectory string,
	binding *reporttypes.ArtifactSetBinding,
	targetRelative string,
	runRelative string,
) (string, error) {
	directories, files, err := privateStagingInventory(stagingDirectory, binding, nil)
	if err != nil {
		return "", err
	}
	inventory := mcpWorkInventory{
		Token: operation.token, Stage: "validation", Payload: mcpWorkPayloadValidation,
		Directories: directories, Files: files,
	}
	publishRelative := filepath.Join(operation.relative, mcpWorkPayloadValidation)
	if targetRelative != runRelative {
		runName := filepath.Base(runRelative)
		inventory = prefixedWorkInventory(
			operation.token,
			"validation",
			mcpWorkPayloadValidation,
			runName,
			directories,
			files,
		)
		publishRelative = filepath.Join(publishRelative, runName)
	}
	if err := normalizeWorkInventory(&inventory, mcpWorkInventoryValidate); err != nil {
		return "", err
	}
	if err := operation.writeInventory(mcpWorkInventoryValidate, inventory); err != nil {
		return "", err
	}
	service.runCrashBarrier("validation-inventory")
	if err := operation.createPayload(mcpWorkPayloadValidation); err != nil {
		return "", err
	}
	if targetRelative != runRelative {
		if err := service.createOutputDirectory(publishRelative); err != nil {
			return "", err
		}
	}
	if err := service.publishPrivateStagingDirectory(
		stagingDirectory,
		publishRelative,
		binding,
		nil,
	); err != nil {
		return "", err
	}
	if err := operation.advancePhase(".phase-validation-prepared"); err != nil {
		return "", err
	}
	if err := service.installWorkPayload(operation, mcpWorkPayloadValidation, targetRelative); err != nil {
		return "", err
	}
	service.runCrashBarrier("validation-installed")
	if err := operation.advancePhase(".phase-validation-installed"); err != nil {
		return "", err
	}
	if err := service.verifyValidationRun(runRelative); err != nil {
		return "", err
	}
	return filepath.Join(service.config.OutputRoot, runRelative), nil
}

func (service *Service) publishInitialMigration(
	operation *mcpWorkOperation,
	stagingDirectory string,
	directory string,
	migrationID string,
	generation string,
	result app.GrafanaResult,
	sourceName string,
	ruleNames []string,
	variables map[string]string,
	rateInterval time.Duration,
	dashboardIdentity string,
	sourceNamespace string,
) (migrationCheckpoint, error) {
	service.runCrashBarrier("migration-private-staging-ready")
	if generation != attemptGeneration && generation != resultGeneration {
		return migrationCheckpoint{}, fmt.Errorf("unsupported initial migration generation %q", generation)
	}
	if err := verifyCommittedGrafanaResult(result); err != nil {
		return migrationCheckpoint{}, fmt.Errorf("verify staged dashboard artifacts: %w", err)
	}
	ruleBindings, err := bindMigrationRuleInputs(stagingDirectory, ruleNames)
	if err != nil {
		return migrationCheckpoint{}, err
	}
	state, stateData, err := migrationState(
		migrationID, generation, result, sourceName, ruleNames, ruleBindings, variables,
		rateInterval, dashboardIdentity, sourceNamespace,
	)
	if err != nil {
		return migrationCheckpoint{}, err
	}
	directories, files, err := privateStagingInventory(stagingDirectory, result.Evidence.ArtifactSet, append([]string{sourceName}, ruleNames...))
	if err != nil {
		return migrationCheckpoint{}, err
	}
	inventory := prefixedWorkInventory(operation.token, "initial", mcpWorkPayloadInitial, generation, directories, files)
	inventory.Files = append(inventory.Files, filepath.Join(generation, "migration.json"), "migration.json")
	if err := normalizeWorkInventory(&inventory, mcpWorkInventoryInitial); err != nil {
		return migrationCheckpoint{}, err
	}
	if err := operation.writeInventory(mcpWorkInventoryInitial, inventory); err != nil {
		return migrationCheckpoint{}, err
	}
	service.runCrashBarrier("migration-initial-inventory")
	if err := operation.createPayload(mcpWorkPayloadInitial); err != nil {
		return migrationCheckpoint{}, err
	}
	generationRelative := filepath.Join(operation.relative, mcpWorkPayloadInitial, generation)
	if err := service.createOutputDirectory(generationRelative); err != nil {
		return migrationCheckpoint{}, err
	}
	if err := service.publishPrivateStagingDirectory(
		stagingDirectory,
		generationRelative,
		result.Evidence.ArtifactSet,
		append([]string{sourceName}, ruleNames...),
	); err != nil {
		return migrationCheckpoint{}, err
	}
	if err := service.writeOutputAtomic(filepath.Join(generationRelative, "migration.json"), stateData); err != nil {
		return migrationCheckpoint{}, err
	}
	if err := service.writeOutputAtomic(
		filepath.Join(operation.relative, mcpWorkPayloadInitial, "migration.json"),
		stateData,
	); err != nil {
		return migrationCheckpoint{}, err
	}
	if err := operation.advancePhase(".phase-initial-prepared"); err != nil {
		return migrationCheckpoint{}, err
	}
	if err := service.installWorkPayload(operation, mcpWorkPayloadInitial, migrationID); err != nil {
		return migrationCheckpoint{}, err
	}
	service.runCrashBarrier("migration-initial-installed")
	if err := operation.advancePhase(".phase-initial-installed"); err != nil {
		return migrationCheckpoint{}, err
	}
	relocated := relocateGrafanaResult(result, filepath.Join(directory, generation))
	if err := verifyCommittedGrafanaResult(relocated); err != nil {
		return migrationCheckpoint{}, fmt.Errorf("verify published dashboard artifacts: %w", err)
	}
	if err := service.verifyMigrationPointer(migrationID, "migration.json", generation); err != nil {
		return migrationCheckpoint{}, fmt.Errorf("verify published migration pointer: %w", err)
	}
	return migrationCheckpoint{result: relocated, state: state}, nil
}

func (service *Service) publishMigrationResult(
	operation *mcpWorkOperation,
	stagingDirectory string,
	directory string,
	migrationID string,
	result app.GrafanaResult,
	sourceName string,
	ruleNames []string,
	variables map[string]string,
	rateInterval time.Duration,
	dashboardIdentity string,
	sourceNamespace string,
) (migrationCheckpoint, error) {
	if service.publicationFault != nil {
		if err := service.publicationFault("migration-result"); err != nil {
			return migrationCheckpoint{}, err
		}
	}
	if err := verifyCommittedGrafanaResult(result); err != nil {
		return migrationCheckpoint{}, fmt.Errorf("verify staged dashboard artifacts: %w", err)
	}
	ruleBindings, err := bindMigrationRuleInputs(stagingDirectory, ruleNames)
	if err != nil {
		return migrationCheckpoint{}, err
	}
	state, stateData, err := migrationState(
		migrationID, resultGeneration, result, sourceName, ruleNames, ruleBindings, variables,
		rateInterval, dashboardIdentity, sourceNamespace,
	)
	if err != nil {
		return migrationCheckpoint{}, err
	}
	directories, files, err := privateStagingInventory(stagingDirectory, result.Evidence.ArtifactSet, append([]string{sourceName}, ruleNames...))
	if err != nil {
		return migrationCheckpoint{}, err
	}
	inventory := mcpWorkInventory{
		Token: operation.token, Stage: "result", Payload: mcpWorkPayloadResult,
		Directories: directories, Files: append(files, "migration.json", "migration-result.json"),
	}
	if err := normalizeWorkInventory(&inventory, mcpWorkInventoryResult); err != nil {
		return migrationCheckpoint{}, err
	}
	if err := operation.writeInventory(mcpWorkInventoryResult, inventory); err != nil {
		return migrationCheckpoint{}, err
	}
	service.runCrashBarrier("migration-result-inventory")
	if err := operation.createPayload(mcpWorkPayloadResult); err != nil {
		return migrationCheckpoint{}, err
	}
	payloadRelative := filepath.Join(operation.relative, mcpWorkPayloadResult)
	if err := service.publishPrivateStagingDirectory(
		stagingDirectory,
		payloadRelative,
		result.Evidence.ArtifactSet,
		append([]string{sourceName}, ruleNames...),
	); err != nil {
		return migrationCheckpoint{}, err
	}
	if err := service.writeOutputAtomic(filepath.Join(payloadRelative, "migration.json"), stateData); err != nil {
		return migrationCheckpoint{}, err
	}
	// This is deliberately written inside the staged generation. Publishing it
	// later is a same-filesystem rename, so result evidence never depends on a
	// fresh quota admission after the target request has completed.
	if err := service.writeOutputAtomic(filepath.Join(payloadRelative, "migration-result.json"), stateData); err != nil {
		return migrationCheckpoint{}, err
	}
	if err := operation.advancePhase(".phase-result-prepared"); err != nil {
		return migrationCheckpoint{}, err
	}
	service.runCrashBarrier("migration-result-prepared")
	if err := service.installWorkPayload(
		operation,
		mcpWorkPayloadResult,
		filepath.Join(migrationID, resultGeneration),
	); err != nil {
		return migrationCheckpoint{}, err
	}
	service.runCrashBarrier("migration-result-installed")
	if err := operation.advancePhase(".phase-result-installed"); err != nil {
		return migrationCheckpoint{}, err
	}
	if err := service.promoteResultPointer(migrationID); err != nil {
		return migrationCheckpoint{}, err
	}
	service.runCrashBarrier("migration-result-pointer-published")
	if err := operation.advancePhase(".phase-result-published"); err != nil {
		return migrationCheckpoint{}, err
	}
	relocated := relocateGrafanaResult(result, filepath.Join(directory, resultGeneration))
	if err := verifyCommittedGrafanaResult(relocated); err != nil {
		return migrationCheckpoint{}, fmt.Errorf("verify published dashboard artifacts: %w", err)
	}
	if err := service.verifyMigrationPointer(migrationID, "migration-result.json", resultGeneration); err != nil {
		return migrationCheckpoint{}, err
	}
	return migrationCheckpoint{result: relocated, state: state}, nil
}

func migrationState(
	migrationID string,
	generation string,
	result app.GrafanaResult,
	sourceName string,
	ruleNames []string,
	ruleBindings []reporttypes.ArtifactBinding,
	variables map[string]string,
	rateInterval time.Duration,
	dashboardIdentity string,
	sourceNamespace string,
) (manifest, []byte, error) {
	relocated := relocateGrafanaResult(result, generation)
	state := manifest{
		SchemaVersion:     2,
		MigrationID:       migrationID,
		Generation:        generation,
		Source:            sourceName,
		Rules:             append([]string(nil), ruleNames...),
		RuleBindings:      append([]reporttypes.ArtifactBinding(nil), ruleBindings...),
		Variables:         variables,
		Report:            filepath.Base(relocated.ReportPath),
		Dashboard:         filepath.Base(relocated.DashboardPath),
		HTML:              filepath.Base(relocated.HTMLPath),
		RateInterval:      rateInterval.String(),
		DashboardIdentity: dashboardIdentity,
		SourceNamespace:   sourceNamespace,
	}
	data, err := encodeWorkJSON(state)
	if err != nil {
		return manifest{}, nil, fmt.Errorf("encode migration state: %w", err)
	}
	if int64(len(data)) > maxMCPWorkMetadataBytes {
		return manifest{}, nil, fmt.Errorf(
			"migration state exceeds the %d-byte recovery metadata limit",
			maxMCPWorkMetadataBytes,
		)
	}
	return state, data, nil
}

func bindMigrationRuleInputs(
	stagingDirectory string,
	ruleNames []string,
) ([]reporttypes.ArtifactBinding, error) {
	if len(ruleNames) == 0 {
		return nil, nil
	}
	before, err := os.Lstat(stagingDirectory)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() {
		return nil, fmt.Errorf("private MCP staging directory is not a real directory")
	}
	root, err := os.OpenRoot(stagingDirectory)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	opened, err := root.Stat(".")
	if err != nil || !opened.IsDir() || !os.SameFile(before, opened) {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("private MCP staging directory changed while it was opened")
	}
	bindings := make([]reporttypes.ArtifactBinding, 0, len(ruleNames))
	for _, name := range ruleNames {
		data, err := readRootedArtifactBounded(root, name, stagingDirectory, maxMCPArtifactSize)
		if err != nil {
			return nil, fmt.Errorf("bind migration rule input %q: %w", name, err)
		}
		digest := sha256.Sum256(data)
		bindings = append(bindings, reporttypes.ArtifactBinding{
			Path: name, SHA256: fmt.Sprintf("%x", digest[:]), SizeBytes: int64(len(data)),
		})
	}
	return bindings, nil
}

func privateStagingInventory(
	stagingDirectory string,
	binding *reporttypes.ArtifactSetBinding,
	extraFiles []string,
) ([]string, []string, error) {
	if binding == nil {
		return nil, nil, fmt.Errorf("private MCP staging report has no artifact-set binding")
	}
	root, err := os.OpenRoot(stagingDirectory)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = root.Close() }()
	retained, err := artifactset.InspectRetainedStorage(root, *binding, artifactset.KindDashboard)
	if err != nil {
		return nil, nil, err
	}
	directories, entries, err := inspectPrivateStagingTree(root, retained, extraFiles)
	if err != nil {
		return nil, nil, err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		files = append(files, entry.relative)
	}
	return directories, files, nil
}

func prefixedWorkInventory(
	token, stage, payload, prefix string,
	directories, files []string,
) mcpWorkInventory {
	result := mcpWorkInventory{Token: token, Stage: stage, Payload: payload}
	result.Directories = make([]string, 0, len(directories)+1)
	result.Directories = append(result.Directories, prefix)
	for _, path := range directories {
		result.Directories = append(result.Directories, filepath.Join(prefix, path))
	}
	result.Files = make([]string, 0, len(files))
	for _, path := range files {
		result.Files = append(result.Files, filepath.Join(prefix, path))
	}
	return result
}

func normalizeWorkInventory(inventory *mcpWorkInventory, name string) error {
	inventory.SchemaVersion = 1
	sort.Strings(inventory.Directories)
	sort.Strings(inventory.Files)
	return validateWorkInventory(*inventory, inventory.Token, name)
}

func (service *Service) installWorkPayload(
	operation *mcpWorkOperation,
	payload string,
	destination string,
) error {
	if !filepath.IsLocal(destination) || destination == "." {
		return fmt.Errorf("MCP publication destination %q is not local", destination)
	}
	parent := filepath.Dir(destination)
	if parent != "." {
		if err := service.ensureOutputDirectoryStable(parent); err != nil {
			return err
		}
	}
	root, err := openVerifiedRoot(service.config.OutputRoot, service.outputRootInfo)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	source := filepath.Join(operation.relative, payload)
	if sourceInfo, err := root.Lstat(source); err != nil {
		return fmt.Errorf("inspect complete MCP work payload %q: %w", payload, err)
	} else if !sourceInfo.IsDir() {
		return fmt.Errorf("complete MCP work payload %q is not a real directory", payload)
	}
	if _, err := root.Lstat(destination); err == nil {
		return fmt.Errorf("refuse to replace existing MCP publication destination %q", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect MCP publication destination %q: %w", destination, err)
	}
	if err := root.Rename(source, destination); err != nil {
		return fmt.Errorf("publish complete MCP payload at %q: %w", destination, err)
	}
	if err := syncRootDirectory(root, operation.relative); err != nil {
		return err
	}
	if err := syncRootDirectory(root, parent); err != nil {
		return err
	}
	return nil
}

func (service *Service) runCrashBarrier(name string) {
	if service.crashBarrier != nil {
		service.crashBarrier(name)
	}
}
