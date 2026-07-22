package validate

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mansiverma897993/noz-in/internal/rules"
	"github.com/mansiverma897993/noz-in/internal/target/signoz"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAlertRulesKeepsOnlyPreviewValidCandidates(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v5/query_range/preview":
			valid := !strings.Contains(string(body), "invalid_metric")
			var previewError any
			if !valid {
				previewError = map[string]any{"code": "invalid_query", "message": "unknown metric"}
			}
			require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{"data": map[string]any{
				"compositeQuery": map[string]any{"A": map[string]any{"valid": valid, "error": previewError}},
			}}))
		case "/api/v5/query_range":
			require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{
				"status": "success", "data": map[string]any{"data": map[string]any{"results": []any{map[string]any{"queryName": "A"}}}},
			}))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client, err := signoz.NewClient(server.URL, "key", server.Client())
	require.NoError(t, err)

	migration := rules.Migration{Groups: []rules.GroupMigration{{Rules: []rules.RuleMigration{
		{Payload: alertPayload("valid", "up")},
		{Payload: alertPayload("invalid", "invalid_metric")},
	}}}}
	evidence := reporttypes.RuleReport{Groups: []reporttypes.RuleGroupRecord{{Rules: make([]reporttypes.RuleRecord, 2)}}}
	accepted, err := AlertRules(context.Background(), client, migration, &evidence, true, Options{
		Workers: 2, Now: func() time.Time { return time.Unix(1_700_000_000, 0) },
	})

	require.NoError(t, err)
	require.Len(t, accepted, 1)
	assert.Equal(t, "valid", accepted[0].Payload.Alert)
	assert.Equal(t, 2, evidence.Summary.Previewed)
	assert.Equal(t, 1, evidence.Summary.PreviewValid)
	assert.Equal(t, 1, evidence.Summary.PreviewInvalid)
	assert.Equal(t, 1, evidence.Summary.Executed)
	assert.Equal(t, 1, evidence.Summary.DataAbsent)
	assert.True(t, evidence.Groups[0].Rules[0].Validation.PreviewOK)
	assert.Empty(t, evidence.Groups[0].Rules[0].Validation.ErrorCode)
	assert.Empty(t, evidence.Groups[0].Rules[0].Validation.Error)
	assert.Equal(t, "invalid_query", evidence.Groups[0].Rules[1].Validation.ErrorCode)
}

func TestAlertRulesSkipsPreflightWhenDisabled(t *testing.T) {
	t.Parallel()

	migration := rules.Migration{Groups: []rules.GroupMigration{{Rules: []rules.RuleMigration{{Payload: alertPayload("valid", "up")}}}}}
	candidates, err := AlertRules(context.Background(), nil, migration, &reporttypes.RuleReport{}, false, Options{})
	require.NoError(t, err)
	require.Len(t, candidates, 1)
}

func TestAlertRulesRejectsInconsistentValidPreview(t *testing.T) {
	t.Parallel()

	var executions int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v5/query_range/preview":
			require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{"data": map[string]any{
				"compositeQuery": map[string]any{"A": map[string]any{
					"valid": true,
					"error": map[string]any{"code": "invalid_query", "message": "contradictory"},
				}},
			}}))
		case "/api/v5/query_range":
			executions++
			writer.WriteHeader(http.StatusInternalServerError)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client, err := signoz.NewClient(server.URL, "key", server.Client())
	require.NoError(t, err)

	migration := rules.Migration{Groups: []rules.GroupMigration{{Rules: []rules.RuleMigration{{
		Payload: alertPayload("valid-but-error", "up"),
	}}}}}
	evidence := reporttypes.RuleReport{Groups: []reporttypes.RuleGroupRecord{{Rules: make([]reporttypes.RuleRecord, 1)}}}
	accepted, err := AlertRules(context.Background(), client, migration, &evidence, true, Options{
		Workers: 1, Now: func() time.Time { return time.Unix(1_700_000_000, 0) },
	})

	require.NoError(t, err)
	assert.Empty(t, accepted)
	assert.Zero(t, executions)
	validation := evidence.Groups[0].Rules[0].Validation
	assert.False(t, validation.PreviewOK)
	assert.Equal(t, "PREVIEW_RESPONSE_INCONSISTENT", validation.ErrorCode)
	assert.Contains(t, validation.Error, "valid while also returning an error")
}

func alertPayload(name, query string) *signoz.AlertRuleV2 {
	return &signoz.AlertRuleV2{
		Alert: name,
		Condition: signoz.AlertConditionV2{CompositeQuery: signoz.AlertCompositeQuery{Queries: []signoz.AlertQueryEnvelope{{
			Type: "promql", Spec: signoz.AlertQuerySpec{Name: "A", Query: query},
		}}}},
	}
}
