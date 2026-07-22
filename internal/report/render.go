package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mansiverma897993/noz-in/internal/artifactbind"
	"github.com/mansiverma897993/noz-in/internal/artifactset"
	"github.com/mansiverma897993/noz-in/internal/safeoutput"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
)

const maxReportSize = 64 << 20

// RenderFile regenerates a self-contained HTML report from evidence JSON.
func RenderFile(inputPath, outputPath string) error {
	data, err := readReportData(inputPath)
	if err != nil {
		return err
	}
	probe, err := decodeReportProbe(inputPath, data)
	if err != nil {
		return err
	}
	if probe.SchemaVersion != "1" {
		return fmt.Errorf("report %q uses unsupported schema version %q", inputPath, probe.SchemaVersion)
	}
	if len(probe.Dashboard) > 0 && string(probe.Dashboard) != "null" {
		var evidence reporttypes.Report
		if err := decodeReport(data, &evidence); err != nil {
			return fmt.Errorf("decode dashboard report %q: %w", inputPath, err)
		}
		if evidence.ArtifactSet != nil {
			if err := validateCommittedReport(inputPath, data, evidence.ArtifactSet, evidence.PrimaryArtifact, artifactset.KindDashboard); err != nil {
				return fmt.Errorf("verify dashboard report artifact set: %w", err)
			}
		} else if err := artifactbind.ValidateAdjacent(inputPath, evidence.PrimaryArtifact, ".signoz.json", maxReportSize); err != nil {
			return fmt.Errorf("verify dashboard report primary artifact: %w", err)
		}
		destination, err := validatedRenderDestination(
			inputPath, outputPath, evidence.ArtifactSet, evidence.PrimaryArtifact, artifactset.KindDashboard,
		)
		if err != nil {
			return err
		}
		if evidence.ArtifactSet != nil {
			return renderCommittedDashboard(inputPath, destination, evidence)
		}
		return WriteHTML(destination, evidence)
	}
	if len(probe.Groups) > 0 && string(probe.Groups) != "null" {
		var evidence reporttypes.RuleReport
		if err := decodeReport(data, &evidence); err != nil {
			return fmt.Errorf("decode rule report %q: %w", inputPath, err)
		}
		if evidence.ArtifactSet != nil {
			if err := validateCommittedReport(inputPath, data, evidence.ArtifactSet, evidence.PrimaryArtifact, artifactset.KindRules); err != nil {
				return fmt.Errorf("verify rule report artifact set: %w", err)
			}
		} else if err := artifactbind.ValidateAdjacent(inputPath, evidence.PrimaryArtifact, ".signoz-rules.json", maxReportSize); err != nil {
			return fmt.Errorf("verify rule report primary artifact: %w", err)
		}
		destination, err := validatedRenderDestination(
			inputPath, outputPath, evidence.ArtifactSet, evidence.PrimaryArtifact, artifactset.KindRules,
		)
		if err != nil {
			return err
		}
		if evidence.ArtifactSet != nil {
			return renderCommittedRules(inputPath, destination, evidence)
		}
		return WriteRulesHTML(destination, evidence)
	}
	return fmt.Errorf("%q is not a promcast dashboard or rule report", inputPath)
}

type reportProbe struct {
	SchemaVersion string          `json:"schemaVersion"`
	Dashboard     json.RawMessage `json:"dashboard"`
	Groups        json.RawMessage `json:"groups"`
}

func readReportData(path string) ([]byte, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("open report %q: %w", path, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxReportSize+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read report %q: %w", path, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close report %q: %w", path, closeErr)
	}
	if len(data) > maxReportSize {
		return nil, fmt.Errorf("report %q exceeds %d bytes", path, maxReportSize)
	}
	return data, nil
}

func decodeReportProbe(path string, data []byte) (reportProbe, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var probe reportProbe
	if err := decoder.Decode(&probe); err != nil {
		return reportProbe{}, fmt.Errorf("decode report %q: %w", path, err)
	}
	if err := ensureJSONEnd(decoder, path); err != nil {
		return reportProbe{}, err
	}
	return probe, nil
}

// ValidateDashboardOutputPath verifies a migration report and rejects an
// unrelated output destination that aliases any authoritative dashboard
// artifact or artifact-set storage path. It performs no output writes and is
// suitable for CLI preflight before live differential requests.
func ValidateDashboardOutputPath(inputPath, outputPath string) error {
	data, err := readReportData(inputPath)
	if err != nil {
		return err
	}
	probe, err := decodeReportProbe(inputPath, data)
	if err != nil {
		return err
	}
	if probe.SchemaVersion != "1" || len(probe.Dashboard) == 0 || string(probe.Dashboard) == "null" {
		return fmt.Errorf("%q is not a schema version 1 dashboard migration report", inputPath)
	}
	var evidence reporttypes.Report
	if err := decodeReport(data, &evidence); err != nil {
		return fmt.Errorf("decode dashboard report %q: %w", inputPath, err)
	}
	if evidence.ArtifactSet != nil {
		if err := validateCommittedReport(
			inputPath, data, evidence.ArtifactSet, evidence.PrimaryArtifact, artifactset.KindDashboard,
		); err != nil {
			return fmt.Errorf("verify dashboard report artifact set: %w", err)
		}
	} else if err := artifactbind.ValidateAdjacent(
		inputPath, evidence.PrimaryArtifact, ".signoz.json", maxReportSize,
	); err != nil {
		return fmt.Errorf("verify dashboard report primary artifact: %w", err)
	}
	return rejectProtectedOutput(
		inputPath, outputPath, evidence.ArtifactSet, evidence.PrimaryArtifact, artifactset.KindDashboard, true,
	)
}

func validatedRenderDestination(
	inputPath string,
	outputPath string,
	set *reporttypes.ArtifactSetBinding,
	primary *reporttypes.ArtifactBinding,
	kind artifactset.Kind,
) (string, error) {
	designatedHTML := DefaultHTMLPath(inputPath)
	if safeoutput.LexicallyEqual(outputPath, designatedHTML) {
		if err := rejectProtectedOutput(inputPath, designatedHTML, set, primary, kind, false); err != nil {
			return "", err
		}
		return designatedHTML, nil
	}
	if err := rejectProtectedOutput(inputPath, outputPath, set, primary, kind, true); err != nil {
		return "", err
	}
	return outputPath, nil
}

func rejectProtectedOutput(
	inputPath string,
	outputPath string,
	set *reporttypes.ArtifactSetBinding,
	primary *reporttypes.ArtifactBinding,
	kind artifactset.Kind,
	protectDesignatedHTML bool,
) error {
	designatedHTML := DefaultHTMLPath(inputPath)
	protected := []safeoutput.ProtectedPath{{Path: inputPath, Purpose: "input migration report"}}
	if protectDesignatedHTML {
		protected = append(protected, safeoutput.ProtectedPath{
			Path: designatedHTML, Purpose: "designated adjacent HTML artifact",
		})
	}
	if primary != nil {
		protected = append(protected, safeoutput.ProtectedPath{
			Path: filepath.Join(filepath.Dir(inputPath), primary.Path), Purpose: "bound primary artifact",
		})
	}
	var paths []string
	var err error
	if set != nil {
		paths, err = artifactset.ProtectedPathsForReport(inputPath, *set, kind)
	} else {
		// Historical reports may use arbitrary filenames. Canonical legacy
		// filenames still reserve the same adjacent names as committed sets.
		paths, err = artifactset.ReservedPathsForReport(inputPath, kind)
		if err != nil {
			paths = nil
			err = nil
		}
	}
	if err != nil {
		return fmt.Errorf("enumerate protected artifact paths: %w", err)
	}
	for _, path := range paths {
		if !protectDesignatedHTML && safeoutput.LexicallyEqual(path, designatedHTML) {
			continue
		}
		protected = append(protected, safeoutput.ProtectedPath{Path: path, Purpose: "migration artifact storage"})
	}
	return safeoutput.RejectAliases(outputPath, protected...)
}

func validateCommittedReport(
	reportPath string,
	reportData []byte,
	set *reporttypes.ArtifactSetBinding,
	primary *reporttypes.ArtifactBinding,
	kind artifactset.Kind,
) error {
	if primary == nil {
		return fmt.Errorf("report has no primary artifact binding; rerun migration")
	}
	snapshot, err := artifactset.ReadCommitted(
		reportPath,
		reportData,
		set,
		kind,
		[]string{primary.Path},
		maxReportSize,
	)
	if err != nil {
		return err
	}
	for _, entry := range snapshot.Manifest.Artifacts {
		if entry.Role != artifactset.RolePrimary {
			continue
		}
		if entry.Path != primary.Path || entry.SHA256 != primary.SHA256 || entry.SizeBytes != primary.SizeBytes {
			return fmt.Errorf("primary artifact binding does not match the committed primary member")
		}
		return nil
	}
	return fmt.Errorf("artifact commit manifest has no primary member")
}

func renderCommittedDashboard(inputPath, outputPath string, evidence reporttypes.Report) error {
	htmlPath := DefaultHTMLPath(inputPath)
	if !safeoutput.LexicallyEqual(outputPath, htmlPath) {
		return WriteHTML(outputPath, evidence)
	}
	current := *evidence.ArtifactSet
	next, err := artifactset.NextBinding(current)
	if err != nil {
		return err
	}
	evidence.ArtifactSet = &next
	reportData, err := encodedReport(evidence)
	if err != nil {
		return err
	}
	htmlData, err := DashboardHTMLBytes(evidence)
	if err != nil {
		return err
	}
	return artifactset.Update(inputPath, current, next, artifactset.KindDashboard, []artifactset.Artifact{
		{Role: artifactset.RoleReport, Path: inputPath, Data: reportData},
		{Role: artifactset.RoleHTML, Path: htmlPath, Data: htmlData},
	})
}

func renderCommittedRules(inputPath, outputPath string, evidence reporttypes.RuleReport) error {
	htmlPath := DefaultHTMLPath(inputPath)
	if !safeoutput.LexicallyEqual(outputPath, htmlPath) {
		return WriteRulesHTML(outputPath, evidence)
	}
	current := *evidence.ArtifactSet
	next, err := artifactset.NextBinding(current)
	if err != nil {
		return err
	}
	evidence.ArtifactSet = &next
	reportData, err := encodedReport(evidence)
	if err != nil {
		return err
	}
	htmlData, err := RulesHTMLBytes(evidence)
	if err != nil {
		return err
	}
	return artifactset.Update(inputPath, current, next, artifactset.KindRules, []artifactset.Artifact{
		{Role: artifactset.RoleReport, Path: inputPath, Data: reportData},
		{Role: artifactset.RoleHTML, Path: htmlPath, Data: htmlData},
	})
}

func encodedReport(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode committed migration report: %w", err)
	}
	return append(data, '\n'), nil
}

func decodeReport(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return ensureJSONEnd(decoder, "report")
}

// DefaultHTMLPath returns the adjacent report filename used by the CLI.
func DefaultHTMLPath(inputPath string) string {
	if base, found := strings.CutSuffix(inputPath, ".json"); found {
		return base + ".html"
	}
	return inputPath + ".html"
}

func ensureJSONEnd(decoder *json.Decoder, path string) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("report %q contains multiple JSON values", path)
		}
		return fmt.Errorf("read trailing data from report %q: %w", path, err)
	}
	return nil
}
