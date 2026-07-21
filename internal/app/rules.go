package app

// Prometheus rule migration entry point: run options, per-file results, and
// the batch migration plan preparation.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mansiverma897993/signoz/internal/rules"
	"github.com/mansiverma897993/signoz/internal/stableidentity"
	"github.com/mansiverma897993/signoz/internal/target/signoz"
	"github.com/mansiverma897993/signoz/internal/transpile"
	"github.com/mansiverma897993/signoz/pkg/reporttypes"
)

// RuleOptions controls Prometheus rule migration and optional target writes.
type RuleOptions struct {
	OutputDirectory   string
	TargetURL         string
	APIKey            string
	HTTPClient        *http.Client
	AllowInsecureHTTP bool
	Validate          bool
	ValidationWorkers int
	MetricNameMap     map[string]string
	AlertOnAbsent     bool
	DryRun            bool
	SourceNamespace   string
	ProtectedInputs   []ProtectedInputPath
	// ArtifactCheckpoint runs after each committed rule artifact generation.
	// Returning an error prevents a pending target mutation.
	ArtifactCheckpoint func(RuleResult) error
	// outputPreCreateCheckpoint is a deterministic adversarial-test boundary
	// between batch destination preflight and secure output-root creation.
	outputPreCreateCheckpoint func() error
}

// RuleResult identifies artifacts and target writes produced for one rule file.
type RuleResult struct {
	RulesPath      string                        `json:"rulesPath"`
	ReportPath     string                        `json:"reportPath"`
	HTMLPath       string                        `json:"htmlPath"`
	Summary        reporttypes.RuleSummary       `json:"summary"`
	Writes         []signoz.AlertRuleWriteResult `json:"writes,omitempty"`
	WriteRequested bool                          `json:"writeRequested"`
	WriteAttempted bool                          `json:"writeAttempted"`
	WriteSucceeded bool                          `json:"writeSucceeded"`
	TargetAction   string                        `json:"targetAction"`
	TargetError    string                        `json:"targetError,omitempty"`
	// Published is true only after the on-disk artifact set has been durably
	// committed. Reporting must not claim rule artifacts were written otherwise.
	Published bool                   `json:"published"`
	Evidence  reporttypes.RuleReport `json:"-"`
}

type ruleMigrationPlan struct {
	startedAt   time.Time
	options     RuleOptions
	client      *signoz.Client
	validInputs []bool
	migrations  []rules.Migration
	bases       []string
}

// MigratePrometheusRules translates, validates, and idempotently writes rule files.
func MigratePrometheusRules(ctx context.Context, paths []string, options RuleOptions) ([]RuleResult, error) {
	plan, err := prepareRuleMigrationPlan(ctx, paths, options)
	if err != nil {
		return nil, err
	}

	results := make([]RuleResult, 0, len(paths))
	for pathIndex, path := range paths {
		if !plan.validInputs[pathIndex] {
			continue
		}
		result, includeResult, migrateErr := migratePrometheusRuleFile(
			ctx,
			path,
			plan.bases[pathIndex],
			plan.migrations[pathIndex],
			plan,
		)
		if includeResult {
			results = append(results, result)
		}
		if migrateErr != nil {
			return results, migrateErr
		}
	}
	return results, nil
}

func prepareRuleMigrationPlan(
	ctx context.Context,
	paths []string,
	options RuleOptions,
) (ruleMigrationPlan, error) {
	plan := ruleMigrationPlan{startedAt: time.Now().UTC()}
	if err := stableidentity.ValidateComponent("rule source namespace", options.SourceNamespace, 512); err != nil {
		return plan, inputError(err)
	}
	if strings.TrimSpace(options.TargetURL) != "" && strings.TrimSpace(options.SourceNamespace) == "" {
		return plan, inputError(fmt.Errorf(
			"rule source namespace is required for a live SigNoz target; set --source-namespace to a stable logical rule collection identifier",
		))
	}
	if len(paths) == 0 {
		return plan, inputError(fmt.Errorf("at least one Prometheus rule file is required"))
	}
	options.SourceNamespace = strings.TrimSpace(options.SourceNamespace)
	if options.OutputDirectory == "" {
		options.OutputDirectory = "out"
	}

	ruleSets, validInputs, inputErrors := parsePrometheusRuleSets(paths, options.SourceNamespace)
	if len(inputErrors) > 0 {
		return plan, inputError(errors.Join(inputErrors...))
	}
	if err := rules.ValidateStableIdentities(ruleSets); err != nil {
		return plan, inputError(err)
	}
	recordingRules, err := recordingRuleIndex(ruleSets, validInputs)
	if err != nil {
		return plan, inputError(err)
	}
	analyzerOptions := transpile.Options{RecordingRules: recordingRules, Metrics: mappedMetrics(options.MetricNameMap)}
	preflightMigrations, err := translatePrometheusRuleSets(paths, ruleSets, validInputs, options, analyzerOptions)
	if err != nil {
		return plan, err
	}
	if err := validateTargetRuleNames(preflightMigrations); err != nil {
		return plan, inputError(err)
	}
	bases := artifactBases(paths)
	if err := preflightRuleOutput(options.OutputDirectory, bases, paths, options.ProtectedInputs); err != nil {
		return plan, inputError(err)
	}
	if options.outputPreCreateCheckpoint != nil {
		if err := options.outputPreCreateCheckpoint(); err != nil {
			return plan, inputError(fmt.Errorf("prepare rule output directory: %w", err))
		}
	}
	if err := ensureDirectory(options.OutputDirectory); err != nil {
		return plan, inputError(err)
	}

	var client *signoz.Client
	if options.TargetURL != "" {
		targetClient, clientErr := newRuleTargetClient(options)
		if clientErr != nil {
			return plan, clientErr
		}
		client = targetClient
	}
	if err := resolveRuleMetricMetadata(
		ctx,
		client,
		ruleSets,
		options.MetricNameMap,
		plan.startedAt,
		&analyzerOptions,
	); err != nil {
		return plan, targetError(fmt.Errorf("resolve target metrics for Prometheus rules: %w", err))
	}
	migrations := preflightMigrations
	if client != nil {
		migrations, err = translatePrometheusRuleSets(paths, ruleSets, validInputs, options, analyzerOptions)
		if err != nil {
			return plan, err
		}
	}
	if err := validateTargetRuleNames(migrations); err != nil {
		return plan, inputError(err)
	}
	plan.options = options
	plan.client = client
	plan.validInputs = validInputs
	plan.migrations = migrations
	plan.bases = bases
	return plan, nil
}
