package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mansiverma897993/signoz/internal/app"
	"github.com/mansiverma897993/signoz/internal/target/signoz"
	"github.com/mansiverma897993/signoz/pkg/reporttypes"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrationResponseExposesEveryReviewScopeWithStableSelector(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		evidence reporttypes.Report
		want     reviewItem
	}{
		{
			name: "dashboard source feature",
			evidence: reporttypes.Report{SourceFeatures: []reporttypes.SourceFeatureRecord{{
				Kind: "dashboard_property", SourcePath: "/time", Verdict: "needs_review",
				ReasonCode: "UNMAPPED_DASHBOARD_CONFIG",
			}}},
			want: reviewItem{
				Scope: "dashboard_source_feature", Kind: "dashboard", Panel: "Dashboard", SourcePath: "/time",
				ReasonCodes: []string{"UNMAPPED_DASHBOARD_CONFIG"},
			},
		},
		{
			name: "variable source feature",
			evidence: reporttypes.Report{Variables: []reporttypes.VariableRecord{{
				Name: "cluster", SourcePath: "/templating/list/0", Verdict: "needs_review",
				ReasonCodes: []string{"UNMAPPED_VARIABLE_CONFIG"},
				SourceFeatures: []reporttypes.SourceFeatureRecord{{
					Kind: "variable_property", SourcePath: "/templating/list/0/hide", Verdict: "needs_review",
					ReasonCode: "UNMAPPED_VARIABLE_CONFIG",
				}},
			}}},
			want: reviewItem{
				Scope: "variable_source_feature", Kind: "variable", Panel: "Variable: cluster",
				SourcePath: "/templating/list/0/hide", ReasonCodes: []string{"UNMAPPED_VARIABLE_CONFIG"},
			},
		},
		{
			name: "panel feature with native query",
			evidence: reporttypes.Report{Panels: []reporttypes.PanelRecord{{
				Title: "Latency", SourcePath: "/panels/0", Verdict: "needs_review",
				ReasonCodes: []string{"UNMAPPED_VISUALIZATION_CONFIG"},
				SourceFeatures: []reporttypes.SourceFeatureRecord{{
					Kind: "panel_property", SourcePath: "/panels/0/transparent", Verdict: "needs_review",
					ReasonCode: "UNMAPPED_VISUALIZATION_CONFIG",
				}},
				Queries: []reporttypes.QueryRecord{{
					RefID: "A", SourcePath: "/panels/0/targets/0", CandidateKind: "builder",
					EmittedKind: "builder", Verdict: "native",
				}},
			}}},
			want: reviewItem{
				Scope: "panel", Kind: "panel", Panel: "Latency", SourcePath: "/panels/0",
				ReasonCodes: []string{"UNMAPPED_VISUALIZATION_CONFIG"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			response := migrationResponse("migration-1", app.GrafanaResult{Evidence: test.evidence}, false)
			require.Equal(t, 1, response.NeedsReviewTotal)
			require.Zero(t, response.NeedsReviewTruncated)
			require.Len(t, response.NeedsReview, 1)
			item := response.NeedsReview[0]
			assert.Equal(t, test.want.Scope, item.Scope)
			assert.Equal(t, test.want.Kind, item.Kind)
			assert.Equal(t, test.want.Panel, item.Panel)
			assert.Equal(t, test.want.SourcePath, item.SourcePath)
			assert.Equal(t, test.want.ReasonCodes, item.ReasonCodes)
			assert.Equal(t, explainHint(test.want.Kind, test.want.SourcePath), item.Hint)
			assert.NotContains(t, item.Hint, test.want.Panel)
		})
	}
}

func TestMigrationResponseReviewPreviewIsBoundedAndExact(t *testing.T) {
	t.Parallel()

	features := make([]reporttypes.SourceFeatureRecord, 21)
	for index := range features {
		features[index] = reporttypes.SourceFeatureRecord{
			Kind: "dashboard_property", SourcePath: "/property/" + string(rune('a'+index)),
			Verdict: "needs_review", ReasonCode: "UNMAPPED_DASHBOARD_CONFIG",
		}
	}
	response := migrationResponse("migration-1", app.GrafanaResult{
		Evidence: reporttypes.Report{SourceFeatures: features},
	}, false)

	assert.Equal(t, 21, response.NeedsReviewTotal)
	assert.Equal(t, 1, response.NeedsReviewTruncated)
	require.Len(t, response.NeedsReview, 20)
	assert.Equal(t, "/property/a", response.NeedsReview[0].SourcePath)
	assert.Equal(t, "/property/t", response.NeedsReview[19].SourcePath)
}

func TestMigrationResponseReportsTargetOutcomeExplicitly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		result          app.GrafanaResult
		importRequested bool
		status          migrateTargetStatus
		reason          string
	}{
		{
			name: "offline import not requested",
			result: app.GrafanaResult{TargetAction: "offline", Evidence: reporttypes.Report{Run: reporttypes.Run{
				Flags: map[string]any{"offline": true},
			}}},
			status: migrateTargetNotRequested,
		},
		{
			name: "live validation dry run",
			result: app.GrafanaResult{
				TargetAction:  "dry_run",
				TargetSkipped: "dry run: target validation passed; dashboard was not imported",
				Evidence: reporttypes.Report{Run: reporttypes.Run{
					Target: "https://signoz.example", Flags: map[string]any{"offline": false},
				}},
			},
			status: migrateTargetDryRun,
			reason: "dry run: target validation passed; dashboard was not imported",
		},
		{
			name: "live validation failed before dry run completed",
			result: app.GrafanaResult{
				TargetAction:  "skipped",
				TargetSkipped: "target validation failed; dashboard was not imported",
				TargetError:   "metadata endpoint rejected credentials",
			},
			status: migrateTargetSkipped,
			reason: "target validation failed; dashboard was not imported",
		},
		{
			name: "requested import skipped",
			result: app.GrafanaResult{
				ImportRequested: true,
				TargetAction:    "skipped",
				TargetSkipped:   "no executable widgets were emitted; dashboard was not imported",
			},
			importRequested: true,
			status:          migrateTargetSkipped,
			reason:          "no executable widgets were emitted; dashboard was not imported",
		},
		{
			name:            "requested import missing outcome is fail closed",
			result:          app.GrafanaResult{ImportRequested: true, TargetAction: "skipped"},
			importRequested: true,
			status:          migrateTargetSkipped,
			reason:          "target import was requested, but no dashboard write result was returned",
		},
		{
			name: "requested import was attempted and failed",
			result: app.GrafanaResult{
				ImportRequested: true,
				ImportAttempted: true,
				TargetAction:    "failed",
				TargetSkipped:   "target import failed; dashboard outcome is unchanged or unknown",
				TargetError:     "target rejected dashboard",
			},
			importRequested: true,
			status:          migrateTargetFailed,
			reason:          "target import failed; dashboard outcome is unchanged or unknown",
		},
		{
			name: "durable write-ahead survives missing final checkpoint",
			result: app.GrafanaResult{
				ImportRequested: true,
				ImportAttempted: true,
				TargetAction:    "attempted",
				TargetSkipped:   "target request may have completed; the final outcome was not durably recorded",
			},
			importRequested: true,
			status:          migrateTargetFailed,
			reason:          "target request may have completed; the final outcome was not durably recorded",
		},
		{
			name: "dashboard imported",
			result: app.GrafanaResult{
				ImportRequested:   true,
				ImportAttempted:   true,
				ImportSucceeded:   true,
				TargetAction:      "updated",
				TargetDashboardID: "dashboard-1",
				Target: &signoz.DashboardWriteResult{
					ID: "dashboard-1", Action: "updated",
				},
			},
			importRequested: true,
			status:          migrateTargetImported,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			response := migrationResponse("migration-1", test.result, test.importRequested)
			assert.Equal(t, test.importRequested, response.ImportRequested)
			assert.Equal(t, test.status, response.TargetStatus)
			assert.Equal(t, test.reason, response.TargetSkippedReason)
			assert.Contains(t, migrationResponseText(response), "Target status: "+string(test.status)+".")

			encoded, err := json.Marshal(response)
			require.NoError(t, err)
			assert.Contains(t, string(encoded), `"import_requested":`)
			assert.Contains(t, string(encoded), `"target_status":`)
			assert.Contains(t, string(encoded), `"target_skipped_reason":`)
		})
	}
}

func TestMigrationResponseFailsClosedOnInconsistentImportSuccess(t *testing.T) {
	t.Parallel()

	response := migrationResponse("migration-1", app.GrafanaResult{
		ImportRequested: true,
		ImportAttempted: true,
		ImportSucceeded: true,
		TargetAction:    "created",
	}, true)

	assert.True(t, response.ImportRequested)
	assert.Equal(t, migrateTargetSkipped, response.TargetStatus)
	assert.Equal(t, "incoherent migration target outcome: import success was recorded without a dashboard write result", response.TargetSkippedReason)
	assert.Nil(t, response.Imported)
}

func TestMigrationResponseFailsClosedOnWriteResultWithoutSuccessfulOutcome(t *testing.T) {
	t.Parallel()

	response := migrationResponse("migration-1", app.GrafanaResult{
		ImportRequested: true,
		Target:          &signoz.DashboardWriteResult{ID: "dashboard-1", Action: "created"},
	}, true)

	assert.True(t, response.ImportRequested)
	assert.Equal(t, migrateTargetSkipped, response.TargetStatus)
	assert.Contains(t, response.TargetSkippedReason, "without requested, attempted, and succeeded import flags")
	assert.Nil(t, response.Imported)
}

func TestExplainSourcePathSupportsEveryStableSelectorKind(t *testing.T) {
	t.Parallel()

	evidence := reporttypes.Report{
		ReasonCodes: map[string]string{"REVIEW": "Review this source construct."},
		SourceFeatures: []reporttypes.SourceFeatureRecord{{
			SourcePath: "/time", Detail: `{"from":"now-1h"}`, Verdict: "needs_review", ReasonCode: "REVIEW",
		}},
		Variables: []reporttypes.VariableRecord{{
			Name: "cluster", SourcePath: "/templating/list/0", Verdict: "needs_review",
			ReasonCodes: []string{"REVIEW"},
		}},
		Panels: []reporttypes.PanelRecord{{
			Title: "Latency", SourcePath: "/panels/0", Verdict: "needs_review",
			ReasonCodes: []string{"REVIEW"},
			Queries: []reporttypes.QueryRecord{{
				RefID: "A", SourcePath: "/panels/0/targets/0", Verdict: "needs_review",
				ReasonCodes: []string{"REVIEW"},
			}},
		}},
	}
	for _, selector := range []struct {
		kind string
		path string
	}{
		{kind: "dashboard", path: "/time"},
		{kind: "variable", path: "/templating/list/0"},
		{kind: "panel", path: "/panels/0"},
		{kind: "query", path: "/panels/0/targets/0"},
	} {
		items, err := explainSourcePath(evidence, selector.kind, selector.path)
		require.NoError(t, err)
		require.Len(t, items, 1)
		assert.Equal(t, selector.kind, items[0].Kind)
		assert.Equal(t, selector.path, items[0].SourcePath)
		assert.Equal(t, []string{"REVIEW"}, items[0].ReasonCodes)
		if selector.kind == "dashboard" {
			assert.Equal(t, `{"from":"now-1h"}`, items[0].SourceDetail)
		}
	}
}

func TestExplainVerdictRejectsDuplicateTitlesAndSelectsExactSourcePaths(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	output := filepath.Join(root, "out")
	service, err := New(Config{Root: root, OutputRoot: output})
	require.NoError(t, err)
	const migrationID = "migration-duplicate-titles"
	directory, err := service.migrationDirectory(migrationID)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(directory, 0o755))

	evidence := reporttypes.Report{
		SchemaVersion: "1", Dashboard: reporttypes.DashboardInfo{Title: "Duplicate titles"},
		ReasonCodes: map[string]string{"UNMAPPED_VISUALIZATION_CONFIG": "Unmapped visualization setting."},
		Panels: []reporttypes.PanelRecord{
			{
				Title: "Latency", SourcePath: "/panels/0", Verdict: "needs_review",
				ReasonCodes: []string{"UNMAPPED_VISUALIZATION_CONFIG"},
				Queries: []reporttypes.QueryRecord{{
					RefID: "A", SourcePath: "/panels/0/targets/0", Verdict: "native", EmittedKind: "builder",
				}},
			},
			{
				Title: "Latency", SourcePath: "/panels/1", Verdict: "needs_review",
				ReasonCodes: []string{"UNMAPPED_VISUALIZATION_CONFIG"},
				Queries: []reporttypes.QueryRecord{{
					RefID: "A", SourcePath: "/panels/1/targets/0", Verdict: "needs_review", EmittedKind: "none",
					ReasonCodes: []string{"UNMAPPED_VISUALIZATION_CONFIG"},
				}},
			},
		},
	}
	require.NoError(t, writeJSONAtomic(filepath.Join(directory, "report.json"), evidence))
	require.NoError(t, writeJSONAtomic(filepath.Join(directory, "migration.json"), manifest{
		SchemaVersion: 1, MigrationID: migrationID, Source: "source.json", Report: "report.json",
		Dashboard: "dashboard.json", HTML: "report.html", RateInterval: "5m",
	}))

	legacy, err := service.handleExplainVerdict(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{
			"migration_id": migrationID, "panel": "Latency",
		}},
	})
	require.NoError(t, err)
	require.True(t, legacy.IsError)

	selected, err := service.handleExplainVerdict(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{
			"migration_id": migrationID, "kind": "query", "source_path": "/panels/1/targets/0",
		}},
	})
	require.NoError(t, err)
	require.False(t, selected.IsError)
	encoded, err := json.Marshal(selected.StructuredContent)
	require.NoError(t, err)
	var response explainResponse
	require.NoError(t, json.Unmarshal(encoded, &response))
	require.Len(t, response.Items, 1)
	assert.Equal(t, "query", response.Items[0].Kind)
	assert.Equal(t, "/panels/1/targets/0", response.Items[0].SourcePath)
	assert.Equal(t, "needs_review", response.Items[0].Verdict)
}
