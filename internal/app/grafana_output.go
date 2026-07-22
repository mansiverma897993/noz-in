package app

// Output-directory preflight and on-disk artifact write helpers shared by the
// dashboard and rule migration flows.

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mansiverma897993/noz-in/internal/artifactset"
	"github.com/mansiverma897993/noz-in/internal/atomicfile"
	"github.com/mansiverma897993/noz-in/internal/safeoutput"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
)

func preflightGrafanaOutput(
	outputDirectory string,
	bases, dashboardPaths, rulePaths []string,
	extraInputs []ProtectedInputPath,
) error {
	info, err := os.Lstat(outputDirectory)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return fmt.Errorf("inspect output directory %q: %w", outputDirectory, err)
	case !info.IsDir():
		return fmt.Errorf("output directory %q is not a real directory", outputDirectory)
	}
	protected := make([]safeoutput.ProtectedPath, 0, len(dashboardPaths)+len(rulePaths))
	for _, path := range dashboardPaths {
		protected = append(protected, safeoutput.ProtectedPath{Path: path, Purpose: "Grafana input"})
	}
	for _, path := range rulePaths {
		protected = append(protected, safeoutput.ProtectedPath{Path: path, Purpose: "Prometheus rule input"})
	}
	protected = appendProtectedInputs(protected, extraInputs)
	for _, base := range bases {
		for _, name := range dashboardArtifactNames(base) {
			destination := filepath.Join(outputDirectory, name)
			if err := ensureRegularOrAbsent(destination); err != nil {
				return err
			}
		}
		reserved, err := artifactset.ReservedPathsForReport(
			filepath.Join(outputDirectory, base+".report.json"), artifactset.KindDashboard,
		)
		if err != nil {
			return fmt.Errorf("derive reserved dashboard artifact paths for %q: %w", base, err)
		}
		for _, destination := range reserved {
			if err := safeoutput.RejectAliases(destination, protected...); err != nil {
				return err
			}
		}
	}
	return nil
}

func ensureArtifactDestinations(paths ...string) error {
	for _, path := range paths {
		if err := ensureRegularOrAbsent(path); err != nil {
			return err
		}
	}
	return nil
}

func ensureRegularOrAbsent(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect artifact destination %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refuse artifact destination %q: existing path is not a regular file", path)
	}
	return nil
}

func removeStaleArtifact(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect stale artifact %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refuse to remove stale artifact %q: existing path is not a regular file", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale artifact %q: %w", path, err)
	}
	if err := atomicfile.SyncDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("persist stale artifact removal %q: %w", path, err)
	}
	return nil
}

func writeJSON(path string, value any) error {
	if err := ensureRegularOrAbsent(path); err != nil {
		return err
	}
	data, err := jsonArtifactBytes(value)
	if err != nil {
		return fmt.Errorf("encode %q: %w", path, err)
	}
	if err := safeoutput.WriteFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("publish JSON artifact %q: %w", path, err)
	}
	return nil
}

func jsonArtifactBytes(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func artifactBindingFor(path string, value any) (*reporttypes.ArtifactBinding, error) {
	data, err := jsonArtifactBytes(value)
	if err != nil {
		return nil, fmt.Errorf("encode primary dashboard artifact %q: %w", path, err)
	}
	digest := sha256.Sum256(data)
	return &reporttypes.ArtifactBinding{
		Path:      filepath.Base(path),
		SHA256:    fmt.Sprintf("%x", digest[:]),
		SizeBytes: int64(len(data)),
	}, nil
}
