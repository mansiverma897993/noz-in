package mcpserver

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type mcpWorkOperation struct {
	service  *Service
	token    string
	relative string
	plan     mcpWorkPlan
	phase    string
}

type recoveredWorkOperation struct {
	token       string
	relative    string
	plan        *mcpWorkPlan
	phase       string
	inventories map[string]mcpWorkInventory
	present     map[string]bool
	temporaries []string
}

func (service *Service) ensureMCPWorkRoot() error {
	root, err := openVerifiedRoot(service.config.OutputRoot, service.outputRootInfo)
	if err != nil {
		return err
	}
	info, err := root.Lstat(mcpWorkRootName)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		_ = root.Close()
		return fmt.Errorf("inspect MCP recovery work root: %w", err)
	}
	if !missing && !info.IsDir() {
		_ = root.Close()
		return fmt.Errorf("MCP recovery work root %q is not a real directory", mcpWorkRootName)
	}
	if err := root.Close(); err != nil {
		return fmt.Errorf("close MCP output root: %w", err)
	}
	if missing {
		if err := service.createOutputDirectory(mcpWorkRootName); err != nil {
			return fmt.Errorf("create MCP recovery work root: %w", err)
		}
	}

	work, err := service.openMCPWorkRoot()
	if err != nil {
		return err
	}
	entries, err := readRootDirectoryBounded(work, maxMCPWorkOperations+1)
	if err != nil {
		_ = work.Close()
		return fmt.Errorf("list MCP recovery work root: %w", err)
	}
	ownerMissing := true
	for _, entry := range entries {
		if entry.Name() == mcpWorkRootOwnerName {
			ownerMissing = false
			break
		}
	}
	if ownerMissing && len(entries) != 0 {
		_ = work.Close()
		return fmt.Errorf("MCP recovery work root has no ownership record; preserving it unchanged")
	}
	if err := work.Close(); err != nil {
		return fmt.Errorf("close MCP recovery work root: %w", err)
	}
	if ownerMissing {
		owner := mcpWorkRootOwner{SchemaVersion: 1, Namespace: "promcast/mcp-work-v1"}
		data, err := encodeWorkJSON(owner)
		if err != nil {
			return err
		}
		if err := service.writeOutputAtomic(filepath.Join(mcpWorkRootName, mcpWorkRootOwnerName), data); err != nil {
			_ = service.recoverOutputWork()
			return fmt.Errorf("write MCP recovery root ownership record: %w", err)
		}
	}
	work, err = service.openMCPWorkRoot()
	if err != nil {
		return err
	}
	defer func() { _ = work.Close() }()
	var owner mcpWorkRootOwner
	if err := readRootedWorkJSON(work, mcpWorkRootOwnerName, &owner); err != nil {
		return fmt.Errorf("read MCP recovery root ownership record: %w", err)
	}
	if owner.SchemaVersion != 1 || owner.Namespace != "promcast/mcp-work-v1" {
		return fmt.Errorf("MCP recovery work root has an unsupported ownership record; preserving it unchanged")
	}
	return nil
}

func (service *Service) openMCPWorkRoot() (*os.Root, error) {
	parent, err := openVerifiedRoot(service.config.OutputRoot, service.outputRootInfo)
	if err != nil {
		return nil, err
	}
	before, err := parent.Lstat(mcpWorkRootName)
	if err != nil {
		_ = parent.Close()
		return nil, err
	}
	if !before.IsDir() {
		_ = parent.Close()
		return nil, fmt.Errorf("MCP recovery work root is not a real directory")
	}
	work, err := parent.OpenRoot(mcpWorkRootName)
	if err != nil {
		_ = parent.Close()
		return nil, err
	}
	after, err := work.Stat(".")
	if err != nil || !after.IsDir() || !os.SameFile(before, after) {
		_ = work.Close()
		_ = parent.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("MCP recovery work root changed while it was opened")
	}
	if err := parent.Close(); err != nil {
		_ = work.Close()
		return nil, err
	}
	return work, nil
}

func (service *Service) startMCPWork(plan mcpWorkPlan) (_ *mcpWorkOperation, err error) {
	if err := service.ensureMCPWorkRoot(); err != nil {
		return nil, err
	}
	stagingParent, err := resolvePrivateMCPStagingParent()
	if err != nil {
		_ = service.recoverOutputWork()
		return nil, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		_ = service.recoverOutputWork()
		return nil, fmt.Errorf("generate MCP work token: %w", err)
	}
	token := hex.EncodeToString(nonce[:])
	plan.SchemaVersion = 2
	plan.Token = token
	plan.StagingParent = stagingParent
	if err := validateWorkPlan(plan, token); err != nil {
		_ = service.recoverOutputWork()
		return nil, err
	}
	operation := &mcpWorkOperation{
		service:  service,
		token:    token,
		relative: filepath.Join(mcpWorkRootName, mcpWorkOperationPrefix+token),
		plan:     plan,
		phase:    ".phase-allocated",
	}
	defer func() {
		if err != nil {
			_ = service.recoverOutputWork()
		}
	}()
	if err := service.createOutputDirectory(operation.relative); err != nil {
		return nil, fmt.Errorf("create MCP work operation: %w", err)
	}
	ownerData, err := encodeWorkJSON(mcpWorkOwner{SchemaVersion: 1, Token: token})
	if err != nil {
		return nil, err
	}
	if err := service.writeOutputAtomic(filepath.Join(operation.relative, mcpWorkOwnerName), ownerData); err != nil {
		return nil, fmt.Errorf("write MCP work ownership record: %w", err)
	}
	planData, err := encodeWorkJSON(plan)
	if err != nil {
		return nil, err
	}
	if err := service.writeOutputAtomic(filepath.Join(operation.relative, mcpWorkPlanName), planData); err != nil {
		return nil, fmt.Errorf("write MCP work plan: %w", err)
	}
	if err := service.writeOutputAtomic(filepath.Join(operation.relative, operation.phase), nil); err != nil {
		return nil, fmt.Errorf("write MCP work phase: %w", err)
	}
	return operation, nil
}

func (operation *mcpWorkOperation) advancePhase(next string) error {
	if !mcpWorkPhases[next] {
		return fmt.Errorf("unsupported MCP work phase %q", next)
	}
	root, err := openVerifiedRoot(operation.service.config.OutputRoot, operation.service.outputRootInfo)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := root.Rename(
		filepath.Join(operation.relative, operation.phase),
		filepath.Join(operation.relative, next),
	); err != nil {
		return fmt.Errorf("advance MCP work phase from %q to %q: %w", operation.phase, next, err)
	}
	if err := syncRootDirectory(root, operation.relative); err != nil {
		return fmt.Errorf("persist MCP work phase %q: %w", next, err)
	}
	operation.phase = next
	return nil
}

func (operation *mcpWorkOperation) writeInventory(name string, inventory mcpWorkInventory) error {
	inventory.SchemaVersion = 1
	inventory.Token = operation.token
	if err := validateWorkInventory(inventory, operation.token, name); err != nil {
		return err
	}
	data, err := encodeWorkJSON(inventory)
	if err != nil {
		return err
	}
	if int64(len(data)) > maxMCPWorkMetadataBytes {
		return fmt.Errorf("MCP work inventory %q exceeds %d bytes", name, maxMCPWorkMetadataBytes)
	}
	if err := operation.service.writeOutputAtomic(filepath.Join(operation.relative, name), data); err != nil {
		return fmt.Errorf("write MCP work inventory %q: %w", name, err)
	}
	return nil
}

func (operation *mcpWorkOperation) createPayload(name string) error {
	switch name {
	case mcpWorkPayloadInitial, mcpWorkPayloadResult, mcpWorkPayloadValidation:
	default:
		return fmt.Errorf("unsupported MCP work payload %q", name)
	}
	if err := operation.service.createOutputDirectory(filepath.Join(operation.relative, name)); err != nil {
		return fmt.Errorf("create MCP work payload %q: %w", name, err)
	}
	return nil
}

func (operation *mcpWorkOperation) cleanup() error {
	return operation.service.recoverOutputWork()
}

func (service *Service) recoverOutputWork() error {
	parent, work, entries, err := service.openRecoveryWorkRoot()
	if err != nil || work == nil {
		return err
	}
	cleaned, err := cleanupInterruptedWorkRootOwner(parent, work, entries)
	if err != nil {
		_ = work.Close()
		_ = parent.Close()
		return err
	}
	if cleaned {
		return nil
	}
	if err := validateRecoveryRootOwner(work); err != nil {
		_ = work.Close()
		_ = parent.Close()
		return err
	}
	operationNames, err := recoveryOperationNames(work, entries)
	if err != nil {
		_ = work.Close()
		_ = parent.Close()
		return err
	}
	for _, name := range operationNames {
		if err := service.recoverWorkOperation(work, name); err != nil {
			_ = work.Close()
			_ = parent.Close()
			return err
		}
	}
	return finalizeRecoveryWorkRoot(parent, work)
}
