// Package metricmap loads explicit Prometheus-to-OpenTelemetry metric name mappings.
package metricmap

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const maxFileSize = 1 << 20

// Load reads a YAML mapping whose keys are source names and values are target names.
func Load(path string) (map[string]string, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("open metric name map %q: %w", path, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxFileSize+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read metric name map %q: %w", path, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close metric name map %q: %w", path, closeErr)
	}
	if len(data) > maxFileSize {
		return nil, fmt.Errorf("metric name map %q exceeds %d bytes", path, maxFileSize)
	}

	var document yaml.Node
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode metric name map %q: %w", path, err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("decode metric name map %q: expected a source-to-target mapping", path)
	}
	result := make(map[string]string, len(document.Content[0].Content)/2)
	entries := document.Content[0].Content
	for index := 0; index < len(entries); index += 2 {
		source := strings.TrimSpace(entries[index].Value)
		target := strings.TrimSpace(entries[index+1].Value)
		if entries[index].Kind != yaml.ScalarNode || entries[index+1].Kind != yaml.ScalarNode || source == "" || target == "" {
			return nil, fmt.Errorf("decode metric name map %q: names must be non-empty strings", path)
		}
		if _, duplicate := result[source]; duplicate {
			return nil, fmt.Errorf("decode metric name map %q: source %q is duplicated", path, source)
		}
		result[source] = target
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode metric name map %q: multiple YAML documents are not supported", path)
		}
		return nil, fmt.Errorf("decode metric name map %q trailing data: %w", path, err)
	}
	return result, nil
}
