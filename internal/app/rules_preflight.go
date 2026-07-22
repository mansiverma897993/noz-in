package app

// Batch Prometheus rule preflight: input parsing, translation, target-name
// uniqueness, metric metadata resolution, and output-directory checks.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mansiverma897993/noz-in/internal/artifactset"
	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/mansiverma897993/noz-in/internal/rules"
	"github.com/mansiverma897993/noz-in/internal/safeoutput"
	sourceprometheus "github.com/mansiverma897993/noz-in/internal/source/prometheus"
	"github.com/mansiverma897993/noz-in/internal/target/signoz"
	"github.com/mansiverma897993/noz-in/internal/transpile"
)

func preflightRuleOutput(
	outputDirectory string,
	bases, rulePaths []string,
	extraInputs []ProtectedInputPath,
) error {
	info, err := os.Lstat(outputDirectory)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return fmt.Errorf("inspect output directory %q: %w", outputDirectory, err)
	case !info.IsDir():
		return fmt.Errorf("output directory %q is not a real directory", outputDirectory)
	}
	protected := make([]safeoutput.ProtectedPath, 0, len(rulePaths)+len(extraInputs))
	for _, path := range rulePaths {
		protected = append(protected, safeoutput.ProtectedPath{Path: path, Purpose: "Prometheus rule input"})
	}
	protected = appendProtectedInputs(protected, extraInputs)
	for _, base := range bases {
		for _, name := range ruleArtifactNames(base) {
			destination := filepath.Join(outputDirectory, name)
			if err := ensureRegularOrAbsent(destination); err != nil {
				return err
			}
		}
		reserved, err := artifactset.ReservedPathsForReport(
			filepath.Join(outputDirectory, base+".rules-report.json"), artifactset.KindRules,
		)
		if err != nil {
			return fmt.Errorf("derive reserved rule artifact paths for %q: %w", base, err)
		}
		for _, destination := range reserved {
			if err := safeoutput.RejectAliases(destination, protected...); err != nil {
				return err
			}
		}
	}
	return nil
}

func newRuleTargetClient(options RuleOptions) (*signoz.Client, error) {
	client, err := signoz.NewClientWithOptions(
		options.TargetURL,
		options.APIKey,
		options.HTTPClient,
		signoz.ClientOptions{AllowInsecureHTTP: options.AllowInsecureHTTP},
	)
	if err != nil {
		return nil, targetError(err)
	}
	return client, nil
}

func parsePrometheusRuleSets(paths []string, namespace string) ([]model.RuleSet, []bool, []error) {
	ruleSets := make([]model.RuleSet, len(paths))
	validInputs := make([]bool, len(paths))
	var inputErrors []error
	for index, path := range paths {
		set, err := sourceprometheus.ParseFile(path)
		if err != nil {
			inputErrors = append(inputErrors, err)
			continue
		}
		set.Source.Namespace = namespace
		ruleSets[index] = set
		validInputs[index] = true
	}
	return ruleSets, validInputs, inputErrors
}

func resolveRuleMetricMetadata(
	ctx context.Context,
	client *signoz.Client,
	ruleSets []model.RuleSet,
	metricNameMap map[string]string,
	startedAt time.Time,
	analyzerOptions *transpile.Options,
) error {
	if client == nil {
		return nil
	}
	probeAnalyzer := transpile.NewAnalyzer(*analyzerOptions)
	metrics := mappedMetrics(metricNameMap)
	missingMetrics := make(map[string]bool)
	metadataErrors := make(map[string]bool)
	start, end := metricMetadataWindow(startedAt, time.Hour)
	for _, source := range ruleSets {
		for _, group := range source.Groups {
			for _, rule := range group.Rules {
				if !rule.IsAlerting() {
					continue
				}
				query := model.Query{RefID: "A", Expression: rule.Expression}
				if err := resolveQueryMetricMetadata(
					ctx, client, query, probeAnalyzer, metrics, missingMetrics, metadataErrors,
					metricNameMap, start, end,
				); err != nil {
					return err
				}
			}
		}
	}
	analyzerOptions.Metrics = metrics
	analyzerOptions.MissingMetrics = missingMetrics
	analyzerOptions.MetadataErrors = metadataErrors
	return nil
}

func translatePrometheusRuleSets(
	paths []string,
	ruleSets []model.RuleSet,
	validInputs []bool,
	options RuleOptions,
	analyzerOptions transpile.Options,
) ([]rules.Migration, error) {
	analyzer := transpile.NewAnalyzer(analyzerOptions)
	nameInventory := rules.NewAlertNameInventory(ruleSets)
	migrations := make([]rules.Migration, len(paths))
	for pathIndex, path := range paths {
		if !validInputs[pathIndex] {
			continue
		}
		source := ruleSets[pathIndex]
		if len(source.Groups) == 0 {
			return nil, inputError(fmt.Errorf("%q does not contain Prometheus rule groups", path))
		}
		migration := rules.TranslateWithAlertNameInventory(source, analyzer, nameInventory)
		applyRuleOptions(&migration, options)
		migrations[pathIndex] = migration
	}
	return migrations, nil
}

func validateTargetRuleNames(migrations []rules.Migration) error {
	type owner struct {
		migrationID string
		location    string
	}
	byName := make(map[string]owner)
	byID := make(map[string]owner)
	for _, migration := range migrations {
		sourceIdentity := strings.TrimSpace(migration.Source.Source.Identity)
		if sourceIdentity == "" {
			sourceIdentity = strings.TrimSpace(migration.Source.Source.Path)
		}
		if sourceIdentity == "" {
			sourceIdentity = "<in-memory>"
		}
		for _, group := range migration.Groups {
			for _, rule := range group.Rules {
				if rule.Payload == nil {
					continue
				}
				name := strings.TrimSpace(rule.Payload.Alert)
				migrationID := strings.TrimSpace(rule.Payload.Labels["promcast_id"])
				location := fmt.Sprintf("%q in group %q from %q", rule.Source.Alert, group.Source.Name, sourceIdentity)
				if name == "" || migrationID == "" {
					return fmt.Errorf("translated alert %s has an incomplete target identity", location)
				}
				if previous, exists := byName[name]; exists {
					return fmt.Errorf(
						"translated alerts %s and %s share target alert name %q (migration ids %q and %q)",
						previous.location, location, name, previous.migrationID, migrationID,
					)
				}
				if previous, exists := byID[migrationID]; exists {
					return fmt.Errorf(
						"translated alerts %s and %s share target migration id %q",
						previous.location, location, migrationID,
					)
				}
				current := owner{migrationID: migrationID, location: location}
				byName[name] = current
				byID[migrationID] = current
			}
		}
	}
	return nil
}

func applyRuleOptions(migration *rules.Migration, options RuleOptions) {
	if !options.AlertOnAbsent {
		return
	}
	for groupIndex := range migration.Groups {
		for ruleIndex := range migration.Groups[groupIndex].Rules {
			payload := migration.Groups[groupIndex].Rules[ruleIndex].Payload
			if payload != nil {
				payload.Condition.AlertOnAbsent = true
			}
		}
	}
}

func ensureDirectory(path string) error {
	directory, err := safeoutput.OpenOrCreateDirectory(path, 0o700)
	if err != nil {
		return fmt.Errorf("create output directory %q: %w", path, err)
	}
	if err := directory.Close(); err != nil {
		return fmt.Errorf("close securely created output directory %q: %w", path, err)
	}
	return nil
}
