package signoz

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConcurrentDashboardUpsertsAcrossClientsCreateOnce(t *testing.T) {
	t.Parallel()

	var mutex sync.Mutex
	var stored *DashboardV5
	var emptyLists atomic.Int32
	var posts atomic.Int32
	var puts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			mutex.Lock()
			current := stored
			mutex.Unlock()
			if current == nil {
				emptyLists.Add(1)
				time.Sleep(20 * time.Millisecond)
				writeTestJSON(t, writer, http.StatusOK, map[string]any{"data": []any{}})
				return
			}
			writeTestJSON(t, writer, http.StatusOK, map[string]any{"data": []any{map[string]any{
				"id": "dashboard-id", "data": current,
			}}})
		case http.MethodPost:
			var dashboard DashboardV5
			require.NoError(t, json.NewDecoder(request.Body).Decode(&dashboard))
			mutex.Lock()
			defer mutex.Unlock()
			if stored != nil {
				writeTestJSON(t, writer, http.StatusConflict, map[string]any{"error": map[string]any{"message": "duplicate"}})
				return
			}
			stored = &dashboard
			posts.Add(1)
			writeTestJSON(t, writer, http.StatusCreated, map[string]any{"data": map[string]any{"id": "dashboard-id"}})
		case http.MethodPut:
			var dashboard DashboardV5
			require.NoError(t, json.NewDecoder(request.Body).Decode(&dashboard))
			mutex.Lock()
			stored = &dashboard
			mutex.Unlock()
			puts.Add(1)
			writer.WriteHeader(http.StatusNoContent)
		default:
			writer.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)

	clients := make([]*Client, 2)
	for index := range clients {
		client, err := NewClient(server.URL, "key", server.Client())
		require.NoError(t, err)
		clients[index] = client
	}
	start := make(chan struct{})
	results := make(chan DashboardWriteResult, 2)
	errors := make(chan error, 2)
	for _, client := range clients {
		go func(client *Client) {
			<-start
			result, err := client.UpsertDashboard(context.Background(), DashboardV5{Title: "Host", UUID: "stable-uuid"})
			results <- result
			errors <- err
		}(client)
	}
	close(start)

	actions := make(map[string]int)
	for range clients {
		require.NoError(t, <-errors)
		actions[(<-results).Action]++
	}
	assert.Equal(t, map[string]int{"created": 1, "updated": 1}, actions)
	assert.Equal(t, int32(1), emptyLists.Load(), "the second client must inventory after the first create")
	assert.Equal(t, int32(1), posts.Load())
	assert.Equal(t, int32(1), puts.Load())
}

func TestConcurrentAlertRuleUpsertsAcrossClientsCreateOnce(t *testing.T) {
	t.Parallel()

	var mutex sync.Mutex
	var stored *StoredAlertRule
	var emptyLists atomic.Int32
	var posts atomic.Int32
	var puts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			mutex.Lock()
			current := stored
			mutex.Unlock()
			if current == nil {
				emptyLists.Add(1)
				time.Sleep(20 * time.Millisecond)
				writeTestJSON(t, writer, http.StatusOK, map[string]any{"data": []any{}})
				return
			}
			writeTestJSON(t, writer, http.StatusOK, map[string]any{"data": []any{current}})
		case http.MethodPost:
			mutex.Lock()
			defer mutex.Unlock()
			if stored != nil {
				writeTestJSON(t, writer, http.StatusConflict, map[string]any{"error": map[string]any{"message": "duplicate"}})
				return
			}
			stored = &StoredAlertRule{ID: "rule-id", Alert: "NodeDown", Labels: map[string]string{migrationIDLabel: "stable-id"}}
			posts.Add(1)
			writeTestJSON(t, writer, http.StatusCreated, map[string]any{"data": stored})
		case http.MethodPut:
			puts.Add(1)
			writer.WriteHeader(http.StatusNoContent)
		default:
			writer.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)

	clients := make([]*Client, 2)
	for index := range clients {
		client, err := NewClient(server.URL, "key", server.Client())
		require.NoError(t, err)
		clients[index] = client
	}
	rule := AlertRuleV2{Alert: "NodeDown", Labels: map[string]string{migrationIDLabel: "stable-id"}}
	start := make(chan struct{})
	results := make(chan AlertRuleWriteResult, 2)
	errors := make(chan error, 2)
	for _, client := range clients {
		go func(client *Client) {
			<-start
			result, err := client.UpsertAlertRule(context.Background(), rule)
			results <- result
			errors <- err
		}(client)
	}
	close(start)

	actions := make(map[string]int)
	for range clients {
		require.NoError(t, <-errors)
		actions[(<-results).Action]++
	}
	assert.Equal(t, map[string]int{"created": 1, "updated": 1}, actions)
	assert.Equal(t, int32(1), emptyLists.Load(), "the second client must inventory after the first create")
	assert.Equal(t, int32(1), posts.Load())
	assert.Equal(t, int32(1), puts.Load())
}

func TestAlertRuleCreateReconcilesCommittedConflict(t *testing.T) {
	t.Parallel()

	var listed atomic.Int32
	var puts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			if listed.Add(1) == 1 {
				writeTestJSON(t, writer, http.StatusOK, map[string]any{"data": []any{}})
				return
			}
			writeTestJSON(t, writer, http.StatusOK, map[string]any{"data": []any{map[string]any{
				"id": "committed-id", "alert": "NodeDown", "labels": map[string]string{migrationIDLabel: "stable-id"},
			}}})
		case http.MethodPost:
			writeTestJSON(t, writer, http.StatusConflict, map[string]any{"error": map[string]any{
				"code": "conflict", "message": "already committed",
			}})
		case http.MethodPut:
			assert.Equal(t, "/api/v2/rules/committed-id", request.URL.Path)
			puts.Add(1)
			writer.WriteHeader(http.StatusNoContent)
		default:
			writer.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "key", server.Client())
	require.NoError(t, err)
	result, err := client.UpsertAlertRule(context.Background(), AlertRuleV2{
		Alert: "NodeDown", Labels: map[string]string{migrationIDLabel: "stable-id"},
	})
	require.NoError(t, err)
	assert.Equal(t, AlertRuleWriteResult{
		ID: "committed-id", Alert: "NodeDown", Action: "updated",
		Requested: true, Attempted: true, Succeeded: true,
	}, result)
	assert.Equal(t, int32(1), puts.Load())
}

func TestUpsertAlertRulesRejectsAmbiguousTargetInventory(t *testing.T) {
	t.Parallel()

	var mutations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			mutations.Add(1)
		}
		writeTestJSON(t, writer, http.StatusOK, map[string]any{"data": []any{
			map[string]any{"id": "one", "alert": "NodeDown", "labels": map[string]string{migrationIDLabel: "stable-id"}},
			map[string]any{"id": "two", "alert": "NodeDown copy", "labels": map[string]string{migrationIDLabel: "stable-id"}},
		}})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "key", server.Client())
	require.NoError(t, err)
	_, err = client.UpsertAlertRule(context.Background(), AlertRuleV2{
		Alert: "NodeDown", Labels: map[string]string{migrationIDLabel: "stable-id"},
	})
	require.ErrorContains(t, err, "multiple SigNoz alert rules")
	assert.Zero(t, mutations.Load())
}
