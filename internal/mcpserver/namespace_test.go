package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestMCPLiveTargetRequiresStableSourceNamespaceForValidationAndImport(t *testing.T) {
	t.Parallel()

	for _, importRequested := range []bool{false, true} {
		t.Run(fmt.Sprintf("import=%t", importRequested), func(t *testing.T) {
			root := t.TempDir()
			output := filepath.Join(root, "migrations")
			service, err := New(Config{
				Root: root, OutputRoot: output,
				TargetURL: "https://signoz.example.test", APIKey: "test-key",
			})
			require.NoError(t, err)
			result, err := service.handleMigrateDashboard(context.Background(), mcp.CallToolRequest{
				Params: mcp.CallToolParams{Arguments: map[string]any{
					"grafana_json": `{"uid":"shared","title":"Dashboard","panels":[]}`,
					"import":       importRequested,
				}},
			})
			require.NoError(t, err)
			assert.True(t, result.IsError)
			require.Len(t, result.Content, 1)
			content, ok := mcp.AsTextContent(result.Content[0])
			require.True(t, ok)
			assert.Contains(t, content.Text, "source_namespace")
			entries, readErr := os.ReadDir(output)
			require.NoError(t, readErr)
			assert.Empty(t, entries)
		})
	}
}

func TestMCPRejectsUnsafeIdentityComponentsBeforeCreatingMigration(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	output := filepath.Join(root, "migrations")
	service, err := New(Config{Root: root, OutputRoot: output})
	require.NoError(t, err)
	result, err := service.handleMigrateDashboard(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{
			"grafana_json":     `{"uid":"safe","title":"Dashboard","panels":[]}`,
			"source_namespace": "grafana\x00production",
		}},
	})
	require.NoError(t, err)
	require.True(t, result.IsError)
	content, ok := mcp.AsTextContent(result.Content[0])
	require.True(t, ok)
	assert.Contains(t, content.Text, "control or formatting")
	entries, err := os.ReadDir(output)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestMCPRejectsUnsafeGrafanaIDIdentityBeforeDownload(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, fmt.Errorf("unexpected outbound request")
	})}
	root := t.TempDir()
	service, err := New(Config{Root: root, OutputRoot: filepath.Join(root, "out"), HTTPClient: client})
	require.NoError(t, err)
	result, err := service.handleMigrateDashboard(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{
			"grafana_id":      "1860",
			"source_identity": "grafana.com:1860\n",
		}},
	})
	require.NoError(t, err)
	require.True(t, result.IsError)
	assert.Zero(t, requests.Load())
	content, ok := mcp.AsTextContent(result.Content[0])
	require.True(t, ok)
	assert.Contains(t, content.Text, "control or formatting")
}

func TestMCPUIDLessInlineImportRequiresStableSourceIdentity(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	service, err := New(Config{
		Root: root, OutputRoot: root, TargetURL: "https://signoz.example.test", APIKey: "test-key",
	})
	require.NoError(t, err)
	result, err := service.handleMigrateDashboard(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{
			"grafana_json":     `{"title":"Dashboard without UID","panels":[]}`,
			"source_namespace": "grafana:production",
			"import":           true,
		}},
	})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	require.Len(t, result.Content, 1)
	content, ok := mcp.AsTextContent(result.Content[0])
	require.True(t, ok)
	assert.Contains(t, content.Text, "source_identity")
}
