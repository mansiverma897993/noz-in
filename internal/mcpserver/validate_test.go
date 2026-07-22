package mcpserver

import (
	"fmt"
	"testing"
	"time"

	"github.com/mansiverma897993/noz-in/internal/target/signoz"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoredVariableValuesRequiresCompletePinnedDynamicAllState(t *testing.T) {
	t.Parallel()

	dashboard := signoz.DashboardV5{Variables: map[string]signoz.VariableV5{
		"complete": {
			Name: "complete", Type: "DYNAMIC", AllSelected: true,
			ShowAllOption: true, MultiSelect: true, SelectedValue: "ignored",
		},
		"show-all-disabled": {
			Name: "show-all-disabled", Type: "DYNAMIC", AllSelected: true,
			ShowAllOption: false, MultiSelect: true, SelectedValue: "api",
		},
		"multi-disabled": {
			Name: "multi-disabled", Type: "DYNAMIC", AllSelected: true,
			ShowAllOption: true, MultiSelect: false, SelectedValue: "worker",
		},
		"custom": {
			Name: "custom", Type: "CUSTOM", AllSelected: true,
			ShowAllOption: true, MultiSelect: true, SelectedValue: []string{"prod", "stage"},
		},
	}}

	values, err := storedVariableValues(dashboard)
	require.Error(t, err)
	assert.Nil(t, values)

	dashboard.Variables["custom"] = signoz.VariableV5{
		Name: "custom", Type: "CUSTOM", CustomValue: "prod,stage",
		MultiSelect: true, SelectedValue: []string{"prod", "stage"},
	}
	values, err = storedVariableValues(dashboard)
	require.NoError(t, err)
	assert.Equal(t, "__all__", values["complete"])
	assert.Equal(t, "api", values["show-all-disabled"])
	assert.Equal(t, "worker", values["multi-disabled"])
	assert.Equal(t, []string{"prod", "stage"}, values["custom"])
}

func TestStoredVariableValuesFailsClosedForCustomReloadMismatch(t *testing.T) {
	t.Parallel()

	for name, variable := range map[string]signoz.VariableV5{
		"unsafe-custom-value": {
			Name: "environment", Type: "CUSTOM", CustomValue: "001",
			SelectedValue: "001",
		},
		"selection-mismatch": {
			Name: "environment", Type: "CUSTOM", CustomValue: "prod",
			SelectedValue: "stage",
		},
		"multi-shape-mismatch": {
			Name: "environment", Type: "CUSTOM", CustomValue: "prod",
			MultiSelect: true, SelectedValue: "prod",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			values, err := storedVariableValues(signoz.DashboardV5{
				Variables: map[string]signoz.VariableV5{"environment": variable},
			})
			require.Error(t, err)
			assert.Nil(t, values)
		})
	}
}

func TestBuildValidationResponseCountsOnlyEligibleQueriesAndRequiresExecution(t *testing.T) {
	t.Parallel()

	previous := reporttypes.Report{Panels: []reporttypes.PanelRecord{{Queries: []reporttypes.QueryRecord{{
		SourcePath: "/panels/0/targets/1",
		Validation: reporttypes.Validation{Previewed: true, PreviewOK: true, Executed: true, DataPresent: true},
	}}}}}
	current := reporttypes.Report{Panels: []reporttypes.PanelRecord{{
		Title: "Host health", PrimaryArtifact: true,
		Queries: []reporttypes.QueryRecord{
			{
				RefID: "A", SourcePath: "/panels/0/targets/0", EmittedKind: "promql",
				Validation: reporttypes.Validation{
					Previewed: true, PreviewOK: true, MetricChecked: true, MetricFound: true,
					Executed: true, DataPresent: true, CheckedAt: "2026-07-20T00:00:00Z",
				},
			},
			{
				RefID: "B", SourcePath: "/panels/0/targets/1", EmittedKind: "promql",
				Validation: reporttypes.Validation{Previewed: true, PreviewOK: true},
			},
			{RefID: "C", SourcePath: "/panels/0/targets/2", EmittedKind: "promql", Disabled: true},
			{RefID: "D", SourcePath: "/panels/0/targets/3", EmittedKind: "none"},
			{
				RefID: "E", SourcePath: "/panels/0/targets/4", EmittedKind: "builder",
				Validation: reporttypes.Validation{Previewed: true, PreviewOK: true, Executed: true},
			},
			{
				RefID: "F", SourcePath: "/panels/0/targets/5", EmittedKind: "formula",
				Validation: reporttypes.Validation{PreviewOK: true, Executed: true, DataPresent: true},
			},
		},
	}}}

	response, err := buildValidationResponse("migration-1", 30*time.Minute, previous, current, "")
	require.NoError(t, err)

	assert.Equal(t, 6, response.Totals.SourceQueries)
	assert.Equal(t, 4, response.Totals.EligibleQueries)
	assert.Equal(t, 2, response.Totals.SkippedQueries)
	assert.Equal(t, 3, response.Totals.PreviewOK)
	assert.Equal(t, 1, response.Totals.MetricExists)
	assert.Equal(t, 2, response.Totals.DataReturned)
	assert.Equal(t, 1, response.Totals.DataAbsent)
	assert.Equal(t, 1, response.Delta.NewDataPresent)
	assert.Equal(t, 1, response.Delta.DataNoLongerPresent)
	require.Len(t, response.Failures, 2)
	assert.Equal(t, 2, response.FailuresTotal)
	assert.False(t, response.FailuresTruncated)
	assert.Equal(t, "QUERY_NOT_EXECUTED", response.Failures[0].ErrorCode)
	assert.Equal(t, "PREVIEW_NOT_VALID", response.Failures[1].ErrorCode)
	require.Len(t, response.NoData, 1)
	assert.Equal(t, 1, response.NoDataTotal)
	assert.False(t, response.NoDataTruncated)
	assert.Equal(t, "NO_DATA_RETURNED", response.NoData[0].ErrorCode)
}

func TestValidationFailureDetailPreservesTargetError(t *testing.T) {
	t.Parallel()

	code, message := validationFailureDetail(reporttypes.Validation{
		ErrorCode: "DEPENDENCY_REJECTED", Error: "dependency B was invalid",
	}, false, true)
	assert.Equal(t, "DEPENDENCY_REJECTED", code)
	assert.Equal(t, "dependency B was invalid", message)
}

func TestBuildValidationResponseDoesNotClassifyTargetErrorAsNoData(t *testing.T) {
	t.Parallel()

	current := reporttypes.Report{Panels: []reporttypes.PanelRecord{{
		Title: "Error", SourcePath: "/panels/0", PrimaryArtifact: true,
		Queries: []reporttypes.QueryRecord{{
			RefID: "A", SourcePath: "/panels/0/targets/0", EmittedKind: "builder",
			Validation: reporttypes.Validation{
				Previewed: true, PreviewOK: true, Executed: true, Error: "query backend timed out",
			},
		}},
	}}}

	response, err := buildValidationResponse("migration-1", 30*time.Minute, reporttypes.Report{}, current, "")
	require.NoError(t, err)
	assert.Equal(t, 1, response.FailuresTotal)
	assert.Zero(t, response.NoDataTotal)
	require.Len(t, response.Failures, 1)
	assert.Equal(t, "query backend timed out", response.Failures[0].Error)
}

func TestBuildValidationResponseSelectsDuplicateTitlePanelByExactSourcePath(t *testing.T) {
	t.Parallel()

	current := reporttypes.Report{Panels: []reporttypes.PanelRecord{
		{
			Title: "Latency", SourcePath: "/panels/0", PrimaryArtifact: true,
			Queries: []reporttypes.QueryRecord{{
				RefID: "A", SourcePath: "/panels/0/targets/0", EmittedKind: "builder",
				Validation: reporttypes.Validation{
					Previewed: true, PreviewOK: true, Executed: true, DataPresent: true,
				},
			}},
		},
		{
			Title: "Latency", SourcePath: "/panels/1", PrimaryArtifact: true,
			Queries: []reporttypes.QueryRecord{{
				RefID: "B", SourcePath: "/panels/1/targets/0", EmittedKind: "builder",
				Validation: reporttypes.Validation{Previewed: true, PreviewOK: false},
			}},
		},
	}}

	response, err := buildValidationResponse("migration-1", 30*time.Minute, reporttypes.Report{}, current, "1")
	require.NoError(t, err)
	assert.Equal(t, 1, response.Totals.SourceQueries)
	assert.Equal(t, 1, response.Totals.EligibleQueries)
	assert.Equal(t, 1, response.FailuresTotal)
	require.Len(t, response.Failures, 1)
	assert.Equal(t, "B", response.Failures[0].Query)

	_, err = buildValidationResponse("migration-1", 30*time.Minute, reporttypes.Report{}, current, "Latency")
	require.ErrorContains(t, err, "ambiguous")
}

func TestBuildValidationResponseBoundsFailureAndNoDataPreviews(t *testing.T) {
	t.Parallel()

	queries := make([]reporttypes.QueryRecord, 0, 42)
	for index := range 21 {
		queries = append(queries, reporttypes.QueryRecord{
			RefID: fmt.Sprintf("F%02d", index), SourcePath: fmt.Sprintf("/panels/0/targets/%d", index),
			EmittedKind: "builder",
			Validation:  reporttypes.Validation{Previewed: true, PreviewOK: false},
		})
	}
	for index := range 21 {
		queries = append(queries, reporttypes.QueryRecord{
			RefID: fmt.Sprintf("U%02d", index), SourcePath: fmt.Sprintf("/panels/0/targets/%d", index+21),
			EmittedKind: "builder",
			Validation: reporttypes.Validation{
				Previewed: true, PreviewOK: true, Executed: true, DataPresent: false,
			},
		})
	}
	current := reporttypes.Report{Panels: []reporttypes.PanelRecord{{
		Title: "Bounded", SourcePath: "/panels/0", PrimaryArtifact: true, Queries: queries,
	}}}

	response, err := buildValidationResponse("migration-1", 30*time.Minute, reporttypes.Report{}, current, "")
	require.NoError(t, err)
	assert.Equal(t, 21, response.FailuresTotal)
	assert.True(t, response.FailuresTruncated)
	require.Len(t, response.Failures, validationResultPreviewLimit)
	assert.Equal(t, "F00", response.Failures[0].Query)
	assert.Equal(t, "F19", response.Failures[validationResultPreviewLimit-1].Query)
	assert.Equal(t, 21, response.NoDataTotal)
	assert.True(t, response.NoDataTruncated)
	require.Len(t, response.NoData, validationResultPreviewLimit)
	assert.Equal(t, "U00", response.NoData[0].Query)
	assert.Equal(t, "U19", response.NoData[validationResultPreviewLimit-1].Query)
}
