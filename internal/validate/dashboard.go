package validate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/mansiverma897993/noz-in/internal/target/signoz"
	"github.com/mansiverma897993/noz-in/internal/transpile"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
)

// Options controls live target validation.
type Options struct {
	Workers        int
	Now            func() time.Time
	Window         time.Duration
	VariableIssues map[string]VariableIssue
}

// VariableIssue preserves why a target-side variable could not be
// resolved before live validation.
type VariableIssue struct {
	Reasons []model.ReasonCode
	Detail  string
}

type dashboardJob struct {
	index  int
	widget signoz.Widget
	panel  reporttypes.PanelRecord
}

type dashboardResult struct {
	index       int
	validations []reporttypes.Validation
	allValid    bool
	err         error
}

// Dashboard previews and executes every emitted widget query against SigNoz.
func Dashboard(
	ctx context.Context,
	client *signoz.Client,
	dashboard signoz.DashboardV5,
	evidence *reporttypes.Report,
	variables map[string]any,
	options Options,
) (bool, error) {
	workers, now := dashboardRunSettings(options)
	jobs, err := dashboardJobs(dashboard, evidence)
	if err != nil {
		return false, err
	}
	if len(jobs) == 0 {
		return false, nil
	}
	workers = min(workers, len(jobs))
	results, present, allValid, firstError := runDashboardJobs(
		ctx, client, jobs, len(evidence.Panels), variables, signoz.DashboardVariableTypes(dashboard),
		options.VariableIssues, now, options.Window, workers,
	)
	applyDashboardResults(evidence, results, present)
	if firstError != nil {
		return false, firstError
	}
	return allValid, nil
}

func dashboardRunSettings(options Options) (int, time.Time) {
	workers := options.Workers
	if workers <= 0 {
		workers = 4
	}
	now := time.Now()
	if options.Now != nil {
		now = options.Now()
	}
	return workers, now
}

func dashboardJobs(dashboard signoz.DashboardV5, evidence *reporttypes.Report) ([]dashboardJob, error) {
	panelIndexes := make(map[string]int, len(evidence.Panels))
	for index, panel := range evidence.Panels {
		panelIndexes[panel.SourcePath] = index
	}
	jobs := make([]dashboardJob, 0, len(dashboard.Widgets))
	seenPanels := make(map[int]bool, len(dashboard.Widgets))
	for index, widget := range dashboard.Widgets {
		if widgetQueryCount(widget) == 0 {
			continue
		}
		panelIndex, found := 0, false
		if widget.SourcePath == "" {
			if index < len(evidence.Panels) {
				panelIndex, found = index, true
			}
		} else {
			panelIndex, found = panelIndexes[widget.SourcePath]
		}
		if !found {
			return nil, fmt.Errorf(
				"validation invariant: executable widget %q at source path %q has no evidence panel",
				widget.Title, widget.SourcePath,
			)
		}
		if seenPanels[panelIndex] {
			return nil, fmt.Errorf(
				"validation invariant: more than one executable widget maps to evidence panel %q",
				evidence.Panels[panelIndex].SourcePath,
			)
		}
		seenPanels[panelIndex] = true
		jobs = append(jobs, dashboardJob{index: panelIndex, widget: widget, panel: evidence.Panels[panelIndex]})
	}
	for panelIndex, panel := range evidence.Panels {
		expectsWidget := false
		for _, query := range panel.Queries {
			expectsWidget = expectsWidget || queryEligible(query)
		}
		if expectsWidget && !seenPanels[panelIndex] {
			return nil, fmt.Errorf(
				"validation invariant: evidence panel %q has executable queries but no emitted widget",
				panel.SourcePath,
			)
		}
	}
	return jobs, nil
}

func runDashboardJobs(
	ctx context.Context,
	client *signoz.Client,
	jobs []dashboardJob,
	panelCount int,
	variables map[string]any,
	variableTypes map[string]string,
	variableIssues map[string]VariableIssue,
	now time.Time,
	window time.Duration,
	workers int,
) ([]dashboardResult, []bool, bool, error) {
	jobChannel := make(chan dashboardJob)
	resultChannel := make(chan dashboardResult, len(jobs))
	var waitGroup sync.WaitGroup
	for range workers {
		waitGroup.Go(func() {
			for job := range jobChannel {
				validations, valid, err := validateWidget(
					ctx, client, job.widget, job.panel, variables, variableTypes, variableIssues, now, window,
				)
				resultChannel <- dashboardResult{index: job.index, validations: validations, allValid: valid, err: err}
			}
		})
	}
	go func() {
		defer close(jobChannel)
		for _, job := range jobs {
			jobChannel <- job
		}
	}()
	go func() {
		waitGroup.Wait()
		close(resultChannel)
	}()

	results := make([]dashboardResult, panelCount)
	present := make([]bool, panelCount)
	allValid := true
	var firstError error
	for result := range resultChannel {
		results[result.index] = result
		present[result.index] = true
		if result.err != nil && firstError == nil {
			firstError = result.err
		}
		allValid = allValid && result.allValid && result.err == nil
	}
	return results, present, allValid, firstError
}

func applyDashboardResults(evidence *reporttypes.Report, results []dashboardResult, present []bool) {
	resetValidationSummary(&evidence.Summary)
	for panelIndex, result := range results {
		if !present[panelIndex] {
			continue
		}
		panelMissingVariables := false
		for queryIndex, validation := range result.validations {
			if queryIndex >= len(evidence.Panels[panelIndex].Queries) {
				break
			}
			query := &evidence.Panels[panelIndex].Queries[queryIndex]
			query.Validation = validation
			if validation.ErrorCode == string(model.ReasonMissingVariableValue) {
				panelMissingVariables = true
				markQueryNeedsReview(query, &evidence.Summary, validation.ReasonCodes)
			}
			addValidationSummary(&evidence.Summary, validation, queryEligible(*query))
		}
		if panelMissingVariables {
			markPanelNeedsReview(&evidence.Panels[panelIndex], &evidence.Summary, result.validations)
		}
	}
}

func validateWidget(
	ctx context.Context,
	client *signoz.Client,
	widget signoz.Widget,
	panel reporttypes.PanelRecord,
	variables map[string]any,
	variableTypes map[string]string,
	variableIssues map[string]VariableIssue,
	now time.Time,
	window time.Duration,
) ([]reporttypes.Validation, bool, error) {
	validations := initialValidations(panel)
	missing := missingWidgetVariables(widget, variables)
	if len(missing) > 0 {
		recordMissingVariableErrors(validations, panel, widget.Title, missing, variableIssues, now)
		return validations, false, nil
	}
	request, err := signoz.PreviewRequestForWidgetWindowWithVariableTypes(
		widget, variables, variableTypes, now, window,
	)
	if err != nil {
		return nil, false, err
	}
	previews, err := client.Preview(ctx, request)
	if err != nil {
		if apiError, ok := widgetScopedAPIError(err); ok {
			recordWidgetAPIError(validations, panel, now, widget.Title, "preview", apiError)
			return validations, false, nil
		}
		return nil, false, fmt.Errorf("preview widget %q: %w", widget.Title, err)
	}
	readyToExecute := applyPreviewResults(validations, panel, previews, now)
	if !readyToExecute {
		return validations, false, nil
	}

	executions, err := client.QueryRange(ctx, request)
	if err != nil {
		if apiError, ok := widgetScopedAPIError(err); ok {
			recordWidgetAPIError(validations, panel, now, widget.Title, "execution", apiError)
			return validations, false, nil
		}
		return nil, false, fmt.Errorf("execute widget %q: %w", widget.Title, err)
	}
	validations, allExecuted := applyExecutionResults(validations, panel, executions)
	return validations, allExecuted, nil
}

func initialValidations(panel reporttypes.PanelRecord) []reporttypes.Validation {
	validations := make([]reporttypes.Validation, len(panel.Queries))
	for index, query := range panel.Queries {
		validations[index] = query.Validation
	}
	return validations
}

func recordMissingVariableErrors(
	validations []reporttypes.Validation,
	panel reporttypes.PanelRecord,
	widgetTitle string,
	missing []string,
	variableIssues map[string]VariableIssue,
	now time.Time,
) {
	reasonCodes := []string{string(model.ReasonMissingVariableValue)}
	details := make([]string, 0)
	for _, name := range missing {
		issue, ok := variableIssues[name]
		if !ok {
			continue
		}
		for _, reason := range issue.Reasons {
			reasonCodes = appendUniqueString(reasonCodes, string(reason))
		}
		if issue.Detail != "" {
			details = append(details, name+": "+issue.Detail)
		}
	}
	errorMessage := fmt.Sprintf(
		"widget %q references dashboard variables without resolved target values: %s",
		widgetTitle, strings.Join(missing, ", "),
	)
	if len(details) > 0 {
		errorMessage += " (" + strings.Join(details, "; ") + ")"
	}
	for index, query := range panel.Queries {
		if !queryEligible(query) {
			continue
		}
		validation := validations[index]
		validation.CheckedAt = now.UTC().Format(time.RFC3339Nano)
		validation.ErrorCode = string(model.ReasonMissingVariableValue)
		validation.Error = errorMessage
		validation.MissingVariables = append([]string(nil), missing...)
		validation.ReasonCodes = append([]string(nil), reasonCodes...)
		validations[index] = validation
	}
}

func applyPreviewResults(
	validations []reporttypes.Validation,
	panel reporttypes.PanelRecord,
	previews map[string]signoz.PreviewResult,
	now time.Time,
) bool {
	readyToExecute := true
	ownedPreviewNames := make(map[string]bool)
	for index, query := range panel.Queries {
		validation := query.Validation
		if !queryEligible(query) {
			validations[index] = validation
			continue
		}
		validation.Previewed = true
		validation.CheckedAt = now.UTC().Format(time.RFC3339Nano)
		validation.PreviewOK = true
		for _, name := range emittedPreviewNames(query) {
			ownedPreviewNames[name] = true
			preview, found := previews[name]
			if !found {
				validation.PreviewOK = false
				validation.ErrorCode = "PREVIEW_RESULT_MISSING"
				validation.Error = fmt.Sprintf("SigNoz preview response did not include emitted query %q", name)
				continue
			}
			validation.PreviewStatements = append(validation.PreviewStatements, preview.Statements...)
			validation.PreviewWarnings = append(validation.PreviewWarnings, preview.Warnings...)
			if preview.Valid {
				if previewErrorPresent(preview.Error) {
					validation.PreviewOK = false
					validation.ErrorCode = "PREVIEW_RESPONSE_INCONSISTENT"
					validation.Error = fmt.Sprintf("SigNoz preview marked emitted query %q valid while also returning an error", name)
				}
				continue
			}
			validation.PreviewOK = false
			code, message := previewFailure(preview.Error)
			if name != queryName(query.RefID) {
				code = "DEPENDENCY_" + code
				message = fmt.Sprintf("emitted dependency %q: %s", name, message)
			}
			validation.ErrorCode, validation.Error = code, message
		}
		readyToExecute = readyToExecute && validation.PreviewOK
		validations[index] = validation
	}
	return rejectUntrackedPreviews(validations, panel, previews, ownedPreviewNames, readyToExecute)
}

func rejectUntrackedPreviews(
	validations []reporttypes.Validation,
	panel reporttypes.PanelRecord,
	previews map[string]signoz.PreviewResult,
	ownedPreviewNames map[string]bool,
	readyToExecute bool,
) bool {
	for name, preview := range previews {
		if preview.Valid || ownedPreviewNames[name] {
			continue
		}
		readyToExecute = false
		for index, query := range panel.Queries {
			if !queryEligible(query) {
				continue
			}
			validation := validations[index]
			validation.PreviewOK = false
			validation.ErrorCode = "UNTRACKED_PREVIEW_REJECTED"
			validation.Error = fmt.Sprintf("SigNoz rejected untracked emitted query %q", name)
			validations[index] = validation
		}
	}
	return readyToExecute
}

func applyExecutionResults(
	validations []reporttypes.Validation,
	panel reporttypes.PanelRecord,
	executions map[string]signoz.QueryExecutionResult,
) ([]reporttypes.Validation, bool) {
	allExecuted := true
	for index, query := range panel.Queries {
		if !queryEligible(query) {
			continue
		}
		execution, found := executions[queryName(query.RefID)]
		if !found {
			validation := validations[index]
			validation.ErrorCode = "EXECUTION_RESULT_MISSING"
			validation.Error = "SigNoz execution response did not include the emitted query"
			validations[index] = validation
			allExecuted = false
			continue
		}
		validation := validations[index]
		validation.Executed = true
		validation.DataPresent = execution.HasData()
		validation.Series = execution.Series
		validation.Points = execution.Points
		validation.Rows = execution.Rows
		validation.Samples = sampleSeries(execution.Sample)
		validations[index] = validation
	}
	return validations, allExecuted
}

func missingWidgetVariables(widget signoz.Widget, values map[string]any) []string {
	missing := make(map[string]bool)
	collect := func(expression string) {
		for _, name := range transpile.VariableNames(expression) {
			if _, ok := values[name]; !ok {
				missing[name] = true
			}
		}
	}
	for _, query := range widget.Query.PromQL {
		collect(query.Query)
	}
	for _, query := range widget.Query.Builder.QueryData {
		collect(query.Filter.Expression)
	}
	for _, formula := range widget.Query.Builder.QueryFormulas {
		collect(formula.Expression)
	}
	result := make([]string, 0, len(missing))
	for name := range missing {
		result = append(result, name)
	}
	slices.Sort(result)
	return result
}

func markQueryNeedsReview(query *reporttypes.QueryRecord, summary *reporttypes.Summary, reasons []string) {
	if query.Verdict != string(model.VerdictNeedsReview) {
		switch query.Verdict {
		case string(model.VerdictNative):
			if summary.Native > 0 {
				summary.Native--
			}
		case string(model.VerdictPassthrough):
			if summary.Passthrough > 0 {
				summary.Passthrough--
			}
		}
		summary.NeedsReview++
		query.Verdict = string(model.VerdictNeedsReview)
	}
	for _, reason := range reasons {
		query.ReasonCodes = appendUniqueString(query.ReasonCodes, reason)
	}
}

func markPanelNeedsReview(panel *reporttypes.PanelRecord, summary *reporttypes.Summary, validations []reporttypes.Validation) {
	if panel.Verdict != string(model.VerdictNeedsReview) {
		switch panel.Verdict {
		case string(model.VerdictNative):
			if summary.PanelsNative > 0 {
				summary.PanelsNative--
			}
		case string(model.VerdictPassthrough):
			if summary.PanelsPassthrough > 0 {
				summary.PanelsPassthrough--
			}
		}
		summary.PanelsNeedsReview++
		panel.Verdict = string(model.VerdictNeedsReview)
	}
	for _, validation := range validations {
		for _, reason := range validation.ReasonCodes {
			panel.ReasonCodes = appendUniqueString(panel.ReasonCodes, reason)
		}
	}
	panel.State = "needs-review"
}

func appendUniqueString(values []string, value string) []string {
	if value != "" && !slices.Contains(values, value) {
		return append(values, value)
	}
	return values
}

func widgetScopedAPIError(err error) (*signoz.APIError, bool) {
	var apiError *signoz.APIError
	if !errors.As(err, &apiError) {
		return nil, false
	}
	switch apiError.StatusCode {
	case http.StatusBadRequest, http.StatusConflict, http.StatusRequestEntityTooLarge,
		http.StatusUnprocessableEntity:
		return apiError, true
	}
	return apiError, apiError.StatusCode >= http.StatusInternalServerError
}

func recordWidgetAPIError(
	validations []reporttypes.Validation,
	panel reporttypes.PanelRecord,
	now time.Time,
	widgetTitle string,
	stage string,
	apiError *signoz.APIError,
) {
	code := apiError.Code
	if code == "" {
		code = strings.ToUpper(stage) + "_API_ERROR"
	}
	for index, query := range panel.Queries {
		if query.EmittedKind == "none" {
			continue
		}
		validation := validations[index]
		if stage == "preview" {
			validation.Previewed = true
			validation.PreviewOK = false
		}
		validation.CheckedAt = now.UTC().Format(time.RFC3339Nano)
		validation.ErrorCode = code
		validation.Error = fmt.Sprintf("%s widget %q: %s", stage, widgetTitle, apiError)
		validation.HTTPStatus = apiError.StatusCode
		validations[index] = validation
	}
}

func queryName(refID string) string {
	if strings.TrimSpace(refID) == "" {
		return "A"
	}
	return refID
}

func queryEligible(query reporttypes.QueryRecord) bool {
	return !query.Disabled && query.EmittedKind != "none"
}

func emittedPreviewNames(query reporttypes.QueryRecord) []string {
	if query.EmittedKind == "formula" && query.Formula != nil {
		names := make([]string, 0, len(query.Formula.Queries)+1)
		for _, dependency := range query.Formula.Queries {
			names = append(names, dependency.Name)
		}
		names = append(names, query.Formula.Name)
		return names
	}
	if query.EmittedKind == "builder" && query.Builder != nil && query.Builder.Name != "" {
		return []string{query.Builder.Name}
	}
	return []string{queryName(query.RefID)}
}

func widgetQueryCount(widget signoz.Widget) int {
	if widget.Query.QueryType == "builder" {
		return len(widget.Query.Builder.QueryData) + len(widget.Query.Builder.QueryFormulas)
	}
	return len(widget.Query.PromQL)
}

func previewFailure(raw json.RawMessage) (string, string) {
	if len(raw) == 0 || string(raw) == "null" {
		return "PREVIEW_REJECTED", "SigNoz rejected the emitted query without error detail"
	}
	var message string
	if json.Unmarshal(raw, &message) == nil {
		return "PREVIEW_REJECTED", message
	}
	var object struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &object) == nil && object.Message != "" {
		if object.Code == "" {
			object.Code = "PREVIEW_REJECTED"
		}
		return object.Code, object.Message
	}
	return "PREVIEW_REJECTED", string(raw)
}

func previewErrorPresent(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}

func resetValidationSummary(summary *reporttypes.Summary) {
	summary.Previewed = 0
	summary.PreviewValid = 0
	summary.PreviewInvalid = 0
	summary.ValidationEligible = 0
	summary.ValidationFailed = 0
	summary.Executed = 0
	summary.DataPresent = 0
	summary.DataAbsent = 0
}

func addValidationSummary(summary *reporttypes.Summary, validation reporttypes.Validation, eligible bool) {
	if eligible {
		summary.ValidationEligible++
		if !validation.PreviewOK || !validation.Executed || validation.ErrorCode != "" {
			summary.ValidationFailed++
		}
	}
	if validation.Previewed {
		summary.Previewed++
		if validation.PreviewOK {
			summary.PreviewValid++
		} else {
			summary.PreviewInvalid++
		}
	}
	if validation.Executed {
		summary.Executed++
		if validation.DataPresent {
			summary.DataPresent++
		} else {
			summary.DataAbsent++
		}
	}
}
