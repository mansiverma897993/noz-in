package mcpserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
)

func (service *Service) readManifest(id string) (manifest, error) {
	data, err := service.readMigrationBounded(id, "migration-result.json")
	if errors.Is(err, os.ErrNotExist) {
		data, err = service.readMigrationBounded(id, "migration.json")
	}
	if err != nil {
		return manifest{}, err
	}
	value, err := decodeManifest(data)
	if err != nil {
		return manifest{}, err
	}
	if value.MigrationID != id {
		return manifest{}, fmt.Errorf(
			"migration manifest id %q does not match requested migration_id %q",
			value.MigrationID,
			id,
		)
	}
	return value, nil
}

func decodeManifest(data []byte) (manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var value manifest
	if err := decoder.Decode(&value); err != nil {
		return manifest{}, fmt.Errorf("decode migration manifest: %w", err)
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		return manifest{}, fmt.Errorf("decode migration manifest: %w", err)
	}
	if value.SchemaVersion != 1 && value.SchemaVersion != 2 || value.MigrationID == "" {
		return manifest{}, fmt.Errorf("decode migration manifest: unsupported or incomplete manifest")
	}
	if value.Generation != "" {
		if err := validateManifestName("generation", value.Generation); err != nil {
			return manifest{}, err
		}
	}
	for field, path := range map[string]string{
		"source": value.Source, "report": value.Report, "dashboard": value.Dashboard, "html": value.HTML,
	} {
		if err := validateManifestName(field, path); err != nil {
			return manifest{}, err
		}
	}
	for _, path := range value.Rules {
		if err := validateManifestName("rule", path); err != nil {
			return manifest{}, err
		}
	}
	if value.SchemaVersion == 1 {
		if len(value.RuleBindings) != 0 {
			return manifest{}, fmt.Errorf("decode migration manifest: schema v1 cannot carry rule bindings")
		}
	} else {
		if err := validateManifestRuleBindings(value.Rules, value.RuleBindings); err != nil {
			return manifest{}, err
		}
	}
	return value, nil
}

func validateManifestRuleBindings(rules []string, bindings []reporttypes.ArtifactBinding) error {
	if len(bindings) != len(rules) {
		return fmt.Errorf("decode migration manifest: rule bindings do not cover every rule input")
	}
	for index, binding := range bindings {
		if binding.Path != rules[index] || binding.SizeBytes < 0 || binding.SizeBytes > maxMCPArtifactSize ||
			len(binding.SHA256) != sha256.Size*2 || strings.ToLower(binding.SHA256) != binding.SHA256 {
			return fmt.Errorf("decode migration manifest: invalid rule binding for %q", rules[index])
		}
		if _, err := hex.DecodeString(binding.SHA256); err != nil {
			return fmt.Errorf("decode migration manifest: invalid rule digest for %q", binding.Path)
		}
	}
	return nil
}

func rejectTrailingJSON(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("manifest contains multiple JSON values")
}

func validateManifestName(field, path string) error {
	if path == "" || path == "." || !filepath.IsLocal(path) || filepath.Base(path) != path || strings.ContainsAny(path, `/\`) {
		return fmt.Errorf("decode migration manifest: invalid %s filename %q", field, path)
	}
	return nil
}
