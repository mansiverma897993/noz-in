package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	internalrules "github.com/mansiverma897993/signoz/internal/rules"
	sourceprometheus "github.com/mansiverma897993/signoz/internal/source/prometheus"
	"github.com/mansiverma897993/signoz/internal/target/signoz"
	"github.com/mansiverma897993/signoz/pkg/reporttypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigratePrometheusRulesWritesAndValidates(t *testing.T) {
	t.Parallel()

	var mutations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "test-key", request.Header.Get("SIGNOZ-API-KEY"))
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeAppJSON(t, writer, http.StatusOK, map[string]any{"data": map[string]any{
				"type": "sum", "temporality": "cumulative", "isMonotonic": true,
			}})
		case "/api/v2/metrics/attributes":
			writeAppJSON(t, writer, http.StatusOK, map[string]any{"data": map[string]any{"attributes": []any{
				map[string]any{"key": "service.name"},
				map[string]any{"key": "service.instance.id"},
			}}})
		case "/api/v5/query_range/preview":
			writeAppJSON(t, writer, http.StatusOK, map[string]any{"data": map[string]any{"compositeQuery": map[string]any{
				"A": map[string]any{"valid": true},
			}}})
		case "/api/v5/query_range":
			writeAppJSON(t, writer, http.StatusOK, map[string]any{"status": "success", "data": map[string]any{
				"data": map[string]any{"results": []any{map[string]any{
					"queryName": "A", "aggregations": []any{map[string]any{"series": []any{map[string]any{
						"values": []any{map[string]any{"timestamp": 1, "value": 0}},
					}}}},
				}}},
			}})
		case "/api/v2/rules":
			switch request.Method {
			case http.MethodGet:
				writeAppJSON(t, writer, http.StatusOK, map[string]any{"data": []any{}})
			default:
				mutations.Add(1)
				writer.WriteHeader(http.StatusInternalServerError)
			}
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	output := t.TempDir()
	results, err := MigratePrometheusRules(context.Background(), []string{"../source/prometheus/testdata/rules.yaml"}, RuleOptions{
		OutputDirectory: output,
		TargetURL:       server.URL,
		APIKey:          "test-key",
		HTTPClient:      server.Client(),
		Validate:        true,
		SourceNamespace: "prometheus:test",
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.FileExists(t, results[0].RulesPath)
	assert.FileExists(t, results[0].ReportPath)
	assert.Equal(t, 1, results[0].Summary.Alerting)
	assert.Equal(t, 1, results[0].Summary.Recording)
	assert.Equal(t, 1, results[0].Summary.PreviewValid)
	assert.Equal(t, 1, results[0].Summary.DataPresent)
	assert.Zero(t, results[0].Summary.Created)
	assert.Equal(t, 1, results[0].Summary.NotCreatedDisabled)
	assert.True(t, results[0].WriteRequested)
	assert.False(t, results[0].WriteAttempted)
	assert.False(t, results[0].WriteSucceeded)
	assert.Equal(t, "review_only", results[0].TargetAction)
	assert.Zero(t, mutations.Load())
	require.Equal(t, []signoz.AlertRuleWriteResult{{
		Alert: "NodeDown", Action: signoz.AlertRuleActionNotCreatedDisabled, Requested: true,
	}}, results[0].Writes)

	var evidence reporttypes.RuleReport
	decodeFile(t, results[0].ReportPath, &evidence)
	require.Len(t, evidence.Groups, 1)
	require.NotNil(t, evidence.PrimaryArtifact)
	assert.NotEmpty(t, evidence.Source.Identity)
	assert.Len(t, evidence.Source.SHA256, 64)
	assert.Equal(t, signoz.AlertRuleActionNotCreatedDisabled, evidence.Groups[0].Rules[0].Write.Action)
	assert.True(t, evidence.Groups[0].Rules[0].Write.Requested)
	assert.False(t, evidence.Groups[0].Rules[0].Write.Attempted)
	assert.False(t, evidence.Groups[0].Rules[0].Write.Succeeded)
	assert.Contains(t, evidence.Groups[0].Rules[1].ReasonCodes, "RECORDING_RULE_DEFINITION")
	require.NoError(t, ValidateStoredRuleArtifact(results[0].ReportPath, evidence))
}

func TestMigratePrometheusRulesPreflightsArtifactsBeforeTargetMutation(t *testing.T) {
	t.Parallel()

	var mutations atomic.Int32
	server := newRuleWriteServer(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost || request.Method == http.MethodPut {
			mutations.Add(1)
		}
		writeAppJSON(t, writer, http.StatusCreated, map[string]any{"data": map[string]any{"id": "unexpected"}})
	})
	t.Cleanup(server.Close)

	output := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(output, "rules.signoz-rules.json"), 0o755))
	results, err := MigratePrometheusRules(
		context.Background(),
		[]string{"../source/prometheus/testdata/rules.yaml"},
		RuleOptions{
			OutputDirectory: output, TargetURL: server.URL, APIKey: "test-key", HTTPClient: server.Client(),
			SourceNamespace: "prometheus:test",
		},
	)
	require.Error(t, err)
	assert.Empty(t, results)
	assert.Zero(t, mutations.Load(), "target mutation must not precede artifact destination preflight")
}

func TestMigratePrometheusRulesPreservesPartialWritesAndWriteAheadEvidence(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "two-alerts.yaml")
	source := []byte("groups:\n- name: hostile\n  rules:\n  - alert: FirstDown\n    expr: first_up == 0\n  - alert: SecondDown\n    expr: second_up == 0\n")
	require.NoError(t, os.WriteFile(sourcePath, source, 0o600))
	output := filepath.Join(directory, "out")
	reportPath := filepath.Join(output, "two-alerts.rules-report.json")
	const namespace = "prometheus:test"
	inventory := targetRuleInventoryForSource(t, sourcePath, "first-id", "second-id")
	var puts atomic.Int32
	var posts atomic.Int32
	server := newRuleWriteServerWithInventory(t, inventory, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			posts.Add(1)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		require.Equal(t, http.MethodPut, request.Method)
		put := puts.Add(1)
		var writeAhead reporttypes.RuleReport
		decodeFile(t, reportPath, &writeAhead)
		assert.Equal(t, true, writeAhead.Run.Flags["writeAttempted"])
		assert.Equal(t, "attempted", writeAhead.Run.Flags["targetAction"])
		require.NotNil(t, writeAhead.PrimaryArtifact)

		if put == 1 {
			assert.Equal(t, "attempting_update", writeAhead.Groups[0].Rules[0].Write.Action)
			assert.Equal(t, "pending_update", writeAhead.Groups[0].Rules[1].Write.Action)
			assert.Equal(t, "/api/v2/rules/first-id", request.URL.Path)
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		assert.Equal(t, "updated", writeAhead.Groups[0].Rules[0].Write.Action)
		assert.True(t, writeAhead.Groups[0].Rules[0].Write.Succeeded)
		assert.Equal(t, "attempting_update", writeAhead.Groups[0].Rules[1].Write.Action)
		assert.Equal(t, "/api/v2/rules/second-id", request.URL.Path)
		writeAppJSON(t, writer, http.StatusBadRequest, map[string]any{"error": "second failed"})
	})
	t.Cleanup(server.Close)

	results, err := MigratePrometheusRules(context.Background(), []string{sourcePath}, RuleOptions{
		OutputDirectory: output, TargetURL: server.URL, APIKey: "test-key", HTTPClient: server.Client(),
		SourceNamespace: namespace,
	})
	require.Error(t, err)
	assert.Equal(t, ErrorTarget, KindOf(err))
	require.Len(t, results, 1)
	result := results[0]
	assert.True(t, result.WriteRequested)
	assert.True(t, result.WriteAttempted)
	assert.False(t, result.WriteSucceeded)
	assert.Equal(t, "partial", result.TargetAction)
	require.Len(t, result.Writes, 2)
	assert.Equal(t, "first-id", result.Writes[0].ID)
	assert.True(t, result.Writes[0].Succeeded)
	assert.Equal(t, "failed", result.Writes[1].Action)
	assert.True(t, result.Writes[1].Attempted)
	assert.Zero(t, posts.Load())

	var evidence reporttypes.RuleReport
	decodeFile(t, result.ReportPath, &evidence)
	assert.Equal(t, fmt.Sprintf("%x", sha256.Sum256(source)), evidence.Source.SHA256)
	assert.Equal(t, true, evidence.Run.Flags["writeAttempted"])
	assert.Equal(t, false, evidence.Run.Flags["writeSucceeded"])
	assert.Equal(t, "partial", evidence.Run.Flags["targetAction"])
	assert.True(t, evidence.Groups[0].Rules[0].Write.Succeeded)
	assert.Equal(t, "first-id", evidence.Groups[0].Rules[0].Write.ID)
	assert.Equal(t, "failed", evidence.Groups[0].Rules[1].Write.Action)
	assert.NotEmpty(t, evidence.Groups[0].Rules[1].Write.Error)
	require.NoError(t, ValidateStoredRuleArtifact(result.ReportPath, evidence))
	html, readErr := os.ReadFile(result.HTMLPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(html), "Target write outcome")
	assert.Contains(t, string(html), `class="badge review">failed`)

	require.NoError(t, os.WriteFile(result.RulesPath, []byte("[]\n"), 0o600))
	err = ValidateStoredRuleArtifact(result.ReportPath, evidence)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match migration evidence")
}

func TestMigratePrometheusRulesAttemptedCheckpointFailurePreventsMutation(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "checkpoint-failure.yaml")
	require.NoError(t, os.WriteFile(sourcePath, []byte(`groups:
- name: availability
  rules:
  - alert: ExistingDown
    expr: up == 0
`), 0o600))
	const namespace = "prometheus:test"
	inventory := targetRuleInventoryForSource(t, sourcePath, "existing-id")
	var mutations atomic.Int32
	server := newRuleWriteServerWithInventory(t, inventory, func(writer http.ResponseWriter, _ *http.Request) {
		mutations.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	})
	t.Cleanup(server.Close)

	var phases []string
	results, err := MigratePrometheusRules(context.Background(), []string{sourcePath}, RuleOptions{
		OutputDirectory: filepath.Join(directory, "out"), TargetURL: server.URL,
		APIKey: "test-key", HTTPClient: server.Client(), SourceNamespace: namespace,
		ArtifactCheckpoint: func(result RuleResult) error {
			phases = append(phases, result.TargetAction)
			if result.TargetAction == "attempted" {
				return fmt.Errorf("external checkpoint unavailable")
			}
			return nil
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "external checkpoint unavailable")
	assert.Equal(t, []string{"ready", "planned", "attempted"}, phases)
	assert.Zero(t, mutations.Load(), "a failed attempted checkpoint must prevent the PUT")
	require.Len(t, results, 1)
	result := results[0]
	assert.False(t, result.WriteAttempted)
	assert.False(t, result.WriteSucceeded)
	assert.Equal(t, "failed", result.TargetAction)
	require.Len(t, result.Writes, 1)
	assert.Equal(t, signoz.AlertRuleActionNotAttempted, result.Writes[0].Action)
	assert.False(t, result.Writes[0].Attempted)

	var evidence reporttypes.RuleReport
	decodeFile(t, result.ReportPath, &evidence)
	assert.Equal(t, false, evidence.Run.Flags["writeAttempted"])
	assert.Equal(t, "failed", evidence.Run.Flags["targetAction"])
	assert.Equal(t, signoz.AlertRuleActionNotAttempted, evidence.Groups[0].Rules[0].Write.Action)
}

func TestMigratePrometheusRulesRecordsMixedSafeUpdateAndReviewOnlySkip(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "mixed.yaml")
	require.NoError(t, os.WriteFile(sourcePath, []byte(`groups:
- name: availability
  rules:
  - alert: ExistingDown
    expr: existing_up == 0
  - alert: MissingDown
    expr: missing_up == 0
`), 0o600))
	const namespace = "prometheus:test"
	inventory := targetRuleInventoryForSource(t, sourcePath, "existing-id", "")
	var posts atomic.Int32
	var puts atomic.Int32
	server := newRuleWriteServerWithInventory(t, inventory[:1], func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodPost:
			posts.Add(1)
			writer.WriteHeader(http.StatusInternalServerError)
		case http.MethodPut:
			puts.Add(1)
			assert.Equal(t, "/api/v2/rules/existing-id", request.URL.Path)
			writer.WriteHeader(http.StatusNoContent)
		default:
			writer.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	t.Cleanup(server.Close)

	results, err := MigratePrometheusRules(context.Background(), []string{sourcePath}, RuleOptions{
		OutputDirectory: filepath.Join(directory, "out"), TargetURL: server.URL,
		APIKey: "test-key", HTTPClient: server.Client(), SourceNamespace: namespace,
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	result := results[0]
	assert.Equal(t, "partial_review", result.TargetAction)
	assert.True(t, result.WriteRequested)
	assert.True(t, result.WriteAttempted)
	assert.False(t, result.WriteSucceeded)
	assert.Equal(t, 1, result.Summary.Updated)
	assert.Equal(t, 1, result.Summary.NotCreatedDisabled)
	require.Len(t, result.Writes, 2)
	assert.Equal(t, "updated", result.Writes[0].Action)
	assert.True(t, result.Writes[0].Succeeded)
	assert.Equal(t, signoz.AlertRuleActionNotCreatedDisabled, result.Writes[1].Action)
	assert.False(t, result.Writes[1].Attempted)
	assert.Zero(t, posts.Load())
	assert.Equal(t, int32(1), puts.Load())

	var evidence reporttypes.RuleReport
	decodeFile(t, result.ReportPath, &evidence)
	assert.Equal(t, "partial_review", evidence.Run.Flags["targetAction"])
	assert.Equal(t, "updated", evidence.Groups[0].Rules[0].Write.Action)
	assert.Equal(t, signoz.AlertRuleActionNotCreatedDisabled, evidence.Groups[0].Rules[1].Write.Action)
}

func newRuleWriteServer(
	t *testing.T,
	mutate func(http.ResponseWriter, *http.Request),
) *httptest.Server {
	t.Helper()
	return newRuleWriteServerWithInventory(t, []any{}, mutate)
}

func newRuleWriteServerWithInventory(
	t *testing.T,
	inventory []any,
	mutate func(http.ResponseWriter, *http.Request),
) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/metrics/metadata":
			writeAppJSON(t, writer, http.StatusOK, map[string]any{"data": map[string]any{
				"type": "gauge", "temporality": "unspecified", "isMonotonic": false,
			}})
		case "/api/v2/metrics/attributes":
			writeAppJSON(t, writer, http.StatusOK, map[string]any{"data": map[string]any{"attributes": []any{}}})
		case "/api/v2/rules":
			if request.Method == http.MethodGet {
				writeAppJSON(t, writer, http.StatusOK, map[string]any{"data": inventory})
				return
			}
			mutate(writer, request)
		default:
			if strings.HasPrefix(request.URL.Path, "/api/v2/rules/") {
				mutate(writer, request)
				return
			}
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
}

func targetRuleInventoryForSource(t *testing.T, path string, ids ...string) []any {
	t.Helper()
	set, err := sourceprometheus.ParseFile(path)
	require.NoError(t, err)
	set.Source.Namespace = "prometheus:test"
	payloads := rulePayloads(internalrules.Translate(set, nil))
	require.Len(t, payloads, len(ids))
	inventory := make([]any, 0, len(ids))
	for index, id := range ids {
		inventory = append(inventory, map[string]any{
			"id": id, "alert": payloads[index].Alert, "labels": payloads[index].Labels,
		})
	}
	return inventory
}

func TestMigratePrometheusRulesRejectsInvalidBatchBeforeAnySideEffect(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	invalid := filepath.Join(directory, "invalid.yaml")
	require.NoError(t, os.WriteFile(invalid, []byte(`groups:
- name: invalid
  rules:
  - alert: Both
    record: both_metric
    expr: up
`), 0o600))
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	output := filepath.Join(directory, "out")
	results, err := MigratePrometheusRules(context.Background(), []string{
		invalid, "../source/prometheus/testdata/rules.yaml",
	}, RuleOptions{
		OutputDirectory: output,
		TargetURL:       server.URL,
		APIKey:          "test-key",
		HTTPClient:      server.Client(),
		SourceNamespace: "prometheus:test",
	})

	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "only one of 'record' and 'alert' must be set")
	assert.Empty(t, results)
	assert.Zero(t, requests.Load(), "input preflight must precede target metadata access")
	assert.NoDirExists(t, output, "input preflight must precede output-directory creation")
}

func TestMigratePrometheusRulesRequiresNamespaceForLiveTargetBeforeAnySideEffect(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	output := filepath.Join(t.TempDir(), "not-created")

	for _, dryRun := range []bool{false, true} {
		results, err := MigratePrometheusRules(
			context.Background(),
			[]string{"../source/prometheus/testdata/rules.yaml"},
			RuleOptions{
				OutputDirectory: output,
				TargetURL:       server.URL,
				APIKey:          "test-key",
				HTTPClient:      server.Client(),
				DryRun:          dryRun,
			},
		)
		require.Error(t, err)
		assert.Equal(t, ErrorInput, KindOf(err))
		assert.Contains(t, err.Error(), "source namespace is required")
		assert.Empty(t, results)
		assert.Zero(t, requests.Load())
		assert.NoDirExists(t, output)
	}
}

func TestMigratePrometheusRulesRejectsOwnedLabelsAndNoncanonicalSourceIDBeforeSideEffects(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		labels string
		want   string
	}{
		{name: "alert name", labels: "      prometheus_alertname: source\n", want: "prometheus_alertname"},
		{name: "rule group", labels: "      prometheus_rule_group: source\n", want: "prometheus_rule_group"},
		{name: "migration id", labels: "      promcast_id: source\n", want: "promcast_id"},
		{name: "target threshold name", labels: "      threshold.name: source\n", want: "threshold.name"},
		{name: "target rule id", labels: "      ruleId: source\n", want: "ruleId"},
		{name: "target rule source", labels: "      ruleSource: source\n", want: "ruleSource"},
		{name: "target nodata", labels: "      nodata: source\n", want: "nodata"},
		{name: "target alertname", labels: "      alertname: source\n", want: "alertname"},
		{name: "target UTF8 incompatibility", labels: "      地域: 東京\n", want: "pinned SigNoz v0.133 rejects"},
		{
			name:   "configured remap collision",
			labels: "      service.name: target\n",
			want:   "may retain source label \"job\"",
		},
		{
			name: "configured source and target collision",
			labels: "      job: source\n" +
				"      service.name: target\n",
			want: "would collide",
		},
		{
			name: "conditional severity preservation",
			labels: "      severity: WARN\n" +
				"      prometheus_severity: source\n",
			want: "prometheus_severity",
		},
		{
			name:   "noncanonical source id",
			labels: "      promcast_source_id: ' node-primary '\n",
			want:   "surrounding whitespace",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			sourcePath := filepath.Join(directory, "hostile.yaml")
			source := "groups:\n- name: hostile\n  rules:\n  - alert: Collision\n    expr: up == 0\n    labels:\n" + test.labels
			require.NoError(t, os.WriteFile(sourcePath, []byte(source), 0o600))
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				writer.WriteHeader(http.StatusInternalServerError)
			}))
			t.Cleanup(server.Close)
			output := filepath.Join(directory, "out")

			results, err := MigratePrometheusRules(context.Background(), []string{sourcePath}, RuleOptions{
				OutputDirectory: output,
				TargetURL:       server.URL,
				APIKey:          "test-key",
				HTTPClient:      server.Client(),
				SourceNamespace: "prometheus:test",
			})
			require.Error(t, err)
			assert.Equal(t, ErrorInput, KindOf(err))
			assert.Contains(t, err.Error(), test.want)
			assert.Empty(t, results)
			assert.Zero(t, requests.Load())
			assert.NoDirExists(t, output)
		})
	}
}

func TestMigratePrometheusRulesRejectsAmbiguousNamespacedIdentitiesBeforeTargetAccess(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "duplicates.yaml")
	require.NoError(t, os.WriteFile(sourcePath, []byte(`groups:
- name: disk
  rules:
  - alert: DiskFull
    expr: disk_free < 10
  - alert: DiskFull
    expr: disk_free < 5
`), 0o600))
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	output := filepath.Join(directory, "out")

	results, err := MigratePrometheusRules(context.Background(), []string{sourcePath}, RuleOptions{
		OutputDirectory: output,
		TargetURL:       server.URL,
		APIKey:          "test-key",
		HTTPClient:      server.Client(),
		SourceNamespace: "prometheus:production",
	})

	require.Error(t, err)
	assert.Equal(t, ErrorInput, KindOf(err))
	assert.Contains(t, err.Error(), "promcast_source_id")
	assert.Empty(t, results)
	assert.Zero(t, requests.Load(), "identity preflight must run before target metadata or writes")
	assert.NoDirExists(t, output, "identity preflight must run before output-directory creation")
}

func TestMigratePrometheusRulesCanEnableAlertOnAbsent(t *testing.T) {
	t.Parallel()

	results, err := MigratePrometheusRules(context.Background(), []string{"../source/prometheus/testdata/rules.yaml"}, RuleOptions{
		OutputDirectory: t.TempDir(), AlertOnAbsent: true,
	})
	require.NoError(t, err)
	var payloads []map[string]any
	decodeFile(t, results[0].RulesPath, &payloads)
	require.NotEmpty(t, payloads)
	condition := payloads[0]["condition"].(map[string]any)
	assert.Equal(t, true, condition["alertOnAbsent"])
}

func writeAppJSON(t *testing.T, writer http.ResponseWriter, status int, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	require.NoError(t, json.NewEncoder(writer).Encode(value))
}
