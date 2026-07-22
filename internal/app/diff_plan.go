package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mansiverma897993/noz-in/internal/diff"
	"github.com/mansiverma897993/noz-in/internal/model"
	sourceprometheus "github.com/mansiverma897993/noz-in/internal/source/prometheus"
	"github.com/mansiverma897993/noz-in/internal/target/signoz"
	"github.com/mansiverma897993/noz-in/internal/transpile"
)

func planDifferentialQueries(
	report *DifferentialReport,
	runtime differentialRuntime,
	options DifferentialOptions,
) ([]differentialTask, error) {
	var tasks []differentialTask
	eligible := 0
	for _, panel := range runtime.dashboard.Panels {
		widget, panelEmitted := runtime.widgets[panel.SourcePath]
		for _, query := range panel.Queries {
			task, scheduled, err := planDifferentialQuery(report, runtime, panel, query, widget, panelEmitted, options, eligible)
			if err != nil {
				return nil, err
			}
			if scheduled {
				tasks = append(tasks, task)
				eligible++
			}
		}
	}
	return tasks, nil
}

func planDifferentialQuery(
	report *DifferentialReport,
	runtime differentialRuntime,
	panel model.Panel,
	query model.Query,
	widget signoz.Widget,
	panelEmitted bool,
	options DifferentialOptions,
	eligible int,
) (differentialTask, bool, error) {
	translation, ok := differentialTranslationFor(runtime, query)
	record := DifferentialQuery{PanelTitle: panel.Title, RefID: query.RefID, SourcePath: query.SourcePath}
	if ok {
		record.Verdict = translation.Decision.Verdict
		record.Reasons = translation.Decision.Reasons
	}
	identity, err := bindDifferentialIdentity(widget, query, translation, ok, panelEmitted)
	if err != nil {
		return differentialTask{}, false, fmt.Errorf("bind differential query %q to emitted specification: %w", query.SourcePath, err)
	}
	record.TargetExpression = identity.TargetExpression
	record.TargetKind = identity.TargetKind
	record.TargetQueryName = identity.TargetQueryName
	record.TargetSpecSHA256 = identity.SHA256
	record.Stats.Status = diff.StatusSkipped
	report.Comparisons = append(report.Comparisons, record)
	index := len(report.Comparisons) - 1

	if reason := differentialSkipReason(query, translation, ok, panelEmitted, options.MaxQueries, eligible); reason != "" {
		report.Comparisons[index].SkippedReason = reason
		return differentialTask{}, false, nil
	}
	return prepareDifferentialTask(report, runtime, query, widget, identity, options, index)
}

func differentialTranslationFor(runtime differentialRuntime, query model.Query) (model.Translation, bool) {
	if runtime.evidenceBindings == nil {
		return runtime.migration.TranslationFor(query)
	}
	binding, found := runtime.evidenceBindings[query.SourcePath]
	if !found {
		return model.Translation{}, false
	}
	kind := model.TranslationKind(binding.query.EmittedKind)
	if !binding.panel.PrimaryArtifact {
		kind = model.TranslationNone
	}
	reasons := make([]model.ReasonCode, 0, len(binding.query.ReasonCodes))
	for _, reason := range binding.query.ReasonCodes {
		reasons = append(reasons, model.ReasonCode(reason))
	}
	return model.Translation{
		Kind:   kind,
		PromQL: binding.query.PromQL,
		Decision: model.Decision{
			Verdict: model.Verdict(binding.query.Verdict),
			Reasons: reasons,
		},
	}, true
}

func bindDifferentialIdentity(
	widget signoz.Widget,
	query model.Query,
	translation model.Translation,
	translated bool,
	panelEmitted bool,
) (emittedQueryIdentity, error) {
	if !translated || translation.Kind == model.TranslationNone || !panelEmitted {
		return nonEmittedQuerySpec(query.RefID)
	}
	identity, found, err := emittedQuerySpec(widget, query.RefID)
	if err == nil && !found {
		err = fmt.Errorf("query was not present in emitted widget %q", widget.Title)
	}
	return identity, err
}

func differentialSkipReason(
	query model.Query,
	translation model.Translation,
	translated bool,
	panelEmitted bool,
	maxQueries int,
	eligible int,
) string {
	switch {
	case query.Hidden:
		return "hidden Grafana target"
	case !translated || translation.Kind == model.TranslationNone:
		return "query was not emitted"
	case !panelEmitted:
		return "panel was deliberately omitted"
	case maxQueries > 0 && eligible >= maxQueries:
		return "query limit reached"
	default:
		return ""
	}
}

func prepareDifferentialTask(
	report *DifferentialReport,
	runtime differentialRuntime,
	query model.Query,
	widget signoz.Widget,
	identity emittedQueryIdentity,
	options DifferentialOptions,
	index int,
) (differentialTask, bool, error) {
	evaluationStep, queryWindow := differentialQueryWindow(widget, query.RefID, options)
	sourceExpression, missingSource := runtime.analyzer.MaterializeSourceQueryForWindow(
		query,
		map[string]string(runtime.sourceResolution.Values),
		runtime.sourceResolution.Multi,
		queryWindow.Start,
		queryWindow.End,
	)
	missingTarget := missingWidgetVariables(widget, map[string]any(runtime.targetResolution.Values))
	record := &report.Comparisons[index]
	record.SourceExpression = sourceExpression
	record.EvaluationStepMillis = evaluationStep.Milliseconds()
	record.Window = &queryWindow
	record.MissingSource = missingSource
	record.MissingTarget = missingTarget
	if len(missingSource) > 0 || len(missingTarget) > 0 {
		markDifferentialVariableResolution(
			record, missingSource, missingTarget,
			runtime.sourceResolution.Issues, runtime.targetResolution.Issues,
		)
		record.SkippedReason = "missing dashboard variable value"
		return differentialTask{}, false, nil
	}
	aliases, aliasBindings, aliasErr := scopedVariableAliases(
		runtime.dashboard,
		runtime.sourceResolution.Values,
		runtime.targetResolution.Values,
		variableNameSet(query.Expression),
		targetVariableNamesForQuery(widget, identity),
		func(variableName, labelName string) bool {
			return differentialAliasHasExactLabelProvenance(
				runtime, query, widget, identity, variableName, labelName, queryWindow,
			)
		},
	)
	if aliasErr != nil {
		record.Verdict = model.VerdictNeedsReview
		record.SkippedReason = "conflicting dashboard variable label aliases: " + aliasErr.Error()
		return differentialTask{}, false, nil
	}
	targetRequest, err := signoz.DashboardRequestForWidgetWindowWithVariableTypes(
		widget,
		map[string]any(runtime.targetResolution.Values),
		runtime.targetVarTypes,
		queryWindow.End,
		queryWindow.End.Sub(queryWindow.Start),
	)
	if err != nil {
		record.Stats.Status = diff.StatusError
		record.Error = "construct emitted target request: " + err.Error()
		return differentialTask{}, false, nil
	}
	artifact, artifactHash, err := differentialArtifact(targetRequest)
	if err != nil {
		record.Stats.Status = diff.StatusError
		record.Error = err.Error()
		return differentialTask{}, false, nil
	}
	record.LabelValueAliases = aliases
	record.LabelValueAliasBindings = aliasBindings
	record.TargetArtifact = artifact
	record.TargetArtifactSHA256 = artifactHash
	if targetRequest.RequestType != "time_series" {
		record.SkippedReason = fmt.Sprintf(
			"exact differential comparison does not yet normalize %s target results",
			targetRequest.RequestType,
		)
		return differentialTask{}, false, nil
	}
	return differentialTask{
		index: index, sourceExpression: sourceExpression, targetRequest: targetRequest,
		targetQueryName: identity.TargetQueryName, targetKind: diff.TargetKind(identity.TargetKind), step: evaluationStep,
		window: queryWindow, labelValueAliases: aliases,
	}, true, nil
}

func executeDifferentialTasks(
	ctx context.Context,
	comparisons []DifferentialQuery,
	tasks []differentialTask,
	runtime differentialRuntime,
	workers int,
) {
	jobs := make(chan differentialTask)
	var waitGroup sync.WaitGroup
	workerCount := min(workers, max(len(tasks), 1))
	for range workerCount {
		waitGroup.Go(func() {
			for task := range jobs {
				comparisons[task.index] = executeDifferentialTask(
					ctx,
					comparisons[task.index],
					task,
					runtime.sourceClient,
					runtime.targetClient,
					runtime.compareOptions,
				)
			}
		})
	}
	for _, task := range tasks {
		jobs <- task
	}
	close(jobs)
	waitGroup.Wait()
}

func alignedDifferentialWindow(now time.Time, lookback, step time.Duration) DifferentialWindow {
	stepMillis := max(step.Milliseconds(), int64(1))
	floorMillis := func(value int64) int64 {
		remainder := value % stepMillis
		if remainder < 0 {
			remainder += stepMillis
		}
		return value - remainder
	}
	endMillis := floorMillis(now.UnixMilli())
	startMillis := floorMillis(endMillis - lookback.Milliseconds())
	if startMillis >= endMillis {
		startMillis = endMillis - stepMillis
	}
	location := now.Location()
	return DifferentialWindow{
		Start:      time.UnixMilli(startMillis).In(location),
		End:        time.UnixMilli(endMillis).In(location),
		StepMillis: stepMillis,
	}
}

func executeDifferentialTask(
	ctx context.Context,
	record DifferentialQuery,
	task differentialTask,
	sourceClient *sourceprometheus.Client,
	targetClient *signoz.Client,
	options diff.Options,
) DifferentialQuery {
	if task.step <= 0 {
		task.step = time.Minute
	}
	sourceSeries, err := sourceClient.QueryRange(ctx, task.sourceExpression, task.window.Start, task.window.End, task.step)
	if err != nil {
		record.Stats.Status = diff.StatusError
		record.Error = "source query: " + err.Error()
		return record
	}
	targetResults, err := targetClient.QueryRangeSeries(ctx, task.targetRequest)
	if err != nil {
		record.Stats.Status = diff.StatusError
		record.Error = "target query: " + err.Error()
		return record
	}
	options.TargetKind = task.targetKind
	options.LabelValueAliases = task.labelValueAliases
	options.TargetTimestampShift = 0
	if task.targetKind == diff.TargetKindBuilderQuery || task.targetKind == diff.TargetKindBuilderFormula {
		options.TargetTimestampShift = task.step
	}
	record.Stats = diff.Compare(sourceSeries, targetResults[task.targetQueryName], options)
	return record
}

func differentialArtifact(request signoz.QueryRangeRequest) (json.RawMessage, string, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, "", fmt.Errorf("encode emitted target artifact: %w", err)
	}
	digest, err := canonicalJSONSHA256(encoded)
	if err != nil {
		return nil, "", fmt.Errorf("hash emitted target artifact: %w", err)
	}
	return encoded, digest, nil
}

func canonicalJSONSHA256(value json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(value)) == 0 {
		return "", fmt.Errorf("JSON value is empty")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, value); err != nil {
		return "", fmt.Errorf("compact JSON: %w", err)
	}
	if compact.Len() == 0 || compact.Bytes()[0] != '{' {
		return "", fmt.Errorf("expected a JSON object")
	}
	digest := sha256.Sum256(compact.Bytes())
	return fmt.Sprintf("%x", digest[:]), nil
}

func emittedQueryStep(
	widget signoz.Widget,
	refID string,
	fallback time.Duration,
	window time.Duration,
) time.Duration {
	name := strings.TrimSpace(refID)
	if name == "" {
		name = "A"
	}
	if widget.Query.QueryType == "promql" {
		return signoz.RecommendedPromQLStep(window)
	}
	for _, query := range widget.Query.Builder.QueryData {
		if query.QueryName == name {
			return effectiveBuilderMetricStep(query.StepInterval, window)
		}
	}
	for _, formula := range widget.Query.Builder.QueryFormulas {
		if formula.QueryName != name {
			continue
		}
		return effectiveBuilderFormulaStep(widget.Query.Builder, formula.Expression, window)
	}
	if fallback <= 0 {
		return time.Minute
	}
	return fallback
}

func effectiveBuilderMetricStep(configuredSeconds int, window time.Duration) time.Duration {
	configured := time.Duration(max(configuredSeconds, 60)) * time.Second
	return max(configured, signoz.MinimumMetricStep(window))
}

var formulaVariablePattern = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z0-9_]+)*`)

func effectiveBuilderFormulaStep(
	builder signoz.BuilderContainer,
	expression string,
	window time.Duration,
) time.Duration {
	references := make(map[string]bool)
	for _, token := range formulaVariablePattern.FindAllString(expression, -1) {
		name, _, _ := strings.Cut(token, ".")
		references[name] = true
	}
	var result time.Duration
	for _, query := range builder.QueryData {
		if !references[query.QueryName] {
			continue
		}
		step := effectiveBuilderMetricStep(query.StepInterval, window)
		if result == 0 {
			result = step
		} else {
			result = greatestCommonStep(result, step)
		}
	}
	if result == 0 {
		return time.Minute
	}
	return result
}

func greatestCommonStep(left, right time.Duration) time.Duration {
	for right != 0 {
		left, right = right, left%right
	}
	return left
}

func differentialQueryWindow(
	widget signoz.Widget,
	refID string,
	options DifferentialOptions,
) (time.Duration, DifferentialWindow) {
	evaluationStep := emittedQueryStep(widget, refID, options.Step, options.Range)
	window := alignedDifferentialWindow(options.Now, options.Range, evaluationStep)
	// The backend derives an unset PromQL step and clamps Builder metric steps
	// from the exact request start/end. Stabilize the aligned window against
	// that policy so the source and target evaluate the same interval.
	for range 3 {
		actual := emittedQueryStep(widget, refID, options.Step, window.End.Sub(window.Start))
		if actual == evaluationStep {
			break
		}
		evaluationStep = actual
		window = alignedDifferentialWindow(options.Now, options.Range, evaluationStep)
	}
	return evaluationStep, window
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

func markDifferentialVariableResolution(
	record *DifferentialQuery,
	missingSource []string,
	missingTarget []string,
	sourceIssues map[string]variableResolutionIssue,
	targetIssues map[string]variableResolutionIssue,
) {
	record.Verdict = model.VerdictNeedsReview
	record.Reasons = appendUniqueReasonCode(record.Reasons, model.ReasonMissingVariableValue)
	for _, name := range missingSource {
		for _, reason := range sourceIssues[name].Reasons {
			record.Reasons = appendUniqueReasonCode(record.Reasons, reason)
		}
	}
	for _, name := range missingTarget {
		for _, reason := range targetIssues[name].Reasons {
			record.Reasons = appendUniqueReasonCode(record.Reasons, reason)
		}
	}
}

func appendUniqueReasonCode(reasons []model.ReasonCode, reason model.ReasonCode) []model.ReasonCode {
	if !slices.Contains(reasons, reason) {
		return append(reasons, reason)
	}
	return reasons
}
