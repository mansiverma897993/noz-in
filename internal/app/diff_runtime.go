package app

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/mansiverma897993/signoz/internal/diff"
	"github.com/mansiverma897993/signoz/internal/migrate"
	"github.com/mansiverma897993/signoz/internal/model"
	sourcegrafana "github.com/mansiverma897993/signoz/internal/source/grafana"
	sourceprometheus "github.com/mansiverma897993/signoz/internal/source/prometheus"
	"github.com/mansiverma897993/signoz/internal/target/signoz"
	"github.com/mansiverma897993/signoz/internal/transpile"
	"github.com/mansiverma897993/signoz/pkg/reporttypes"
)

type differentialRuntime struct {
	dashboard        model.Dashboard
	migration        model.Migration
	analyzer         *transpile.Analyzer
	sourceClient     *sourceprometheus.Client
	targetClient     *signoz.Client
	sourceResolution sourceVariableResolution
	targetResolution targetVariableResolution
	targetVarTypes   map[string]string
	widgets          map[string]signoz.Widget
	compareOptions   diff.Options
	evidenceBindings map[string]storedDifferentialBinding
	primaryArtifact  *reporttypes.ArtifactBinding
	materialization  reporttypes.DifferentialMaterialization
}

type storedDifferentialBinding struct {
	panel reporttypes.PanelRecord
	query reporttypes.QueryRecord
}

func canonicalEndpointIdentity(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("endpoint is empty")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("parse endpoint: %w", err)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("endpoint scheme must be http or https")
	}
	if parsed.Hostname() == "" || parsed.Opaque != "" {
		return "", fmt.Errorf("endpoint must include a host")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("endpoint must not contain credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("endpoint must not contain a query or fragment")
	}
	hostname := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		parsed.Host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		parsed.Host = "[" + hostname + "]"
	} else {
		parsed.Host = hostname
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	parsed.ForceQuery = false
	return parsed.String(), nil
}

func normalizeDifferentialOptions(options DifferentialOptions) (DifferentialOptions, diff.Options, error) {
	sourceURL, err := canonicalEndpointIdentity(options.SourceURL)
	if err != nil {
		return DifferentialOptions{}, diff.Options{}, fmt.Errorf("invalid source endpoint: %w", err)
	}
	targetURL, err := canonicalEndpointIdentity(options.TargetURL)
	if err != nil {
		return DifferentialOptions{}, diff.Options{}, fmt.Errorf("invalid target endpoint: %w", err)
	}
	options.SourceURL = sourceURL
	options.TargetURL = targetURL
	options.TargetProvenance = strings.TrimSpace(options.TargetProvenance)
	if options.RateInterval <= 0 {
		options.RateInterval = 5 * time.Minute
	}
	if options.Interval <= 0 {
		options.Interval = time.Minute
	}
	options.Interval = max(options.Interval.Truncate(time.Second), time.Minute)
	if options.Range <= 0 {
		options.Range = 30 * time.Minute
	}
	if options.Step <= 0 {
		options.Step = time.Minute
	}
	if options.TimestampTolerance <= 0 {
		options.TimestampTolerance = options.Step
	}
	if options.Workers <= 0 {
		options.Workers = 4
	}
	if options.Now.IsZero() {
		options.Now = time.Now()
	}
	compareOptions := diff.Options{
		TimestampTolerance:   options.TimestampTolerance,
		TargetProvenance:     diff.TargetProvenance(options.TargetProvenance),
		RelativeTolerance:    options.RelativeTolerance,
		AbsoluteTolerance:    options.AbsoluteTolerance,
		MinimumCoverage:      options.MinimumCoverage,
		MinimumMatchedPoints: options.MinimumMatchedPoints,
		// The comparator applies the canonical job->service.name /
		// instance->service.instance.id resource-label rename by default
		// (diff.withDefaults), reconciling the same mapping the migration performs.
	}
	if err := diff.ValidateOptions(compareOptions); err != nil {
		return DifferentialOptions{}, diff.Options{}, err
	}
	if options.MaxQueries < 0 {
		return DifferentialOptions{}, diff.Options{}, fmt.Errorf("maximum queries must not be negative")
	}
	return options, compareOptions, nil
}

func differentialMaterializationFromOptions(
	options DifferentialOptions,
) (reporttypes.DifferentialMaterialization, transpile.Options) {
	record := reporttypes.DifferentialMaterialization{
		RateInterval: options.RateInterval.String(),
		Interval:     options.Interval.String(),
		Range:        options.Range.String(),
	}
	return record, transpile.Options{
		RateInterval: options.RateInterval,
		Interval:     options.Interval,
		Range:        options.Range,
	}
}

func differentialMaterializationFromEvidence(
	evidence reporttypes.Report,
) (reporttypes.DifferentialMaterialization, transpile.Options, error) {
	rateInterval, err := migrationDurationFlag(evidence, "rateInterval")
	if err != nil {
		return reporttypes.DifferentialMaterialization{}, transpile.Options{}, err
	}
	interval, err := migrationDurationFlag(evidence, "intervalDefault")
	if err != nil {
		return reporttypes.DifferentialMaterialization{}, transpile.Options{}, err
	}
	queryRange, err := migrationDurationFlag(evidence, "range")
	if err != nil {
		return reporttypes.DifferentialMaterialization{}, transpile.Options{}, err
	}
	record := reporttypes.DifferentialMaterialization{
		RateInterval: rateInterval.String(),
		Interval:     interval.String(),
		Range:        queryRange.String(),
	}
	return record, transpile.Options{
		RateInterval: rateInterval,
		Interval:     interval,
		Range:        queryRange,
	}, nil
}

func migrationDurationFlag(evidence reporttypes.Report, name string) (time.Duration, error) {
	raw, found := evidence.Run.Flags[name]
	if !found {
		return 0, fmt.Errorf("migration report has no %q materialization setting; rerun migration", name)
	}
	value, ok := raw.(string)
	if !ok || strings.TrimSpace(value) == "" {
		return 0, fmt.Errorf("migration report has an invalid %q materialization setting; rerun migration", name)
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("migration report has an invalid %q materialization setting %q; rerun migration", name, value)
	}
	return duration, nil
}

func prepareDifferentialRuntime(
	ctx context.Context,
	path string,
	options DifferentialOptions,
	compareOptions diff.Options,
) (differentialRuntime, error) {
	dashboard, err := sourcegrafana.ParseFile(path)
	if err != nil {
		return differentialRuntime{}, inputError(err)
	}
	var storedDashboard *signoz.DashboardV5
	var evidenceBindings map[string]storedDifferentialBinding
	var primaryArtifact *reporttypes.ArtifactBinding
	resolutionDashboard := dashboard
	materialization, analyzerOptions := differentialMaterializationFromOptions(options)
	if strings.TrimSpace(options.MigrationReportPath) != "" {
		evidence, reportData, readErr := readMigrationEvidence(options.MigrationReportPath)
		if readErr != nil {
			return differentialRuntime{}, readErr
		}
		if bindErr := validateDifferentialSourceBinding(options.MigrationReportPath, evidence.Source, dashboard.Source); bindErr != nil {
			return differentialRuntime{}, inputError(bindErr)
		}
		if bindErr := validateDifferentialTargetBinding(evidence.Run.Target, options.TargetURL); bindErr != nil {
			return differentialRuntime{}, inputError(bindErr)
		}
		stored, binding, bindErr := readBoundPrimaryDashboard(options.MigrationReportPath, reportData, evidence)
		if bindErr != nil {
			return differentialRuntime{}, inputError(bindErr)
		}
		evidenceBindings, bindErr = storedDifferentialBindings(dashboard, evidence)
		if bindErr != nil {
			return differentialRuntime{}, inputError(bindErr)
		}
		storedDashboard = &stored
		primaryArtifact = &binding
		resolutionDashboard, bindErr = dashboardWithEvidenceVariableSelections(dashboard, evidence)
		if bindErr != nil {
			return differentialRuntime{}, inputError(bindErr)
		}
		materialization, analyzerOptions, bindErr = differentialMaterializationFromEvidence(evidence)
		if bindErr != nil {
			return differentialRuntime{}, inputError(bindErr)
		}
	}
	sourceResolution := resolveSourceVariables(resolutionDashboard, options.SourceVariables)
	baseAnalyzerOptions := analyzerOptions
	baseAnalyzerOptions.Metrics = mappedMetrics(options.MetricNameMap)
	sourceClient, err := sourceprometheus.NewClientWithOptions(
		options.SourceURL,
		options.SourceBearerToken,
		options.HTTPClient,
		sourceprometheus.ClientOptions{AllowInsecureHTTP: options.AllowInsecureHTTP},
	)
	if err != nil {
		return differentialRuntime{}, targetError(err)
	}
	targetClient, err := signoz.NewClientWithOptions(
		options.TargetURL,
		options.TargetAPIKey,
		options.HTTPClient,
		signoz.ClientOptions{AllowInsecureHTTP: options.AllowInsecureHTTP},
	)
	if err != nil {
		return differentialRuntime{}, targetError(err)
	}
	metrics := mappedMetrics(options.MetricNameMap)
	missingMetrics := make(map[string]bool)
	metadataErrors := make(map[string]bool)
	if storedDashboard == nil {
		probeAnalyzer := transpile.NewAnalyzer(baseAnalyzerOptions)
		metadataStart, metadataEnd := metricMetadataWindow(options.Now, options.Range)
		if err := resolveMetricMetadata(
			ctx, targetClient, dashboard, probeAnalyzer, metrics, missingMetrics, metadataErrors,
			options.MetricNameMap, metadataStart, metadataEnd,
		); err != nil {
			return differentialRuntime{}, targetError(fmt.Errorf("resolve target metrics for differential validation: %w", err))
		}
	}
	baseAnalyzerOptions.Metrics = metrics
	baseAnalyzerOptions.MissingMetrics = missingMetrics
	baseAnalyzerOptions.MetadataErrors = metadataErrors
	analyzer := transpile.NewAnalyzer(baseAnalyzerOptions)
	var migration model.Migration
	var payload signoz.DashboardV5
	targetDashboard := dashboard
	if storedDashboard == nil {
		// A target-side override is a concrete dashboard selection, not merely a
		// request-time value. Apply it before translation so it can safely resolve
		// an otherwise unrepresentable All or contradictory selection. Source-side
		// materialization continues to use the untouched source dashboard above.
		targetDashboard.Variables = append([]model.Variable(nil), dashboard.Variables...)
		applyVariableOverrides(&targetDashboard, options.TargetVariables)
		migration = migrate.Dashboard(targetDashboard, analyzer)
		payload = signoz.EmitV5(migration)
	} else {
		payload = *storedDashboard
	}
	targetVarTypes := signoz.DashboardVariableTypes(payload)
	var targetResolution targetVariableResolution
	if storedDashboard == nil {
		targetResolution = resolveTargetVariables(targetDashboard, options.TargetVariables, targetVarTypes)
	} else {
		targetResolution, err = resolveStoredTargetVariables(payload, options.TargetVariables)
		if err != nil {
			return differentialRuntime{}, inputError(fmt.Errorf("resolve bound target dashboard variables: %w", err))
		}
	}
	widgets := make(map[string]signoz.Widget, len(payload.Widgets))
	for _, widget := range payload.Widgets {
		widgets[widget.SourcePath] = widget
	}
	return differentialRuntime{
		dashboard:        dashboard,
		migration:        migration,
		analyzer:         analyzer,
		sourceClient:     sourceClient,
		targetClient:     targetClient,
		sourceResolution: sourceResolution,
		targetResolution: targetResolution,
		targetVarTypes:   targetVarTypes,
		widgets:          widgets,
		compareOptions:   compareOptions,
		evidenceBindings: evidenceBindings,
		primaryArtifact:  primaryArtifact,
		materialization:  materialization,
	}, nil
}

func dashboardWithEvidenceVariableSelections(
	dashboard model.Dashboard,
	evidence reporttypes.Report,
) (model.Dashboard, error) {
	byPath := make(map[string]reporttypes.VariableRecord, len(evidence.Variables))
	for _, variable := range evidence.Variables {
		if _, duplicate := byPath[variable.SourcePath]; duplicate {
			return model.Dashboard{}, fmt.Errorf("migration report has duplicate variable source path %q", variable.SourcePath)
		}
		byPath[variable.SourcePath] = variable
	}
	if len(byPath) != len(dashboard.Variables) {
		return model.Dashboard{}, fmt.Errorf(
			"migration report variable count %d does not match source dashboard count %d",
			len(byPath), len(dashboard.Variables),
		)
	}
	bound := dashboard
	bound.Variables = append([]model.Variable(nil), dashboard.Variables...)
	for index := range bound.Variables {
		variable := &bound.Variables[index]
		record, found := byPath[variable.SourcePath]
		if !found || record.Name != variable.Name || record.SourceKind != string(variable.Kind) {
			return model.Dashboard{}, fmt.Errorf(
				"migration report variable binding does not match source variable %q at %q",
				variable.Name, variable.SourcePath,
			)
		}
		if record.AllValue != variable.AllValue {
			return model.Dashboard{}, fmt.Errorf(
				"migration report All value does not match source variable %q at %q",
				variable.Name, variable.SourcePath,
			)
		}
		variable.Current = append([]string(nil), record.Current...)
	}
	return bound, nil
}

func newDifferentialReport(
	source model.Source,
	options DifferentialOptions,
	artifact *reporttypes.ArtifactBinding,
	materialization reporttypes.DifferentialMaterialization,
) DifferentialReport {
	runWindow := alignedDifferentialWindow(options.Now, options.Range, options.Step)
	return DifferentialReport{
		Source:            source,
		SourceURL:         options.SourceURL,
		TargetURL:         options.TargetURL,
		TargetProvenance:  diff.TargetProvenance(options.TargetProvenance),
		AllowInsecureHTTP: options.AllowInsecureHTTP,
		PrimaryArtifact:   artifact,
		Materialization:   materialization,
		Window:            runWindow,
		Tolerances: DifferentialTolerances{
			TimestampMillis:      options.TimestampTolerance.Milliseconds(),
			Relative:             options.RelativeTolerance,
			Absolute:             options.AbsoluteTolerance,
			Coverage:             defaultCoverage(options.MinimumCoverage),
			MinimumMatchedPoints: defaultMinimumMatchedPoints(options.MinimumMatchedPoints),
		},
	}
}

func storedDifferentialBindings(
	dashboard model.Dashboard,
	evidence reporttypes.Report,
) (map[string]storedDifferentialBinding, error) {
	locations, err := indexMigrationQueries(evidence)
	if err != nil {
		return nil, err
	}
	bindings := make(map[string]storedDifferentialBinding, len(locations))
	for _, panel := range dashboard.Panels {
		for _, query := range panel.Queries {
			location, found := locations[query.SourcePath]
			if !found {
				return nil, fmt.Errorf("source query %q is missing from migration evidence", query.SourcePath)
			}
			recordedPanel := evidence.Panels[location.panel]
			if recordedPanel.SourcePath != panel.SourcePath {
				return nil, fmt.Errorf(
					"source query %q belongs to panel %q but migration evidence binds it to %q",
					query.SourcePath,
					panel.SourcePath,
					recordedPanel.SourcePath,
				)
			}
			bindings[query.SourcePath] = storedDifferentialBinding{
				panel: recordedPanel,
				query: recordedPanel.Queries[location.query],
			}
		}
	}
	if len(bindings) != len(locations) {
		return nil, fmt.Errorf(
			"migration evidence contains %d queries but the bound source contains %d",
			len(locations),
			len(bindings),
		)
	}
	return bindings, nil
}
