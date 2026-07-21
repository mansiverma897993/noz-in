package artifactset

// This file derives the stable and hidden storage names bound to one artifact
// set, plus small shared helpers.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func expectedArtifactNames(reportName string, kind Kind) (map[Role]string, error) {
	switch kind {
	case KindDashboard:
		if !strings.HasSuffix(reportName, ".report.json") || strings.HasSuffix(reportName, ".rules-report.json") {
			return nil, fmt.Errorf("dashboard report path %q must end in .report.json", reportName)
		}
		base := strings.TrimSuffix(reportName, ".report.json")
		return map[Role]string{
			RolePrimary: base + ".signoz.json", RoleCandidate: base + ".candidate.signoz.json",
			RoleReport: reportName, RoleHTML: base + ".report.html",
		}, nil
	case KindRules:
		if !strings.HasSuffix(reportName, ".rules-report.json") {
			return nil, fmt.Errorf("rule report path %q must end in .rules-report.json", reportName)
		}
		base := strings.TrimSuffix(reportName, ".rules-report.json")
		return map[Role]string{
			RolePrimary: base + ".signoz-rules.json", RoleReport: reportName,
			RoleHTML: base + ".rules-report.html",
		}, nil
	default:
		return nil, fmt.Errorf("unsupported artifact-set kind %q", kind)
	}
}

func expectedManifestName(reportName string, kind Kind) (string, error) {
	switch kind {
	case KindDashboard:
		if !strings.HasSuffix(reportName, ".report.json") || strings.HasSuffix(reportName, ".rules-report.json") {
			return "", fmt.Errorf("dashboard report path %q must end in .report.json", reportName)
		}
		return strings.TrimSuffix(reportName, ".report.json") + ".artifacts.json", nil
	case KindRules:
		if !strings.HasSuffix(reportName, ".rules-report.json") {
			return "", fmt.Errorf("rule report path %q must end in .rules-report.json", reportName)
		}
		return strings.TrimSuffix(reportName, ".rules-report.json") + ".rules-artifacts.json", nil
	default:
		return "", fmt.Errorf("unsupported artifact-set kind %q", kind)
	}
}

func reportNameForManifest(manifestName string, kind Kind) (string, error) {
	switch kind {
	case KindDashboard:
		if !strings.HasSuffix(manifestName, ".artifacts.json") || strings.HasSuffix(manifestName, ".rules-artifacts.json") {
			return "", fmt.Errorf("invalid dashboard artifact-set manifest name %q", manifestName)
		}
		return strings.TrimSuffix(manifestName, ".artifacts.json") + ".report.json", nil
	case KindRules:
		if !strings.HasSuffix(manifestName, ".rules-artifacts.json") {
			return "", fmt.Errorf("invalid rule artifact-set manifest name %q", manifestName)
		}
		return strings.TrimSuffix(manifestName, ".rules-artifacts.json") + ".rules-report.json", nil
	default:
		return "", fmt.Errorf("unsupported artifact-set kind %q", kind)
	}
}

func expectedCandidateName(reportName string) string {
	return strings.TrimSuffix(reportName, ".report.json") + ".candidate.signoz.json"
}

func entryForRole(manifest Manifest, role Role) (Entry, bool) {
	for _, entry := range manifest.Artifacts {
		if entry.Role == role {
			return entry, true
		}
	}
	return Entry{}, false
}

func normalizedArtifact(artifacts []Artifact, role Role) Artifact {
	for _, artifact := range artifacts {
		if artifact.Role == role {
			return artifact
		}
	}
	return Artifact{}
}

func hasRole(artifacts []Artifact, role Role) bool {
	for _, artifact := range artifacts {
		if artifact.Role == role {
			return true
		}
	}
	return false
}

func newGeneration() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("create artifact-set generation: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func validGeneration(value string) bool {
	if len(value) != 32 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func portableName(value string) bool {
	if value == "" || value == "." || filepath.IsAbs(value) || filepath.Base(value) != value ||
		len(value) > 240 || strings.TrimRight(value, " .") != value || windowsReservedName(value) {
		return false
	}
	for _, character := range value {
		if character < 32 || strings.ContainsRune(`<>:"/\|?*`, character) {
			return false
		}
	}
	return true
}

func windowsReservedName(value string) bool {
	stem := strings.ToUpper(strings.TrimRight(strings.SplitN(value, ".", 2)[0], " ."))
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "CLOCK$":
		return true
	}
	return len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) &&
		stem[3] >= '1' && stem[3] <= '9'
}

func samePath(left, right string) bool {
	leftInfo, leftStatErr := os.Stat(left)
	rightInfo, rightStatErr := os.Stat(right)
	if leftStatErr == nil && rightStatErr == nil && os.SameFile(leftInfo, rightInfo) {
		return true
	}
	leftAbsolute, leftErr := filepath.Abs(left)
	rightAbsolute, rightErr := filepath.Abs(right)
	return leftErr == nil && rightErr == nil && platformPathEqual(filepath.Clean(leftAbsolute), filepath.Clean(rightAbsolute))
}

func stagePrefix(manifestName string) string {
	return ".promcast-stage-" + storageNameDigest(manifestName) + "-"
}

func ownedStageName(manifestName, generation, purpose, nonce string) string {
	prefix := stagePrefix(manifestName)
	if strings.HasPrefix(purpose, "facade:") {
		prefix = facadeStagePrefix(manifestName)
	}
	return prefix + generation + "-" + nonce
}

func parseOwnedStageName(name, manifestName string) (string, string, string, bool) {
	kind := "generation"
	prefix := stagePrefix(manifestName)
	if strings.HasPrefix(name, facadeStagePrefix(manifestName)) {
		kind = "facade"
		prefix = facadeStagePrefix(manifestName)
	}
	if !strings.HasPrefix(name, prefix) {
		return "", "", "", false
	}
	suffix := strings.TrimPrefix(name, prefix)
	if len(suffix) != 32+1+stageNonceBytes*2 || suffix[32] != '-' {
		return "", "", "", false
	}
	generation := suffix[:32]
	nonce := suffix[33:]
	return kind, generation, nonce, validGeneration(generation) && validStageNonce(nonce)
}

func lockName(manifestName string) string {
	return ".promcast-lock-" + storageNameDigest(manifestName)
}

func generationContainerName(manifestName string) string {
	return ".promcast-generations-" + storageNameDigest(manifestName)
}

func currentPointerName(manifestName string) string {
	return ".promcast-current-" + storageNameDigest(manifestName) + ".json"
}

func facadeStagePrefix(manifestName string) string {
	return ".promcast-facade-" + storageNameDigest(manifestName) + "-"
}

func pruneTombstonePrefix(manifestName string) string {
	return ".promcast-prune-" + storageNameDigest(manifestName) + "-"
}

func pruneTombstoneName(manifestName, generation, nonce string) string {
	return pruneTombstonePrefix(manifestName) + generation + "-" + nonce
}

func parsePruneTombstoneName(name, manifestName string) (string, string, bool) {
	prefix := pruneTombstonePrefix(manifestName)
	if !strings.HasPrefix(name, prefix) {
		return "", "", false
	}
	suffix := strings.TrimPrefix(name, prefix)
	if len(suffix) != 32+1+stageNonceBytes*2 || suffix[32] != '-' {
		return "", "", false
	}
	generation := suffix[:32]
	nonce := suffix[33:]
	return generation, nonce, validGeneration(generation) && validStageNonce(nonce)
}

func storageNameDigest(name string) string {
	digest := sha256.Sum256([]byte(name))
	return hex.EncodeToString(digest[:])
}

func requireJSONEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("contains multiple JSON values")
		}
		return fmt.Errorf("read trailing JSON data: %w", err)
	}
	return nil
}
