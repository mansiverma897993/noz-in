package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mansiverma897993/signoz/internal/artifactbind"
	"github.com/mansiverma897993/signoz/internal/artifactset"
	"github.com/mansiverma897993/signoz/internal/target/signoz"
	"github.com/mansiverma897993/signoz/pkg/reporttypes"
)

// ValidateStoredRuleArtifact verifies the exact primary rules file bound by a
// migration report and proves a one-to-one mapping from emitted rule evidence
// to the stored SigNoz payloads. Reports without the current bindings must be
// regenerated instead of being trusted by path or display name.
func ValidateStoredRuleArtifact(reportPath string, evidence reporttypes.RuleReport) error {
	reportData, err := readStoredReportBytes(reportPath, maxMigrationReportSize)
	if err != nil {
		return err
	}
	var stored reporttypes.RuleReport
	if err := decodeStrictJSON(reportData, &stored); err != nil {
		return fmt.Errorf("decode stored rule migration report %q: %w", reportPath, err)
	}
	claimedJSON, err := json.Marshal(evidence)
	if err != nil {
		return fmt.Errorf("encode supplied rule migration evidence: %w", err)
	}
	storedJSON, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("encode stored rule migration evidence: %w", err)
	}
	if !bytes.Equal(claimedJSON, storedJSON) {
		return fmt.Errorf("supplied rule migration evidence does not match stored report %q", reportPath)
	}
	evidence = stored
	if evidence.PrimaryArtifact == nil {
		return fmt.Errorf("rule migration report %q has no primary rules artifact binding; rerun migration", reportPath)
	}
	if strings.TrimSpace(evidence.Source.Identity) == "" || !validSHA256(evidence.Source.SHA256) {
		return fmt.Errorf("rule migration report %q has no exact source identity and SHA-256; rerun migration", reportPath)
	}
	binding := *evidence.PrimaryArtifact
	if err := validateRuleArtifactBinding(binding); err != nil {
		return fmt.Errorf("rule migration report %q has an invalid primary artifact binding: %w", reportPath, err)
	}
	artifactPath := filepath.Join(filepath.Dir(reportPath), binding.Path)
	var data []byte
	if evidence.ArtifactSet != nil {
		data, err = readCommittedPrimaryArtifact(
			reportPath, reportData, evidence.ArtifactSet, &binding, artifactset.KindRules,
		)
	} else {
		data, err = artifactbind.ReadAdjacent(reportPath, &binding, ".signoz-rules.json", maxMigrationReportSize)
	}
	if err != nil {
		return fmt.Errorf("bound primary rules artifact %q does not match migration evidence: %w", artifactPath, err)
	}
	var payloads []signoz.AlertRuleV2
	if err := decodeStrictJSON(data, &payloads); err != nil {
		return fmt.Errorf("decode bound primary rules artifact %q: %w", artifactPath, err)
	}
	if err := validateStoredRulePayloads(payloads, evidence); err != nil {
		return fmt.Errorf("verify bound primary rules artifact %q: %w", artifactPath, err)
	}
	return nil
}

func validateRuleArtifactBinding(binding reporttypes.ArtifactBinding) error {
	if binding.Path == "" || binding.Path == "." || filepath.IsAbs(binding.Path) ||
		filepath.Base(binding.Path) != binding.Path || strings.ContainsAny(binding.Path, `/\`) {
		return fmt.Errorf("path %q must be a portable filename relative to the report", binding.Path)
	}
	if !strings.HasSuffix(binding.Path, ".signoz-rules.json") {
		return fmt.Errorf("path %q is not a primary SigNoz rules filename", binding.Path)
	}
	if !validSHA256(binding.SHA256) {
		return fmt.Errorf("SHA-256 is missing or invalid")
	}
	if binding.SizeBytes <= 0 || binding.SizeBytes > maxMigrationReportSize {
		return fmt.Errorf("size %d is outside the supported range", binding.SizeBytes)
	}
	return nil
}

func validateStoredRulePayloads(payloads []signoz.AlertRuleV2, evidence reporttypes.RuleReport) error {
	records := make(map[string]reporttypes.RuleRecord)
	for _, group := range evidence.Groups {
		for _, record := range group.Rules {
			if record.TargetAlert == "" && record.TargetMigrationID == "" && record.EmittedSpecSHA256 == "" {
				continue
			}
			if strings.TrimSpace(record.TargetMigrationID) == "" || !validSHA256(record.EmittedSpecSHA256) {
				return fmt.Errorf("emitted rule %q has incomplete artifact identity", record.SourcePath)
			}
			if _, duplicate := records[record.TargetMigrationID]; duplicate {
				return fmt.Errorf("migration report contains duplicate target migration id %q", record.TargetMigrationID)
			}
			records[record.TargetMigrationID] = record
		}
	}
	if len(payloads) != len(records) {
		return fmt.Errorf("stored payload count %d does not match emitted evidence count %d", len(payloads), len(records))
	}
	seen := make(map[string]bool, len(payloads))
	for _, payload := range payloads {
		migrationID := strings.TrimSpace(payload.Labels["promcast_id"])
		if migrationID == "" {
			return fmt.Errorf("stored alert rule %q has no promcast_id label", payload.Alert)
		}
		if seen[migrationID] {
			return fmt.Errorf("stored rules contain duplicate migration id %q", migrationID)
		}
		seen[migrationID] = true
		record, found := records[migrationID]
		if !found {
			return fmt.Errorf("stored alert rule %q with migration id %q has no evidence record", payload.Alert, migrationID)
		}
		if record.TargetAlert != payload.Alert {
			return fmt.Errorf("stored alert rule %q does not match evidence target name %q", payload.Alert, record.TargetAlert)
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode stored alert rule %q: %w", payload.Alert, err)
		}
		digest := sha256.Sum256(encoded)
		if fmt.Sprintf("%x", digest[:]) != record.EmittedSpecSHA256 {
			return fmt.Errorf("stored alert rule %q emitted specification does not match migration evidence", payload.Alert)
		}
	}
	return nil
}
