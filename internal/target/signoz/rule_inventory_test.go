package signoz

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListAlertRulesConsumesCurrentSigNozGetAllContract(t *testing.T) {
	t.Parallel()

	rules := make([]any, 75)
	for index := range rules {
		rules[index] = map[string]any{
			"id": fmt.Sprintf("rule-%03d", index), "alert": fmt.Sprintf("Rule %03d", index),
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/api/v2/rules", request.URL.Path)
		assert.Empty(t, request.URL.RawQuery,
			"SigNoz's current ListRules handler returns the complete inventory and does not bind pagination parameters")
		writeTestJSON(t, writer, http.StatusOK, map[string]any{"data": rules})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "key", server.Client())
	require.NoError(t, err)
	inventory, err := client.ListAlertRules(context.Background())
	require.NoError(t, err)
	assert.Len(t, inventory, 75)
}
