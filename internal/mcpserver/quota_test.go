package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutputQuotaDefaultsAndRejectsNegativeLimits(t *testing.T) {
	quota, err := newOutputQuota(0, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(DefaultMaxOutputEntries), quota.maxEntries)
	assert.Equal(t, DefaultMaxOutputBytes, quota.maxBytes)

	_, err = newOutputQuota(-1, 1)
	assert.EqualError(t, err, "MCP max output entries must be zero (default) or greater")
	assert.True(t, IsConfigError(err))
	_, err = newOutputQuota(1, -1)
	assert.EqualError(t, err, "MCP max output bytes must be zero (default) or greater")
	assert.True(t, IsConfigError(err))
}

func TestOutputQuotaMeasuresNestedEntriesAndLogicalBytes(t *testing.T) {
	service, output := newQuotaTestService(t, Config{})
	require.NoError(t, os.MkdirAll(filepath.Join(output, "one", "two"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(output, "root.json"), []byte("123"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(output, "one", "two", "leaf.json"), []byte("4567"), 0o600))

	usage, err := service.measureOutputUsage()
	require.NoError(t, err)
	assert.Equal(t, int64(4), usage.entries)
	assert.Equal(t, int64(7), usage.bytes)
}

func TestOutputQuotaRefusesSymlinksWithoutFollowingThem(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "out")
	require.NoError(t, os.Mkdir(output, 0o700))
	outside := filepath.Join(root, "outside")
	require.NoError(t, os.Mkdir(outside, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "sentinel"), []byte("unchanged"), 0o600))
	if err := os.Symlink(outside, filepath.Join(output, "redirect")); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	service, err := New(Config{Root: root, OutputRoot: output})
	require.NoError(t, err)

	_, _, _, err = service.beginMigrationWork([]byte(`{"title":"safe"}`), time.Unix(1, 0), false)
	require.ErrorContains(t, err, "refuse symbolic link")
	data, readErr := os.ReadFile(filepath.Join(outside, "sentinel"))
	require.NoError(t, readErr)
	assert.Equal(t, "unchanged", string(data))
	entries, readErr := os.ReadDir(output)
	require.NoError(t, readErr)
	assert.Len(t, entries, 1)
}

func TestOutputEntryQuotaFailsBeforeCreatingMigrationAndPreservesArtifacts(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "out")
	require.NoError(t, os.Mkdir(output, 0o700))
	existing := filepath.Join(output, "operator-note.txt")
	require.NoError(t, os.WriteFile(existing, []byte("keep"), 0o600))
	service, err := New(Config{Root: root, OutputRoot: output, MaxOutputEntries: 1})
	require.NoError(t, err)

	_, _, _, err = service.beginMigrationWork([]byte(`{"title":"safe"}`), time.Unix(1, 0), false)
	require.ErrorContains(t, err, "entry quota would be exceeded")
	data, readErr := os.ReadFile(existing)
	require.NoError(t, readErr)
	assert.Equal(t, "keep", string(data))
	entries, readErr := os.ReadDir(output)
	require.NoError(t, readErr)
	assert.Len(t, entries, 1)
}

func TestOutputByteQuotaFailsBeforePublishingArtifact(t *testing.T) {
	service, output := newQuotaTestService(t, Config{MaxOutputBytes: 2})
	id := "dashboard-byte-quota"
	require.NoError(t, service.createOutputDirectory(id))

	err := service.writeOutputAtomic(filepath.Join(id, "too-large.json"), []byte("123"))
	require.ErrorContains(t, err, "byte quota would be exceeded")
	assert.NoFileExists(t, filepath.Join(output, id, "too-large.json"))
	entries, readErr := os.ReadDir(filepath.Join(output, id))
	require.NoError(t, readErr)
	assert.Empty(t, entries)
}

func TestValidationDirectoriesAndArtifactsConsumeOutputQuota(t *testing.T) {
	service, output := newQuotaTestService(t, Config{MaxOutputEntries: 5})
	id := "dashboard-validation-entry-quota"
	require.NoError(t, service.createOutputDirectory(id))
	validationRoot := filepath.Join(id, "validations")
	require.NoError(t, service.createOutputDirectory(validationRoot))
	relative := filepath.Join(validationRoot, "run-quota-test")
	require.NoError(t, service.createOutputDirectory(relative))

	require.NoError(t, service.writeOutputAtomic(filepath.Join(relative, "first.json"), []byte("1")))
	require.NoError(t, service.writeOutputAtomic(filepath.Join(relative, "second.json"), []byte("2")))
	err := service.writeOutputAtomic(filepath.Join(relative, "third.json"), []byte("3"))
	require.ErrorContains(t, err, "entry quota would be exceeded")
	assert.NoFileExists(t, filepath.Join(output, relative, "third.json"))

	usage, err := service.measureOutputUsage()
	require.NoError(t, err)
	assert.Equal(t, int64(5), usage.entries)
	assert.Equal(t, int64(2), usage.bytes)
}

func TestValidationArtifactsConsumeOutputByteQuota(t *testing.T) {
	service, output := newQuotaTestService(t, Config{MaxOutputBytes: 2})
	id := "dashboard-validation-byte-quota"
	require.NoError(t, service.createOutputDirectory(id))
	validationRoot := filepath.Join(id, "validations")
	require.NoError(t, service.createOutputDirectory(validationRoot))
	relative := filepath.Join(validationRoot, "run-quota-test")
	require.NoError(t, service.createOutputDirectory(relative))

	require.NoError(t, service.writeOutputAtomic(filepath.Join(relative, "first.json"), []byte("12")))
	err := service.writeOutputAtomic(filepath.Join(relative, "second.json"), []byte("3"))
	require.ErrorContains(t, err, "byte quota would be exceeded")
	assert.NoFileExists(t, filepath.Join(output, relative, "second.json"))
}

func TestOutputQuotaRejectsImportBeforeTargetAccess(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		targetRequests.Add(1)
		response.WriteHeader(http.StatusInternalServerError)
	}))
	defer target.Close()

	root := t.TempDir()
	output := filepath.Join(root, "out")
	require.NoError(t, os.Mkdir(output, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(output, "retained.json"), []byte("keep"), 0o600))
	service, err := New(Config{
		Root: root, OutputRoot: output, MaxOutputEntries: 1,
		TargetURL: target.URL, APIKey: "test-key",
	})
	require.NoError(t, err)

	result, err := service.handleMigrateDashboard(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{
			"grafana_json":     `{"uid":"quota-test","title":"Quota test","schemaVersion":39,"panels":[]}`,
			"source_namespace": "grafana:test",
			"import":           true,
		}},
	})
	require.NoError(t, err)
	require.True(t, result.IsError)
	content, ok := mcp.AsTextContent(result.Content[0])
	require.True(t, ok)
	assert.Contains(t, content.Text, "ARTIFACT_WRITE_FAILED")
	assert.Contains(t, content.Text, "entry quota would be exceeded")
	assert.Zero(t, targetRequests.Load())
	assert.FileExists(t, filepath.Join(output, "retained.json"))
}

func TestOutputQuotaSerializesConcurrentAdmissions(t *testing.T) {
	service, _ := newQuotaTestService(t, Config{MaxOutputEntries: 3})
	id := "dashboard-concurrent-quota"
	require.NoError(t, service.createOutputDirectory(id))

	var group sync.WaitGroup
	results := make(chan error, 8)
	for index := range 8 {
		group.Go(func() {
			results <- service.writeOutputAtomic(filepath.Join(id, "artifact-"+string(rune('a'+index))+".json"), []byte("x"))
		})
	}
	group.Wait()
	close(results)
	succeeded := 0
	failed := 0
	for err := range results {
		if err == nil {
			succeeded++
			continue
		}
		assert.ErrorContains(t, err, "entry quota would be exceeded")
		failed++
	}
	assert.Equal(t, 2, succeeded)
	assert.Equal(t, 6, failed)
	usage, err := service.measureOutputUsage()
	require.NoError(t, err)
	assert.Equal(t, int64(3), usage.entries)
	assert.Equal(t, int64(2), usage.bytes)
}

func newQuotaTestService(t *testing.T, config Config) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	output := filepath.Join(root, "out")
	config.Root = root
	config.OutputRoot = output
	service, err := New(config)
	require.NoError(t, err)
	return service, output
}
