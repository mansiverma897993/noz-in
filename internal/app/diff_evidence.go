package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/mansiverma897993/noz-in/internal/artifactbind"
	"github.com/mansiverma897993/noz-in/internal/artifactset"
	"github.com/mansiverma897993/noz-in/internal/diff"
	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/mansiverma897993/noz-in/internal/target/signoz"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
)

// AttachDifferentialEvidence stores each measured comparison in its dashboard query record.
func AttachDifferentialEvidence(path string, differential DifferentialReport) error {
	evidence, reportData, err := readMigrationEvidence(path)
	if err != nil {
		return err
	}
	if err := validateDifferentialSourceBinding(path, evidence.Source, differential.Source); err != nil {
		return inputError(err)
	}
	if err := normalizeDifferentialEvidenceEnvelope(&differential); err != nil {
		return inputError(err)
	}
	if err := validateDifferentialTargetBinding(evidence.Run.Target, differential.TargetURL); err != nil {
		return inputError(err)
	}
	if err := validateDifferentialMaterializationBinding(evidence, differential.Materialization); err != nil {
		return inputError(err)
	}
	primaryDashboard, primaryArtifact, err := readBoundPrimaryDashboard(path, reportData, evidence)
	if err != nil {
		return inputError(err)
	}
	if err := validateDifferentialArtifactBinding(differential.PrimaryArtifact, primaryArtifact); err != nil {
		return inputError(err)
	}
	reportQueries, err := indexMigrationQueries(evidence)
	if err != nil {
		return inputError(err)
	}
	comparisons, err := bindDifferentialComparisons(
		evidence,
		primaryDashboard,
		differential.Comparisons,
		reportQueries,
		differential.TargetProvenance,
	)
	if err != nil {
		return err
	}
	if err := requireDifferentialQueryBijection(evidence, comparisons, reportQueries); err != nil {
		return inputError(err)
	}
	if err := validateDifferentialSummary(differential); err != nil {
		return inputError(err)
	}
	attachDifferentialComparisons(&evidence, comparisons, reportQueries)
	evidence.Differential = storedDifferentialEvidence(differential, primaryArtifact)
	return updateDashboardReportArtifactSet(path, &evidence)
}

func normalizeDifferentialEvidenceEnvelope(differential *DifferentialReport) error {
	sourceURL, err := canonicalEndpointIdentity(differential.SourceURL)
	if err != nil {
		return fmt.Errorf("differential source endpoint is invalid: %w", err)
	}
	targetURL, err := canonicalEndpointIdentity(differential.TargetURL)
	if err != nil {
		return fmt.Errorf("differential target endpoint is invalid: %w", err)
	}
	if differential.Window.Start.IsZero() || differential.Window.End.IsZero() ||
		!differential.Window.Start.Before(differential.Window.End) || differential.Window.StepMillis <= 0 {
		return fmt.Errorf("differential report has an invalid overall comparison window")
	}
	materialization, err := normalizeDifferentialMaterialization(differential.Materialization)
	if err != nil {
		return err
	}
	compareOptions := diff.Options{
		TimestampTolerance:   time.Duration(differential.Tolerances.TimestampMillis) * time.Millisecond,
		TargetProvenance:     differential.TargetProvenance,
		RelativeTolerance:    differential.Tolerances.Relative,
		AbsoluteTolerance:    differential.Tolerances.Absolute,
		MinimumCoverage:      differential.Tolerances.Coverage,
		MinimumMatchedPoints: differential.Tolerances.MinimumMatchedPoints,
	}
	if err := diff.ValidateOptions(compareOptions); err != nil {
		return fmt.Errorf("differential report has invalid tolerances: %w", err)
	}
	differential.SourceURL = sourceURL
	differential.TargetURL = targetURL
	differential.Materialization = materialization
	return nil
}

func normalizeDifferentialMaterialization(
	materialization reporttypes.DifferentialMaterialization,
) (reporttypes.DifferentialMaterialization, error) {
	parse := func(name, value string) (time.Duration, error) {
		duration, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil || duration <= 0 {
			return 0, fmt.Errorf("differential report has an invalid %s materialization setting %q", name, value)
		}
		return duration, nil
	}
	rateInterval, err := parse("rateInterval", materialization.RateInterval)
	if err != nil {
		return reporttypes.DifferentialMaterialization{}, err
	}
	interval, err := parse("interval", materialization.Interval)
	if err != nil {
		return reporttypes.DifferentialMaterialization{}, err
	}
	queryRange, err := parse("range", materialization.Range)
	if err != nil {
		return reporttypes.DifferentialMaterialization{}, err
	}
	return reporttypes.DifferentialMaterialization{
		RateInterval: rateInterval.String(),
		Interval:     interval.String(),
		Range:        queryRange.String(),
	}, nil
}

func validateDifferentialMaterializationBinding(
	evidence reporttypes.Report,
	actual reporttypes.DifferentialMaterialization,
) error {
	expected, _, err := differentialMaterializationFromEvidence(evidence)
	if err != nil {
		return err
	}
	normalized, err := normalizeDifferentialMaterialization(actual)
	if err != nil {
		return err
	}
	if normalized != expected {
		return fmt.Errorf("differential materialization settings do not match migration evidence")
	}
	return nil
}

func validateDifferentialSummary(differential DifferentialReport) error {
	expected := summarizeDifferential(differential.Comparisons)
	if differential.Summary != expected {
		return fmt.Errorf("differential report summary does not match its comparison records")
	}
	return nil
}

func storedDifferentialEvidence(
	differential DifferentialReport,
	artifact reporttypes.ArtifactBinding,
) *reporttypes.DifferentialEvidence {
	return &reporttypes.DifferentialEvidence{
		SourceURL:        differential.SourceURL,
		TargetURL:        differential.TargetURL,
		TargetProvenance: string(differential.TargetProvenance),
		PrimaryArtifact:  artifact,
		Materialization:  differential.Materialization,
		Window: reporttypes.DifferentialWindow{
			Start:      differential.Window.Start.Format(time.RFC3339Nano),
			End:        differential.Window.End.Format(time.RFC3339Nano),
			StepMillis: differential.Window.StepMillis,
		},
		Tolerances: reporttypes.DifferentialTolerances{
			TimestampMillis:      differential.Tolerances.TimestampMillis,
			Relative:             differential.Tolerances.Relative,
			Absolute:             differential.Tolerances.Absolute,
			Coverage:             differential.Tolerances.Coverage,
			MinimumMatchedPoints: differential.Tolerances.MinimumMatchedPoints,
		},
		Summary: reporttypes.DifferentialSummary{
			Queries:             differential.Summary.Queries,
			Compared:            differential.Summary.Compared,
			Equivalent:          differential.Summary.Equivalent,
			ValueMismatch:       differential.Summary.ValueMismatch,
			InsufficientOverlap: differential.Summary.InsufficientOverlap,
			NoSourceData:        differential.Summary.NoSourceData,
			NoTargetData:        differential.Summary.NoTargetData,
			BothEmpty:           differential.Summary.BothEmpty,
			TargetOnlyData:      differential.Summary.TargetOnlyData,
			NoSeriesMatch:       differential.Summary.NoSeriesMatch,
			Errors:              differential.Summary.Errors,
			Skipped:             differential.Summary.Skipped,
		},
	}
}

type queryLocation struct {
	panel int
	query int
}

func readMigrationEvidence(path string) (reporttypes.Report, []byte, error) {
	data, err := readStoredReportBytes(path, maxMigrationReportSize)
	if err != nil {
		return reporttypes.Report{}, nil, inputError(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var evidence reporttypes.Report
	if err := decoder.Decode(&evidence); err != nil {
		return reporttypes.Report{}, nil, inputError(fmt.Errorf("decode migration report %q: %w", path, err))
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return reporttypes.Report{}, nil, inputError(fmt.Errorf("migration report %q contains multiple JSON values", path))
		}
		return reporttypes.Report{}, nil, inputError(fmt.Errorf("decode trailing migration report data %q: %w", path, err))
	}
	if evidence.SchemaVersion != "1" || evidence.Dashboard.Title == "" {
		return reporttypes.Report{}, nil, inputError(fmt.Errorf("migration report %q is not schema version 1 dashboard evidence", path))
	}
	return evidence, data, nil
}

func readBoundPrimaryDashboard(
	reportPath string,
	reportData []byte,
	evidence reporttypes.Report,
) (signoz.DashboardV5, reporttypes.ArtifactBinding, error) {
	if evidence.PrimaryArtifact == nil {
		return signoz.DashboardV5{}, reporttypes.ArtifactBinding{}, fmt.Errorf(
			"migration report %q has no primary dashboard artifact binding; rerun migration",
			reportPath,
		)
	}
	binding := *evidence.PrimaryArtifact
	if err := validatePrimaryArtifactBinding(binding); err != nil {
		return signoz.DashboardV5{}, reporttypes.ArtifactBinding{}, fmt.Errorf(
			"migration report %q has an invalid primary dashboard artifact binding: %w",
			reportPath,
			err,
		)
	}
	artifactPath := filepath.Join(filepath.Dir(reportPath), binding.Path)
	var data []byte
	var err error
	if evidence.ArtifactSet != nil {
		data, err = readCommittedPrimaryArtifact(
			reportPath, reportData, evidence.ArtifactSet, &binding, artifactset.KindDashboard,
		)
	} else {
		data, err = artifactbind.ReadAdjacent(reportPath, &binding, ".signoz.json", maxMigrationReportSize)
	}
	if err != nil {
		return signoz.DashboardV5{}, reporttypes.ArtifactBinding{}, fmt.Errorf(
			"bound primary dashboard %q does not match migration evidence: %w",
			artifactPath,
			err,
		)
	}
	var dashboard signoz.DashboardV5
	if err := decodeStrictJSON(data, &dashboard); err != nil {
		return signoz.DashboardV5{}, reporttypes.ArtifactBinding{}, fmt.Errorf("decode bound primary dashboard %q: %w", artifactPath, err)
	}
	if err := ValidateStoredDashboardEvidence(&dashboard, evidence); err != nil {
		return signoz.DashboardV5{}, reporttypes.ArtifactBinding{}, fmt.Errorf("verify bound primary dashboard %q: %w", artifactPath, err)
	}
	return dashboard, binding, nil
}

func validatePrimaryArtifactBinding(binding reporttypes.ArtifactBinding) error {
	if binding.Path == "" || binding.Path == "." || filepath.IsAbs(binding.Path) ||
		filepath.Base(binding.Path) != binding.Path || strings.ContainsAny(binding.Path, `/\\`) {
		return fmt.Errorf("path %q must be a portable filename relative to the report", binding.Path)
	}
	if !strings.HasSuffix(binding.Path, ".signoz.json") {
		return fmt.Errorf("path %q is not a primary SigNoz dashboard filename", binding.Path)
	}
	if !validSHA256(binding.SHA256) {
		return fmt.Errorf("SHA-256 is missing or invalid")
	}
	if binding.SizeBytes <= 0 || binding.SizeBytes > maxMigrationReportSize {
		return fmt.Errorf("size %d is outside the supported range", binding.SizeBytes)
	}
	return nil
}

func validateDifferentialTargetBinding(migrationTarget, differentialTarget string) error {
	actual, err := canonicalEndpointIdentity(differentialTarget)
	if err != nil {
		return fmt.Errorf("differential target endpoint is invalid: %w", err)
	}
	if strings.TrimSpace(migrationTarget) == "" {
		return nil
	}
	expected, err := canonicalEndpointIdentity(migrationTarget)
	if err != nil {
		return fmt.Errorf("migration target endpoint is invalid: %w", err)
	}
	if actual != expected {
		return fmt.Errorf("differential target endpoint %q does not match migration target endpoint %q", actual, expected)
	}
	return nil
}

func validateDifferentialArtifactBinding(
	differential *reporttypes.ArtifactBinding,
	expected reporttypes.ArtifactBinding,
) error {
	if differential == nil {
		return fmt.Errorf("differential report has no primary dashboard artifact binding; rerun differential validation with the migration report")
	}
	if err := validatePrimaryArtifactBinding(*differential); err != nil {
		return fmt.Errorf("differential report has an invalid primary dashboard artifact binding: %w", err)
	}
	if *differential != expected {
		return fmt.Errorf("differential primary dashboard artifact binding does not match migration evidence")
	}
	return nil
}

func validateDifferentialSourceBinding(path string, evidence reporttypes.Source, differential model.Source) error {
	if strings.TrimSpace(evidence.Path) == "" {
		return fmt.Errorf("migration report %q has no source path; rerun migration", path)
	}
	if strings.TrimSpace(differential.Path) == "" {
		return fmt.Errorf("differential report has no source path; rerun differential validation")
	}
	if filepath.Clean(evidence.Path) != filepath.Clean(differential.Path) {
		return fmt.Errorf("migration report source %q does not match differential source %q", evidence.Path, differential.Path)
	}
	if strings.TrimSpace(evidence.Kind) == "" || strings.TrimSpace(differential.Kind) == "" {
		return fmt.Errorf("source kind is missing from migration or differential evidence; rerun both operations")
	}
	if evidence.Kind != differential.Kind {
		return fmt.Errorf("migration report source kind %q does not match differential source kind %q", evidence.Kind, differential.Kind)
	}
	if evidence.SchemaVersion != differential.SchemaVersion {
		return fmt.Errorf(
			"migration report source schema version %d does not match differential source schema version %d",
			evidence.SchemaVersion, differential.SchemaVersion,
		)
	}
	if !validSHA256(evidence.SHA256) {
		return fmt.Errorf("migration report has no valid source SHA-256; rerun migration")
	}
	if !validSHA256(differential.SHA256) {
		return fmt.Errorf("differential report has no valid source SHA-256; rerun differential validation")
	}
	if evidence.SHA256 != differential.SHA256 {
		return fmt.Errorf(
			"migration report source SHA-256 %q does not match differential source SHA-256 %q",
			evidence.SHA256, differential.SHA256,
		)
	}
	return nil
}

func indexMigrationQueries(evidence reporttypes.Report) (map[string]queryLocation, error) {
	reportQueries := make(map[string]queryLocation)
	for panelIndex := range evidence.Panels {
		for queryIndex := range evidence.Panels[panelIndex].Queries {
			query := evidence.Panels[panelIndex].Queries[queryIndex]
			if strings.TrimSpace(query.SourcePath) == "" {
				return nil, fmt.Errorf("migration report contains a query with an empty source path; rerun migration")
			}
			if _, exists := reportQueries[query.SourcePath]; exists {
				return nil, fmt.Errorf("migration report contains duplicate query source path %q; rerun migration", query.SourcePath)
			}
			if err := validateMigrationQueryBinding(query); err != nil {
				return nil, err
			}
			reportQueries[query.SourcePath] = queryLocation{panel: panelIndex, query: queryIndex}
		}
	}
	return reportQueries, nil
}

func validateMigrationQueryBinding(query reporttypes.QueryRecord) error {
	targetKind, ok := targetKindForEmittedKind(query.EmittedKind)
	if !ok {
		return fmt.Errorf(
			"migration query %q has missing or unsupported emitted kind %q; rerun migration",
			query.SourcePath, query.EmittedKind,
		)
	}
	expectedTargetName, err := migrationTargetQueryName(query)
	if err != nil {
		return err
	}
	if strings.TrimSpace(query.EmittedQueryName) == "" {
		return fmt.Errorf("migration query %q has no emitted query name; rerun migration", query.SourcePath)
	}
	if query.EmittedQueryName != expectedTargetName {
		return fmt.Errorf(
			"migration query %q emitted query name %q does not match its recorded target name %q; rerun migration",
			query.SourcePath, query.EmittedQueryName, expectedTargetName,
		)
	}
	if err := validateMigrationQueryExpression(query, targetKind); err != nil {
		return err
	}
	if !validSHA256(query.EmittedSpecSHA256) {
		return fmt.Errorf("migration query %q has no valid emitted specification SHA-256; rerun migration", query.SourcePath)
	}
	return nil
}

func validateMigrationQueryExpression(query reporttypes.QueryRecord, targetKind string) error {
	switch query.EmittedKind {
	case "promql":
		if query.EmittedExpression != query.PromQL {
			return fmt.Errorf("migration query %q emitted PromQL expression does not match its recorded PromQL; rerun migration", query.SourcePath)
		}
	case "formula":
		if query.Formula == nil || query.EmittedExpression != query.Formula.Expression {
			return fmt.Errorf("migration query %q emitted formula expression does not match its recorded formula; rerun migration", query.SourcePath)
		}
	case "builder":
		var builderSpec signoz.BuilderQuerySpec
		if err := decodeStrictJSON([]byte(query.EmittedExpression), &builderSpec); err != nil || builderSpec.Name != query.EmittedQueryName {
			return fmt.Errorf("migration query %q has an invalid emitted Builder expression; rerun migration", query.SourcePath)
		}
	case "none":
		if targetKind == targetKindNone && query.EmittedExpression != "" {
			return fmt.Errorf("non-emitted migration query %q has an emitted expression; rerun migration", query.SourcePath)
		}
	}
	return nil
}

func bindDifferentialComparisons(
	evidence reporttypes.Report,
	primaryDashboard signoz.DashboardV5,
	differential []DifferentialQuery,
	reportQueries map[string]queryLocation,
	targetProvenance diff.TargetProvenance,
) (map[string]json.RawMessage, error) {
	comparisons := make(map[string]json.RawMessage, len(differential))
	for _, comparison := range differential {
		if strings.TrimSpace(comparison.SourcePath) == "" {
			return nil, inputError(fmt.Errorf("differential comparison has an empty source path"))
		}
		if _, exists := comparisons[comparison.SourcePath]; exists {
			return nil, inputError(fmt.Errorf("differential report contains duplicate source path %q", comparison.SourcePath))
		}
		location, found := reportQueries[comparison.SourcePath]
		if !found {
			return nil, inputError(fmt.Errorf("differential query %q is not mapped to a migration query", comparison.SourcePath))
		}
		panel := evidence.Panels[location.panel]
		query := panel.Queries[location.query]
		if err := validateDifferentialComparisonBinding(
			evidence, primaryDashboard, panel, query, comparison, targetProvenance,
		); err != nil {
			return nil, inputError(err)
		}
		encoded, err := json.Marshal(comparison)
		if err != nil {
			return nil, fmt.Errorf("encode comparison for %q: %w", comparison.SourcePath, err)
		}
		comparisons[comparison.SourcePath] = encoded
	}
	return comparisons, nil
}

func validateDifferentialComparisonBinding(
	evidence reporttypes.Report,
	primaryDashboard signoz.DashboardV5,
	panel reporttypes.PanelRecord,
	query reporttypes.QueryRecord,
	comparison DifferentialQuery,
	targetProvenance diff.TargetProvenance,
) error {
	if comparison.RefID != query.RefID {
		return fmt.Errorf(
			"differential query %q refId %q does not match migration refId %q",
			comparison.SourcePath, comparison.RefID, query.RefID,
		)
	}
	expectedIdentity, err := effectiveRecordedQueryIdentity(panel, query)
	if err != nil {
		return err
	}
	if strings.TrimSpace(comparison.TargetKind) == "" {
		return fmt.Errorf("differential query %q has no target kind; rerun differential validation", comparison.SourcePath)
	}
	if comparison.TargetKind != expectedIdentity.TargetKind {
		return fmt.Errorf(
			"differential query %q target kind %q does not match the effective primary artifact kind %q",
			comparison.SourcePath, comparison.TargetKind, expectedIdentity.TargetKind,
		)
	}
	if err := diff.ValidateIgnoredTargetLabels(comparison.Stats.IgnoredTargetLabels, diff.Options{
		TargetKind:       diff.TargetKind(comparison.TargetKind),
		TargetProvenance: targetProvenance,
	}); err != nil {
		return fmt.Errorf("differential query %q has invalid ignored target labels: %w", comparison.SourcePath, err)
	}
	if err := validateDifferentialLabelValueAliases(comparison.LabelValueAliases); err != nil {
		return fmt.Errorf("differential query %q has invalid label-value aliases: %w", comparison.SourcePath, err)
	}
	if strings.TrimSpace(comparison.TargetQueryName) == "" {
		return fmt.Errorf("differential query %q has no target query name; rerun differential validation", comparison.SourcePath)
	}
	if comparison.TargetQueryName != expectedIdentity.TargetQueryName {
		return fmt.Errorf(
			"differential query %q target query name %q does not match migration target query name %q",
			comparison.SourcePath, comparison.TargetQueryName, expectedIdentity.TargetQueryName,
		)
	}
	if comparison.TargetExpression != expectedIdentity.TargetExpression {
		return fmt.Errorf(
			"differential query %q target expression does not match the migration's emitted expression",
			comparison.SourcePath,
		)
	}
	if !validSHA256(comparison.TargetSpecSHA256) {
		return fmt.Errorf("differential query %q has no valid target specification SHA-256; rerun differential validation", comparison.SourcePath)
	}
	if comparison.TargetSpecSHA256 != expectedIdentity.SHA256 {
		return fmt.Errorf(
			"differential query %q target specification SHA-256 does not match the migration's emitted specification",
			comparison.SourcePath,
		)
	}
	if err := validateDifferentialTargetArtifact(comparison, expectedIdentity.TargetKind); err != nil {
		return err
	}
	return validateDifferentialLabelValueAliasBindings(
		evidence, primaryDashboard, query, comparison,
	)
}

func effectiveRecordedQueryIdentity(
	panel reporttypes.PanelRecord,
	query reporttypes.QueryRecord,
) (emittedQueryIdentity, error) {
	if !panel.PrimaryArtifact {
		return nonEmittedQuerySpec(query.RefID)
	}
	return recordedQueryIdentity(query)
}

func validateDifferentialTargetArtifact(comparison DifferentialQuery, expectedKind string) error {
	hasTargetArtifact := len(bytes.TrimSpace(comparison.TargetArtifact)) > 0
	hasTargetArtifactHash := strings.TrimSpace(comparison.TargetArtifactSHA256) != ""
	if hasTargetArtifact != hasTargetArtifactHash {
		return fmt.Errorf(
			"differential query %q must contain both target artifact and target artifact SHA-256, or neither",
			comparison.SourcePath,
		)
	}
	if err := validateDifferentialComparisonOutcome(comparison, hasTargetArtifact); err != nil {
		return err
	}
	if expectedKind == targetKindNone && hasTargetArtifact {
		return fmt.Errorf("non-emitted differential query %q must not contain a target request artifact", comparison.SourcePath)
	}
	if !hasTargetArtifact {
		return nil
	}
	if !validSHA256(comparison.TargetArtifactSHA256) {
		return fmt.Errorf("differential query %q has an invalid target artifact SHA-256", comparison.SourcePath)
	}
	artifactHash, err := canonicalJSONSHA256(comparison.TargetArtifact)
	if err != nil {
		return fmt.Errorf("differential query %q has an invalid target artifact: %w", comparison.SourcePath, err)
	}
	if artifactHash != comparison.TargetArtifactSHA256 {
		return fmt.Errorf("differential query %q target artifact SHA-256 does not match its artifact", comparison.SourcePath)
	}
	targetRequest, err := decodeTargetArtifact(comparison.TargetArtifact)
	if err != nil {
		return fmt.Errorf("differential query %q target artifact is not a strict query-range request: %w", comparison.SourcePath, err)
	}
	artifactIdentity, found, err := emittedQuerySpecFromRequest(targetRequest, comparison.TargetQueryName)
	if err != nil {
		return fmt.Errorf("differential query %q target artifact has an invalid static query envelope: %w", comparison.SourcePath, err)
	}
	if !found {
		return fmt.Errorf(
			"differential query %q target artifact does not contain target query %q",
			comparison.SourcePath, comparison.TargetQueryName,
		)
	}
	if artifactIdentity.TargetKind != comparison.TargetKind ||
		artifactIdentity.TargetExpression != comparison.TargetExpression ||
		artifactIdentity.SHA256 != comparison.TargetSpecSHA256 {
		return fmt.Errorf(
			"differential query %q target artifact static envelope does not match its bound target specification",
			comparison.SourcePath,
		)
	}
	if comparison.Window == nil {
		return fmt.Errorf("differential query %q has a target artifact but no comparison window", comparison.SourcePath)
	}
	if comparison.Window.Start.UnixMilli() < 0 || comparison.Window.End.UnixMilli() < 0 ||
		targetRequest.Start != uint64(comparison.Window.Start.UnixMilli()) ||
		targetRequest.End != uint64(comparison.Window.End.UnixMilli()) {
		return fmt.Errorf("differential query %q target artifact window does not match its comparison window", comparison.SourcePath)
	}
	return nil
}

func validateDifferentialComparisonOutcome(comparison DifferentialQuery, hasTargetArtifact bool) error {
	switch comparison.Stats.Status {
	case diff.StatusSkipped:
		if strings.TrimSpace(comparison.SkippedReason) == "" {
			return fmt.Errorf("skipped differential query %q has no reason", comparison.SourcePath)
		}
	case diff.StatusError:
		if strings.TrimSpace(comparison.Error) == "" {
			return fmt.Errorf("failed differential query %q has no error detail", comparison.SourcePath)
		}
	case diff.StatusEquivalent, diff.StatusValueMismatch, diff.StatusInsufficientOverlap,
		diff.StatusNoSourceData, diff.StatusNoTargetData, diff.StatusBothEmpty,
		diff.StatusTargetOnlyData, diff.StatusNoSeriesMatch:
		if !hasTargetArtifact {
			return fmt.Errorf("measured differential query %q has no exact target request artifact", comparison.SourcePath)
		}
		if comparison.Window == nil {
			return fmt.Errorf("measured differential query %q has no comparison window", comparison.SourcePath)
		}
		if strings.TrimSpace(comparison.SourceExpression) == "" {
			return fmt.Errorf("measured differential query %q has no materialized source expression", comparison.SourcePath)
		}
	default:
		return fmt.Errorf("differential query %q has unsupported status %q", comparison.SourcePath, comparison.Stats.Status)
	}
	return nil
}

func requireDifferentialQueryBijection(
	evidence reporttypes.Report,
	comparisons map[string]json.RawMessage,
	reportQueries map[string]queryLocation,
) error {
	if len(comparisons) != len(reportQueries) {
		for panelIndex := range evidence.Panels {
			for queryIndex := range evidence.Panels[panelIndex].Queries {
				sourcePath := evidence.Panels[panelIndex].Queries[queryIndex].SourcePath
				if _, found := comparisons[sourcePath]; !found {
					return fmt.Errorf("differential report is missing migration query %q", sourcePath)
				}
			}
		}
	}
	return nil
}

func attachDifferentialComparisons(
	evidence *reporttypes.Report,
	comparisons map[string]json.RawMessage,
	reportQueries map[string]queryLocation,
) {
	for sourcePath, location := range reportQueries {
		evidence.Panels[location.panel].Queries[location.query].Comparison = comparisons[sourcePath]
	}
}

func migrationTargetQueryName(query reporttypes.QueryRecord) (string, error) {
	switch query.EmittedKind {
	case "builder":
		if query.Builder == nil || strings.TrimSpace(query.Builder.Name) == "" {
			return "", fmt.Errorf("migration query %q has no emitted Builder query name; rerun migration", query.SourcePath)
		}
		return query.Builder.Name, nil
	case "formula":
		if query.Formula == nil || strings.TrimSpace(query.Formula.Name) == "" {
			return "", fmt.Errorf("migration query %q has no emitted formula name; rerun migration", query.SourcePath)
		}
		return query.Formula.Name, nil
	default:
		return defaultEmittedQueryName(query.RefID), nil
	}
}
