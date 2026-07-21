package integration_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mansiverma897993/signoz/internal/migrate"
	"github.com/mansiverma897993/signoz/internal/model"
	"github.com/mansiverma897993/signoz/internal/report"
	"github.com/mansiverma897993/signoz/internal/source/grafana"
	"github.com/mansiverma897993/signoz/internal/target/signoz"
	"github.com/mansiverma897993/signoz/internal/transpile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	uiAuthoredFixtureRoot = "testdata/ui-authored-grafana"
	uiAuthoredFetchEnv    = "PROMCAST_FETCH_UPSTREAM_GRAFANA_FIXTURES"
	maxFetchedFixtureSize = 64 << 20
)

type uiAuthoredManifest struct {
	SchemaVersion int
	Fixtures      []uiAuthoredFixture
}

type uiAuthoredFixture struct {
	Name               string
	Distribution       string
	File               string
	UpstreamRepository string
	UpstreamCommit     string
	UpstreamPath       string
	UpstreamURL        string
	UpstreamSHA256     string
	VendoredSHA256     string
	Normalization      string
	License            string
	LicenseURL         string
	GrafanaVersion     string
	VersionEvidenceURL string
	Expected           uiAuthoredExpected
	Features           []string
}

type uiAuthoredExpected struct {
	Title          string
	SchemaVersion  int
	Panels         int
	Queries        int
	Variables      int
	SourceFeatures int
}

func TestUIAuthoredGrafanaManifestContract(t *testing.T) {
	t.Parallel()

	manifest := loadUIAuthoredManifest(t)
	require.Equal(t, 1, manifest.SchemaVersion)
	require.Len(t, manifest.Fixtures, 7)

	expectedNames := []string{
		"grafana-11.6-dashboard-links",
		"grafana-11.6-elasticsearch-expressions",
		"grafana-11.6-library-panels",
		"grafana-11.6-repeating-kitchen-sink",
		"otel-demo-grafana-10.1-demo",
		"otel-demo-grafana-11.5-demo",
		"otel-demo-grafana-11.5-spanmetrics",
	}
	var names []string
	seenNames := make(map[string]bool)
	seenURLs := make(map[string]bool)
	coveredFeatures := make(map[string]bool)
	versions := make(map[string]bool)

	for _, fixture := range manifest.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			require.NotEmpty(t, fixture.Name)
			require.False(t, seenNames[fixture.Name], "duplicate fixture name")
			seenNames[fixture.Name] = true
			names = append(names, fixture.Name)

			require.Equal(t, 40, len(fixture.UpstreamCommit))
			assertHexBytes(t, fixture.UpstreamCommit, 20)
			require.Equal(t, 64, len(fixture.UpstreamSHA256))
			assertHexBytes(t, fixture.UpstreamSHA256, sha256.Size)
			require.NotEmpty(t, fixture.UpstreamRepository)
			require.NotEmpty(t, fixture.UpstreamPath)
			require.False(t, filepath.IsAbs(fixture.UpstreamPath))
			require.NotContains(t, filepath.Clean(fixture.UpstreamPath), "..")
			require.True(t, strings.HasPrefix(fixture.UpstreamURL, "https://raw.githubusercontent.com/"))
			require.Contains(t, fixture.UpstreamURL, "/"+fixture.UpstreamCommit+"/")
			require.Contains(t, fixture.UpstreamURL, fixture.UpstreamPath)
			require.False(t, seenURLs[fixture.UpstreamURL], "duplicate immutable upstream URL")
			seenURLs[fixture.UpstreamURL] = true
			require.Contains(t, fixture.LicenseURL, fixture.UpstreamCommit)
			require.True(t, strings.HasPrefix(fixture.VersionEvidenceURL, "https://"))
			require.True(t, strings.HasPrefix(fixture.GrafanaVersion, "10.") || strings.HasPrefix(fixture.GrafanaVersion, "11."))
			versions[strings.SplitN(fixture.GrafanaVersion, ".", 2)[0]] = true

			require.NotEmpty(t, fixture.Expected.Title)
			require.Positive(t, fixture.Expected.SchemaVersion)
			require.Positive(t, fixture.Expected.Panels)
			require.GreaterOrEqual(t, fixture.Expected.Queries, 0)
			require.GreaterOrEqual(t, fixture.Expected.Variables, 0)
			require.Positive(t, fixture.Expected.SourceFeatures)
			require.NotEmpty(t, fixture.Features)
			for _, feature := range fixture.Features {
				coveredFeatures[feature] = true
			}

			switch fixture.Distribution {
			case "vendored":
				require.Equal(t, "Apache-2.0", fixture.License)
				require.NotEmpty(t, fixture.File)
				require.Equal(t, filepath.Base(fixture.File), fixture.File)
				require.Equal(t, "appended-final-newline", fixture.Normalization)
				require.Equal(t, 64, len(fixture.VendoredSHA256))
				assertHexBytes(t, fixture.VendoredSHA256, sha256.Size)
			case "fetch-only":
				require.Equal(t, "AGPL-3.0-only", fixture.License)
				require.Empty(t, fixture.File)
				require.Empty(t, fixture.VendoredSHA256)
				require.Equal(t, "none", fixture.Normalization)
			default:
				require.Failf(t, "unknown fixture distribution", "%q", fixture.Distribution)
			}
		})
	}

	slices.Sort(names)
	assert.Equal(t, expectedNames, names)
	assert.Equal(t, map[string]bool{"10": true, "11": true}, versions)
	for _, feature := range []string{
		"annotations",
		"dashboard-links",
		"expressions",
		"fieldConfig",
		"library-panels",
		"mixed-datasources",
		"modern-variables",
		"repeats",
		"rows",
		"transformations",
	} {
		assert.True(t, coveredFeatures[feature], "manifest does not cover %s", feature)
	}
}

func TestVendoredUIAuthoredGrafanaFixtures(t *testing.T) {
	manifest := loadUIAuthoredManifest(t)
	for _, fixture := range manifest.Fixtures {
		if fixture.Distribution != "vendored" {
			continue
		}
		t.Run(fixture.Name, func(t *testing.T) {
			t.Parallel()

			fixturePath := filepath.Join(uiAuthoredFixtureRoot, fixture.File)
			data, err := os.ReadFile(fixturePath)
			require.NoError(t, err)
			require.Equal(t, fixture.VendoredSHA256, sha256Hex(data), "vendored bytes changed")

			upstreamBytes := data
			switch fixture.Normalization {
			case "appended-final-newline":
				require.NotEmpty(t, data)
				require.Equal(t, byte('\n'), data[len(data)-1])
				upstreamBytes = data[:len(data)-1]
			case "none":
			default:
				require.Failf(t, "unknown normalization", "%q", fixture.Normalization)
			}
			require.Equal(t, fixture.UpstreamSHA256, sha256Hex(upstreamBytes), "normalized bytes do not reconstruct upstream")

			auditUIAuthoredFixture(t, fixture, data, fixturePath)
		})
	}
}

func TestFetchOnlyUIAuthoredGrafanaFixtures(t *testing.T) {
	if os.Getenv(uiAuthoredFetchEnv) != "1" {
		t.Skipf("set %s=1 to verify the hash-pinned AGPL sources without vendoring them", uiAuthoredFetchEnv)
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	manifest := loadUIAuthoredManifest(t)
	fetched := 0
	for _, fixture := range manifest.Fixtures {
		if fixture.Distribution != "fetch-only" {
			continue
		}
		fetched++
		fixture := fixture
		t.Run(fixture.Name, func(t *testing.T) {
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, fixture.UpstreamURL, nil)
			require.NoError(t, err)
			response, err := client.Do(request)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, response.StatusCode)

			data, readErr := io.ReadAll(io.LimitReader(response.Body, maxFetchedFixtureSize+1))
			closeErr := response.Body.Close()
			require.NoError(t, readErr)
			require.NoError(t, closeErr)
			require.LessOrEqual(t, len(data), maxFetchedFixtureSize)
			require.Equal(t, fixture.UpstreamSHA256, sha256Hex(data), "immutable upstream bytes changed")

			auditUIAuthoredFixture(t, fixture, data, fixture.UpstreamURL)
		})
	}
	require.Equal(t, 4, fetched, "manifest must retain the four hash-pinned official Grafana fixtures")
}

func loadUIAuthoredManifest(t *testing.T) uiAuthoredManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(uiAuthoredFixtureRoot, "manifest.json"))
	require.NoError(t, err)
	var manifest uiAuthoredManifest
	require.NoError(t, json.Unmarshal(data, &manifest))
	return manifest
}

func auditUIAuthoredFixture(t *testing.T, fixture uiAuthoredFixture, data []byte, sourcePath string) {
	t.Helper()
	dashboard, err := grafana.Parse(bytes.NewReader(data), sourcePath)
	require.NoError(t, err)
	assert.Equal(t, fixture.Expected.Title, dashboard.Title)
	assert.Equal(t, fixture.Expected.SchemaVersion, dashboard.Source.SchemaVersion)
	assert.Equal(t, model.SourceInventory{
		Captured:       true,
		Panels:         fixture.Expected.Panels,
		Queries:        fixture.Expected.Queries,
		Variables:      fixture.Expected.Variables,
		SourceFeatures: fixture.Expected.SourceFeatures,
	}, dashboard.SourceInventory)

	migration := migrate.Dashboard(dashboard, transpile.NewAnalyzer(transpile.Options{}))
	evidence := report.Build(migration)
	assert.Equal(t, fixture.Expected.Panels, evidence.Summary.Panels)
	assert.Equal(t, fixture.Expected.Queries, evidence.Summary.Queries)
	assert.Equal(t, fixture.Expected.Variables, evidence.Summary.Variables)
	assert.Equal(t, fixture.Expected.SourceFeatures, evidence.Summary.SourceFeatures)
	assert.Equal(t, evidence.Summary.Panels, evidence.Summary.PanelsAccounted)
	assert.Equal(t, evidence.Summary.Queries, evidence.Summary.QueriesAccounted)
	assert.Equal(t, evidence.Summary.Variables, evidence.Summary.VariablesAccounted)
	assert.Equal(t, evidence.Summary.SourceFeatures, evidence.Summary.SourceFeaturesAccounted)
	assert.True(t, evidence.Summary.ReconciliationComplete)

	payload := signoz.EmitV5(migration)
	assertLayoutSet(t, payload.Layout)
	for _, group := range payload.PanelMap {
		assertLayoutSet(t, group.Widgets)
	}
	assertUniqueWidgetAndQueryIDs(t, payload)
	assertUniqueWidgetSourcePaths(t, payload)
	first, err := json.Marshal(payload)
	require.NoError(t, err)
	second, err := json.Marshal(signoz.EmitV5(migration))
	require.NoError(t, err)
	assert.Equal(t, first, second, "emission must be deterministic")

	assertUIAuthoredFeatures(t, fixture.Features, dashboard, migration)
}

func assertUIAuthoredFeatures(t *testing.T, features []string, dashboard model.Dashboard, migration model.Migration) {
	t.Helper()
	for _, feature := range features {
		switch feature {
		case "all-value":
			assert.True(t, variableMatches(dashboard, func(variable model.Variable) bool { return variable.AllValue != "" }), feature)
		case "annotations":
			assert.True(t, dashboardHasReason(dashboard, model.ReasonAnnotationQuery), feature)
		case "dashboard-links":
			assert.True(t, dashboardHasReason(dashboard, model.ReasonDashboardLink), feature)
		case "expressions":
			assertGrafanaExpressions(t, dashboard, migration)
		case "fieldConfig":
			assert.True(t, dashboardHasReason(dashboard, model.ReasonFieldThresholds) || dashboardHasReason(dashboard, model.ReasonFieldOverrides), feature)
		case "library-panels":
			assert.True(t, dashboardHasReason(dashboard, model.ReasonLibraryPanel), feature)
		case "mixed-datasources":
			assert.GreaterOrEqual(t, len(dashboardDatasourceKinds(dashboard)), 2, feature)
		case "modern-variables":
			assert.True(t, variableMatches(dashboard, func(variable model.Variable) bool {
				return variable.Query != "" && len(variable.SourceFeatures) > 0
			}), feature)
		case "multi-value":
			assert.True(t, variableMatches(dashboard, func(variable model.Variable) bool { return variable.Multi }), feature)
		case "query-formats":
			formats := dashboardQueryFormats(dashboard)
			assert.Contains(t, formats, "table", feature)
			assert.Contains(t, formats, "time_series", feature)
		case "repeats":
			assert.True(t, panelMatches(dashboard, func(panel model.Panel) bool { return panel.Repeat != "" }), feature)
		case "rows":
			assert.True(t, panelMatches(dashboard, func(panel model.Panel) bool { return panel.Kind == model.PanelKindRow }), feature)
		case "transformations":
			assert.True(t, panelMatches(dashboard, func(panel model.Panel) bool { return len(panel.Transforms) > 0 }), feature)
		default:
			require.Failf(t, "unknown declared fixture feature", "%q", feature)
		}
	}
}

func assertGrafanaExpressions(t *testing.T, dashboard model.Dashboard, migration model.Migration) {
	t.Helper()
	found := 0
	for _, panel := range dashboard.Panels {
		for _, query := range panel.Queries {
			if !isGrafanaExpression(query) {
				continue
			}
			found++
			translation, ok := migration.TranslationFor(query)
			require.True(t, ok, query.SourcePath)
			assert.Equal(t, model.TranslationNone, translation.Kind, query.SourcePath)
			assert.Contains(t, translation.Decision.Reasons, model.ReasonGrafanaExpression, query.SourcePath)
		}
	}
	assert.Positive(t, found, "no Grafana expression targets found")
}

func isGrafanaExpression(query model.Query) bool {
	return strings.EqualFold(query.Datasource.Type, "__expr__") ||
		strings.EqualFold(query.Datasource.UID, "__expr__") ||
		slices.ContainsFunc([]string{"expression", "math", "reduce", "threshold"}, func(kind string) bool {
			return strings.EqualFold(query.QueryType, kind)
		})
}

func dashboardHasReason(dashboard model.Dashboard, reason model.ReasonCode) bool {
	if sourceFeaturesHaveReason(dashboard.SourceFeatures, reason) {
		return true
	}
	for _, panel := range dashboard.Panels {
		if sourceFeaturesHaveReason(panel.SourceFeatures, reason) {
			return true
		}
		for _, query := range panel.Queries {
			if sourceFeaturesHaveReason(query.SourceFeatures, reason) {
				return true
			}
		}
	}
	for _, variable := range dashboard.Variables {
		if sourceFeaturesHaveReason(variable.SourceFeatures, reason) {
			return true
		}
	}
	return false
}

func sourceFeaturesHaveReason(features []model.SourceFeature, reason model.ReasonCode) bool {
	return slices.ContainsFunc(features, func(feature model.SourceFeature) bool { return feature.Reason == reason })
}

func variableMatches(dashboard model.Dashboard, match func(model.Variable) bool) bool {
	return slices.ContainsFunc(dashboard.Variables, match)
}

func panelMatches(dashboard model.Dashboard, match func(model.Panel) bool) bool {
	return slices.ContainsFunc(dashboard.Panels, match)
}

func dashboardDatasourceKinds(dashboard model.Dashboard) map[string]bool {
	kinds := make(map[string]bool)
	add := func(datasource model.Datasource) {
		for _, value := range []string{datasource.Type, datasource.UID, datasource.Name} {
			if value = strings.ToLower(strings.TrimSpace(value)); value != "" {
				kinds[value] = true
				return
			}
		}
	}
	for _, panel := range dashboard.Panels {
		add(panel.Datasource)
		for _, query := range panel.Queries {
			add(query.Datasource)
		}
	}
	return kinds
}

func dashboardQueryFormats(dashboard model.Dashboard) map[string]bool {
	formats := make(map[string]bool)
	for _, panel := range dashboard.Panels {
		for _, query := range panel.Queries {
			if query.Format != "" {
				formats[query.Format] = true
			}
		}
	}
	return formats
}

func assertUniqueWidgetSourcePaths(t *testing.T, dashboard signoz.DashboardV5) {
	t.Helper()
	paths := make(map[string]bool, len(dashboard.Widgets))
	for _, widget := range dashboard.Widgets {
		assert.NotEmpty(t, widget.SourcePath)
		assert.False(t, paths[widget.SourcePath], "duplicate emitted source path %s", widget.SourcePath)
		paths[widget.SourcePath] = true
	}
}

func assertHexBytes(t *testing.T, value string, expectedBytes int) {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	require.NoError(t, err)
	assert.Len(t, decoded, expectedBytes)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:])
}
