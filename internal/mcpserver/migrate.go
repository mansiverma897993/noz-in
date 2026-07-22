package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mansiverma897993/noz-in/internal/app"
	"github.com/mansiverma897993/noz-in/internal/artifactset"
	"github.com/mansiverma897993/noz-in/internal/stableidentity"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
	"github.com/mark3labs/mcp-go/mcp"
)

var grafanaIDPattern = regexp.MustCompile(`^[0-9]{1,10}$`)

const maxMCPRuleInputs = 32

const (
	attemptGeneration = "attempt"
	resultGeneration  = "result"
)

type migrationCheckpoint struct {
	result app.GrafanaResult
	state  manifest
}

type migrateSummary struct {
	PanelsTotal               int             `json:"panels_total"`
	PanelsAccounted           int             `json:"panels_accounted"`
	QueriesTotal              int             `json:"queries_total"`
	PanelsNeedsReview         int             `json:"panels_needs_review"`
	VariablesNeedsReview      int             `json:"variables_needs_review"`
	SourceFeaturesNeedsReview int             `json:"source_features_needs_review"`
	Verdicts                  migrateVerdicts `json:"verdicts"`
	DataPresentPct            float64         `json:"data_present_pct"`
	Headline                  string          `json:"headline"`
}

type migrateVerdicts struct {
	Builder     int `json:"builder"`
	Formula     int `json:"formula"`
	Passthrough int `json:"passthrough"`
	NeedsReview int `json:"needs_review"`
}

type reviewItem struct {
	Scope       string   `json:"scope"`
	Kind        string   `json:"kind"`
	Panel       string   `json:"panel"`
	Query       string   `json:"query,omitempty"`
	SourcePath  string   `json:"source_path,omitempty"`
	ReasonCodes []string `json:"reason_codes"`
	Hint        string   `json:"hint"`
}

type migrateArtifacts struct {
	ReportJSON         string `json:"report_json"`
	ReportHTML         string `json:"report_html"`
	DashboardV5        string `json:"dashboard_v5"`
	CandidateDashboard string `json:"candidate_dashboard_v5,omitempty"`
}

type importedDashboard struct {
	Action                  string   `json:"action"`
	DashboardID             string   `json:"dashboard_id"`
	URL                     string   `json:"url,omitempty"`
	Widgets                 int      `json:"widgets"`
	Partial                 bool     `json:"partial"`
	ValidationRejectedPaths []string `json:"validation_rejected_paths,omitempty"`
}

type migrateTargetStatus string

const (
	migrateTargetImported     migrateTargetStatus = "imported"
	migrateTargetSkipped      migrateTargetStatus = "skipped"
	migrateTargetFailed       migrateTargetStatus = "failed_or_unknown"
	migrateTargetDryRun       migrateTargetStatus = "dry_run"
	migrateTargetNotRequested migrateTargetStatus = "not_requested"
)

type migrateResponse struct {
	MigrationID             string              `json:"migration_id"`
	DashboardTitle          string              `json:"dashboard_title"`
	ImportRequested         bool                `json:"import_requested"`
	TargetStatus            migrateTargetStatus `json:"target_status"`
	TargetSkippedReason     string              `json:"target_skipped_reason"`
	TargetError             string              `json:"target_error,omitempty"`
	Failure                 *migrateFailure     `json:"failure,omitempty"`
	Summary                 migrateSummary      `json:"summary"`
	NeedsReview             []reviewItem        `json:"needs_review"`
	NeedsReviewTotal        int                 `json:"needs_review_total"`
	NeedsReviewTruncated    int                 `json:"needs_review_truncated"`
	Artifacts               migrateArtifacts    `json:"artifacts"`
	Imported                *importedDashboard  `json:"imported,omitempty"`
	ValidationRejectedPaths []string            `json:"validation_rejected_paths,omitempty"`
	ValidationBlockedPaths  []string            `json:"validation_blocked_paths,omitempty"`
}

type migrateFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type migrateDashboardInput struct {
	rateInterval      time.Duration
	variables         map[string]string
	importRequested   bool
	sourceNamespace   string
	dashboardIdentity string
	data              []byte
	ruleData          [][]byte
	now               time.Time
}

type migrateInputError struct {
	code string
	err  error
}

func (service *Service) parseMigrateDashboardInput(
	ctx context.Context,
	request mcp.CallToolRequest,
) (migrateDashboardInput, *migrateInputError) {
	rateInterval, err := time.ParseDuration(request.GetString("rate_interval", "5m"))
	if err != nil || rateInterval <= 0 {
		return migrateDashboardInput{}, &migrateInputError{
			code: "INVALID_INPUT",
			err:  fmt.Errorf("rate_interval must be a positive Go duration such as 5m"),
		}
	}
	variables, err := parseAssignments(request.GetStringSlice("variables", nil))
	if err != nil {
		return migrateDashboardInput{}, &migrateInputError{code: "INVALID_INPUT", err: err}
	}
	importRequested := request.GetBool("import", false)
	if importRequested && strings.TrimSpace(service.config.TargetURL) == "" {
		return migrateDashboardInput{}, &migrateInputError{
			code: "TARGET_REQUIRED",
			err:  fmt.Errorf("import=true requires SIGNOZ_URL and SIGNOZ_API_KEY on the MCP server"),
		}
	}
	rawSourceNamespace := request.GetString("source_namespace", "")
	if err := stableidentity.ValidateComponent("dashboard source namespace", rawSourceNamespace, 512); err != nil {
		return migrateDashboardInput{}, &migrateInputError{code: "INVALID_INPUT", err: err}
	}
	sourceNamespace := strings.TrimSpace(rawSourceNamespace)
	if strings.TrimSpace(service.config.TargetURL) != "" && sourceNamespace == "" {
		return migrateDashboardInput{}, &migrateInputError{
			code: "INVALID_INPUT",
			err: fmt.Errorf(
				"a configured SigNoz target requires source_namespace for validation or import so generated target identities remain stable",
			),
		}
	}
	rawSourceIdentity := request.GetString("source_identity", "")
	if err := stableidentity.ValidateComponent("dashboard source identity", rawSourceIdentity, 4096); err != nil {
		return migrateDashboardInput{}, &migrateInputError{code: "INVALID_INPUT", err: err}
	}
	sourceIdentity := strings.TrimSpace(rawSourceIdentity)
	if sourceIdentity == "" {
		switch {
		case strings.TrimSpace(request.GetString("grafana_path", "")) != "":
			sourceIdentity = "mcp-path:" + filepath.ToSlash(filepath.Clean(request.GetString("grafana_path", "")))
		case strings.TrimSpace(request.GetString("grafana_id", "")) != "":
			sourceIdentity = "grafana.com:" + strings.TrimSpace(request.GetString("grafana_id", ""))
		}
	}
	if err := stableidentity.ValidateComponent("dashboard source identity", sourceIdentity, 4096); err != nil {
		return migrateDashboardInput{}, &migrateInputError{code: "INVALID_INPUT", err: err}
	}
	// Request-only identity checks deliberately precede dashboardInput: a
	// grafana_id source performs outbound I/O and must not be fetched for an
	// identity that cannot safely participate in stable upserts.
	data, err := service.dashboardInput(ctx, request)
	if err != nil {
		return migrateDashboardInput{}, &migrateInputError{code: "INVALID_INPUT", err: err}
	}
	if importRequested && sourceIdentity == "" {
		var header struct {
			UID string `json:"uid"`
		}
		if err := json.Unmarshal(data, &header); err == nil && strings.TrimSpace(header.UID) == "" {
			return migrateDashboardInput{}, &migrateInputError{
				code: "INVALID_INPUT",
				err:  fmt.Errorf("importing a UID-less inline dashboard requires source_identity"),
			}
		}
	}
	ruleData, err := service.ruleInputs(request.GetStringSlice("rules", nil))
	if err != nil {
		return migrateDashboardInput{}, &migrateInputError{code: "INVALID_INPUT", err: err}
	}

	now := time.Now().UTC()
	if service.config.Now != nil {
		now = service.config.Now().UTC()
	}
	identityDigest := sha256.Sum256(data)
	dashboardIdentity := sourceIdentity
	if dashboardIdentity == "" {
		dashboardIdentity = fmt.Sprintf("sha256:%x", identityDigest[:])
	}
	return migrateDashboardInput{
		rateInterval:      rateInterval,
		variables:         variables,
		importRequested:   importRequested,
		sourceNamespace:   sourceNamespace,
		dashboardIdentity: dashboardIdentity,
		data:              data,
		ruleData:          ruleData,
		now:               now,
	}, nil
}

func (service *Service) handleMigrateDashboard(
	ctx context.Context,
	request mcp.CallToolRequest,
) (*mcp.CallToolResult, error) {
	release, err := service.acquireTool(ctx)
	if err != nil {
		return toolError("RESOURCE_BUSY", err), nil
	}
	defer release()
	input, inputErr := service.parseMigrateDashboardInput(ctx, request)
	if inputErr != nil {
		return toolError(inputErr.code, inputErr.err), nil
	}
	operation, directory, migrationID, err := service.beginMigrationWork(
		input.data,
		input.now,
		input.importRequested,
	)
	if err != nil {
		return toolError("ARTIFACT_WRITE_FAILED", err), nil
	}
	workCleaned := false
	defer func() {
		if !workCleaned {
			_ = operation.cleanup()
		}
	}()
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

	sourceName := "source.grafana.json"
	sourcePath := filepath.Join(stagingDirectory, sourceName)
	if err := writeAtomic(sourcePath, input.data); err != nil {
		return toolError("ARTIFACT_WRITE_FAILED", err), nil
	}
	storedRules := make([]string, 0, len(input.ruleData))
	storedRuleNames := make([]string, 0, len(input.ruleData))
	for index, contents := range input.ruleData {
		name := fmt.Sprintf("source.rules.%03d.yaml", index+1)
		path := filepath.Join(stagingDirectory, name)
		if err := writeAtomic(path, contents); err != nil {
			return toolError("ARTIFACT_WRITE_FAILED", err), nil
		}
		storedRules = append(storedRules, path)
		storedRuleNames = append(storedRuleNames, name)
	}
	service.runCrashBarrier("migration-work-created")
	var attemptedCheckpoint *migrationCheckpoint
	var finalCheckpoint *migrationCheckpoint
	checkpointFailed := false
	publishCheckpoint := func(result app.GrafanaResult) error {
		if !result.ImportAttempted {
			return nil
		}
		var published migrationCheckpoint
		var err error
		if result.TargetAction == "attempted" {
			published, err = service.publishInitialMigration(
				operation,
				stagingDirectory,
				directory,
				migrationID,
				attemptGeneration,
				result,
				sourceName,
				storedRuleNames,
				input.variables,
				input.rateInterval,
				input.dashboardIdentity,
				input.sourceNamespace,
			)
		} else {
			service.runCrashBarrier("migration-target-completed")
			published, err = service.publishMigrationResult(
				operation,
				stagingDirectory,
				directory,
				migrationID,
				result,
				sourceName,
				storedRuleNames,
				input.variables,
				input.rateInterval,
				input.dashboardIdentity,
				input.sourceNamespace,
			)
		}
		if err != nil {
			checkpointFailed = true
			return err
		}
		if result.TargetAction == "attempted" {
			attemptedCheckpoint = &published
			service.runCrashBarrier("migration-attempt-published")
		} else {
			finalCheckpoint = &published
		}
		return nil
	}
	results, migrateErr := app.MigrateGrafana(ctx, []string{sourcePath}, app.GrafanaOptions{
		OutputDirectory:   stagingDirectory,
		RateInterval:      input.rateInterval,
		Range:             time.Hour,
		TargetURL:         service.config.TargetURL,
		APIKey:            service.config.APIKey,
		HTTPClient:        service.config.HTTPClient,
		AllowInsecureHTTP: service.config.AllowInsecureHTTP,
		Validate:          service.config.TargetURL != "",
		Variables:         input.variables,
		MetricNameMap:     service.config.MetricNameMap,
		RuleFiles:         storedRules,
		ValidationWorkers: service.config.Workers,
		DryRun:            !input.importRequested,
		SourceNamespace:   input.sourceNamespace,
		DashboardIdentity: input.dashboardIdentity,
		SourcePathOverrides: map[string]string{
			sourcePath: filepath.Join(directory, sourceName),
		},
		ArtifactCheckpoint: publishCheckpoint,
	})
	if len(results) != 1 {
		if migrateErr != nil {
			return toolError("MIGRATION_FAILED", migrateErr), nil
		}
		return toolError("MIGRATION_FAILED", fmt.Errorf("migration did not return exactly one result")), nil
	}
	var durable migrationCheckpoint
	switch {
	case finalCheckpoint != nil:
		durable = *finalCheckpoint
	case attemptedCheckpoint != nil:
		// A final artifact publication failure after the target request must
		// retain and return the durable attempted-write generation. It is more
		// honest to report an unknown final outcome than to expose in-memory
		// success alongside evidence that could not be committed.
		durable = *attemptedCheckpoint
	case checkpointFailed:
		return toolError("ARTIFACT_WRITE_FAILED", migrateErr), nil
	default:
		published, err := service.publishInitialMigration(
			operation,
			stagingDirectory,
			directory,
			migrationID,
			resultGeneration,
			results[0],
			sourceName,
			storedRuleNames,
			input.variables,
			input.rateInterval,
			input.dashboardIdentity,
			input.sourceNamespace,
		)
		if err != nil {
			return toolError("ARTIFACT_WRITE_FAILED", err), nil
		}
		durable = published
	}
	if err := service.cleanupPrivateStagingDirectory(operation.token, operation.plan.StagingParent); err != nil {
		return toolError("ARTIFACT_WRITE_FAILED", err), nil
	}
	stagingCleaned = true
	if err := operation.cleanup(); err != nil {
		return toolError("ARTIFACT_WRITE_FAILED", err), nil
	}
	workCleaned = true
	if finalCheckpoint == nil && checkpointFailed {
		state, stateErr := service.readManifest(migrationID)
		if stateErr == nil && state.Generation == resultGeneration {
			relocated := relocateGrafanaResult(results[0], filepath.Join(directory, resultGeneration))
			if verifyErr := verifyCommittedGrafanaResult(relocated); verifyErr == nil {
				durable = migrationCheckpoint{result: relocated, state: state}
			}
		}
	}

	response := migrationResponse(migrationID, durable.result, input.importRequested)
	if migrateErr != nil {
		response.Failure = &migrateFailure{Code: "MIGRATION_FAILED", Message: migrateErr.Error()}
		toolResult := mcp.NewToolResultStructured(response, migrationResponseText(response))
		toolResult.IsError = true
		return toolResult, nil
	}
	return mcp.NewToolResultStructured(response, migrationResponseText(response)), nil
}

func verifyCommittedGrafanaResult(result app.GrafanaResult) error {
	if result.Evidence.ArtifactSet == nil {
		return fmt.Errorf("dashboard report has no committed artifact-set binding")
	}
	data, err := os.ReadFile(result.ReportPath)
	if err != nil {
		return fmt.Errorf("read dashboard report %q: %w", result.ReportPath, err)
	}
	if len(data) > maxMCPArtifactSize {
		return fmt.Errorf("dashboard report %q exceeds %d bytes", result.ReportPath, maxMCPArtifactSize)
	}
	requested := []string{filepath.Base(result.DashboardPath), filepath.Base(result.HTMLPath)}
	if result.CandidateDashboardPath != "" {
		requested = append(requested, filepath.Base(result.CandidateDashboardPath))
	}
	_, err = artifactset.ReadCommitted(
		result.ReportPath,
		data,
		result.Evidence.ArtifactSet,
		artifactset.KindDashboard,
		requested,
		maxMCPArtifactSize,
	)
	return err
}

func relocateGrafanaResult(result app.GrafanaResult, directory string) app.GrafanaResult {
	relocate := func(path string) string {
		if path == "" {
			return ""
		}
		return filepath.Join(directory, filepath.Base(path))
	}
	result.DashboardPath = relocate(result.DashboardPath)
	result.CandidateDashboardPath = relocate(result.CandidateDashboardPath)
	result.ReportPath = relocate(result.ReportPath)
	result.HTMLPath = relocate(result.HTMLPath)
	return result
}

func (service *Service) dashboardInput(ctx context.Context, request mcp.CallToolRequest) ([]byte, error) {
	inline := strings.TrimSpace(request.GetString("grafana_json", ""))
	path := strings.TrimSpace(request.GetString("grafana_path", ""))
	id := strings.TrimSpace(request.GetString("grafana_id", ""))
	provided := 0
	for _, value := range []string{inline, path, id} {
		if value != "" {
			provided++
		}
	}
	if provided != 1 {
		return nil, fmt.Errorf("provide exactly one of grafana_json, grafana_path, or grafana_id")
	}
	if inline != "" {
		if len(inline) > maxMCPArtifactSize {
			return nil, fmt.Errorf("grafana_json exceeds %d bytes", maxMCPArtifactSize)
		}
		return []byte(inline), nil
	}
	if path != "" {
		return service.readInputBounded(path)
	}
	return service.fetchGrafanaDashboard(ctx, id)
}

func (service *Service) fetchGrafanaDashboard(ctx context.Context, id string) ([]byte, error) {
	if !grafanaIDPattern.MatchString(id) {
		return nil, fmt.Errorf("grafana_id must contain 1 to 10 digits")
	}
	endpoint := "https://grafana.com/api/dashboards/" + url.PathEscape(id) + "/revisions/latest/download"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create grafana.com request: %w", err)
	}
	client := service.config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	guardedClient := *client
	configuredRedirect := client.CheckRedirect
	guardedClient.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if configuredRedirect != nil {
			if err := configuredRedirect(next, via); err != nil {
				return err
			}
		}
		if next.URL.Scheme != "https" || !isGrafanaHost(next.URL.Hostname()) {
			return fmt.Errorf("refuse grafana.com redirect to %q", next.URL.Redacted())
		}
		return nil
	}
	response, err := guardedClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download grafana.com dashboard %s: %w", id, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, maxMCPArtifactSize+1))
	closeErr := response.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read grafana.com dashboard %s: %w", id, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close grafana.com dashboard %s: %w", id, closeErr)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("grafana.com dashboard %s returned HTTP %d", id, response.StatusCode)
	}
	if len(data) > maxMCPArtifactSize {
		return nil, fmt.Errorf("grafana.com dashboard %s exceeds %d bytes", id, maxMCPArtifactSize)
	}
	return data, nil
}

func isGrafanaHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "grafana.com" || strings.HasSuffix(host, ".grafana.com")
}

func (service *Service) ruleInputs(paths []string) ([][]byte, error) {
	if len(paths) > maxMCPRuleInputs {
		return nil, fmt.Errorf("rules contains %d paths; maximum is %d", len(paths), maxMCPRuleInputs)
	}
	contents := make([][]byte, 0, len(paths))
	remaining := int64(maxMCPArtifactSize)
	for _, path := range paths {
		if remaining == 0 {
			return nil, fmt.Errorf("rules exceed the %d-byte aggregate limit", maxMCPArtifactSize)
		}
		data, err := service.readInputBoundedLimit(path, remaining)
		if err != nil {
			return nil, err
		}
		remaining -= int64(len(data))
		contents = append(contents, data)
	}
	return contents, nil
}

func parseAssignments(values []string) (map[string]string, error) {
	result := make(map[string]string, len(values))
	for _, value := range values {
		name, assignment, found := strings.Cut(value, "=")
		name = strings.TrimSpace(name)
		if !found || name == "" {
			return nil, fmt.Errorf("invalid variable %q: expected name=value", value)
		}
		if _, duplicate := result[name]; duplicate {
			return nil, fmt.Errorf("variable %q was provided more than once", name)
		}
		result[name] = assignment
	}
	return result, nil
}

func migrationResponse(id string, result app.GrafanaResult, importRequested bool) migrateResponse {
	targetStatus, skippedReason := migrationTargetOutcome(result, importRequested)
	response := migrateResponse{
		MigrationID:         id,
		DashboardTitle:      result.Evidence.Dashboard.Title,
		ImportRequested:     importRequested,
		TargetStatus:        targetStatus,
		TargetSkippedReason: skippedReason,
		TargetError:         result.TargetError,
		Summary: migrateSummary{
			PanelsTotal:               result.Summary.Panels,
			PanelsAccounted:           result.Summary.PanelsAccounted,
			QueriesTotal:              result.Summary.Queries,
			PanelsNeedsReview:         result.Summary.PanelsNeedsReview,
			VariablesNeedsReview:      result.Summary.VariablesNeedsReview,
			SourceFeaturesNeedsReview: result.Summary.SourceFeaturesNeedsReview,
			DataPresentPct:            result.Summary.DataPresentPercent,
			Headline:                  result.Summary.Headline,
		},
		Artifacts: migrateArtifacts{
			ReportJSON: result.ReportPath, ReportHTML: result.HTMLPath, DashboardV5: result.DashboardPath,
			CandidateDashboard: result.CandidateDashboardPath,
		},
		ValidationRejectedPaths: append([]string(nil), result.ValidationRejected...),
		ValidationBlockedPaths:  append([]string(nil), result.ValidationBlocked...),
	}
	appendReview := func(item reviewItem) {
		response.NeedsReviewTotal++
		if len(response.NeedsReview) < 20 {
			response.NeedsReview = append(response.NeedsReview, item)
		}
	}
	for _, feature := range result.Evidence.SourceFeatures {
		appendReview(reviewItem{
			Scope: "dashboard_source_feature", Kind: "dashboard", Panel: "Dashboard", SourcePath: feature.SourcePath,
			ReasonCodes: []string{feature.ReasonCode}, Hint: explainHint("dashboard", feature.SourcePath),
		})
	}
	for _, variable := range result.Evidence.Variables {
		residualReasons := reasonsWithoutFeatures(variable.ReasonCodes, variable.SourceFeatures)
		if variable.Verdict == "needs_review" && len(residualReasons) > 0 {
			appendReview(reviewItem{
				Scope: "variable", Kind: "variable", Panel: "Variable: " + variable.Name, SourcePath: variable.SourcePath,
				ReasonCodes: residualReasons, Hint: explainHint("variable", variable.SourcePath),
			})
		}
		for _, feature := range variable.SourceFeatures {
			appendReview(reviewItem{
				Scope: "variable_source_feature", Kind: "variable", Panel: "Variable: " + variable.Name, SourcePath: feature.SourcePath,
				ReasonCodes: []string{feature.ReasonCode}, Hint: explainHint("variable", feature.SourcePath),
			})
		}
	}
	for _, panel := range result.Evidence.Panels {
		panelReasons := panelOnlyReviewReasons(panel)
		if panel.Verdict == "needs_review" && len(panelReasons) > 0 {
			appendReview(reviewItem{
				Scope: "panel", Kind: "panel", Panel: panel.Title, SourcePath: panel.SourcePath, ReasonCodes: panelReasons,
				Hint: explainHint("panel", panel.SourcePath),
			})
		}
		for _, query := range panel.Queries {
			switch query.Verdict {
			case "native":
				if query.CandidateKind == "formula" {
					response.Summary.Verdicts.Formula++
				} else {
					response.Summary.Verdicts.Builder++
				}
			case "passthrough":
				response.Summary.Verdicts.Passthrough++
			case "needs_review":
				response.Summary.Verdicts.NeedsReview++
				appendReview(reviewItem{
					Scope: "query", Kind: "query", Panel: panel.Title, Query: query.RefID, SourcePath: query.SourcePath,
					ReasonCodes: query.ReasonCodes, Hint: explainHint("query", query.SourcePath),
				})
			}
		}
	}
	response.NeedsReviewTruncated = response.NeedsReviewTotal - len(response.NeedsReview)
	if targetStatus == migrateTargetImported && result.Target != nil {
		response.Imported = &importedDashboard{
			Action: result.Target.Action, DashboardID: result.Target.ID, Widgets: result.ImportedWidgets,
			Partial:                 result.PartialImportPerformed,
			ValidationRejectedPaths: append([]string(nil), result.ValidationRejected...),
		}
	}
	return response
}

func migrationTargetOutcome(result app.GrafanaResult, importRequested bool) (migrateTargetStatus, string) {
	if detail := targetOutcomeIncoherence(result, importRequested); detail != "" {
		return incoherentTargetOutcome(detail)
	}
	if result.Target != nil {
		return migrateTargetImported, ""
	}
	if importRequested {
		reason := strings.TrimSpace(result.TargetSkipped)
		if result.ImportAttempted {
			if reason == "" {
				reason = "target import was attempted, but it failed or the final dashboard outcome is unknown"
			}
			return migrateTargetFailed, reason
		}
		if reason == "" {
			reason = "target import was requested, but no dashboard write result was returned"
		}
		return migrateTargetSkipped, reason
	}
	return nonImportTargetOutcome(result)
}

func targetOutcomeIncoherence(result app.GrafanaResult, importRequested bool) string {
	if result.ImportRequested != importRequested {
		return fmt.Sprintf(
			"request declared import_requested=%t but migration recorded import_requested=%t",
			importRequested,
			result.ImportRequested,
		)
	}
	if result.ImportAttempted && !result.ImportRequested {
		return "an import attempt was recorded without an import request"
	}
	if result.ImportSucceeded && !result.ImportAttempted {
		return "import success was recorded without an import attempt"
	}
	if result.Target != nil {
		return writeResultIncoherence(result)
	}
	if result.ImportSucceeded {
		return "import success was recorded without a dashboard write result"
	}
	if strings.TrimSpace(result.TargetDashboardID) != "" {
		return "a target dashboard id was recorded without a dashboard write result"
	}
	if importRequested {
		if !result.ImportAttempted && result.TargetAction != "skipped" {
			return fmt.Sprintf(
				"an import that was not attempted recorded target action %q instead of %q",
				result.TargetAction,
				"skipped",
			)
		}
		if result.ImportAttempted && result.TargetAction != "failed" && result.TargetAction != "attempted" {
			return fmt.Sprintf(
				"an unsuccessful attempted import recorded target action %q instead of %q or %q",
				result.TargetAction,
				"failed",
				"attempted",
			)
		}
		return ""
	}
	if result.ImportAttempted {
		return "an import attempt was recorded even though import was not requested"
	}
	return ""
}

func writeResultIncoherence(result app.GrafanaResult) string {
	if !result.ImportRequested || !result.ImportAttempted || !result.ImportSucceeded {
		return "a dashboard write result was returned without requested, attempted, and succeeded import flags"
	}
	if strings.TrimSpace(result.Target.ID) == "" || strings.TrimSpace(result.Target.Action) == "" {
		return "the dashboard write result is missing its id or action"
	}
	if result.TargetDashboardID != result.Target.ID {
		return "the recorded target dashboard id does not match the dashboard write result"
	}
	if result.TargetAction != result.Target.Action {
		return "the recorded target action does not match the dashboard write result"
	}
	if strings.TrimSpace(result.TargetSkipped) != "" || strings.TrimSpace(result.TargetError) != "" {
		return "an imported dashboard also carries a skipped reason or target error"
	}
	return ""
}

func nonImportTargetOutcome(result app.GrafanaResult) (migrateTargetStatus, string) {
	if result.TargetAction == "skipped" && strings.TrimSpace(result.TargetError) != "" {
		reason := strings.TrimSpace(result.TargetSkipped)
		if reason == "" {
			reason = "target validation failed before an import was requested"
		}
		return migrateTargetSkipped, reason
	}
	switch result.TargetAction {
	case "dry_run":
		reason := strings.TrimSpace(result.TargetSkipped)
		if reason == "" {
			reason = "import was not requested; target validation ran without modifying SigNoz"
		}
		return migrateTargetDryRun, reason
	case "offline":
		if strings.TrimSpace(result.TargetSkipped) != "" || strings.TrimSpace(result.TargetError) != "" {
			return incoherentTargetOutcome("an offline migration carries a target skipped reason or error")
		}
		return migrateTargetNotRequested, ""
	default:
		return incoherentTargetOutcome(fmt.Sprintf("a non-importing migration recorded unsupported target action %q", result.TargetAction))
	}
}

func incoherentTargetOutcome(detail string) (migrateTargetStatus, string) {
	return migrateTargetSkipped, "incoherent migration target outcome: " + detail
}

func migrationResponseText(response migrateResponse) string {
	text := fmt.Sprintf("%s Target status: %s.", response.Summary.Headline, response.TargetStatus)
	if response.TargetSkippedReason != "" {
		text += " " + response.TargetSkippedReason + "."
	}
	if response.Failure != nil {
		text += " " + response.Failure.Code + ": " + response.Failure.Message + "."
	}
	return text
}

func explainHint(kind string, sourcePath string) string {
	return fmt.Sprintf("call explain_verdict with this migration_id, kind=%q, and source_path=%q", kind, sourcePath)
}

func reasonsWithoutFeatures(reasons []string, features []reporttypes.SourceFeatureRecord) []string {
	featureReasons := make(map[string]bool, len(features))
	for _, feature := range features {
		featureReasons[feature.ReasonCode] = true
	}
	result := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if !featureReasons[reason] {
			result = append(result, reason)
		}
	}
	return result
}

func panelOnlyReviewReasons(panel reporttypes.PanelRecord) []string {
	queryReasons := make(map[string]bool)
	for _, query := range panel.Queries {
		for _, reason := range query.ReasonCodes {
			queryReasons[reason] = true
		}
	}
	result := make([]string, 0, len(panel.ReasonCodes))
	for _, reason := range panel.ReasonCodes {
		if !queryReasons[reason] {
			result = append(result, reason)
		}
	}
	return result
}

func toolError(code string, err error) *mcp.CallToolResult {
	return mcp.NewToolResultError(code + ": " + err.Error())
}

func validationMap(report reporttypes.Report) map[string]reporttypes.Validation {
	result := make(map[string]reporttypes.Validation)
	for _, panel := range report.Panels {
		for _, query := range panel.Queries {
			result[query.SourcePath] = query.Validation
		}
	}
	return result
}
