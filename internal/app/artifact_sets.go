package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mansiverma897993/signoz/internal/artifactset"
	"github.com/mansiverma897993/signoz/internal/report"
	"github.com/mansiverma897993/signoz/internal/target/signoz"
	"github.com/mansiverma897993/signoz/pkg/reporttypes"
)

func readCommittedPrimaryArtifact(
	reportPath string,
	reportData []byte,
	set *reporttypes.ArtifactSetBinding,
	primary *reporttypes.ArtifactBinding,
	kind artifactset.Kind,
) ([]byte, error) {
	if set == nil || primary == nil {
		return nil, fmt.Errorf("migration report has incomplete committed artifact bindings")
	}
	snapshot, err := artifactset.ReadCommitted(
		reportPath,
		reportData,
		set,
		kind,
		[]string{primary.Path},
		maxMigrationReportSize,
	)
	if err != nil {
		return nil, err
	}
	for _, entry := range snapshot.Manifest.Artifacts {
		if entry.Role != artifactset.RolePrimary {
			continue
		}
		if entry.Path != primary.Path || entry.SHA256 != primary.SHA256 || entry.SizeBytes != primary.SizeBytes {
			return nil, fmt.Errorf("primary artifact binding does not match committed primary member")
		}
		return snapshot.Data[primary.Path], nil
	}
	return nil, fmt.Errorf("artifact commit manifest has no primary member")
}

func readStoredReportBytes(path string, maxSize int64) ([]byte, error) {
	if maxSize <= 0 {
		return nil, fmt.Errorf("maximum report size must be positive")
	}
	directory := filepath.Dir(path)
	name := filepath.Base(path)
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("open report directory %q: %w", directory, err)
	}
	defer func() { _ = root.Close() }()
	before, err := root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("inspect report %q: %w", path, err)
	}
	if !before.Mode().IsRegular() || before.Size() > maxSize {
		return nil, fmt.Errorf("report %q is not a supported regular file", path)
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open report %q: %w", path, err)
	}
	after, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect opened report %q: %w", path, err)
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		_ = file.Close()
		return nil, fmt.Errorf("report %q changed while it was opened", path)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxSize+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read report %q: %w", path, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close report %q: %w", path, closeErr)
	}
	if int64(len(data)) > maxSize {
		return nil, fmt.Errorf("report %q exceeds %d bytes", path, maxSize)
	}
	return data, nil
}

func publishGrafanaArtifactSet(
	result *GrafanaResult,
	evidence *reporttypes.Report,
	prepared preparedGrafanaDashboard,
) error {
	binding, err := artifactset.NewBindingForReport(result.ReportPath, artifactset.KindDashboard)
	if err != nil {
		return err
	}
	previousBinding := evidence.ArtifactSet
	evidence.ArtifactSet = &binding
	recordTargetOutcome(evidence, *result)

	primaryData, err := jsonArtifactBytes(prepared.importPayload)
	if err != nil {
		evidence.ArtifactSet = previousBinding
		return fmt.Errorf("encode primary dashboard artifact %q: %w", result.DashboardPath, err)
	}
	reportData, err := jsonArtifactBytes(*evidence)
	if err != nil {
		evidence.ArtifactSet = previousBinding
		return fmt.Errorf("encode dashboard migration report %q: %w", result.ReportPath, err)
	}
	htmlData, err := report.DashboardHTMLBytes(*evidence)
	if err != nil {
		evidence.ArtifactSet = previousBinding
		return err
	}
	artifacts := []artifactset.Artifact{
		{Role: artifactset.RolePrimary, Path: result.DashboardPath, Data: primaryData},
		{Role: artifactset.RoleReport, Path: result.ReportPath, Data: reportData},
		{Role: artifactset.RoleHTML, Path: result.HTMLPath, Data: htmlData},
	}
	if result.CandidateDashboardPath != "" {
		candidateData, encodeErr := jsonArtifactBytes(prepared.payload)
		if encodeErr != nil {
			evidence.ArtifactSet = previousBinding
			return fmt.Errorf("encode candidate dashboard artifact %q: %w", result.CandidateDashboardPath, encodeErr)
		}
		artifacts = append(artifacts, artifactset.Artifact{
			Role: artifactset.RoleCandidate, Path: result.CandidateDashboardPath, Data: candidateData,
		})
	}
	if err := artifactset.Commit(result.ReportPath, binding, artifactset.KindDashboard, artifacts); err != nil {
		evidence.ArtifactSet = previousBinding
		return err
	}
	result.Summary = evidence.Summary
	result.Evidence = *evidence
	result.Published = true
	return nil
}

func publishRuleArtifactSet(
	result *RuleResult,
	evidence *reporttypes.RuleReport,
	payloads []signoz.AlertRuleV2,
) error {
	binding, err := artifactset.NewBindingForReport(result.ReportPath, artifactset.KindRules)
	if err != nil {
		return err
	}
	previousBinding := evidence.ArtifactSet
	evidence.ArtifactSet = &binding
	primaryData, err := jsonArtifactBytes(payloads)
	if err != nil {
		evidence.ArtifactSet = previousBinding
		return fmt.Errorf("encode primary rule artifact %q: %w", result.RulesPath, err)
	}
	reportData, err := jsonArtifactBytes(*evidence)
	if err != nil {
		evidence.ArtifactSet = previousBinding
		return fmt.Errorf("encode rule migration report %q: %w", result.ReportPath, err)
	}
	htmlData, err := report.RulesHTMLBytes(*evidence)
	if err != nil {
		evidence.ArtifactSet = previousBinding
		return err
	}
	if err := artifactset.Commit(result.ReportPath, binding, artifactset.KindRules, []artifactset.Artifact{
		{Role: artifactset.RolePrimary, Path: result.RulesPath, Data: primaryData},
		{Role: artifactset.RoleReport, Path: result.ReportPath, Data: reportData},
		{Role: artifactset.RoleHTML, Path: result.HTMLPath, Data: htmlData},
	}); err != nil {
		evidence.ArtifactSet = previousBinding
		return err
	}
	result.Summary = evidence.Summary
	result.Evidence = *evidence
	result.Published = true
	return nil
}

func updateDashboardReportArtifactSet(path string, evidence *reporttypes.Report) error {
	if evidence.ArtifactSet == nil {
		if err := writeJSON(path, *evidence); err != nil {
			return err
		}
		return report.WriteHTML(report.DefaultHTMLPath(path), *evidence)
	}
	current := *evidence.ArtifactSet
	next, err := artifactset.NextBinding(current)
	if err != nil {
		return err
	}
	evidence.ArtifactSet = &next
	reportData, err := jsonArtifactBytes(*evidence)
	if err != nil {
		evidence.ArtifactSet = &current
		return fmt.Errorf("encode updated dashboard migration report %q: %w", path, err)
	}
	htmlPath := report.DefaultHTMLPath(path)
	htmlData, err := report.DashboardHTMLBytes(*evidence)
	if err != nil {
		evidence.ArtifactSet = &current
		return err
	}
	if err := artifactset.Update(path, current, next, artifactset.KindDashboard, []artifactset.Artifact{
		{Role: artifactset.RoleReport, Path: path, Data: reportData},
		{Role: artifactset.RoleHTML, Path: htmlPath, Data: htmlData},
	}); err != nil {
		evidence.ArtifactSet = &current
		return err
	}
	return nil
}
