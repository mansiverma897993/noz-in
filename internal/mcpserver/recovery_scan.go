package mcpserver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func cleanupInterruptedWorkRootOwner(
	parent, work *os.Root,
	entries []os.DirEntry,
) (bool, error) {
	for _, entry := range entries {
		if entry.Name() == mcpWorkRootOwnerName {
			return false, nil
		}
	}
	if len(entries) != 1 {
		return false, nil
	}
	destination, temporary := atomicTemporaryDestination(entries[0].Name())
	if !temporary || destination != mcpWorkRootOwnerName {
		return false, nil
	}
	info, err := work.Lstat(entries[0].Name())
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxMCPWorkMetadataBytes {
		if err != nil {
			return true, err
		}
		return true, fmt.Errorf("interrupted MCP work-root owner temporary is not a bounded regular file")
	}
	if err := work.Remove(entries[0].Name()); err != nil {
		return true, err
	}
	if err := work.Close(); err != nil {
		_ = parent.Close()
		return true, err
	}
	if err := parent.Remove(mcpWorkRootName); err != nil {
		_ = parent.Close()
		return true, err
	}
	if err := syncRootDirectory(parent, "."); err != nil {
		_ = parent.Close()
		return true, err
	}
	return true, parent.Close()
}

func (service *Service) openRecoveryWorkRoot() (*os.Root, *os.Root, []os.DirEntry, error) {
	parent, err := openVerifiedRoot(service.config.OutputRoot, service.outputRootInfo)
	if err != nil {
		return nil, nil, nil, err
	}
	info, err := parent.Lstat(mcpWorkRootName)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil, parent.Close()
	}
	if err != nil || !info.IsDir() {
		_ = parent.Close()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("inspect MCP recovery work root: %w", err)
		}
		return nil, nil, nil, fmt.Errorf("MCP recovery work root is not a real directory; preserving it unchanged")
	}
	work, err := parent.OpenRoot(mcpWorkRootName)
	if err != nil {
		_ = parent.Close()
		return nil, nil, nil, fmt.Errorf("open MCP recovery work root: %w", err)
	}
	opened, err := work.Stat(".")
	if err != nil || !opened.IsDir() || !os.SameFile(info, opened) {
		_ = work.Close()
		_ = parent.Close()
		if err != nil {
			return nil, nil, nil, err
		}
		return nil, nil, nil, fmt.Errorf("MCP recovery work root changed while it was opened")
	}
	entries, err := readRootDirectoryBounded(work, maxMCPWorkOperations+1)
	if err != nil {
		_ = work.Close()
		_ = parent.Close()
		return nil, nil, nil, fmt.Errorf("list MCP recovery work root: %w", err)
	}
	if len(entries) != 0 {
		return parent, work, entries, nil
	}
	if err := work.Close(); err != nil {
		_ = parent.Close()
		return nil, nil, nil, err
	}
	if err := parent.Remove(mcpWorkRootName); err != nil {
		_ = parent.Close()
		return nil, nil, nil, fmt.Errorf("remove empty interrupted MCP recovery work root: %w", err)
	}
	if err := syncRootDirectory(parent, "."); err != nil {
		_ = parent.Close()
		return nil, nil, nil, err
	}
	return nil, nil, nil, parent.Close()
}

func validateRecoveryRootOwner(work *os.Root) error {
	var rootOwner mcpWorkRootOwner
	if err := readRootedWorkJSON(work, mcpWorkRootOwnerName, &rootOwner); err != nil {
		return fmt.Errorf("MCP recovery work root is not owned by this server; preserving it unchanged: %w", err)
	}
	if rootOwner.SchemaVersion != 1 || rootOwner.Namespace != "promcast/mcp-work-v1" {
		return fmt.Errorf("MCP recovery work root ownership is invalid; preserving it unchanged")
	}
	return nil
}

func recoveryOperationNames(work *os.Root, entries []os.DirEntry) ([]string, error) {
	operationNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if name == mcpWorkRootOwnerName {
			continue
		}
		if !strings.HasPrefix(name, mcpWorkOperationPrefix) || validateWorkToken(strings.TrimPrefix(name, mcpWorkOperationPrefix)) != nil {
			return nil, fmt.Errorf("MCP recovery work root contains unowned entry %q; preserving it unchanged", name)
		}
		entryInfo, err := work.Lstat(name)
		if err != nil {
			return nil, err
		}
		if !entryInfo.IsDir() {
			return nil, fmt.Errorf("MCP recovery operation %q is not a real directory; preserving it unchanged", name)
		}
		operationNames = append(operationNames, name)
	}
	sort.Strings(operationNames)
	return operationNames, nil
}

func finalizeRecoveryWorkRoot(parent, work *os.Root) error {
	remaining, err := readRootDirectoryBounded(work, 1)
	if err != nil {
		_ = work.Close()
		_ = parent.Close()
		return err
	}
	if len(remaining) != 1 || remaining[0].Name() != mcpWorkRootOwnerName {
		_ = work.Close()
		_ = parent.Close()
		return fmt.Errorf("MCP recovery work root changed during recovery; preserving it unchanged")
	}
	if err := work.Remove(mcpWorkRootOwnerName); err != nil {
		_ = work.Close()
		_ = parent.Close()
		return fmt.Errorf("remove MCP recovery root ownership record: %w", err)
	}
	if err := work.Close(); err != nil {
		_ = parent.Close()
		return err
	}
	if err := parent.Remove(mcpWorkRootName); err != nil {
		_ = parent.Close()
		return fmt.Errorf("remove empty MCP recovery work root: %w", err)
	}
	if err := syncRootDirectory(parent, "."); err != nil {
		_ = parent.Close()
		return err
	}
	return parent.Close()
}

func (service *Service) recoverWorkOperation(work *os.Root, name string) error {
	before, err := work.Lstat(name)
	if err != nil {
		return fmt.Errorf("inspect MCP recovery operation %q: %w", name, err)
	}
	if !before.IsDir() {
		return fmt.Errorf("MCP recovery operation %q is not a real directory; preserving it unchanged", name)
	}
	return service.recoverPinnedWorkOperation(work, name, before)
}

// recoverPinnedWorkOperation opens the operation immediately after its caller
// captured the directory identity. The opened root must still refer to that
// same directory before recovery is allowed to inspect or remove anything.
func (service *Service) recoverPinnedWorkOperation(
	work *os.Root,
	name string,
	before os.FileInfo,
) error {
	token := strings.TrimPrefix(name, mcpWorkOperationPrefix)
	operationRoot, err := work.OpenRoot(name)
	if err != nil {
		return fmt.Errorf("open MCP recovery operation %q: %w", name, err)
	}
	opened, err := operationRoot.Stat(".")
	if err != nil || !opened.IsDir() || !os.SameFile(before, opened) {
		_ = operationRoot.Close()
		if err != nil {
			return fmt.Errorf("verify MCP recovery operation %q identity: %w", name, err)
		}
		return fmt.Errorf("MCP recovery operation %q changed while it was opened; preserving it unchanged", name)
	}
	entries, err := readRootDirectoryBounded(operationRoot, 16)
	if err != nil {
		_ = operationRoot.Close()
		return fmt.Errorf("list MCP recovery operation %q: %w", name, err)
	}
	if len(entries) == 0 {
		if err := operationRoot.Close(); err != nil {
			return err
		}
		return removePinnedRecoveryOperation(work, name, before)
	}
	cleaned, err := cleanupInterruptedOperationOwner(work, operationRoot, name, before, entries)
	if err != nil {
		_ = operationRoot.Close()
		return err
	}
	if cleaned {
		return nil
	}
	recovered, err := readRecoveredWorkOperation(operationRoot, name, token, entries)
	if err != nil {
		_ = operationRoot.Close()
		return err
	}
	if err := validateRecoveredWorkShape(recovered); err != nil {
		_ = operationRoot.Close()
		return fmt.Errorf("MCP recovery operation %q has an impossible state; preserving it unchanged: %w", name, err)
	}
	if recovered.plan == nil {
		if len(recovered.inventories) != 0 {
			_ = operationRoot.Close()
			return fmt.Errorf("MCP recovery operation %q has inventory data without a plan; preserving it unchanged", name)
		}
	} else if recovered.phase != mcpWorkPhaseCleaning {
		if err := service.recoverPublishedWork(recovered); err != nil {
			_ = operationRoot.Close()
			return fmt.Errorf("recover published MCP work %q: %w", name, err)
		}
	}
	if recovered.phase != mcpWorkPhaseCleaning {
		if recovered.plan != nil && recovered.plan.SchemaVersion >= 2 {
			if err := service.cleanupPrivateStagingDirectory(token, recovered.plan.StagingParent); err != nil {
				_ = operationRoot.Close()
				return fmt.Errorf("reclaim private MCP staging for %q: %w", name, err)
			}
		}
		if recovered.phase != "" {
			if err := operationRoot.Rename(recovered.phase, mcpWorkPhaseCleaning); err != nil {
				_ = operationRoot.Close()
				return fmt.Errorf("mark MCP recovery operation %q for cleanup: %w", name, err)
			}
			if err := syncRootDirectory(operationRoot, "."); err != nil {
				_ = operationRoot.Close()
				return err
			}
			recovered.phase = mcpWorkPhaseCleaning
			service.runCrashBarrier("recovery-cleaning-started")
		}
	}
	if err := service.cleanupRecoveredWorkOperation(operationRoot, recovered); err != nil {
		_ = operationRoot.Close()
		return err
	}
	if err := operationRoot.Close(); err != nil {
		return err
	}
	return removePinnedRecoveryOperation(work, name, before)
}

func removePinnedRecoveryOperation(work *os.Root, name string, expected os.FileInfo) error {
	current, err := work.Lstat(name)
	if err != nil {
		return fmt.Errorf("inspect cleaned MCP recovery operation %q: %w", name, err)
	}
	if !current.IsDir() || !os.SameFile(expected, current) {
		return fmt.Errorf("MCP recovery operation %q changed before removal; preserving it unchanged", name)
	}
	if err := work.Remove(name); err != nil {
		return fmt.Errorf("remove cleaned MCP recovery operation %q: %w", name, err)
	}
	return syncRootDirectory(work, ".")
}

func validateRecoveredWorkShape(recovered recoveredWorkOperation) error {
	initial := recovered.inventories[mcpWorkInventoryInitial]
	result := recovered.inventories[mcpWorkInventoryResult]
	validation := recovered.inventories[mcpWorkInventoryValidate]
	if recovered.plan == nil {
		return validateUnplannedRecoveredWork(recovered, initial, result, validation)
	}
	plan := *recovered.plan
	switch plan.Kind {
	case "migration":
		return validateRecoveredMigrationShape(plan, recovered.phase, initial, result, validation)
	case "validation":
		return validateRecoveredValidationShape(recovered.phase, initial, result, validation)
	default:
		return fmt.Errorf("unsupported work kind %q", plan.Kind)
	}
}

func validateUnplannedRecoveredWork(
	recovered recoveredWorkOperation,
	initial, result, validation mcpWorkInventory,
) error {
	if initial.Payload != "" || result.Payload != "" || validation.Payload != "" {
		return fmt.Errorf("inventory exists without an immutable plan")
	}
	if recovered.phase != "" && recovered.phase != mcpWorkPhaseCleaning {
		return fmt.Errorf("phase %q exists without an immutable plan", recovered.phase)
	}
	return nil
}

func validateRecoveredMigrationShape(
	plan mcpWorkPlan,
	phase string,
	initial, result, validation mcpWorkInventory,
) error {
	if validation.Payload != "" {
		return fmt.Errorf("migration plan carries validation inventory")
	}
	if result.Payload != "" && (initial.Payload == "" || !plan.ImportRequested) {
		return fmt.Errorf("result inventory requires initial inventory and importRequested=true")
	}
	switch phase {
	case "":
		if initial.Payload != "" || result.Payload != "" {
			return fmt.Errorf("inventory exists without a phase")
		}
	case ".phase-allocated":
		if result.Payload != "" {
			return fmt.Errorf("result inventory exists in allocated phase")
		}
	case ".phase-initial-prepared":
		if initial.Payload == "" || result.Payload != "" {
			return fmt.Errorf("initial publication phase has incompatible inventories")
		}
	case ".phase-initial-installed":
		if initial.Payload == "" {
			return fmt.Errorf("installed initial publication phase has no initial inventory")
		}
	case ".phase-result-prepared", ".phase-result-installed", ".phase-result-published":
		if initial.Payload == "" || result.Payload == "" || !plan.ImportRequested {
			return fmt.Errorf("result publication phase has incompatible inventories")
		}
	case mcpWorkPhaseCleaning:
	default:
		return fmt.Errorf("migration plan carries phase %q", phase)
	}
	return nil
}

func validateRecoveredValidationShape(
	phase string,
	initial, result, validation mcpWorkInventory,
) error {
	if initial.Payload != "" || result.Payload != "" {
		return fmt.Errorf("validation plan carries migration inventory")
	}
	switch phase {
	case "":
		if validation.Payload != "" {
			return fmt.Errorf("validation inventory exists without a phase")
		}
	case ".phase-allocated":
	case ".phase-validation-prepared", ".phase-validation-installed":
		if validation.Payload == "" {
			return fmt.Errorf("validation publication phase has no validation inventory")
		}
	case mcpWorkPhaseCleaning:
	default:
		return fmt.Errorf("validation plan carries phase %q", phase)
	}
	return nil
}

func cleanupInterruptedOperationOwner(
	work, operationRoot *os.Root,
	name string,
	expected os.FileInfo,
	entries []os.DirEntry,
) (bool, error) {
	for _, entry := range entries {
		if entry.Name() == mcpWorkOwnerName {
			return false, nil
		}
	}
	if len(entries) != 1 {
		return false, nil
	}
	destination, temporary := atomicTemporaryDestination(entries[0].Name())
	if !temporary || destination != mcpWorkOwnerName {
		return false, nil
	}
	info, err := operationRoot.Lstat(entries[0].Name())
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxMCPWorkMetadataBytes {
		if err != nil {
			return true, err
		}
		return true, fmt.Errorf("interrupted MCP operation owner temporary is not a bounded regular file")
	}
	if err := operationRoot.Remove(entries[0].Name()); err != nil {
		return true, err
	}
	if err := operationRoot.Close(); err != nil {
		return true, err
	}
	return true, removePinnedRecoveryOperation(work, name, expected)
}

func readRecoveredWorkOperation(
	operationRoot *os.Root,
	name, token string,
	entries []os.DirEntry,
) (recoveredWorkOperation, error) {
	var owner mcpWorkOwner
	if err := readRootedWorkJSON(operationRoot, mcpWorkOwnerName, &owner); err != nil {
		return recoveredWorkOperation{}, fmt.Errorf("MCP recovery operation %q is unowned; preserving it unchanged: %w", name, err)
	}
	if owner.SchemaVersion != 1 || owner.Token != token || validateWorkToken(owner.Token) != nil {
		return recoveredWorkOperation{}, fmt.Errorf("MCP recovery operation %q ownership is invalid; preserving it unchanged", name)
	}
	recovered := recoveredWorkOperation{
		token:       token,
		relative:    filepath.Join(mcpWorkRootName, name),
		inventories: make(map[string]mcpWorkInventory),
		present:     make(map[string]bool),
	}
	phaseCount := 0
	for _, entry := range entries {
		entryName := entry.Name()
		switch entryName {
		case mcpWorkOwnerName:
		case mcpWorkPlanName:
			var plan mcpWorkPlan
			if err := readRootedWorkJSON(operationRoot, entryName, &plan); err != nil {
				return recoveredWorkOperation{}, fmt.Errorf("decode MCP work plan for %q: %w", name, err)
			}
			if err := validateWorkPlan(plan, token); err != nil {
				return recoveredWorkOperation{}, fmt.Errorf("validate MCP work plan for %q: %w", name, err)
			}
			recovered.plan = &plan
		case mcpWorkInventoryInitial, mcpWorkInventoryResult, mcpWorkInventoryValidate:
			var inventory mcpWorkInventory
			if err := readRootedWorkJSON(operationRoot, entryName, &inventory); err != nil {
				return recoveredWorkOperation{}, fmt.Errorf("decode MCP work inventory %q: %w", entryName, err)
			}
			if err := validateWorkInventory(inventory, token, entryName); err != nil {
				return recoveredWorkOperation{}, err
			}
			recovered.inventories[entryName] = inventory
		case mcpWorkPayloadInitial, mcpWorkPayloadResult, mcpWorkPayloadValidation:
			recovered.present[entryName] = true
		default:
			if destination, temporary := atomicTemporaryDestination(entryName); temporary && isWorkMetadataName(destination) {
				if _, err := operationRoot.Lstat(destination); err == nil {
					return recoveredWorkOperation{}, fmt.Errorf("MCP recovery operation %q contains both temporary and final metadata %q", name, destination)
				} else if !errors.Is(err, os.ErrNotExist) {
					return recoveredWorkOperation{}, err
				}
				info, err := operationRoot.Lstat(entryName)
				if err != nil || !info.Mode().IsRegular() || info.Size() > maxMCPWorkMetadataBytes {
					if err != nil {
						return recoveredWorkOperation{}, err
					}
					return recoveredWorkOperation{}, fmt.Errorf("MCP recovery metadata temporary %q is not a bounded regular file", entryName)
				}
				recovered.temporaries = append(recovered.temporaries, entryName)
				continue
			}
			if !mcpWorkPhases[entryName] {
				return recoveredWorkOperation{}, fmt.Errorf("MCP recovery operation %q contains unowned entry %q; preserving it unchanged", name, entryName)
			}
			info, err := operationRoot.Lstat(entryName)
			if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
				if err != nil {
					return recoveredWorkOperation{}, err
				}
				return recoveredWorkOperation{}, fmt.Errorf("MCP recovery phase %q is not an empty regular file; preserving it unchanged", entryName)
			}
			phaseCount++
			recovered.phase = entryName
		}
	}
	if phaseCount > 1 {
		return recoveredWorkOperation{}, fmt.Errorf("MCP recovery operation %q contains multiple phase records; preserving it unchanged", name)
	}
	if err := validateRecoveredPayloads(operationRoot, name, recovered); err != nil {
		return recoveredWorkOperation{}, err
	}
	return recovered, nil
}

func validateRecoveredPayloads(operationRoot *os.Root, name string, recovered recoveredWorkOperation) error {
	knownPayloads := make(map[string]bool, len(recovered.inventories))
	for _, inventory := range recovered.inventories {
		present, _, err := inspectWorkPayload(operationRoot, inventory)
		if err != nil {
			return fmt.Errorf("inspect owned MCP work payload for %q: %w", name, err)
		}
		if present != recovered.present[inventory.Payload] {
			return fmt.Errorf("MCP work payload %q changed during recovery", inventory.Payload)
		}
		knownPayloads[inventory.Payload] = true
	}
	for payload := range recovered.present {
		if !knownPayloads[payload] {
			return fmt.Errorf("MCP recovery operation %q contains a payload without an inventory; preserving it unchanged", name)
		}
	}
	return nil
}

func (service *Service) cleanupRecoveredWorkOperation(
	operationRoot *os.Root,
	recovered recoveredWorkOperation,
) error {
	for _, inventoryName := range []string{mcpWorkInventoryResult, mcpWorkInventoryInitial, mcpWorkInventoryValidate} {
		inventory, found := recovered.inventories[inventoryName]
		if !found {
			continue
		}
		if err := removeWorkPayload(operationRoot, inventory); err != nil {
			return err
		}
		if err := operationRoot.Remove(inventoryName); err != nil {
			return err
		}
		if err := syncRootDirectory(operationRoot, "."); err != nil {
			return err
		}
		service.runCrashBarrier("recovery-cleaning-" + inventory.Stage + "-removed")
	}
	for _, temporary := range recovered.temporaries {
		if err := operationRoot.Remove(temporary); err != nil {
			return err
		}
	}
	for _, metadata := range []string{mcpWorkPlanName, recovered.phase, mcpWorkOwnerName} {
		if metadata == "" {
			continue
		}
		if _, err := operationRoot.Lstat(metadata); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := operationRoot.Remove(metadata); err != nil {
			return err
		}
		if err := syncRootDirectory(operationRoot, "."); err != nil {
			return err
		}
		switch metadata {
		case mcpWorkPlanName:
			service.runCrashBarrier("recovery-cleaning-plan-removed")
		case mcpWorkPhaseCleaning:
			service.runCrashBarrier("recovery-cleaning-phase-removed")
		case mcpWorkOwnerName:
			service.runCrashBarrier("recovery-cleaning-owner-removed")
		}
	}
	remaining, err := readRootDirectoryBounded(operationRoot, 0)
	if err != nil {
		return err
	}
	if len(remaining) != 0 {
		return fmt.Errorf("MCP recovery operation changed during cleanup")
	}
	return nil
}
