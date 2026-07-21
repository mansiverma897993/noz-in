package mcpserver

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mansiverma897993/signoz/internal/artifactset"
	"github.com/mansiverma897993/signoz/pkg/reporttypes"
)

func (service *Service) recoverPublishedWork(operation recoveredWorkOperation) error {
	plan := *operation.plan
	switch plan.Kind {
	case "migration":
		initial, initialPrepared := operation.inventories[mcpWorkInventoryInitial]
		_, resultPrepared := operation.inventories[mcpWorkInventoryResult]
		visible, err := service.outputEntryIsDirectory(plan.MigrationID)
		if err != nil {
			return err
		}
		initialPayloadPresent := initialPrepared && operation.present[initial.Payload]
		if initialPayloadPresent {
			if visible {
				return fmt.Errorf("migration destination %q exists while its complete payload remains staged", plan.MigrationID)
			}
			return nil
		}
		if !initialPrepared {
			if visible {
				return fmt.Errorf("migration destination %q exists before an owned publication inventory", plan.MigrationID)
			}
			return nil
		}
		// The inventory is durable before its payload directory is created. If
		// neither the payload nor destination exists, publication never began and
		// this exact owned operation is safe to reclaim.
		if !visible {
			return nil
		}
		if resultPrepared {
			return service.recoverMigrationResultPointer(plan.MigrationID, plan.ImportRequested)
		}
		return service.verifyInitialMigrationPointer(plan.MigrationID, plan.ImportRequested)
	case "validation":
		inventory, prepared := operation.inventories[mcpWorkInventoryValidate]
		visible, err := service.outputEntryIsDirectory(plan.ValidationTarget)
		if err != nil {
			return err
		}
		payloadPresent := prepared && operation.present[inventory.Payload]
		if payloadPresent {
			if visible {
				return fmt.Errorf("validation destination %q exists while its complete payload remains staged", plan.ValidationTarget)
			}
			return nil
		}
		if !prepared {
			if visible && plan.ValidationTarget == plan.ValidationRun {
				return fmt.Errorf("validation destination %q exists before an owned publication inventory", plan.ValidationTarget)
			}
			return nil
		}
		// As with migrations, an inventory without either its payload or visible
		// destination is the bounded pre-publication crash window.
		if !visible {
			return nil
		}
		return service.verifyValidationRun(plan.ValidationRun)
	default:
		return fmt.Errorf("unsupported MCP work kind %q", plan.Kind)
	}
}

func (service *Service) outputEntryIsDirectory(relative string) (bool, error) {
	root, err := openVerifiedRoot(service.config.OutputRoot, service.outputRootInfo)
	if err != nil {
		return false, err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(relative)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("output entry %q is not a real directory", relative)
	}
	return true, nil
}

func (service *Service) verifyMigrationPointer(id, pointer, expectedGeneration string) error {
	_, _, err := service.verifyMigrationPointerAt(id, "", pointer, expectedGeneration)
	return err
}

func (service *Service) verifyMigrationPointerAt(
	id string,
	pointerGeneration string,
	pointer string,
	expectedGeneration string,
) (manifest, reporttypes.Report, error) {
	data, err := service.readMigrationGenerationMember(
		id,
		pointerGeneration,
		pointer,
		maxMCPWorkMetadataBytes,
	)
	if err != nil {
		return manifest{}, reporttypes.Report{}, err
	}
	state, err := decodeManifest(data)
	if err != nil {
		return manifest{}, reporttypes.Report{}, err
	}
	if state.MigrationID != id || expectedGeneration != "" && state.Generation != expectedGeneration {
		return manifest{}, reporttypes.Report{}, fmt.Errorf("migration pointer %q selects unexpected id or generation", pointer)
	}
	self, err := service.readMigrationGenerationMember(
		id,
		state.Generation,
		"migration.json",
		maxMCPWorkMetadataBytes,
	)
	if err != nil {
		return manifest{}, reporttypes.Report{}, err
	}
	if !bytes.Equal(data, self) {
		return manifest{}, reporttypes.Report{}, fmt.Errorf("migration pointer %q does not match its generation manifest", pointer)
	}
	evidence, err := service.verifyMigrationGeneration(id, state)
	if err != nil {
		return manifest{}, reporttypes.Report{}, err
	}
	return state, evidence, nil
}

func (service *Service) readMigrationGenerationMember(
	id string,
	generation string,
	name string,
	maxSize int64,
) ([]byte, error) {
	root, displayPath, err := service.openMigrationGenerationRoot(id, generation)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return readRootedArtifactBounded(root, name, displayPath, maxSize)
}

func (service *Service) verifyMigrationGeneration(id string, state manifest) (reporttypes.Report, error) {
	stored, err := service.readDashboardReport(id, state, state.Dashboard, state.HTML)
	if err != nil {
		return reporttypes.Report{}, err
	}
	generation, displayPath, err := service.openMigrationGenerationRoot(id, state.Generation)
	if err != nil {
		return reporttypes.Report{}, err
	}
	defer func() { _ = generation.Close() }()
	sourceData, err := readRootedArtifactBounded(generation, state.Source, displayPath, maxMCPArtifactSize)
	if err != nil {
		return reporttypes.Report{}, fmt.Errorf("verify migration source member %q: %w", state.Source, err)
	}
	sourceDigest := sha256.Sum256(sourceData)
	if stored.Evidence.Source.SHA256 == "" || stored.Evidence.Source.SHA256 != fmt.Sprintf("%x", sourceDigest[:]) {
		return reporttypes.Report{}, fmt.Errorf("migration source member does not match the report source digest")
	}
	if state.SchemaVersion < 2 && len(state.Rules) != 0 {
		return reporttypes.Report{}, fmt.Errorf("legacy migration rule inputs have no digest bindings and cannot be accepted during crash recovery")
	}
	for _, binding := range state.RuleBindings {
		data, err := readRootedArtifactBounded(generation, binding.Path, displayPath, maxMCPArtifactSize)
		if err != nil {
			return reporttypes.Report{}, fmt.Errorf("verify migration source member %q: %w", binding.Path, err)
		}
		digest := sha256.Sum256(data)
		if int64(len(data)) != binding.SizeBytes || fmt.Sprintf("%x", digest[:]) != binding.SHA256 {
			return reporttypes.Report{}, fmt.Errorf("migration rule input %q does not match its persisted digest and size", binding.Path)
		}
	}
	if state.Generation == attemptGeneration {
		if stored.Evidence.Run.Flags == nil || stored.Evidence.Run.Flags["importAttempted"] != true ||
			stored.Evidence.Run.Flags["targetAction"] != "attempted" {
			return reporttypes.Report{}, fmt.Errorf("attempt generation does not carry conservative attempted-write evidence")
		}
	}
	return stored.Evidence, nil
}

func (service *Service) verifyInitialMigrationPointer(id string, importRequested bool) error {
	state, evidence, err := service.verifyMigrationPointerAt(id, "", "migration.json", "")
	if err != nil {
		return err
	}
	switch state.Generation {
	case attemptGeneration:
		if !importRequested {
			return fmt.Errorf("migration without an import request selected attempted-write evidence")
		}
		return nil
	case resultGeneration:
		flags := evidence.Run.Flags
		if flags == nil || flags["importRequested"] != importRequested ||
			flags["importAttempted"] != false || flags["importSucceeded"] != false {
			return fmt.Errorf("initial result generation does not prove that target import was skipped before any attempt")
		}
		targetAction, actionOK := flags["targetAction"].(string)
		if !actionOK || importRequested && targetAction != "skipped" ||
			!importRequested && targetAction != "offline" && targetAction != "dry_run" && targetAction != "skipped" {
			return fmt.Errorf("initial result generation carries incoherent non-attempt target action %q", targetAction)
		}
		return nil
	default:
		return fmt.Errorf("initial migration pointer selects unsupported generation %q", state.Generation)
	}
}

func (service *Service) recoverMigrationResultPointer(id string, importRequested bool) error {
	rootPointer := filepath.Join(id, "migration-result.json")
	embeddedPointer := filepath.Join(id, resultGeneration, "migration-result.json")
	root, err := openVerifiedRoot(service.config.OutputRoot, service.outputRootInfo)
	if err != nil {
		return err
	}
	rootInfo, rootErr := root.Lstat(rootPointer)
	embeddedInfo, embeddedErr := root.Lstat(embeddedPointer)
	resultInfo, resultErr := root.Lstat(filepath.Join(id, resultGeneration))
	rootExists := rootErr == nil
	embeddedExists := embeddedErr == nil
	resultExists := resultErr == nil
	if rootErr != nil && !errors.Is(rootErr, os.ErrNotExist) {
		_ = root.Close()
		return rootErr
	}
	if embeddedErr != nil && !errors.Is(embeddedErr, os.ErrNotExist) {
		_ = root.Close()
		return embeddedErr
	}
	if resultErr != nil && !errors.Is(resultErr, os.ErrNotExist) {
		_ = root.Close()
		return resultErr
	}
	if rootExists && !rootInfo.Mode().IsRegular() || embeddedExists && !embeddedInfo.Mode().IsRegular() {
		_ = root.Close()
		return fmt.Errorf("migration result pointer is not a regular file")
	}
	if resultExists && !resultInfo.IsDir() {
		_ = root.Close()
		return fmt.Errorf("migration result generation is not a real directory")
	}
	if rootExists && embeddedExists {
		_ = root.Close()
		return fmt.Errorf("migration result pointer exists in both staged and published locations")
	}
	if !rootExists && !embeddedExists {
		_ = root.Close()
		if resultExists {
			return fmt.Errorf("migration result generation exists without either owned result pointer; preserving recovery state")
		}
		return service.verifyInitialMigrationPointer(id, importRequested)
	}
	if err := root.Close(); err != nil {
		return err
	}
	pointerGeneration := ""
	if embeddedExists {
		pointerGeneration = resultGeneration
	}
	_, evidence, err := service.verifyMigrationPointerAt(
		id,
		pointerGeneration,
		"migration-result.json",
		resultGeneration,
	)
	if err != nil {
		return err
	}
	if importRequested {
		if err := verifyRecoveredFinalOutcome(evidence); err != nil {
			return err
		}
		if err := service.verifyMigrationPointer(id, "migration.json", attemptGeneration); err != nil {
			return fmt.Errorf("verify attempted evidence before accepting recovered result: %w", err)
		}
	}
	if embeddedExists {
		if err := service.promoteResultPointer(id); err != nil {
			return err
		}
	}
	return service.verifyMigrationPointer(id, "migration-result.json", resultGeneration)
}

func verifyRecoveredFinalOutcome(evidence reporttypes.Report) error {
	flags := evidence.Run.Flags
	if flags == nil || flags["importRequested"] != true || flags["importAttempted"] != true {
		return fmt.Errorf("migration result does not carry completed attempted-import evidence")
	}
	succeeded, succeededOK := flags["importSucceeded"].(bool)
	action, actionOK := flags["targetAction"].(string)
	if !succeededOK || !actionOK || action == "attempted" {
		return fmt.Errorf("migration result does not carry a terminal target outcome")
	}
	dashboardID, _ := flags["targetDashboardID"].(string)
	targetError, _ := flags["targetError"].(string)
	targetSkipped, _ := flags["targetSkippedReason"].(string)
	if succeeded {
		if action != "created" && action != "updated" || strings.TrimSpace(dashboardID) == "" ||
			strings.TrimSpace(targetError) != "" || strings.TrimSpace(targetSkipped) != "" {
			return fmt.Errorf("migration result carries incoherent successful target evidence")
		}
		return nil
	}
	if action != "failed" || strings.TrimSpace(dashboardID) != "" || strings.TrimSpace(targetError) == "" {
		return fmt.Errorf("migration result carries incoherent terminal failure evidence")
	}
	return nil
}

func (service *Service) promoteResultPointer(id string) error {
	root, err := openVerifiedRoot(service.config.OutputRoot, service.outputRootInfo)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	source := filepath.Join(id, resultGeneration, "migration-result.json")
	destination := filepath.Join(id, "migration-result.json")
	if _, err := root.Lstat(destination); err == nil {
		return fmt.Errorf("refuse to replace existing migration result pointer")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := root.Rename(source, destination); err != nil {
		return fmt.Errorf("publish migration result pointer: %w", err)
	}
	if err := syncRootDirectory(root, filepath.Join(id, resultGeneration)); err != nil {
		return err
	}
	if err := syncRootDirectory(root, id); err != nil {
		return err
	}
	return nil
}

func (service *Service) verifyValidationRun(relative string) error {
	root, err := openVerifiedRoot(service.config.OutputRoot, service.outputRootInfo)
	if err != nil {
		return err
	}
	before, err := root.Lstat(relative)
	if err != nil {
		_ = root.Close()
		return err
	}
	if !before.IsDir() {
		_ = root.Close()
		return fmt.Errorf("validation run %q is not a real directory", relative)
	}
	defer func() { _ = root.Close() }()
	return service.verifyPinnedValidationRun(root, relative, before)
}

func (service *Service) verifyPinnedValidationRun(
	root *os.Root,
	relative string,
	before os.FileInfo,
) error {
	run, err := root.OpenRoot(relative)
	if err != nil {
		return err
	}
	opened, err := run.Stat(".")
	if err != nil || !opened.IsDir() || !os.SameFile(before, opened) {
		_ = run.Close()
		if err != nil {
			return fmt.Errorf("inspect opened validation run %q: %w", relative, err)
		}
		return fmt.Errorf("validation run %q changed while it was opened", relative)
	}
	defer func() { _ = run.Close() }()
	reportData, err := readRootedArtifactBounded(run, "validated.report.json", filepath.Join(service.config.OutputRoot, relative), maxMCPArtifactSize)
	if err != nil {
		return err
	}
	evidence, err := decodeDashboardReport(reportData)
	if err != nil {
		return err
	}
	if evidence.ArtifactSet == nil {
		return fmt.Errorf("validation report has no committed artifact-set binding")
	}
	committed, err := artifactset.ReadCommittedRoot(
		run,
		"validated.report.json",
		reportData,
		evidence.ArtifactSet,
		artifactset.KindDashboard,
		[]string{"validated.signoz.json", "validated.report.html"},
		artifactset.MaxMemberSize,
	)
	if err != nil {
		return fmt.Errorf("verify committed validation artifact set: %w", err)
	}
	state := manifest{
		Report: "validated.report.json", Dashboard: "validated.signoz.json", HTML: "validated.report.html",
	}
	return verifyDashboardManifestBindings(state, evidence, committed.Manifest)
}
