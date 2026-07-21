package signoz

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientUpsertAlertRuleCreatesAndUpdates(t *testing.T) {
	t.Parallel()

	listCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			listCalls++
			if listCalls == 1 {
				writeTestJSON(t, writer, http.StatusOK, map[string]any{"data": []any{}})
				return
			}
			writeTestJSON(t, writer, http.StatusOK, map[string]any{"data": []any{map[string]any{
				"id": "rule-id", "alert": "NodeDown", "labels": map[string]string{"promcast_id": "stable-id"},
			}}})
		case http.MethodPost:
			assert.Equal(t, "/api/v2/rules", request.URL.Path)
			writeTestJSON(t, writer, http.StatusCreated, map[string]any{"data": map[string]any{"id": "rule-id", "alert": "NodeDown"}})
		case http.MethodPut:
			assert.Equal(t, "/api/v2/rules/rule-id", request.URL.Path)
			writer.WriteHeader(http.StatusNoContent)
		default:
			writer.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "test-key", server.Client())
	require.NoError(t, err)
	rule := AlertRuleV2{Alert: "NodeDown", Labels: map[string]string{"promcast_id": "stable-id"}}
	created, err := client.UpsertAlertRule(context.Background(), rule)
	require.NoError(t, err)
	assert.Equal(t, AlertRuleWriteResult{
		ID: "rule-id", Alert: "NodeDown", Action: "created",
		Requested: true, Attempted: true, Succeeded: true,
	}, created)
	updated, err := client.UpsertAlertRule(context.Background(), rule)
	require.NoError(t, err)
	assert.Equal(t, AlertRuleWriteResult{
		ID: "rule-id", Alert: "NodeDown", Action: "updated",
		Requested: true, Attempted: true, Succeeded: true,
	}, updated)
}

func TestClientUpsertAlertRuleNeverCreatesMissingDisabledCandidate(t *testing.T) {
	t.Parallel()

	var inventoryCalls atomic.Int32
	var mutations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			mutations.Add(1)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		inventoryCalls.Add(1)
		writeTestJSON(t, writer, http.StatusOK, map[string]any{"data": []any{}})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "test-key", server.Client())
	require.NoError(t, err)
	rule := AlertRuleV2{
		Alert: "NodeDown", Disabled: true,
		Labels: map[string]string{migrationIDLabel: "stable-id"},
	}
	for range 2 {
		checkpointCalled := false
		results, err := client.UpsertAlertRulesWithCheckpoint(
			context.Background(),
			[]AlertRuleV2{rule},
			func(plan []AlertRuleWritePlan) error {
				checkpointCalled = true
				require.Equal(t, []AlertRuleWritePlan{{
					Alert: "NodeDown", Action: AlertRuleActionNotCreatedDisabled,
				}}, plan)
				assert.Zero(t, mutations.Load())
				return nil
			},
		)
		require.NoError(t, err)
		assert.True(t, checkpointCalled)
		require.Equal(t, []AlertRuleWriteResult{{
			Alert: "NodeDown", Action: AlertRuleActionNotCreatedDisabled, Requested: true,
		}}, results)
	}
	assert.Equal(t, int32(2), inventoryCalls.Load())
	assert.Zero(t, mutations.Load(), "a missing disabled candidate must never be POSTed")
}

func TestClientUpsertAlertRuleCheckpointsEnabledCreateBeforePost(t *testing.T) {
	t.Parallel()

	var checkpointed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			writeTestJSON(t, writer, http.StatusOK, map[string]any{"data": []any{}})
		case http.MethodPost:
			assert.True(t, checkpointed.Load(), "the attempted checkpoint must precede POST")
			writeTestJSON(t, writer, http.StatusCreated, map[string]any{"data": map[string]any{"id": "rule-id"}})
		default:
			writer.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "test-key", server.Client())
	require.NoError(t, err)
	results, err := client.UpsertAlertRulesWithCheckpoints(
		context.Background(),
		[]AlertRuleV2{{
			Alert: "NodeDown", Labels: map[string]string{migrationIDLabel: "stable-id"},
		}},
		AlertRuleWriteCheckpoints{
			BeforeMutation: func(checkpoint AlertRuleMutationCheckpoint) error {
				require.Equal(t, AlertRuleMutationCheckpoint{
					Index: 0, Alert: "NodeDown", Action: AlertRulePlanCreate,
					Completed: []AlertRuleWriteResult{},
				}, checkpoint)
				checkpointed.Store(true)
				return nil
			},
		},
	)
	require.NoError(t, err)
	require.Equal(t, []AlertRuleWriteResult{{
		ID: "rule-id", Alert: "NodeDown", Action: "created",
		Requested: true, Attempted: true, Succeeded: true,
	}}, results)
}

func TestClientUpsertAlertRuleSafelyDisablesExistingOwnedRuleWithPut(t *testing.T) {
	t.Parallel()

	var posts atomic.Int32
	var puts atomic.Int32
	checkpointCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			writeTestJSON(t, writer, http.StatusOK, map[string]any{"data": []any{map[string]any{
				"id": "rule-id", "alert": "NodeDown",
				"labels": map[string]string{migrationIDLabel: "stable-id"},
			}}})
		case http.MethodPost:
			posts.Add(1)
			writer.WriteHeader(http.StatusInternalServerError)
		case http.MethodPut:
			assert.True(t, checkpointCalled, "the durable plan checkpoint must precede mutation")
			assert.Equal(t, "/api/v2/rules/rule-id", request.URL.Path)
			var payload AlertRuleV2
			require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
			assert.True(t, payload.Disabled)
			puts.Add(1)
			writer.WriteHeader(http.StatusNoContent)
		default:
			writer.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "test-key", server.Client())
	require.NoError(t, err)
	results, err := client.UpsertAlertRulesWithCheckpoint(
		context.Background(),
		[]AlertRuleV2{{
			Alert: "NodeDown", Disabled: true,
			Labels: map[string]string{migrationIDLabel: "stable-id"},
		}},
		func(plan []AlertRuleWritePlan) error {
			checkpointCalled = true
			require.Equal(t, []AlertRuleWritePlan{{
				ID: "rule-id", Alert: "NodeDown", Action: AlertRulePlanUpdate,
			}}, plan)
			assert.Zero(t, posts.Load())
			assert.Zero(t, puts.Load())
			return nil
		},
	)
	require.NoError(t, err)
	require.Equal(t, []AlertRuleWriteResult{{
		ID: "rule-id", Alert: "NodeDown", Action: "updated",
		Requested: true, Attempted: true, Succeeded: true,
	}}, results)
	assert.Zero(t, posts.Load())
	assert.Equal(t, int32(1), puts.Load())
}

func TestClientUpsertAlertRulesCheckpointFailureAbortsBeforeMutation(t *testing.T) {
	t.Parallel()

	var mutations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writeTestJSON(t, writer, http.StatusOK, map[string]any{"data": []any{}})
			return
		}
		mutations.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "test-key", server.Client())
	require.NoError(t, err)
	checkpointErr := errors.New("checkpoint unavailable")
	results, err := client.UpsertAlertRulesWithCheckpoint(
		context.Background(),
		[]AlertRuleV2{{
			Alert: "NodeDown", Labels: map[string]string{migrationIDLabel: "stable-id"},
		}},
		func([]AlertRuleWritePlan) error { return checkpointErr },
	)
	require.ErrorIs(t, err, checkpointErr)
	assert.Empty(t, results)
	assert.Zero(t, mutations.Load())
}

func TestClientUpsertAlertRulesMutationCheckpointFailureAbortsRequest(t *testing.T) {
	t.Parallel()

	var mutations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writeTestJSON(t, writer, http.StatusOK, map[string]any{"data": []any{map[string]any{
				"id": "rule-id", "alert": "NodeDown",
				"labels": map[string]string{migrationIDLabel: "stable-id"},
			}}})
			return
		}
		mutations.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "test-key", server.Client())
	require.NoError(t, err)
	checkpointErr := errors.New("attempted checkpoint unavailable")
	results, err := client.UpsertAlertRulesWithCheckpoints(
		context.Background(),
		[]AlertRuleV2{{
			Alert: "NodeDown", Disabled: true,
			Labels: map[string]string{migrationIDLabel: "stable-id"},
		}},
		AlertRuleWriteCheckpoints{
			BeforeMutation: func(checkpoint AlertRuleMutationCheckpoint) error {
				assert.Equal(t, AlertRuleMutationCheckpoint{
					Index: 0, ID: "rule-id", Alert: "NodeDown", Action: AlertRulePlanUpdate,
					Completed: []AlertRuleWriteResult{},
				}, checkpoint)
				return checkpointErr
			},
		},
	)
	require.ErrorIs(t, err, checkpointErr)
	require.Len(t, results, 1)
	assert.Equal(t, AlertRuleActionNotAttempted, results[0].Action)
	assert.False(t, results[0].Attempted)
	assert.Zero(t, mutations.Load())
}

func TestClientUpsertAlertRuleRejectsNameCollision(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(t, writer, http.StatusOK, map[string]any{"data": []any{map[string]any{
			"id": "other", "alert": "NodeDown", "labels": map[string]string{"promcast_id": "other-id"},
		}}})
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, "test-key", server.Client())
	require.NoError(t, err)
	_, err = client.UpsertAlertRule(context.Background(), AlertRuleV2{
		Alert: "NodeDown", Labels: map[string]string{"promcast_id": "stable-id"},
	})
	require.ErrorContains(t, err, "different rule")
}

func TestQueryRequestForAlert(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_700_000_000_000)
	request := QueryRequestForAlert(AlertRuleV2{Condition: AlertConditionV2{CompositeQuery: AlertCompositeQuery{
		Queries: []AlertQueryEnvelope{{Type: "promql", Spec: AlertQuerySpec{Name: "A", Query: "up"}}},
	}}}, now)
	assert.Equal(t, "time_series", request.RequestType)
	assert.Equal(t, uint64(now.UnixMilli()), request.End)
	require.Len(t, request.CompositeQuery.Queries, 1)
	assert.Equal(t, "promql", request.CompositeQuery.Queries[0].Type)
}
