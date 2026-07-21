package signoz

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTargetInventoriesRequireExplicitDataArrays(t *testing.T) {
	t.Parallel()

	for name, response := range map[string]map[string]any{
		"missing": {},
		"null":    {"data": nil},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writeTestJSON(t, writer, http.StatusOK, response)
			}))
			t.Cleanup(server.Close)

			client, err := NewClient(server.URL, "key", server.Client())
			require.NoError(t, err)
			_, dashboardErr := client.ListDashboards(context.Background())
			require.ErrorContains(t, dashboardErr, "missing data array")
			_, ruleErr := client.ListAlertRules(context.Background())
			require.ErrorContains(t, ruleErr, "missing data array")
		})
	}
}

func TestTargetInventoriesPreserveExplicitEmptyArrays(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(t, writer, http.StatusOK, map[string]any{"data": []any{}})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "key", server.Client())
	require.NoError(t, err)
	dashboards, err := client.ListDashboards(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, dashboards)
	rules, err := client.ListAlertRules(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, rules)
}
