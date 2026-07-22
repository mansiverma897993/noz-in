package mcpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mansiverma897993/noz-in/internal/app"
	"github.com/mansiverma897993/noz-in/internal/artifactset"
	"github.com/mansiverma897993/noz-in/internal/model"
	reportpkg "github.com/mansiverma897993/noz-in/internal/report"
	"github.com/mansiverma897993/noz-in/internal/target/signoz"
	"github.com/mansiverma897993/noz-in/internal/transpile"
	"github.com/mansiverma897993/noz-in/internal/validate"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
	"github.com/mark3labs/mcp-go/mcp"
)

const validationResultPreviewLimit = 20

type validationTotals struct {
	SourceQueries   int `json:"source_queries"`
	EligibleQueries int `json:"eligible_queries"`
	SkippedQueries  int `json:"skipped_queries"`
	PreviewOK       int `json:"preview_ok"`
	MetricExists    int `json:"metric_exists"`
	DataReturned    int `json:"data_returned"`
	DataAbsent      int `json:"data_absent"`
}

type validationDelta struct {
	NewDataPresent      int `json:"new_data_present"`
	DataNoLongerPresent int `json:"data_no_longer_present"`
}

type validationChecks struct {
	PreviewOK    bool `json:"preview_ok"`
	MetricExists bool `json:"metric_exists"`
	DataReturned bool `json:"data_returned"`
}

type validationFailure struct {
	Panel       string           `json:"panel"`
	Query       string           `json:"query"`
	Checks      validationChecks `json:"checks"`
	ReasonCodes []string         `json:"reason_codes,omitempty"`
	ErrorCode   string           `json:"error_code,omitempty"`
	Error       string           `json:"error,omitempty"`
}

type validateResponse struct {
	MigrationID       string              `json:"migration_id"`
	CheckedAt         string              `json:"checked_at"`
	Window            string              `json:"window"`
	Totals            validationTotals    `json:"totals"`
	Delta             validationDelta     `json:"delta_since_migration"`
	Failures          []validationFailure `json:"failures"`
	FailuresTotal     int                 `json:"failures_total"`
	FailuresTruncated bool                `json:"failures_truncated"`
	NoData            []validationFailure `json:"no_data"`
	NoDataTotal       int                 `json:"no_data_total"`
	NoDataTruncated   bool                `json:"no_data_truncated"`
	Artifacts         validationArtifacts `json:"artifacts"`
}

type validationArtifacts struct {
	ReportJSON  string `json:"report_json"`
	ReportHTML  string `json:"report_html"`
	DashboardV5 string `json:"dashboard_v5"`
}

type validationRun struct {
	migrationID   string
	window        time.Duration
	panelSelector string
	previous      reporttypes.Report
	current       reporttypes.Report
	dashboardData []byte
}

type validationRunError struct {
	code string
	err  error
}

func (service *Service) prepareValidationRun(
	ctx context.Context,
	request mcp.CallToolRequest,
) (validationRun, *validationRunError) {
	if strings.TrimSpace(service.config.TargetURL) == "" || strings.TrimSpace(service.config.APIKey) == "" {
		return validationRun{}, &validationRunError{
			code: "TARGET_REQUIRED",
			err:  fmt.Errorf("validate_queries requires SIGNOZ_URL and SIGNOZ_API_KEY on the MCP server"),
		}
	}
	id, err := request.RequireString("migration_id")
	if err != nil {
		return validationRun{}, &validationRunError{code: "INVALID_INPUT", err: err}
	}
	window, err := time.ParseDuration(request.GetString("window", "30m"))
	if err != nil || window <= 0 {
		return validationRun{}, &validationRunError{
			code: "INVALID_INPUT",
			err:  fmt.Errorf("window must be a positive Go duration such as 30m or 6h"),
		}
	}
	directory, err := service.migrationDirectory(id)
	if err != nil {
		return validationRun{}, &validationRunError{code: "INVALID_INPUT", err: err}
	}
	if err := service.ensureMigrationDirectoryStable(id); err != nil {
		return validationRun{}, &validationRunError{code: "MIGRATION_NOT_FOUND", err: err}
	}
	state, err := service.readManifest(id)
	if err != nil {
		return validationRun{}, &validationRunError{code: "MIGRATION_NOT_FOUND", err: err}
	}
	stored, err := service.readDashboardReport(id, state, state.Dashboard)
	if err != nil {
		return validationRun{}, &validationRunError{code: "ARTIFACT_PROVENANCE_INVALID", err: err}
	}
	previous := stored.Evidence
	panelSelector := request.GetString("panel", "")
	selectedPath, selectionEnabled, err := selectedValidationPath(previous.Panels, panelSelector)
	if err != nil {
		return validationRun{}, &validationRunError{code: "PANEL_NOT_FOUND", err: err}
	}
	dashboardPath := filepath.Join(directory, state.Dashboard)
	if state.Generation != "" {
		dashboardPath = filepath.Join(directory, state.Generation, state.Dashboard)
	}
	dashboardData, found := stored.Members[state.Dashboard]
	if !found {
		return validationRun{}, &validationRunError{
			code: "ARTIFACT_PROVENANCE_INVALID",
			err:  fmt.Errorf("dashboard %q was not read from the selected migration generation", state.Dashboard),
		}
	}
	if _, err := decodeStoredDashboard(dashboardPath, dashboardData); err != nil {
		return validationRun{}, &validationRunError{code: "DASHBOARD_READ_FAILED", err: err}
	}
	dashboard, err := app.ValidateStoredDashboardArtifact(dashboardPath, dashboardData, previous)
	if err != nil {
		return validationRun{}, &validationRunError{code: "ARTIFACT_PROVENANCE_INVALID", err: err}
	}
	targetDashboard := dashboard
	if selectionEnabled {
		targetDashboard = validationDashboardSubset(dashboard, selectedPath)
	}
	current, err := cloneDashboardReport(previous)
	if err != nil {
		return validationRun{}, &validationRunError{code: "REPORT_READ_FAILED", err: err}
	}
	presentPanels := make(map[string]bool, len(targetDashboard.Widgets))
	for _, widget := range targetDashboard.Widgets {
		presentPanels[widget.SourcePath] = true
	}
	validationEvidence := validationSubset(current, presentPanels)
	clearLiveValidation(&validationEvidence)
	variableValues, err := storedVariableValues(dashboard)
	if err != nil {
		return validationRun{}, &validationRunError{code: "ARTIFACT_PROVENANCE_INVALID", err: err}
	}
	client, err := signoz.NewClientWithOptions(
		service.config.TargetURL,
		service.config.APIKey,
		service.config.HTTPClient,
		signoz.ClientOptions{AllowInsecureHTTP: service.config.AllowInsecureHTTP},
	)
	if err != nil {
		return validationRun{}, &validationRunError{code: "TARGET_REQUIRED", err: err}
	}
	now := time.Now().UTC()
	if service.config.Now != nil {
		now = service.config.Now().UTC()
	}
	if err := refreshLiveMetricMetadata(ctx, client, &validationEvidence, now, window); err != nil {
		return validationRun{}, &validationRunError{
			code: "VALIDATION_FAILED",
			err:  fmt.Errorf("refresh live metric metadata: %w", err),
		}
	}
	_, err = validate.Dashboard(
		ctx,
		client,
		targetDashboard,
		&validationEvidence,
		variableValues,
		validate.Options{Workers: service.config.Workers, Window: window, Now: func() time.Time { return now }},
	)
	if err != nil {
		return validationRun{}, &validationRunError{code: "VALIDATION_FAILED", err: err}
	}
	mergeValidatedPanels(&current, validationEvidence)
	current.Summary = validationEvidence.Summary
	refreshLiveValidationSummary(&current)
	current.Run.StartedAt = now.Format(time.RFC3339Nano)
	current.Run.Flags = cloneFlags(current.Run.Flags)
	current.Run.Flags["revalidation"] = true
	current.Run.Flags["validationWindow"] = window.String()
	current.Run.Flags["validatedDashboardArtifact"] = state.Dashboard
	if selectionEnabled {
		current.Run.Flags["validationPanel"] = selectedPath
	} else {
		delete(current.Run.Flags, "validationPanel")
	}
	reportpkg.RefreshSummary(&current)
	return validationRun{
		migrationID:   id,
		window:        window,
		panelSelector: panelSelector,
		previous:      previous,
		current:       current,
		dashboardData: dashboardData,
	}, nil
}

func (service *Service) handleValidateQueries(
	ctx context.Context,
	request mcp.CallToolRequest,
) (*mcp.CallToolResult, error) {
	release, err := service.acquireTool(ctx)
	if err != nil {
		return toolError("RESOURCE_BUSY", err), nil
	}
	defer release()
	run, runErr := service.prepareValidationRun(ctx, request)
	if runErr != nil {
		return toolError(runErr.code, runErr.err), nil
	}

	operation, validationTarget, validationRelative, err := service.beginValidationWork(run.migrationID)
	if err != nil {
		return toolError("ARTIFACT_WRITE_FAILED", err), nil
	}
	workCleaned := false
	defer func() {
		if !workCleaned {
			_ = operation.cleanup()
		}
	}()
	service.runCrashBarrier("validation-work-created")
	stagingDirectory, err := service.createPrivateStagingDirectory(operation.token, operation.plan.StagingParent)
	if err != nil {
		return toolError("ARTIFACT_WRITE_FAILED", err), nil
	}
	stagingCleaned := false
	defer func() {
		if !stagingCleaned {
			_ = service.cleanupPrivateStagingDirectory(operation.token, operation.plan.StagingParent)
		}
	}()
	staged, err := stageValidationArtifactSet(stagingDirectory, &run.current, run.dashboardData)
	if err != nil {
		return toolError("ARTIFACT_WRITE_FAILED", err), nil
	}
	service.runCrashBarrier("validation-private-staging-ready")
	validationDirectory, err := service.publishValidationWork(
		operation,
		stagingDirectory,
		run.current.ArtifactSet,
		validationTarget,
		validationRelative,
	)
	if err != nil {
		return toolError("ARTIFACT_WRITE_FAILED", err), nil
	}
	validationDashboardPath := filepath.Join(validationDirectory, filepath.Base(staged.dashboard))
	validationReportPath := filepath.Join(validationDirectory, filepath.Base(staged.report))
	validationHTMLPath := filepath.Join(validationDirectory, filepath.Base(staged.html))
	response, err := buildValidationResponse(
		run.migrationID,
		run.window,
		run.previous,
		run.current,
		run.panelSelector,
	)
	if err != nil {
		return toolError("PANEL_NOT_FOUND", err), nil
	}
	response.Artifacts = validationArtifacts{
		ReportJSON: validationReportPath, ReportHTML: validationHTMLPath, DashboardV5: validationDashboardPath,
	}
	if err := service.cleanupPrivateStagingDirectory(operation.token, operation.plan.StagingParent); err != nil {
		return toolError("ARTIFACT_WRITE_FAILED", err), nil
	}
	stagingCleaned = true
	if err := operation.cleanup(); err != nil {
		return toolError("ARTIFACT_WRITE_FAILED", err), nil
	}
	workCleaned = true
	fallback := fmt.Sprintf(
		"Validated %d eligible queries (%d skipped): %d previewed successfully, %d returned data, %d invalid, %d returned no data",
		response.Totals.EligibleQueries,
		response.Totals.SkippedQueries,
		response.Totals.PreviewOK,
		response.Totals.DataReturned,
		response.FailuresTotal,
		response.NoDataTotal,
	)
	return mcp.NewToolResultStructured(response, fallback), nil
}

type stagedValidationArtifacts struct {
	dashboard string
	report    string
	html      string
}

func stageValidationArtifactSet(
	directory string,
	evidence *reporttypes.Report,
	dashboardData []byte,
) (stagedValidationArtifacts, error) {
	if evidence == nil {
		return stagedValidationArtifacts{}, fmt.Errorf("validation report is nil")
	}
	paths := stagedValidationArtifacts{
		dashboard: filepath.Join(directory, "validated.signoz.json"),
		report:    filepath.Join(directory, "validated.report.json"),
		html:      filepath.Join(directory, "validated.report.html"),
	}
	dashboardDigest := sha256.Sum256(dashboardData)
	evidence.PrimaryArtifact = &reporttypes.ArtifactBinding{
		Path: filepath.Base(paths.dashboard), SHA256: fmt.Sprintf("%x", dashboardDigest[:]), SizeBytes: int64(len(dashboardData)),
	}
	binding, err := artifactset.NewBindingForReport(paths.report, artifactset.KindDashboard)
	if err != nil {
		return stagedValidationArtifacts{}, err
	}
	evidence.ArtifactSet = &binding
	reportData, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return stagedValidationArtifacts{}, fmt.Errorf("encode validation report: %w", err)
	}
	reportData = append(reportData, '\n')
	htmlData, err := reportpkg.DashboardHTMLBytes(*evidence)
	if err != nil {
		return stagedValidationArtifacts{}, err
	}
	if err := artifactset.Commit(paths.report, binding, artifactset.KindDashboard, []artifactset.Artifact{
		{Role: artifactset.RolePrimary, Path: paths.dashboard, Data: dashboardData},
		{Role: artifactset.RoleReport, Path: paths.report, Data: reportData},
		{Role: artifactset.RoleHTML, Path: paths.html, Data: htmlData},
	}); err != nil {
		return stagedValidationArtifacts{}, fmt.Errorf("commit validation artifact set: %w", err)
	}
	return paths, nil
}

func decodeStoredDashboard(path string, data []byte) (signoz.DashboardV5, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var dashboard signoz.DashboardV5
	if err := decoder.Decode(&dashboard); err != nil {
		return signoz.DashboardV5{}, fmt.Errorf("decode stored dashboard %q: %w", path, err)
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		return signoz.DashboardV5{}, fmt.Errorf("decode stored dashboard %q: %w", path, err)
	}
	return dashboard, nil
}

func cloneDashboardReport(source reporttypes.Report) (reporttypes.Report, error) {
	data, err := json.Marshal(source)
	if err != nil {
		return reporttypes.Report{}, fmt.Errorf("clone migration report: %w", err)
	}
	var cloned reporttypes.Report
	if err := json.Unmarshal(data, &cloned); err != nil {
		return reporttypes.Report{}, fmt.Errorf("clone migration report: %w", err)
	}
	return cloned, nil
}

func clearLiveValidation(evidence *reporttypes.Report) {
	for panelIndex := range evidence.Panels {
		for queryIndex := range evidence.Panels[panelIndex].Queries {
			previous := evidence.Panels[panelIndex].Queries[queryIndex].Validation
			evidence.Panels[panelIndex].Queries[queryIndex].Validation = reporttypes.Validation{
				MetricChecked: previous.MetricChecked,
				MetricFound:   previous.MetricFound,
			}
		}
	}
	clearLiveValidationSummary(&evidence.Summary)
}

func refreshLiveValidationSummary(evidence *reporttypes.Report) {
	clearLiveValidationSummary(&evidence.Summary)
	for _, panel := range evidence.Panels {
		if !panel.PrimaryArtifact {
			continue
		}
		for _, query := range panel.Queries {
			validation := query.Validation
			eligible := !query.Disabled && query.EmittedKind != "none"
			if eligible {
				evidence.Summary.ValidationEligible++
				if !validation.PreviewOK || !validation.Executed || validation.ErrorCode != "" {
					evidence.Summary.ValidationFailed++
				}
			}
			if validation.Previewed {
				evidence.Summary.Previewed++
				if validation.PreviewOK {
					evidence.Summary.PreviewValid++
				} else {
					evidence.Summary.PreviewInvalid++
				}
			}
			if validation.Executed {
				evidence.Summary.Executed++
				if validation.DataPresent {
					evidence.Summary.DataPresent++
				} else {
					evidence.Summary.DataAbsent++
				}
			}
		}
	}
}

func clearLiveValidationSummary(summary *reporttypes.Summary) {
	summary.Previewed = 0
	summary.PreviewValid = 0
	summary.PreviewInvalid = 0
	summary.ValidationEligible = 0
	summary.ValidationFailed = 0
	summary.Executed = 0
	summary.DataPresent = 0
	summary.DataAbsent = 0
	summary.DataPresentPercent = 0
}

type liveMetricMetadataState struct {
	checked bool
	found   bool
}

func refreshLiveMetricMetadata(
	ctx context.Context,
	client *signoz.Client,
	evidence *reporttypes.Report,
	now time.Time,
	window time.Duration,
) error {
	analyzer := transpile.NewAnalyzer(transpile.Options{})
	states := make(map[string]liveMetricMetadataState)
	for panelIndex := range evidence.Panels {
		panel := &evidence.Panels[panelIndex]
		for queryIndex := range panel.Queries {
			query := &panel.Queries[queryIndex]
			if !panel.PrimaryArtifact || query.Disabled || query.EmittedKind == "none" {
				continue
			}
			query.Validation.MetricChecked = false
			query.Validation.MetricFound = false
			names := emittedMetricNames(*query, analyzer)
			if len(names) == 0 {
				continue
			}
			query.Validation.MetricChecked = true
			query.Validation.MetricFound = true
			for _, name := range names {
				state, found := states[name]
				if !found {
					var err error
					state, err = inspectLiveMetricMetadata(ctx, client, name, now.Add(-window), now)
					if err != nil {
						return fmt.Errorf("metric %q: %w", name, err)
					}
					states[name] = state
				}
				query.Validation.MetricChecked = query.Validation.MetricChecked && state.checked
				query.Validation.MetricFound = query.Validation.MetricFound && state.found
			}
		}
	}
	return nil
}

func emittedMetricNames(query reporttypes.QueryRecord, analyzer *transpile.Analyzer) []string {
	names := make(map[string]bool)
	add := func(name string) {
		if name = strings.TrimSpace(name); name != "" {
			names[name] = true
		}
	}
	switch query.EmittedKind {
	case "builder":
		if query.Builder != nil {
			add(query.Builder.MetricName)
		}
	case "formula":
		if query.Formula != nil {
			for _, dependency := range query.Formula.Queries {
				add(dependency.MetricName)
			}
		}
	case "promql":
		expression := query.EmittedExpression
		if strings.TrimSpace(expression) == "" {
			expression = query.PromQL
		}
		for _, name := range analyzer.MetricNames(model.Query{Expression: expression}) {
			add(name)
		}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func inspectLiveMetricMetadata(
	ctx context.Context,
	client *signoz.Client,
	name string,
	start time.Time,
	end time.Time,
) (liveMetricMetadataState, error) {
	if _, err := client.MetricMetadata(ctx, name); err != nil {
		if signoz.IsNotFound(err) {
			return liveMetricMetadataState{checked: true}, nil
		}
		if isolatedMetricMetadataFailure(err) {
			return liveMetricMetadataState{}, nil
		}
		return liveMetricMetadataState{}, err
	}
	if _, err := client.MetricAttributes(ctx, name, start, end); err != nil {
		if signoz.IsNotFound(err) || isolatedMetricMetadataFailure(err) {
			return liveMetricMetadataState{}, nil
		}
		return liveMetricMetadataState{}, err
	}
	return liveMetricMetadataState{checked: true, found: true}, nil
}

func isolatedMetricMetadataFailure(err error) bool {
	var apiError *signoz.APIError
	if !errors.As(err, &apiError) {
		return false
	}
	if apiError.StatusCode >= http.StatusInternalServerError && apiError.StatusCode <= 599 {
		return true
	}
	switch apiError.StatusCode {
	case http.StatusBadRequest, http.StatusConflict, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return true
	default:
		return false
	}
}

func validationSubset(evidence reporttypes.Report, present map[string]bool) reporttypes.Report {
	subset := evidence
	subset.Panels = make([]reporttypes.PanelRecord, 0, len(present))
	for _, panel := range evidence.Panels {
		if present[panel.SourcePath] {
			panel.Queries = append([]reporttypes.QueryRecord(nil), panel.Queries...)
			subset.Panels = append(subset.Panels, panel)
		}
	}
	return subset
}

func validationDashboardSubset(dashboard signoz.DashboardV5, selectedPath string) signoz.DashboardV5 {
	subset := dashboard
	subset.Widgets = make([]signoz.Widget, 0, 1)
	keptIDs := make(map[string]bool, 1)
	for _, widget := range dashboard.Widgets {
		if widget.SourcePath != selectedPath {
			continue
		}
		subset.Widgets = append(subset.Widgets, widget)
		keptIDs[widget.ID] = true
	}
	if dashboard.Layout != nil {
		subset.Layout = validationLayoutsSubset(dashboard.Layout, keptIDs)
	}
	if dashboard.PanelMap != nil {
		subset.PanelMap = make(map[string]signoz.PanelGroup)
		for rowID, group := range dashboard.PanelMap {
			group.Widgets = validationLayoutsSubset(group.Widgets, keptIDs)
			if keptIDs[rowID] || len(group.Widgets) > 0 {
				subset.PanelMap[rowID] = group
			}
		}
	}
	return subset
}

func validationLayoutsSubset(layouts []signoz.Layout, keptIDs map[string]bool) []signoz.Layout {
	kept := make([]signoz.Layout, 0, len(layouts))
	for _, layout := range layouts {
		if keptIDs[layout.I] {
			kept = append(kept, layout)
		}
	}
	return kept
}

func mergeValidatedPanels(destination *reporttypes.Report, validated reporttypes.Report) {
	byPath := make(map[string]reporttypes.PanelRecord, len(validated.Panels))
	for _, panel := range validated.Panels {
		byPath[panel.SourcePath] = panel
	}
	for index := range destination.Panels {
		if panel, found := byPath[destination.Panels[index].SourcePath]; found {
			destination.Panels[index] = panel
		}
	}
}

func cloneFlags(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source)+3)
	maps.Copy(cloned, source)
	return cloned
}

func storedVariableValues(dashboard signoz.DashboardV5) (map[string]any, error) {
	return signoz.RuntimeVariableValues(dashboard)
}

func buildValidationResponse(
	id string,
	window time.Duration,
	previous reporttypes.Report,
	current reporttypes.Report,
	panelSelector string,
) (validateResponse, error) {
	selectedPath, selectionEnabled, err := selectedValidationPath(current.Panels, panelSelector)
	if err != nil {
		return validateResponse{}, err
	}
	response := validateResponse{MigrationID: id, Window: window.String()}
	builder := validationResponseBuilder{response: &response, before: validationMap(previous)}
	for _, panel := range current.Panels {
		if selectionEnabled && panel.SourcePath != selectedPath {
			continue
		}
		builder.addPanel(panel)
	}
	response.FailuresTruncated = response.FailuresTotal > len(response.Failures)
	response.NoDataTruncated = response.NoDataTotal > len(response.NoData)
	if response.CheckedAt == "" {
		response.CheckedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return response, nil
}

func selectedValidationPath(panels []reporttypes.PanelRecord, selector string) (string, bool, error) {
	if strings.TrimSpace(selector) == "" {
		return "", false, nil
	}
	panel, err := selectPanel(panels, selector)
	if err != nil {
		return "", false, err
	}
	selectedPath := panel.SourcePath
	if selectedPath == "" {
		return "", false, fmt.Errorf("selected panel %q has no stable source path", panel.Title)
	}
	matches := 0
	for _, candidate := range panels {
		if candidate.SourcePath == selectedPath {
			matches++
		}
	}
	if matches != 1 {
		return "", false, fmt.Errorf("selected source path %q maps to %d panels", selectedPath, matches)
	}
	return selectedPath, true, nil
}

type validationResponseBuilder struct {
	response *validateResponse
	before   map[string]reporttypes.Validation
}

type validationQueryFacts struct {
	previewOK             bool
	metricExists          bool
	hadCompleteDataResult bool
	hasCompleteDataResult bool
	targetValid           bool
}

func (builder validationResponseBuilder) addPanel(panel reporttypes.PanelRecord) {
	for _, query := range panel.Queries {
		builder.addQuery(panel, query)
	}
}

func (builder validationResponseBuilder) addQuery(panel reporttypes.PanelRecord, query reporttypes.QueryRecord) {
	response := builder.response
	response.Totals.SourceQueries++
	if !panel.PrimaryArtifact || query.Disabled || query.EmittedKind == "none" {
		response.Totals.SkippedQueries++
		return
	}
	response.Totals.EligibleQueries++
	facts := validationFacts(query.Validation, builder.before[query.SourcePath])
	builder.addEligibleTotals(query.Validation, facts)
	builder.addDelta(facts)
	if facts.targetValid && query.Validation.DataPresent {
		return
	}
	if facts.targetValid {
		builder.addNoData(panel, query)
		return
	}
	builder.addFailure(panel, query, facts)
}

func validationFacts(current reporttypes.Validation, previous reporttypes.Validation) validationQueryFacts {
	previewOK := current.Previewed && current.PreviewOK
	metricExists := !current.MetricChecked || current.MetricFound
	previousMetricExists := !previous.MetricChecked || previous.MetricFound
	hadCompleteDataResult := previous.Previewed && previous.PreviewOK && previous.Executed && previous.DataPresent &&
		previousMetricExists && previous.ErrorCode == "" && previous.Error == ""
	hasCompleteDataResult := previewOK && metricExists && current.Executed && current.DataPresent &&
		current.ErrorCode == "" && current.Error == ""
	targetValid := previewOK && metricExists && current.Executed && current.ErrorCode == "" && current.Error == ""
	return validationQueryFacts{
		previewOK: previewOK, metricExists: metricExists, hadCompleteDataResult: hadCompleteDataResult,
		hasCompleteDataResult: hasCompleteDataResult, targetValid: targetValid,
	}
}

func (builder validationResponseBuilder) addEligibleTotals(
	validation reporttypes.Validation,
	facts validationQueryFacts,
) {
	response := builder.response
	if response.CheckedAt == "" && validation.CheckedAt != "" {
		response.CheckedAt = validation.CheckedAt
	}
	if facts.previewOK {
		response.Totals.PreviewOK++
	}
	if validation.MetricChecked && validation.MetricFound {
		response.Totals.MetricExists++
	}
	if validation.Executed && validation.DataPresent {
		response.Totals.DataReturned++
	} else if validation.Executed {
		response.Totals.DataAbsent++
	}
}

func (builder validationResponseBuilder) addDelta(facts validationQueryFacts) {
	if facts.hasCompleteDataResult && !facts.hadCompleteDataResult {
		builder.response.Delta.NewDataPresent++
	}
	if !facts.hasCompleteDataResult && facts.hadCompleteDataResult {
		builder.response.Delta.DataNoLongerPresent++
	}
}

func (builder validationResponseBuilder) addNoData(
	panel reporttypes.PanelRecord,
	query reporttypes.QueryRecord,
) {
	response := builder.response
	response.NoDataTotal++
	if len(response.NoData) >= validationResultPreviewLimit {
		return
	}
	response.NoData = append(response.NoData, validationFailure{
		Panel: panel.Title, Query: query.RefID, ReasonCodes: query.ReasonCodes,
		Checks:    validationChecks{PreviewOK: true, MetricExists: true, DataReturned: false},
		ErrorCode: "NO_DATA_RETURNED",
		Error:     "the emitted query executed but returned no data in the validation window",
	})
}

func (builder validationResponseBuilder) addFailure(
	panel reporttypes.PanelRecord,
	query reporttypes.QueryRecord,
	facts validationQueryFacts,
) {
	response := builder.response
	response.FailuresTotal++
	if len(response.Failures) >= validationResultPreviewLimit {
		return
	}
	validation := query.Validation
	errorCode, errorMessage := validationFailureDetail(validation, facts.previewOK, facts.metricExists)
	response.Failures = append(response.Failures, validationFailure{
		Panel: panel.Title, Query: query.RefID, ReasonCodes: query.ReasonCodes,
		Checks: validationChecks{
			PreviewOK: facts.previewOK, MetricExists: facts.metricExists,
			DataReturned: validation.Executed && validation.DataPresent,
		},
		ErrorCode: errorCode, Error: errorMessage,
	})
}

func validationFailureDetail(
	validation reporttypes.Validation,
	previewOK bool,
	metricExists bool,
) (string, string) {
	if validation.ErrorCode != "" || validation.Error != "" {
		return validation.ErrorCode, validation.Error
	}
	if !previewOK {
		return "PREVIEW_NOT_VALID", "the emitted query did not complete a successful SigNoz preview"
	}
	if !metricExists {
		return "METRIC_NOT_FOUND", "one or more source metrics were not found in the target metadata window"
	}
	if !validation.Executed {
		return "QUERY_NOT_EXECUTED", "the emitted query was not executed after preview"
	}
	if !validation.DataPresent {
		return "NO_DATA_RETURNED", "the emitted query executed but returned no data in the validation window"
	}
	return "VALIDATION_INCOMPLETE", "the emitted query did not satisfy every validation check"
}
