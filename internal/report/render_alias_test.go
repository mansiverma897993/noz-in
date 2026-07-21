package report

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/mansiverma897993/signoz/internal/artifactset"
	"github.com/mansiverma897993/signoz/internal/safeoutput"
	"github.com/mansiverma897993/signoz/pkg/reporttypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type seededRenderReport struct {
	input   string
	primary string
	html    string
	kind    artifactset.Kind
	binding reporttypes.ArtifactSetBinding
}

func TestRenderFileRejectsEveryCommittedArtifactPathWithoutMutation(t *testing.T) {
	for _, kind := range []artifactset.Kind{artifactset.KindDashboard, artifactset.KindRules} {
		t.Run(string(kind), func(t *testing.T) {
			seeded := seedCommittedRenderReport(t, kind)
			// Create a second immutable generation so the guard must cover both
			// current and retained generation members.
			require.NoError(t, RenderFile(seeded.input, seeded.html))
			seeded.binding = readCommittedRenderBinding(t, seeded)

			protected, err := artifactset.ProtectedPathsForReport(seeded.input, seeded.binding, seeded.kind)
			require.NoError(t, err)
			require.NotEmpty(t, protected)
			before := snapshotRegularFiles(t, protected)
			for _, destination := range protected {
				if safeoutput.LexicallyEqual(destination, seeded.html) {
					continue
				}
				err := RenderFile(seeded.input, destination)
				require.Error(t, err, destination)
				assert.Contains(t, err.Error(), "aliases protected", destination)
				assertRegularFilesUnchanged(t, before)
			}

			alternate := filepath.Join(filepath.Dir(seeded.input), "independent-review.html")
			require.NoError(t, RenderFile(seeded.input, alternate))
			assert.FileExists(t, alternate)
		})
	}
}

func TestRenderFileRejectsCommittedHardlinkAndSymlinkAliases(t *testing.T) {
	seeded := seedCommittedRenderReport(t, artifactset.KindDashboard)
	reportBefore, err := os.ReadFile(seeded.input)
	require.NoError(t, err)

	hardlink := filepath.Join(filepath.Dir(seeded.input), "report-hardlink.html")
	require.NoError(t, os.Link(seeded.input, hardlink))
	symlink := filepath.Join(filepath.Dir(seeded.input), "report-symlink.html")
	require.NoError(t, os.Symlink(seeded.input, symlink))

	for _, destination := range []string{hardlink, symlink} {
		err := RenderFile(seeded.input, destination)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "aliases protected input migration report")
		current, readErr := os.ReadFile(seeded.input)
		require.NoError(t, readErr)
		assert.Equal(t, reportBefore, current)
	}
}

func TestRenderFileRejectsLegacyAliasesAndReservedMissingNames(t *testing.T) {
	for _, kind := range []artifactset.Kind{artifactset.KindDashboard, artifactset.KindRules} {
		t.Run(string(kind), func(t *testing.T) {
			seeded := seedLegacyRenderReport(t, kind)
			inputBefore, err := os.ReadFile(seeded.input)
			require.NoError(t, err)
			primaryBefore, err := os.ReadFile(seeded.primary)
			require.NoError(t, err)

			reserved, err := artifactset.ReservedPathsForReport(seeded.input, kind)
			require.NoError(t, err)
			var missingReserved string
			for _, candidate := range reserved {
				if _, statErr := os.Lstat(candidate); os.IsNotExist(statErr) &&
					!safeoutput.LexicallyEqual(candidate, seeded.html) {
					missingReserved = candidate
					break
				}
			}
			require.NotEmpty(t, missingReserved)

			hardlink := filepath.Join(filepath.Dir(seeded.input), "legacy-hardlink.html")
			require.NoError(t, os.Link(seeded.input, hardlink))
			symlink := filepath.Join(filepath.Dir(seeded.input), "legacy-symlink.html")
			require.NoError(t, os.Symlink(seeded.primary, symlink))

			for _, destination := range []string{
				seeded.input,
				filepath.Join(filepath.Dir(seeded.input), ".", filepath.Base(seeded.primary)),
				missingReserved,
				hardlink,
				symlink,
			} {
				err := RenderFile(seeded.input, destination)
				require.Error(t, err, destination)
				assert.Contains(t, err.Error(), "aliases protected", destination)
				currentInput, readErr := os.ReadFile(seeded.input)
				require.NoError(t, readErr)
				assert.Equal(t, inputBefore, currentInput)
				currentPrimary, readErr := os.ReadFile(seeded.primary)
				require.NoError(t, readErr)
				assert.Equal(t, primaryBefore, currentPrimary)
			}
			assert.NoFileExists(t, missingReserved)

			alternate := filepath.Join(filepath.Dir(seeded.input), "legacy-independent.html")
			require.NoError(t, RenderFile(seeded.input, alternate))
			assert.FileExists(t, alternate)
		})
	}
}

func TestRenderFileRejectsDesignatedLegacyHTMLWhenItAliasesAuthority(t *testing.T) {
	for _, linkKind := range []string{"hardlink", "symlink"} {
		t.Run(linkKind, func(t *testing.T) {
			seeded := seedLegacyRenderReport(t, artifactset.KindDashboard)
			before, err := os.ReadFile(seeded.input)
			require.NoError(t, err)
			switch linkKind {
			case "hardlink":
				require.NoError(t, os.Link(seeded.input, seeded.html))
			case "symlink":
				require.NoError(t, os.Symlink(seeded.input, seeded.html))
			}

			err = RenderFile(seeded.input, seeded.html)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "aliases protected input migration report")
			after, readErr := os.ReadFile(seeded.input)
			require.NoError(t, readErr)
			assert.Equal(t, before, after)
		})
	}
}

func TestValidateDashboardOutputPathRejectsAuthoritativeStorage(t *testing.T) {
	seeded := seedCommittedRenderReport(t, artifactset.KindDashboard)
	protected, err := artifactset.ProtectedPathsForReport(seeded.input, seeded.binding, seeded.kind)
	require.NoError(t, err)
	for _, destination := range protected {
		err := ValidateDashboardOutputPath(seeded.input, destination)
		require.Error(t, err, destination)
		assert.Contains(t, err.Error(), "aliases protected", destination)
	}
	require.NoError(t, ValidateDashboardOutputPath(
		seeded.input,
		filepath.Join(filepath.Dir(seeded.input), "differential-report.json"),
	))
}

func seedCommittedRenderReport(t *testing.T, kind artifactset.Kind) seededRenderReport {
	t.Helper()
	directory := t.TempDir()
	seeded := seededRenderReport{kind: kind}
	var reportData, htmlData, primary []byte
	var err error

	switch kind {
	case artifactset.KindDashboard:
		seeded.input = filepath.Join(directory, "migration.report.json")
		seeded.primary = filepath.Join(directory, "migration.signoz.json")
		seeded.html = filepath.Join(directory, "migration.report.html")
		primary = []byte("{}\n")
		digest := sha256.Sum256(primary)
		seeded.binding, err = artifactset.NewBindingForReport(seeded.input, kind)
		require.NoError(t, err)
		evidence := reporttypes.Report{
			SchemaVersion: "1", Dashboard: reporttypes.DashboardInfo{Title: "Hosts"},
			Panels: []reporttypes.PanelRecord{}, ArtifactSet: &seeded.binding,
			PrimaryArtifact: &reporttypes.ArtifactBinding{
				Path: filepath.Base(seeded.primary), SHA256: fmt.Sprintf("%x", digest[:]), SizeBytes: int64(len(primary)),
			},
		}
		reportData, err = encodedReport(evidence)
		require.NoError(t, err)
		htmlData, err = DashboardHTMLBytes(evidence)
		require.NoError(t, err)
	case artifactset.KindRules:
		seeded.input = filepath.Join(directory, "migration.rules-report.json")
		seeded.primary = filepath.Join(directory, "migration.signoz-rules.json")
		seeded.html = filepath.Join(directory, "migration.rules-report.html")
		primary = []byte("[]\n")
		digest := sha256.Sum256(primary)
		seeded.binding, err = artifactset.NewBindingForReport(seeded.input, kind)
		require.NoError(t, err)
		evidence := reporttypes.RuleReport{
			SchemaVersion: "1", Groups: []reporttypes.RuleGroupRecord{}, ArtifactSet: &seeded.binding,
			PrimaryArtifact: &reporttypes.ArtifactBinding{
				Path: filepath.Base(seeded.primary), SHA256: fmt.Sprintf("%x", digest[:]), SizeBytes: int64(len(primary)),
			},
		}
		reportData, err = encodedReport(evidence)
		require.NoError(t, err)
		htmlData, err = RulesHTMLBytes(evidence)
		require.NoError(t, err)
	default:
		require.FailNow(t, "unsupported artifact-set kind", "%q", kind)
	}

	require.NoError(t, artifactset.Commit(seeded.input, seeded.binding, kind, []artifactset.Artifact{
		{Role: artifactset.RolePrimary, Path: seeded.primary, Data: primary},
		{Role: artifactset.RoleReport, Path: seeded.input, Data: reportData},
		{Role: artifactset.RoleHTML, Path: seeded.html, Data: htmlData},
	}))
	return seeded
}

func seedLegacyRenderReport(t *testing.T, kind artifactset.Kind) seededRenderReport {
	t.Helper()
	directory := t.TempDir()
	seeded := seededRenderReport{kind: kind}
	var evidence any
	var primary []byte
	switch kind {
	case artifactset.KindDashboard:
		seeded.input = filepath.Join(directory, "migration.report.json")
		seeded.primary = filepath.Join(directory, "migration.signoz.json")
		primary = []byte("{}\n")
		digest := sha256.Sum256(primary)
		evidence = reporttypes.Report{
			SchemaVersion: "1", Dashboard: reporttypes.DashboardInfo{Title: "Legacy"}, Panels: []reporttypes.PanelRecord{},
			PrimaryArtifact: &reporttypes.ArtifactBinding{
				Path: filepath.Base(seeded.primary), SHA256: fmt.Sprintf("%x", digest[:]), SizeBytes: int64(len(primary)),
			},
		}
	case artifactset.KindRules:
		seeded.input = filepath.Join(directory, "migration.rules-report.json")
		seeded.primary = filepath.Join(directory, "migration.signoz-rules.json")
		primary = []byte("[]\n")
		digest := sha256.Sum256(primary)
		evidence = reporttypes.RuleReport{
			SchemaVersion: "1", Groups: []reporttypes.RuleGroupRecord{},
			PrimaryArtifact: &reporttypes.ArtifactBinding{
				Path: filepath.Base(seeded.primary), SHA256: fmt.Sprintf("%x", digest[:]), SizeBytes: int64(len(primary)),
			},
		}
	default:
		require.FailNow(t, "unsupported artifact-set kind", "%q", kind)
	}
	seeded.html = DefaultHTMLPath(seeded.input)
	reportData, err := json.Marshal(evidence)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(seeded.primary, primary, 0o600))
	require.NoError(t, os.WriteFile(seeded.input, reportData, 0o600))
	return seeded
}

func readCommittedRenderBinding(t *testing.T, seeded seededRenderReport) reporttypes.ArtifactSetBinding {
	t.Helper()
	data, err := os.ReadFile(seeded.input)
	require.NoError(t, err)
	var envelope struct {
		ArtifactSet *reporttypes.ArtifactSetBinding `json:"artifactSet"`
	}
	require.NoError(t, json.Unmarshal(data, &envelope))
	require.NotNil(t, envelope.ArtifactSet)
	return *envelope.ArtifactSet
}

func snapshotRegularFiles(t *testing.T, paths []string) map[string][]byte {
	t.Helper()
	result := make(map[string][]byte)
	for _, path := range paths {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
		if !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		result[path] = data
	}
	return result
}

func assertRegularFilesUnchanged(t *testing.T, expected map[string][]byte) {
	t.Helper()
	for path, before := range expected {
		after, err := os.ReadFile(path)
		require.NoError(t, err, path)
		assert.Equal(t, before, after, path)
	}
}
