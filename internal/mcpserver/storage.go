package mcpserver

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mansiverma897993/noz-in/internal/atomicfile"
	"github.com/mansiverma897993/noz-in/internal/safeoutput"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
)

const maxMCPArtifactSize = 64 << 20

var migrationIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,127}$`)

type manifest struct {
	SchemaVersion     int                           `json:"schemaVersion"`
	MigrationID       string                        `json:"migrationId"`
	Generation        string                        `json:"generation,omitempty"`
	Source            string                        `json:"source"`
	Rules             []string                      `json:"rules,omitempty"`
	RuleBindings      []reporttypes.ArtifactBinding `json:"ruleBindings,omitempty"`
	Variables         map[string]string             `json:"variables,omitempty"`
	Report            string                        `json:"report"`
	Dashboard         string                        `json:"dashboard"`
	HTML              string                        `json:"html"`
	RateInterval      string                        `json:"rateInterval"`
	DashboardIdentity string                        `json:"dashboardIdentity,omitempty"`
	SourceNamespace   string                        `json:"sourceNamespace,omitempty"`
}

func (service *Service) readInputBounded(path string) ([]byte, error) {
	return service.readInputBoundedLimit(path, maxMCPArtifactSize)
}

func (service *Service) readInputBoundedLimit(path string, maxSize int64) ([]byte, error) {
	name, err := rootedName(service.config.Root, path)
	if err != nil {
		return nil, fmt.Errorf("resolve input path %q: %w", path, err)
	}
	return readBoundedFromRoot(service.config.Root, name, service.inputRootInfo, maxSize)
}

func (service *Service) readMigrationBounded(id, name string) ([]byte, error) {
	if _, err := service.migrationDirectory(id); err != nil {
		return nil, err
	}
	if err := validateManifestName("artifact", name); err != nil {
		return nil, err
	}
	return readBoundedFromRoot(service.config.OutputRoot, filepath.Join(id, name), service.outputRootInfo, maxMCPArtifactSize)
}

func rootedName(root, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is empty")
	}
	name := filepath.Clean(path)
	if filepath.IsAbs(name) {
		var err error
		name, err = filepath.Rel(filepath.Clean(root), name)
		if err != nil {
			return "", err
		}
	}
	if !filepath.IsLocal(name) || name == "." {
		return "", fmt.Errorf("path is outside the configured root")
	}
	return name, nil
}

func readBoundedFromRoot(rootPath, name string, expectedRoot os.FileInfo, maxSize int64) ([]byte, error) {
	if maxSize < 0 {
		return nil, fmt.Errorf("invalid artifact read limit %d", maxSize)
	}
	root, err := openVerifiedRoot(rootPath, expectedRoot)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open %q beneath %q: %w", name, rootPath, err)
	}
	return readBoundedFile(file, filepath.Join(rootPath, name), maxSize)
}

func openVerifiedRoot(path string, expected os.FileInfo) (*os.Root, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect artifact root %q: %w", path, err)
	}
	if !before.IsDir() || expected == nil || !os.SameFile(before, expected) {
		return nil, fmt.Errorf("artifact root %q was replaced after server initialization", path)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("open artifact root %q: %w", path, err)
	}
	after, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("inspect opened artifact root %q: %w", path, err)
	}
	if !after.IsDir() || !os.SameFile(after, expected) {
		_ = root.Close()
		return nil, fmt.Errorf("artifact root %q changed while it was opened", path)
	}
	return root, nil
}

func (service *Service) ensureMigrationDirectoryStable(id string) error {
	if _, err := service.migrationDirectory(id); err != nil {
		return err
	}
	return service.ensureOutputDirectoryStable(id)
}

func (service *Service) ensureOutputDirectoryStable(relative string) error {
	if !filepath.IsLocal(relative) || relative == "." {
		return fmt.Errorf("output directory path %q is not local", relative)
	}
	root, err := openVerifiedRoot(service.config.OutputRoot, service.outputRootInfo)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	before, err := root.Lstat(relative)
	if err != nil {
		return fmt.Errorf("inspect output directory %q: %w", relative, err)
	}
	if !before.IsDir() {
		return fmt.Errorf("output directory %q is not a real directory", relative)
	}
	directory, err := root.OpenRoot(relative)
	if err != nil {
		return fmt.Errorf("open output directory %q: %w", relative, err)
	}
	defer func() { _ = directory.Close() }()
	after, err := directory.Stat(".")
	if err != nil {
		return fmt.Errorf("inspect opened output directory %q: %w", relative, err)
	}
	if !after.IsDir() || !os.SameFile(before, after) {
		return fmt.Errorf("output directory %q changed while it was opened", relative)
	}
	return nil
}

func (service *Service) createOutputDirectory(relative string) error {
	if !filepath.IsLocal(relative) || relative == "." {
		return fmt.Errorf("output directory path %q is not local", relative)
	}
	releaseQuota, err := service.reserveOutputQuota(1, 0)
	if err != nil {
		return err
	}
	defer releaseQuota()
	root, err := openVerifiedRoot(service.config.OutputRoot, service.outputRootInfo)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := root.Mkdir(relative, 0o700); err != nil {
		return fmt.Errorf("create output directory %q: %w", relative, err)
	}
	if err := root.Chmod(relative, 0o700); err != nil {
		return fmt.Errorf("set output directory permissions %q: %w", relative, err)
	}
	if err := syncRootDirectory(root, filepath.Dir(relative)); err != nil {
		return fmt.Errorf("persist output directory %q: %w", relative, err)
	}
	return service.ensureOutputDirectoryStable(relative)
}

func syncRootDirectory(root *os.Root, relative string) error {
	directory, err := root.Open(relative)
	if err != nil {
		return fmt.Errorf("open rooted directory %q for persistence: %w", relative, err)
	}
	syncErr := atomicfile.SyncOpenedDirectory(directory)
	closeErr := directory.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("persist rooted directory %q: %w", relative, err)
	}
	return nil
}

func (service *Service) writeOutputAtomic(relative string, data []byte) error {
	if !filepath.IsLocal(relative) || relative == "." {
		return fmt.Errorf("output artifact path %q is not local", relative)
	}
	parent := filepath.Dir(relative)
	if err := service.ensureOutputDirectoryStable(parent); err != nil {
		return err
	}
	root, err := openVerifiedRoot(service.config.OutputRoot, service.outputRootInfo)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	directoryRoot, err := root.OpenRoot(parent)
	if err != nil {
		return fmt.Errorf("open pinned output directory %q: %w", parent, err)
	}
	defer func() { _ = directoryRoot.Close() }()
	destinationName := filepath.Base(relative)
	if _, err := directoryRoot.Lstat(destinationName); err == nil {
		return fmt.Errorf("refuse to replace existing rooted output artifact %q", relative)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect rooted output artifact %q: %w", relative, err)
	}
	releaseQuota, err := service.reserveOutputQuota(1, int64(len(data)))
	if err != nil {
		return err
	}
	defer releaseQuota()
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("generate rooted output temporary name: %w", err)
	}
	temporary := "." + destinationName + ".tmp-" + hex.EncodeToString(nonce[:])
	file, err := directoryRoot.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create rooted output temporary file for %q: %w", relative, err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = directoryRoot.Remove(temporary)
		}
	}()
	if _, err := io.Copy(file, bytes.NewReader(data)); err != nil {
		_ = file.Close()
		return fmt.Errorf("write rooted output temporary file for %q: %w", relative, err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("set rooted output permissions for %q: %w", relative, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync rooted output temporary file for %q: %w", relative, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close rooted output temporary file for %q: %w", relative, err)
	}
	if err := directoryRoot.Rename(temporary, destinationName); err != nil {
		return fmt.Errorf("publish rooted output artifact %q: %w", relative, err)
	}
	cleanup = false
	directory, err := directoryRoot.Open(".")
	if err != nil {
		return fmt.Errorf("open rooted output directory %q for persistence: %w", parent, err)
	}
	syncErr := atomicfile.SyncOpenedDirectory(directory)
	closeErr := directory.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("persist rooted output artifact %q: %w", relative, err)
	}
	return nil
}

func (service *Service) migrationDirectory(id string) (string, error) {
	if !migrationIDPattern.MatchString(id) {
		return "", fmt.Errorf("invalid migration_id %q", id)
	}
	directory := filepath.Join(service.config.OutputRoot, id)
	if !withinRoot(service.config.OutputRoot, directory) {
		return "", fmt.Errorf("migration_id resolves outside the output root")
	}
	return directory, nil
}

func withinRoot(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func readBoundedFile(file *os.File, displayPath string, maxSize int64) ([]byte, error) {
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect %q: %w", displayPath, err)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("%q is not a regular file", displayPath)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxSize+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read %q: %w", displayPath, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close %q: %w", displayPath, closeErr)
	}
	if int64(len(data)) > maxSize {
		return nil, fmt.Errorf("%q exceeds %d bytes", displayPath, maxSize)
	}
	return data, nil
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %q: %w", path, err)
	}
	return writeAtomic(path, append(data, '\n'))
}

func writeAtomic(path string, data []byte) error {
	directory, err := safeoutput.OpenOrCreateDirectory(filepath.Dir(path), 0o700)
	if err != nil {
		return fmt.Errorf("create directory for %q: %w", path, err)
	}
	if err := directory.Close(); err != nil {
		return fmt.Errorf("close directory for %q: %w", path, err)
	}
	if err := safeoutput.WriteFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("publish %q: %w", path, err)
	}
	return nil
}
