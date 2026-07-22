package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mansiverma897993/noz-in/internal/migrate"
	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/mansiverma897993/noz-in/internal/report"
	"github.com/mansiverma897993/noz-in/internal/target/signoz"
	"github.com/mansiverma897993/noz-in/internal/transpile"
	"github.com/mansiverma897993/noz-in/internal/validate"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
)

type grafanaMigrationRun struct {
	options             GrafanaOptions
	startedAt           time.Time
	client              *signoz.Client
	baseAnalyzerOptions transpile.Options
	probeAnalyzer       *transpile.Analyzer
	metrics             map[string]model.TargetMetric
	missingMetrics      map[string]bool
	metadataErrors      map[string]bool
	metadataStart       time.Time
	metadataEnd         time.Time
	overrides           []DashboardOverride
}

type preparedGrafanaDashboard struct {
	payload                     signoz.DashboardV5
	importPayload               signoz.DashboardV5
	evidence                    reporttypes.Report
	validationPassed            bool
	rejectedWidgets             []string
	blockedWidgets              []string
	missingVariableValidation   bool
	importableExecutableWidgets int
	partialImportEligible       bool
	promotions                  []validate.PromotionRecord
	operationErr                error
}

func (run *grafanaMigrationRun) migrateDashboard(
	ctx context.Context,
	dashboard model.Dashboard,
	base string,
) (GrafanaResult, error) {
	result, managedCandidatePath := run.newGrafanaResult(base)
	prepared, err := run.prepareDashboard(ctx, dashboard)
	if err != nil {
		result.TargetAction = initialTargetAction(run.client, run.options.DryRun)
		result.TargetSkipped = "migration evidence could not be bound; target import was not attempted"
		return result, err
	}
	primaryArtifact, err := artifactBindingFor(result.DashboardPath, prepared.importPayload)
	if err != nil {
		result.TargetAction = initialTargetAction(run.client, run.options.DryRun)
		result.TargetSkipped = "primary dashboard evidence could not be bound; target import was not attempted"
		return result, err
	}
	prepared.evidence.PrimaryArtifact = primaryArtifact
	populateGrafanaResult(
		&result,
		prepared,
		run.client != nil && !run.options.DryRun,
		managedCandidatePath,
	)
	shouldImport := prepared.operationErr == nil && result.ImportRequested &&
		prepared.importableExecutableWidgets > 0 &&
		(!run.options.Validate || prepared.validationPassed || prepared.partialImportEligible)
	setInitialTargetOutcome(&result, prepared, run, shouldImport)
	if prepared.operationErr != nil && result.CandidateDashboardPath == "" && regularArtifactExists(managedCandidatePath) {
		prepared.evidence.Run.Flags["priorCandidatePreserved"] = true
		prepared.evidence.Run.Flags["priorCandidatePath"] = managedCandidatePath
	}
	snapshotGrafanaResult(&result, &prepared.evidence)

	destinations := []string{result.DashboardPath, result.ReportPath, result.HTMLPath}
	if result.CandidateDashboardPath != "" {
		destinations = append(destinations, result.CandidateDashboardPath)
	}
	if err := ensureArtifactDestinations(destinations...); err != nil {
		markPreImportArtifactFailure(&result)
		snapshotGrafanaResult(&result, &prepared.evidence)
		return result, finishGrafanaError(prepared.operationErr, err)
	}
	if err := publishGrafanaArtifactSet(&result, &prepared.evidence, prepared); err != nil {
		markPreImportArtifactFailure(&result)
		snapshotGrafanaResult(&result, &prepared.evidence)
		return result, finishGrafanaError(prepared.operationErr, err)
	}
	if err := run.checkpointGrafanaArtifacts(result); err != nil {
		repairErr := repairPreImportCheckpointFailure(&result, &prepared.evidence, prepared, err)
		return result, finishGrafanaError(prepared.operationErr, repairErr)
	}
	if run.options.EmitV6 {
		if path, err := writeV6Sibling(result.DashboardPath, prepared.importPayload); err == nil {
			result.V6Path = path
		} else {
			return result, finishGrafanaError(prepared.operationErr, err)
		}
	}
	if !shouldImport {
		return result, prepared.operationErr
	}
	return run.importPreparedDashboard(ctx, dashboard.Title, prepared, result)
}

func (run *grafanaMigrationRun) newGrafanaResult(base string) (GrafanaResult, string) {
	dashboardPath := filepath.Join(run.options.OutputDirectory, base+".signoz.json")
	managedCandidatePath := filepath.Join(run.options.OutputDirectory, base+".candidate.signoz.json")
	return GrafanaResult{
		DashboardPath: dashboardPath,
		ReportPath:    filepath.Join(run.options.OutputDirectory, base+".report.json"),
		HTMLPath:      filepath.Join(run.options.OutputDirectory, base+".report.html"),
	}, managedCandidatePath
}

func (run *grafanaMigrationRun) prepareDashboard(
	ctx context.Context,
	dashboard model.Dashboard,
) (preparedGrafanaDashboard, error) {
	prepared := preparedGrafanaDashboard{validationPassed: true}
	dashboardMetrics := run.metrics
	dashboardMissingMetrics := run.missingMetrics
	dashboardMetadataErrors := run.metadataErrors
	if run.client != nil {
		if err := resolveMetricMetadata(
			ctx, run.client, dashboard, run.probeAnalyzer, run.metrics, run.missingMetrics, run.metadataErrors,
			run.options.MetricNameMap, run.metadataStart, run.metadataEnd,
		); err != nil {
			prepared.operationErr = targetError(fmt.Errorf("resolve target metrics for %q: %w", dashboard.Title, err))
			dashboardMetrics = cloneTargetMetrics(run.metrics)
			dashboardMissingMetrics = cloneBoolMap(run.missingMetrics)
			dashboardMetadataErrors = cloneBoolMap(run.metadataErrors)
			markUnresolvedMetricMetadata(
				dashboard, run.probeAnalyzer, dashboardMetrics, dashboardMissingMetrics, dashboardMetadataErrors,
			)
		}
	}
	analyzerOptions := run.baseAnalyzerOptions
	analyzerOptions.Metrics = dashboardMetrics
	analyzerOptions.MissingMetrics = dashboardMissingMetrics
	analyzerOptions.MetadataErrors = dashboardMetadataErrors
	migration := migrate.Dashboard(dashboard, transpile.NewAnalyzer(analyzerOptions))
	if len(run.overrides) > 0 {
		applyOverrides(migration, run.overrides)
	}
	if prepared.operationErr == nil && run.client != nil && run.options.Validate {
		prepared.promotions = run.promoteNativeCandidates(ctx, dashboard, migration)
	}
	prepared.payload = signoz.EmitV5(migration)
	prepared.evidence = report.Build(migration)
	if err := annotateEmittedWidgetIDs(&prepared.evidence, prepared.payload); err != nil {
		return prepared, fmt.Errorf("bind emitted query evidence for %q: %w", dashboard.Title, err)
	}
	prepared.evidence.Run = run.evidenceRun()
	recordPromotionEvidence(&prepared.evidence, prepared.promotions)
	markMetricEvidence(
		dashboard, run.probeAnalyzer, dashboardMetrics, dashboardMissingMetrics, dashboardMetadataErrors, &prepared.evidence,
	)
	if prepared.operationErr == nil && run.client != nil && run.options.Validate {
		prepared.validationPassed, prepared.operationErr = run.validateDashboard(ctx, dashboard, &prepared)
	}
	prepared.finish(run)
	return prepared, nil
}

func (run *grafanaMigrationRun) evidenceRun() reporttypes.Run {
	return reporttypes.Run{
		StartedAt: run.startedAt.Format(time.RFC3339Nano), Target: run.options.TargetURL,
		Flags: map[string]any{
			"rateInterval": run.options.RateInterval.String(), "intervalDefault": run.options.Interval.String(),
			"range": run.options.Range.String(), "dryRun": run.options.DryRun, "offline": run.client == nil,
			"validationEnabled":  run.client != nil && run.options.Validate,
			"validationWorkers":  effectiveWorkers(run.options.ValidationWorkers),
			"recordingRuleFiles": len(run.options.RuleFiles), "metricNameMappings": len(run.options.MetricNameMap),
			"variableOverrides": sortedMapKeys(run.options.Variables),
			"allowInsecureHTTP": run.options.AllowInsecureHTTP,
		},
	}
}

// promoteNativeCandidates proves each Builder/formula candidate equivalent to its
// PromQL passthrough on the live target and promotes the passing ones in place, so
// the subsequent emission renders them as native Builder queries with drilldown.
func (run *grafanaMigrationRun) promoteNativeCandidates(
	ctx context.Context,
	dashboard model.Dashboard,
	migration model.Migration,
) []validate.PromotionRecord {
	variables := resolveTargetVariables(dashboard, run.options.Variables, nil)
	items, err := signoz.VariableItems(map[string]any(variables.Values), nil)
	if err != nil {
		return nil
	}
	threshold := run.options.FidelityThreshold
	if threshold <= 0 {
		threshold = 0.05
	}
	return validate.PromoteNativeCandidates(ctx, run.client, migration, validate.PromoteOptions{
		Now:                run.startedAt,
		Window:             run.options.Range,
		Variables:          items,
		RelativeTolerance:  threshold,
		AbsoluteTolerance:  1e-9,
		TimestampTolerance: time.Minute,
		MinimumPoints:      3,
	})
}

// recordPromotionEvidence surfaces the native-promotion outcome in the run flags
// so every promotion decision is auditable in the evidence report.
func recordPromotionEvidence(evidence *reporttypes.Report, promotions []validate.PromotionRecord) {
	if len(promotions) == 0 {
		return
	}
	promoted := 0
	reasons := make(map[string]int)
	for _, record := range promotions {
		if record.Promoted {
			promoted++
		}
		reasons[record.Reason]++
	}
	if evidence.Run.Flags == nil {
		evidence.Run.Flags = map[string]any{}
	}
	evidence.Run.Flags["nativeCandidates"] = len(promotions)
	evidence.Run.Flags["nativePromoted"] = promoted
	evidence.Run.Flags["nativePromotionReasons"] = reasons
}

func (run *grafanaMigrationRun) validateDashboard(
	ctx context.Context,
	dashboard model.Dashboard,
	prepared *preparedGrafanaDashboard,
) (bool, error) {
	variables := resolveTargetVariables(
		dashboard,
		run.options.Variables,
		signoz.DashboardVariableTypes(prepared.payload),
	)
	issues := make(map[string]validate.VariableIssue, len(variables.Issues))
	for name, issue := range variables.Issues {
		issues[name] = validate.VariableIssue{Reasons: issue.Reasons, Detail: issue.Detail}
	}
	passed, err := validate.Dashboard(
		ctx,
		run.client,
		prepared.payload,
		&prepared.evidence,
		map[string]any(variables.Values),
		validate.Options{Workers: run.options.ValidationWorkers, Window: run.options.Range, VariableIssues: issues},
	)
	if err != nil {
		return false, targetError(fmt.Errorf("preview dashboard %q in SigNoz: %w", dashboard.Title, err))
	}
	return passed, nil
}

func (prepared *preparedGrafanaDashboard) finish(run *grafanaMigrationRun) {
	prepared.importPayload = prepared.payload
	if prepared.operationErr == nil && run.client != nil && run.options.Validate && !prepared.validationPassed {
		prepared.importPayload, prepared.rejectedWidgets, prepared.blockedWidgets = validationSafeDashboard(
			prepared.payload,
			prepared.evidence,
		)
	}
	annotatePrimaryWidgetPresence(&prepared.evidence, prepared.importPayload)
	prepared.missingVariableValidation = hasMissingVariableValidation(prepared.evidence)
	candidateExecutableWidgets := enabledExecutableWidgetCount(prepared.payload)
	prepared.importableExecutableWidgets = enabledExecutableWidgetCount(prepared.importPayload)
	prepared.partialImportEligible = prepared.operationErr == nil && len(prepared.rejectedWidgets) > 0 &&
		len(prepared.blockedWidgets) == 0 && !prepared.missingVariableValidation &&
		prepared.importableExecutableWidgets > 0
	flags := prepared.evidence.Run.Flags
	flags["candidateWidgets"] = len(prepared.payload.Widgets)
	flags["candidateExecutableWidgets"] = candidateExecutableWidgets
	flags["importableWidgets"] = len(prepared.importPayload.Widgets)
	flags["importableExecutableWidgets"] = prepared.importableExecutableWidgets
	flags["validationRejectedWidgets"] = append([]string(nil), prepared.rejectedWidgets...)
	flags["validationBlockedWidgets"] = append([]string(nil), prepared.blockedWidgets...)
	flags["partialImportEligible"] = prepared.partialImportEligible
	flags["missingVariableValidation"] = prepared.missingVariableValidation
	report.RefreshSummary(&prepared.evidence)
}

func populateGrafanaResult(
	result *GrafanaResult,
	prepared preparedGrafanaDashboard,
	importRequested bool,
	managedCandidatePath string,
) {
	result.Summary = prepared.evidence.Summary
	result.ImportRequested = importRequested
	result.PartialImportEligible = prepared.partialImportEligible
	result.ValidationRejected = append([]string(nil), prepared.rejectedWidgets...)
	result.ValidationBlocked = append([]string(nil), prepared.blockedWidgets...)
	if len(prepared.rejectedWidgets) > 0 {
		result.CandidateDashboardPath = managedCandidatePath
	}
}

func setInitialTargetOutcome(
	result *GrafanaResult,
	prepared preparedGrafanaDashboard,
	run *grafanaMigrationRun,
	shouldImport bool,
) {
	if prepared.operationErr != nil {
		result.TargetAction = "skipped"
		result.TargetSkipped = "target validation failed; dashboard was not imported"
		result.TargetError = prepared.operationErr.Error()
		return
	}
	if shouldImport {
		result.TargetAction = "ready"
		result.TargetSkipped = "target preflight completed; the target request has not started"
		return
	}
	result.TargetAction = initialTargetAction(run.client, run.options.DryRun)
	result.TargetSkipped = nonImportReason(prepared, run)
}

func initialTargetAction(client *signoz.Client, dryRun bool) string {
	if client == nil {
		return "offline"
	}
	if dryRun {
		return "dry_run"
	}
	return "skipped"
}

func nonImportReason(prepared preparedGrafanaDashboard, run *grafanaMigrationRun) string {
	if run.client == nil {
		return ""
	}
	if prepared.missingVariableValidation {
		return "unresolved dashboard variable values prevented live validation; dashboard was not imported"
	}
	if run.options.DryRun {
		return dryRunReason(prepared, run.options.Validate)
	}
	if len(prepared.blockedWidgets) > 0 {
		return "one or more widget failures could not be safely isolated; dashboard was not modified"
	}
	if len(prepared.rejectedWidgets) > 0 {
		return "all executable widgets failed target validation; dashboard was not imported"
	}
	if prepared.importableExecutableWidgets == 0 {
		return "no executable widgets were emitted; dashboard was not imported"
	}
	return "one or more emitted queries failed target validation"
}

func dryRunReason(prepared preparedGrafanaDashboard, validationEnabled bool) string {
	if len(prepared.blockedWidgets) > 0 {
		return fmt.Sprintf(
			"dry run: %d widget failure(s) could not be safely isolated; no import decision was made",
			len(prepared.blockedWidgets),
		)
	}
	if !validationEnabled {
		return "dry run: target validation was not requested; dashboard was not imported"
	}
	if len(prepared.rejectedWidgets) > 0 {
		return fmt.Sprintf(
			"dry run: %d widget(s) failed target validation; validated remainder was not imported",
			len(prepared.rejectedWidgets),
		)
	}
	if prepared.importableExecutableWidgets == 0 {
		return "dry run: no executable widgets were emitted; dashboard was not imported"
	}
	return "dry run: target validation passed; dashboard was not imported"
}

func (run *grafanaMigrationRun) importPreparedDashboard(
	ctx context.Context,
	title string,
	prepared preparedGrafanaDashboard,
	result GrafanaResult,
) (GrafanaResult, error) {
	result.ImportAttempted = true
	result.TargetAction = "attempted"
	result.TargetSkipped = "target request may be in flight; the final outcome has not been recorded"
	if err := publishGrafanaArtifactSet(&result, &prepared.evidence, prepared); err != nil {
		markPreImportArtifactFailure(&result)
		snapshotGrafanaResult(&result, &prepared.evidence)
		return result, finishGrafanaError(nil, err)
	}
	if err := run.checkpointGrafanaArtifacts(result); err != nil {
		repairErr := repairPreImportCheckpointFailure(&result, &prepared.evidence, prepared, err)
		return result, finishGrafanaError(nil, repairErr)
	}

	writeResult, err := run.client.UpsertDashboard(ctx, prepared.importPayload)
	var operationErr error
	if err != nil {
		result.TargetAction = "failed"
		result.TargetSkipped = "target import failed; dashboard outcome is unchanged or unknown"
		operationErr = targetError(fmt.Errorf("write dashboard %q to SigNoz: %w", title, err))
		result.TargetError = operationErr.Error()
	} else {
		result.Target = &writeResult
		result.TargetAction = writeResult.Action
		result.TargetSkipped = ""
		result.TargetDashboardID = writeResult.ID
		result.ImportSucceeded = true
		result.PartialImportPerformed = prepared.partialImportEligible
		result.ImportedWidgets = len(prepared.importPayload.Widgets)
	}
	if err := publishGrafanaArtifactSet(&result, &prepared.evidence, prepared); err != nil {
		return result, finishGrafanaError(operationErr, err)
	}
	if err := run.checkpointGrafanaArtifacts(result); err != nil {
		return result, finishGrafanaError(operationErr, err)
	}
	return result, operationErr
}

func (run *grafanaMigrationRun) checkpointGrafanaArtifacts(result GrafanaResult) error {
	if run.options.ArtifactCheckpoint == nil {
		return nil
	}
	if err := run.options.ArtifactCheckpoint(result); err != nil {
		return fmt.Errorf("publish dashboard artifact checkpoint: %w", err)
	}
	return nil
}

func repairPreImportCheckpointFailure(
	result *GrafanaResult,
	evidence *reporttypes.Report,
	prepared preparedGrafanaDashboard,
	checkpointErr error,
) error {
	markPreImportArtifactFailure(result)
	snapshotGrafanaResult(result, evidence)
	if err := publishGrafanaArtifactSet(result, evidence, prepared); err != nil {
		return errors.Join(
			checkpointErr,
			fmt.Errorf("publish corrected pre-import dashboard evidence: %w", err),
		)
	}
	return checkpointErr
}

func markPreImportArtifactFailure(result *GrafanaResult) {
	result.ImportAttempted = false
	result.ImportSucceeded = false
	result.Target = nil
	result.TargetDashboardID = ""
	if result.ImportRequested && result.TargetError == "" {
		result.TargetAction = "skipped"
		result.TargetSkipped = "target import was not attempted because migration artifacts could not be published"
	}
}

func regularArtifactExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func snapshotGrafanaResult(result *GrafanaResult, evidence *reporttypes.Report) {
	recordTargetOutcome(evidence, *result)
	result.Summary = evidence.Summary
	result.Evidence = *evidence
}
