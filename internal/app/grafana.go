package app

// Grafana migration entry point: run options, per-dashboard results, and the
// MigrateGrafana batch orchestration with its run-level error combination.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/mansiverma897993/signoz/internal/stableidentity"
	"github.com/mansiverma897993/signoz/internal/target/signoz"
	"github.com/mansiverma897993/signoz/internal/transpile"
	"github.com/mansiverma897993/signoz/pkg/reporttypes"
)

// GrafanaOptions controls an offline Grafana migration run.
type GrafanaOptions struct {
	OutputDirectory     string
	RateInterval        time.Duration
	Interval            time.Duration
	Range               time.Duration
	TargetURL           string
	APIKey              string
	HTTPClient          *http.Client
	AllowInsecureHTTP   bool
	Validate            bool
	Variables           map[string]string
	MetricNameMap       map[string]string
	RuleFiles           []string
	ValidationWorkers   int
	DryRun              bool
	SourceNamespace     string
	DashboardIdentity   string
	SourcePathOverrides map[string]string
	ProtectedInputs     []ProtectedInputPath
	// ContinueOnInputError migrates every parseable dashboard in a batch instead
	// of aborting the whole run when one input fails to parse or validate. The
	// input errors are still reported and still influence the exit status.
	ContinueOnInputError bool
	// OverridesFile is a YAML file of operator/agent-provided Builder queries that
	// replace generated translations for named source paths. Overrides are still
	// verified live before they can emit natively.
	OverridesFile string
	// FidelityThreshold is the maximum relative deviation a live promotion will
	// accept when confirming a Builder candidate against its PromQL passthrough.
	// Zero uses the default of 0.05 (5%).
	FidelityThreshold float64
	// EmitV6 additionally writes the SigNoz v6 (Perses) dashboard shape as a
	// sibling <base>.v6.json. The v5 output remains the verified primary import
	// target; v6 is a transform of it for the v2 dashboard API.
	EmitV6 bool
	// ArtifactCheckpoint is invoked after each committed dashboard artifact
	// generation. Returning an error prevents a pending target write. Callers
	// that publish through a second durability boundary (for example MCP's
	// rooted output store) use this hook to make the attempted-write evidence
	// durable before SigNoz can be mutated.
	ArtifactCheckpoint func(GrafanaResult) error
	// outputPreCreateCheckpoint is a deterministic adversarial-test boundary
	// between batch destination preflight and secure output-root creation.
	outputPreCreateCheckpoint func() error
}

// GrafanaResult identifies the artifacts produced for one dashboard.
type GrafanaResult struct {
	DashboardPath          string                       `json:"dashboardPath"`
	CandidateDashboardPath string                       `json:"candidateDashboardPath,omitempty"`
	ReportPath             string                       `json:"reportPath"`
	HTMLPath               string                       `json:"htmlPath"`
	Summary                reporttypes.Summary          `json:"summary"`
	Target                 *signoz.DashboardWriteResult `json:"target,omitempty"`
	TargetSkipped          string                       `json:"targetSkipped,omitempty"`
	TargetError            string                       `json:"targetError,omitempty"`
	ImportRequested        bool                         `json:"importRequested"`
	ImportAttempted        bool                         `json:"importAttempted"`
	ImportSucceeded        bool                         `json:"importSucceeded"`
	TargetAction           string                       `json:"targetAction"`
	TargetDashboardID      string                       `json:"targetDashboardId,omitempty"`
	PartialImportEligible  bool                         `json:"partialImportEligible"`
	PartialImportPerformed bool                         `json:"partialImportPerformed"`
	ImportedWidgets        int                          `json:"importedWidgets,omitempty"`
	ValidationRejected     []string                     `json:"validationRejectedWidgets,omitempty"`
	ValidationBlocked      []string                     `json:"validationBlockedWidgets,omitempty"`
	// Published is true only after the on-disk artifact set has been durably
	// committed. Reporting surfaces must not claim artifacts were written unless
	// this is set.
	Published bool `json:"published"`
	// V6Path is the sibling Perses (v6) dashboard file, written only when EmitV6
	// is set and publication succeeded.
	V6Path   string             `json:"v6Path,omitempty"`
	Evidence reporttypes.Report `json:"-"`
}

// MigrateGrafana writes SigNoz v5 payloads and evidence reports.
func MigrateGrafana(ctx context.Context, paths []string, options GrafanaOptions) ([]GrafanaResult, error) {
	startedAt := time.Now().UTC()
	options = withGrafanaDefaults(options)
	if err := stableidentity.ValidateComponent("dashboard source namespace", options.SourceNamespace, 512); err != nil {
		return nil, inputError(err)
	}
	if err := stableidentity.ValidateComponent("dashboard source identity", options.DashboardIdentity, 4096); err != nil {
		return nil, inputError(err)
	}
	if strings.TrimSpace(options.TargetURL) != "" && strings.TrimSpace(options.SourceNamespace) == "" {
		return nil, inputError(fmt.Errorf(
			"dashboard source namespace is required for a live SigNoz target; set --source-namespace to a stable Grafana organization or source estate identifier",
		))
	}
	options.SourceNamespace = strings.TrimSpace(options.SourceNamespace)
	options.DashboardIdentity = strings.TrimSpace(options.DashboardIdentity)
	if options.OutputDirectory == "" {
		options.OutputDirectory = "out"
	}
	_, recordingRules, err := loadRuleSets(options.RuleFiles)
	if err != nil {
		return nil, inputError(err)
	}
	bases := artifactBases(paths)
	inputs, inputErr := prepareGrafanaInputs(paths, bases, options)
	if inputErr != nil && len(inputs) == 0 {
		return nil, inputErr
	}
	if err := preflightGrafanaOutput(
		options.OutputDirectory, bases, paths, options.RuleFiles, options.ProtectedInputs,
	); err != nil {
		return nil, inputError(err)
	}
	if options.outputPreCreateCheckpoint != nil {
		if err := options.outputPreCreateCheckpoint(); err != nil {
			return nil, inputError(fmt.Errorf("prepare dashboard output directory: %w", err))
		}
	}
	if err := ensureDirectory(options.OutputDirectory); err != nil {
		return nil, inputError(err)
	}

	baseAnalyzerOptions := transpile.Options{
		RateInterval:   options.RateInterval,
		Interval:       options.Interval,
		Range:          options.Range,
		RecordingRules: recordingRules,
	}
	probeAnalyzer := transpile.NewAnalyzer(baseAnalyzerOptions)
	var client *signoz.Client
	if options.TargetURL != "" {
		var err error
		client, err = signoz.NewClientWithOptions(
			options.TargetURL,
			options.APIKey,
			options.HTTPClient,
			signoz.ClientOptions{AllowInsecureHTTP: options.AllowInsecureHTTP},
		)
		if err != nil {
			return nil, targetError(err)
		}
	}
	metadataStart, metadataEnd := metricMetadataWindow(startedAt, options.Range)
	overrides, err := loadOverrides(options.OverridesFile)
	if err != nil {
		return nil, err
	}
	run := grafanaMigrationRun{
		options: options, startedAt: startedAt, client: client,
		baseAnalyzerOptions: baseAnalyzerOptions, probeAnalyzer: probeAnalyzer,
		metrics: mappedMetrics(options.MetricNameMap), missingMetrics: make(map[string]bool),
		metadataErrors: make(map[string]bool), metadataStart: metadataStart, metadataEnd: metadataEnd,
		overrides: overrides,
	}
	results := make([]GrafanaResult, 0, len(paths))
	var runErrors []error
	// In continue-on-error mode the skipped inputs are surfaced alongside the
	// migrated ones so the exit status still reflects the failures.
	if inputErr != nil {
		runErrors = append(runErrors, inputErr)
	}
	for _, input := range inputs {
		result, migrateErr := run.migrateDashboard(ctx, input.dashboard, input.base)
		results = append(results, result)
		if migrateErr != nil {
			runErrors = append(runErrors, migrateErr)
		}
	}
	return results, combineGrafanaRunErrors(runErrors)
}

func withGrafanaDefaults(options GrafanaOptions) GrafanaOptions {
	if options.RateInterval <= 0 {
		options.RateInterval = 5 * time.Minute
	}
	if options.Interval <= 0 {
		options.Interval = time.Minute
	}
	options.Interval = max(options.Interval.Truncate(time.Second), time.Minute)
	if options.Range <= 0 {
		options.Range = time.Hour
	}
	return options
}

func finishGrafanaError(operationErr error, artifactErr error) error {
	joined := make([]error, 0, 2)
	if operationErr != nil {
		joined = append(joined, operationErr)
	}
	if artifactErr != nil {
		joined = append(joined, fmt.Errorf("publish migration artifacts: %w", artifactErr))
	}
	if len(joined) == 0 {
		return nil
	}
	if len(joined) == 1 {
		return joined[0]
	}
	kind := ErrorInternal
	if operationErr != nil {
		kind = KindOf(operationErr)
	}
	return &Error{Kind: kind, Err: errors.Join(joined...)}
}

func combineGrafanaRunErrors(runErrors []error) error {
	if len(runErrors) == 0 {
		return nil
	}
	if len(runErrors) == 1 {
		return runErrors[0]
	}
	kind := ErrorInput
	for _, err := range runErrors {
		switch KindOf(err) {
		case ErrorTarget:
			kind = ErrorTarget
		case ErrorInternal:
			if kind != ErrorTarget {
				kind = ErrorInternal
			}
		}
	}
	return &Error{Kind: kind, Err: errors.Join(runErrors...)}
}

func effectiveWorkers(workers int) int {
	if workers > 0 {
		return workers
	}
	return 4
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
