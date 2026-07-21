package mcpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	mcpWorkRootName          = ".promcast-mcp-work-v1"
	mcpWorkRootOwnerName     = ".owner.json"
	mcpWorkOperationPrefix   = "op-"
	mcpWorkOwnerName         = ".owner.json"
	mcpWorkPlanName          = "plan.json"
	mcpWorkPayloadInitial    = "payload-initial"
	mcpWorkPayloadResult     = "payload-result"
	mcpWorkPayloadValidation = "payload-validation"
	mcpWorkInventoryInitial  = "inventory-initial.json"
	mcpWorkInventoryResult   = "inventory-result.json"
	mcpWorkInventoryValidate = "inventory-validation.json"
	mcpWorkPhaseCleaning     = ".phase-cleaning"

	maxMCPWorkOperations     = 64
	maxMCPWorkMetadataBytes  = 64 << 10
	maxMCPWorkPayloadEntries = 512
)

var mcpWorkPhases = map[string]bool{
	".phase-allocated":            true,
	".phase-initial-prepared":     true,
	".phase-initial-installed":    true,
	".phase-result-prepared":      true,
	".phase-result-installed":     true,
	".phase-result-published":     true,
	".phase-validation-prepared":  true,
	".phase-validation-installed": true,
	mcpWorkPhaseCleaning:          true,
}

type mcpWorkRootOwner struct {
	SchemaVersion int    `json:"schemaVersion"`
	Namespace     string `json:"namespace"`
}

type mcpWorkOwner struct {
	SchemaVersion int    `json:"schemaVersion"`
	Token         string `json:"token"`
}

type mcpWorkPlan struct {
	SchemaVersion    int    `json:"schemaVersion"`
	Token            string `json:"token"`
	Kind             string `json:"kind"`
	MigrationID      string `json:"migrationId"`
	ImportRequested  bool   `json:"importRequested,omitempty"`
	StagingParent    string `json:"stagingParent"`
	ValidationTarget string `json:"validationTarget,omitempty"`
	ValidationRun    string `json:"validationRun,omitempty"`
}

type mcpWorkInventory struct {
	SchemaVersion int      `json:"schemaVersion"`
	Token         string   `json:"token"`
	Stage         string   `json:"stage"`
	Payload       string   `json:"payload"`
	Directories   []string `json:"directories,omitempty"`
	Files         []string `json:"files,omitempty"`
}

func encodeWorkJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func decodeWorkJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("contains multiple JSON values")
		}
		return err
	}
	return nil
}

func validateWorkToken(token string) error {
	if len(token) != 32 {
		return fmt.Errorf("work token must contain 32 lowercase hexadecimal characters")
	}
	for _, character := range token {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return fmt.Errorf("work token must contain 32 lowercase hexadecimal characters")
		}
	}
	return nil
}

func atomicTemporaryDestination(path string) (string, bool) {
	base := filepath.Base(path)
	separator := strings.LastIndex(base, ".tmp-")
	if separator < 1 || !strings.HasPrefix(base, ".") {
		return "", false
	}
	nonce := base[separator+len(".tmp-"):]
	if len(nonce) != 24 {
		return "", false
	}
	for _, character := range nonce {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return "", false
		}
	}
	destinationBase := base[1:separator]
	if destinationBase == "" {
		return "", false
	}
	return filepath.Join(filepath.Dir(path), destinationBase), true
}

func isWorkMetadataName(name string) bool {
	if name == mcpWorkOwnerName || name == mcpWorkPlanName ||
		name == mcpWorkInventoryInitial || name == mcpWorkInventoryResult ||
		name == mcpWorkInventoryValidate {
		return true
	}
	return mcpWorkPhases[name]
}

func validateWorkPlan(plan mcpWorkPlan, token string) error {
	if plan.SchemaVersion != 1 && plan.SchemaVersion != 2 || plan.Token != token {
		return fmt.Errorf("unsupported or mismatched work plan")
	}
	if err := validateWorkToken(plan.Token); err != nil {
		return err
	}
	switch plan.SchemaVersion {
	case 1:
		if plan.StagingParent != "" {
			return fmt.Errorf("legacy work plan cannot carry a private staging parent")
		}
	case 2:
		if plan.StagingParent == "" || !filepath.IsAbs(plan.StagingParent) || filepath.Clean(plan.StagingParent) != plan.StagingParent {
			return fmt.Errorf("work plan contains invalid private staging parent %q", plan.StagingParent)
		}
	}
	if !migrationIDPattern.MatchString(plan.MigrationID) {
		return fmt.Errorf("work plan contains invalid migration id %q", plan.MigrationID)
	}
	switch plan.Kind {
	case "migration":
		if plan.ValidationTarget != "" || plan.ValidationRun != "" {
			return fmt.Errorf("migration work plan contains validation fields")
		}
	case "validation":
		if plan.ImportRequested {
			return fmt.Errorf("validation work plan cannot request import")
		}
		if err := validateValidationWorkPath(plan.MigrationID, plan.ValidationTarget, plan.ValidationRun); err != nil {
			return err
		}
	default:
		return fmt.Errorf("work plan contains unsupported kind %q", plan.Kind)
	}
	return nil
}

func validateValidationWorkPath(migrationID, target, run string) error {
	if target == "" || run == "" || !filepath.IsLocal(target) || !filepath.IsLocal(run) {
		return fmt.Errorf("validation work plan contains non-local paths")
	}
	validationRoot := filepath.Join(migrationID, "validations")
	if target != validationRoot && filepath.Dir(target) != validationRoot {
		return fmt.Errorf("validation work target %q is outside the migration validation root", target)
	}
	if filepath.Dir(run) != validationRoot || !strings.HasPrefix(filepath.Base(run), "run-") {
		return fmt.Errorf("validation work run %q has an invalid shape", run)
	}
	if target != validationRoot && target != run {
		return fmt.Errorf("validation work target %q does not publish run %q", target, run)
	}
	return nil
}

func validateWorkInventory(inventory mcpWorkInventory, token, name string) error {
	if inventory.SchemaVersion != 1 || inventory.Token != token {
		return fmt.Errorf("unsupported or mismatched work inventory %q", name)
	}
	var wantStage, wantPayload string
	switch name {
	case mcpWorkInventoryInitial:
		wantStage, wantPayload = "initial", mcpWorkPayloadInitial
	case mcpWorkInventoryResult:
		wantStage, wantPayload = "result", mcpWorkPayloadResult
	case mcpWorkInventoryValidate:
		wantStage, wantPayload = "validation", mcpWorkPayloadValidation
	default:
		return fmt.Errorf("unsupported work inventory name %q", name)
	}
	if inventory.Stage != wantStage || inventory.Payload != wantPayload {
		return fmt.Errorf("work inventory %q has mismatched stage or payload", name)
	}
	if len(inventory.Directories)+len(inventory.Files) > maxMCPWorkPayloadEntries {
		return fmt.Errorf("work inventory %q exceeds %d entries", name, maxMCPWorkPayloadEntries)
	}
	seen := make(map[string]string, len(inventory.Directories)+len(inventory.Files))
	for kind, paths := range map[string][]string{"directory": inventory.Directories, "file": inventory.Files} {
		for _, path := range paths {
			if path == "" || path == "." || !filepath.IsLocal(path) || filepath.Clean(path) != path {
				return fmt.Errorf("work inventory %q contains invalid %s path %q", name, kind, path)
			}
			if previous := seen[path]; previous != "" {
				return fmt.Errorf("work inventory %q lists %q as both %s and %s", name, path, previous, kind)
			}
			seen[path] = kind
		}
	}
	for path, kind := range seen {
		parent := filepath.Dir(path)
		for parent != "." {
			if seen[parent] != "directory" {
				return fmt.Errorf("work inventory %q %s %q has undeclared parent %q", name, kind, path, parent)
			}
			parent = filepath.Dir(parent)
		}
	}
	return nil
}

func readRootDirectoryBounded(root *os.Root, limit int) ([]os.DirEntry, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, readErr := directory.ReadDir(limit + 1)
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	closeErr := directory.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	if len(entries) > limit {
		return nil, fmt.Errorf("directory exceeds the recovery inventory limit of %d entries", limit)
	}
	return entries, nil
}

func readRootedWorkJSON(root *os.Root, name string, destination any) error {
	before, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() || before.Size() < 0 || before.Size() > maxMCPWorkMetadataBytes {
		return fmt.Errorf("work metadata %q is not a bounded regular file", name)
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) || after.Size() != before.Size() {
		_ = file.Close()
		if err != nil {
			return err
		}
		return fmt.Errorf("work metadata %q changed while it was opened", name)
	}
	data, err := readBoundedFile(file, name, maxMCPWorkMetadataBytes)
	if err != nil {
		return err
	}
	return decodeWorkJSON(data, destination)
}

func inspectWorkPayload(
	operation *os.Root,
	inventory mcpWorkInventory,
) (bool, []string, error) {
	info, err := operation.Lstat(inventory.Payload)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	if !info.IsDir() {
		return false, nil, fmt.Errorf("work payload %q is not a real directory", inventory.Payload)
	}
	payload, err := operation.OpenRoot(inventory.Payload)
	if err != nil {
		return false, nil, err
	}
	defer func() { _ = payload.Close() }()
	opened, err := payload.Stat(".")
	if err != nil || !opened.IsDir() || !os.SameFile(info, opened) {
		if err != nil {
			return false, nil, err
		}
		return false, nil, fmt.Errorf("work payload %q changed while it was opened", inventory.Payload)
	}
	wantDirectories := make(map[string]bool, len(inventory.Directories))
	wantFiles := make(map[string]bool, len(inventory.Files))
	for _, path := range inventory.Directories {
		wantDirectories[path] = true
	}
	for _, path := range inventory.Files {
		wantFiles[path] = true
	}
	seen := 0
	seenFiles := make(map[string]string, len(inventory.Files))
	var temporaryFiles []string
	var walk func(*os.Root, string) error
	walk = func(root *os.Root, prefix string) error {
		entries, err := readRootDirectoryBounded(root, maxMCPWorkPayloadEntries-seen)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			seen++
			if seen > maxMCPWorkPayloadEntries {
				return fmt.Errorf("work payload exceeds %d entries", maxMCPWorkPayloadEntries)
			}
			relative := entry.Name()
			if prefix != "" {
				relative = filepath.Join(prefix, relative)
			}
			entryInfo, err := root.Lstat(entry.Name())
			if err != nil {
				return err
			}
			switch {
			case entryInfo.IsDir():
				if !wantDirectories[relative] {
					return fmt.Errorf("work payload contains undeclared directory %q", relative)
				}
				child, err := root.OpenRoot(entry.Name())
				if err != nil {
					return err
				}
				openedInfo, statErr := child.Stat(".")
				if statErr != nil || !openedInfo.IsDir() || !os.SameFile(entryInfo, openedInfo) {
					_ = child.Close()
					if statErr != nil {
						return statErr
					}
					return fmt.Errorf("work payload directory %q changed while it was opened", relative)
				}
				walkErr := walk(child, relative)
				closeErr := child.Close()
				if err := errors.Join(walkErr, closeErr); err != nil {
					return err
				}
			case entryInfo.Mode().IsRegular():
				destination := relative
				if !wantFiles[destination] {
					var temporary bool
					destination, temporary = atomicTemporaryDestination(relative)
					if !temporary || !wantFiles[destination] {
						return fmt.Errorf("work payload contains undeclared file %q", relative)
					}
					temporaryFiles = append(temporaryFiles, relative)
				}
				if previous := seenFiles[destination]; previous != "" {
					return fmt.Errorf("work payload contains multiple representations of declared file %q", destination)
				}
				seenFiles[destination] = relative
			default:
				return fmt.Errorf("work payload contains unsupported entry %q", relative)
			}
		}
		return nil
	}
	if err := walk(payload, ""); err != nil {
		return false, nil, err
	}
	return true, temporaryFiles, nil
}

func removeWorkPayload(operation *os.Root, inventory mcpWorkInventory) error {
	present, temporaryFiles, err := inspectWorkPayload(operation, inventory)
	if err != nil || !present {
		return err
	}
	for _, path := range temporaryFiles {
		if err := operation.Remove(filepath.Join(inventory.Payload, path)); err != nil {
			return fmt.Errorf("remove owned work temporary file %q: %w", path, err)
		}
	}
	files := append([]string(nil), inventory.Files...)
	sort.Slice(files, func(left, right int) bool { return files[left] > files[right] })
	for _, path := range files {
		if _, err := operation.Lstat(filepath.Join(inventory.Payload, path)); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := operation.Remove(filepath.Join(inventory.Payload, path)); err != nil {
			return fmt.Errorf("remove owned work file %q: %w", path, err)
		}
	}
	directories := append([]string(nil), inventory.Directories...)
	sort.Slice(directories, func(left, right int) bool {
		leftDepth := strings.Count(directories[left], string(filepath.Separator))
		rightDepth := strings.Count(directories[right], string(filepath.Separator))
		if leftDepth != rightDepth {
			return leftDepth > rightDepth
		}
		return directories[left] > directories[right]
	})
	for _, path := range directories {
		if _, err := operation.Lstat(filepath.Join(inventory.Payload, path)); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := operation.Remove(filepath.Join(inventory.Payload, path)); err != nil {
			return fmt.Errorf("remove owned work directory %q: %w", path, err)
		}
	}
	if err := operation.Remove(inventory.Payload); err != nil {
		return fmt.Errorf("remove owned work payload %q: %w", inventory.Payload, err)
	}
	return nil
}
