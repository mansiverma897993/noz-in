package mcpserver

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	privateMCPStagingPrefix    = "promcast-mcp-v1-"
	privateMCPStagingOwnerName = ".owner.json"
	privateMCPStagingDataName  = "stage"
)

type privateMCPStagingOwner struct {
	SchemaVersion int    `json:"schemaVersion"`
	Token         string `json:"token"`
	OutputRoot    string `json:"outputRootSHA256"`
}

func (service *Service) createPrivateStagingDirectory(token string, parentPath string) (string, error) {
	if err := validateWorkToken(token); err != nil {
		return "", fmt.Errorf("create private MCP staging directory: %w", err)
	}
	parent, err := openPrivateMCPStagingParent(parentPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = parent.Close() }()
	containerName := privateMCPStagingPrefix + token
	if err := parent.Mkdir(containerName, 0o700); err != nil {
		return "", fmt.Errorf("create private MCP staging container: %w", err)
	}
	if err := parent.Chmod(containerName, 0o700); err != nil {
		_ = parent.Remove(containerName)
		return "", fmt.Errorf("set private MCP staging container permissions: %w", err)
	}
	container, err := parent.OpenRoot(containerName)
	if err != nil {
		_ = parent.Remove(containerName)
		return "", fmt.Errorf("open private MCP staging container: %w", err)
	}
	owner := privateMCPStagingOwner{
		SchemaVersion: 1,
		Token:         token,
		OutputRoot:    fmt.Sprintf("%x", sha256.Sum256([]byte(service.config.OutputRoot))),
	}
	ownerData, err := encodeWorkJSON(owner)
	if err == nil {
		err = writePrivateMCPStagingOwner(container, ownerData)
	}
	if err == nil {
		err = container.Mkdir(privateMCPStagingDataName, 0o700)
	}
	if err == nil {
		err = container.Chmod(privateMCPStagingDataName, 0o700)
	}
	if err == nil {
		err = syncRootDirectory(container, ".")
	}
	closeErr := container.Close()
	if err := errors.Join(err, closeErr); err != nil {
		_ = service.cleanupPrivateStagingDirectory(token, parentPath)
		return "", fmt.Errorf("initialize private MCP staging directory: %w", err)
	}
	if err := syncRootDirectory(parent, "."); err != nil {
		_ = service.cleanupPrivateStagingDirectory(token, parentPath)
		return "", err
	}
	return filepath.Join(parentPath, containerName, privateMCPStagingDataName), nil
}

func writePrivateMCPStagingOwner(container *os.Root, data []byte) error {
	file, err := container.OpenFile(
		privateMCPStagingOwnerName,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		return err
	}
	written, writeErr := file.Write(data)
	if writeErr == nil && written != len(data) {
		writeErr = io.ErrShortWrite
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}

func resolvePrivateMCPStagingParent() (string, error) {
	parentPath, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return "", fmt.Errorf("resolve private MCP staging parent: %w", err)
	}
	return filepath.Clean(parentPath), nil
}

func openPrivateMCPStagingParent(parentPath string) (*os.Root, error) {
	if parentPath == "" || !filepath.IsAbs(parentPath) || filepath.Clean(parentPath) != parentPath {
		return nil, fmt.Errorf("invalid private MCP staging parent %q", parentPath)
	}
	before, err := os.Lstat(parentPath)
	if err != nil {
		return nil, fmt.Errorf("inspect private MCP staging parent: %w", err)
	}
	if !before.IsDir() {
		return nil, fmt.Errorf("private MCP staging parent is not a real directory")
	}
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return nil, fmt.Errorf("open private MCP staging parent: %w", err)
	}
	opened, err := parent.Stat(".")
	if err != nil || !opened.IsDir() || !os.SameFile(before, opened) {
		_ = parent.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("private MCP staging parent changed while it was opened")
	}
	return parent, nil
}

func (service *Service) cleanupPrivateStagingDirectory(token string, parentPath string) error {
	if err := validateWorkToken(token); err != nil {
		return err
	}
	parent, err := openPrivateMCPStagingParent(parentPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() { _ = parent.Close() }()
	container, containerName, before, err := openPrivateMCPStagingContainer(parent, token)
	if err != nil || container == nil {
		return err
	}
	ownerPresent, stagePresent, err := service.inspectPrivateMCPStagingContainer(container, token)
	if err != nil {
		_ = container.Close()
		return err
	}
	if stagePresent {
		remaining := maxMCPWorkPayloadEntries
		if err := removePrivateMCPStagingTree(container, privateMCPStagingDataName, &remaining); err != nil {
			_ = container.Close()
			return err
		}
	}
	if ownerPresent {
		if err := removePrivateMCPStagingOwner(container); err != nil {
			_ = container.Close()
			return err
		}
	}
	if err := syncRootDirectory(container, "."); err != nil {
		_ = container.Close()
		return err
	}
	return removePrivateMCPStagingContainer(parent, container, containerName, before)
}

func openPrivateMCPStagingContainer(
	parent *os.Root,
	token string,
) (*os.Root, string, os.FileInfo, error) {
	containerName := privateMCPStagingPrefix + token
	before, err := parent.Lstat(containerName)
	if errors.Is(err, os.ErrNotExist) {
		return nil, containerName, nil, nil
	}
	if err != nil {
		return nil, containerName, nil, err
	}
	if !before.IsDir() {
		return nil, containerName, nil, fmt.Errorf("private MCP staging container %q is not a real directory", containerName)
	}
	container, err := parent.OpenRoot(containerName)
	if err != nil {
		return nil, containerName, nil, err
	}
	opened, err := container.Stat(".")
	if err != nil || !opened.IsDir() || !os.SameFile(before, opened) {
		_ = container.Close()
		if err != nil {
			return nil, containerName, nil, err
		}
		return nil, containerName, nil, fmt.Errorf("private MCP staging container %q changed while it was opened", containerName)
	}
	return container, containerName, before, nil
}

func (service *Service) inspectPrivateMCPStagingContainer(
	container *os.Root,
	token string,
) (bool, bool, error) {
	entries, err := readRootDirectoryBounded(container, 2)
	if err != nil {
		return false, false, err
	}
	ownerPresent := false
	stagePresent := false
	for _, entry := range entries {
		switch entry.Name() {
		case privateMCPStagingOwnerName:
			ownerPresent = true
		case privateMCPStagingDataName:
			stagePresent = true
		default:
			return false, false, fmt.Errorf("private MCP staging container contains unowned entry %q", entry.Name())
		}
	}
	if stagePresent {
		if !ownerPresent {
			return false, false, fmt.Errorf("private MCP staging container has data without an owner record")
		}
		var owner privateMCPStagingOwner
		if err := readRootedWorkJSON(container, privateMCPStagingOwnerName, &owner); err != nil {
			return false, false, fmt.Errorf("read private MCP staging ownership: %w", err)
		}
		expectedOutput := fmt.Sprintf("%x", sha256.Sum256([]byte(service.config.OutputRoot)))
		if owner.SchemaVersion != 1 || owner.Token != token || owner.OutputRoot != expectedOutput {
			return false, false, fmt.Errorf("private MCP staging ownership does not match the recovery operation")
		}
	}
	return ownerPresent, stagePresent, nil
}

func removePrivateMCPStagingOwner(container *os.Root) error {
	info, err := container.Lstat(privateMCPStagingOwnerName)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxMCPWorkMetadataBytes {
		return fmt.Errorf("private MCP staging owner is not a bounded regular file")
	}
	return container.Remove(privateMCPStagingOwnerName)
}

func removePrivateMCPStagingContainer(
	parent, container *os.Root,
	containerName string,
	expected os.FileInfo,
) error {
	current, err := parent.Lstat(containerName)
	if err != nil || !os.SameFile(expected, current) {
		_ = container.Close()
		if err != nil {
			return err
		}
		return fmt.Errorf("private MCP staging container %q changed during cleanup", containerName)
	}
	if err := container.Close(); err != nil {
		return err
	}
	current, err = parent.Lstat(containerName)
	if err != nil || !os.SameFile(expected, current) {
		if err != nil {
			return err
		}
		return fmt.Errorf("private MCP staging container %q changed before removal", containerName)
	}
	if err := parent.Remove(containerName); err != nil {
		return err
	}
	return syncRootDirectory(parent, ".")
}

func removePrivateMCPStagingTree(parent *os.Root, name string, remaining *int) error {
	before, err := parent.Lstat(name)
	if err != nil {
		return err
	}
	if !before.IsDir() {
		return fmt.Errorf("private MCP staging entry %q is not a real directory", name)
	}
	directory, err := parent.OpenRoot(name)
	if err != nil {
		return err
	}
	opened, err := directory.Stat(".")
	if err != nil || !opened.IsDir() || !os.SameFile(before, opened) {
		_ = directory.Close()
		if err != nil {
			return err
		}
		return fmt.Errorf("private MCP staging entry %q changed while it was opened", name)
	}
	entries, err := readRootDirectoryBounded(directory, *remaining)
	if err != nil {
		_ = directory.Close()
		return err
	}
	*remaining -= len(entries)
	for _, entry := range entries {
		info, err := directory.Lstat(entry.Name())
		if err != nil {
			_ = directory.Close()
			return err
		}
		switch {
		case info.IsDir():
			if err := removePrivateMCPStagingTree(directory, entry.Name(), remaining); err != nil {
				_ = directory.Close()
				return err
			}
		case info.Mode().IsRegular():
			if err := directory.Remove(entry.Name()); err != nil {
				_ = directory.Close()
				return err
			}
		default:
			_ = directory.Close()
			return fmt.Errorf("private MCP staging contains unsupported entry %q", entry.Name())
		}
	}
	if err := syncRootDirectory(directory, "."); err != nil {
		_ = directory.Close()
		return err
	}
	current, err := parent.Lstat(name)
	if err != nil || !os.SameFile(before, current) {
		_ = directory.Close()
		if err != nil {
			return err
		}
		return fmt.Errorf("private MCP staging entry %q changed during cleanup", name)
	}
	if err := directory.Close(); err != nil {
		return err
	}
	if err := parent.Remove(name); err != nil {
		return err
	}
	return syncRootDirectory(parent, ".")
}
