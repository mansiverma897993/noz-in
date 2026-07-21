package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mansiverma897993/signoz/internal/app"
	"github.com/mansiverma897993/signoz/internal/metricmap"
	"github.com/spf13/cobra"
)

func newGrafanaCommand() *cobra.Command {
	var outputDirectory string
	var rateInterval time.Duration
	var interval time.Duration
	var queryRange time.Duration
	var targetURL string
	var apiKey string
	var apiKeyFile string
	var validate bool
	var variables []string
	var ruleFiles []string
	var workers int
	var offline bool
	var dryRun bool
	var metricNameMapPath string
	var sourceNamespace string
	var sourceIdentity string
	var allowInsecureHTTP bool
	var continueOnInputError bool
	var overridesFile string
	var fidelityThreshold float64
	var emitV6 bool
	command := &cobra.Command{
		Use:   "grafana <dashboard.json>...",
		Short: "Migrate Grafana dashboards",
		Args:  minimumArgs(1),
		RunE: func(command *cobra.Command, paths []string) error {
			paths, err := expandInputPaths(paths)
			if err != nil {
				return err
			}
			if offline && targetURL != "" {
				return cliInputError(fmt.Errorf("--offline and --target cannot be used together"))
			}
			if !offline && targetURL == "" {
				return cliInputError(fmt.Errorf("--target is required unless --offline is set"))
			}
			if dryRun && offline {
				return cliInputError(fmt.Errorf("--dry-run requires a live target"))
			}
			if dryRun && !validate {
				return cliInputError(fmt.Errorf("--dry-run requires target validation"))
			}
			resolvedAPIKey, err := resolveAPIKey(apiKey, apiKeyFile)
			if err != nil {
				return err
			}
			variableValues, err := parseVariables(variables)
			if err != nil {
				return err
			}
			metricNames := map[string]string(nil)
			if metricNameMapPath != "" {
				metricNames, err = metricmap.Load(metricNameMapPath)
				if err != nil {
					return &app.Error{Kind: app.ErrorInput, Err: err}
				}
			}
			ruleFiles, err = expandInputPaths(ruleFiles)
			if err != nil {
				return err
			}
			results, migrateErr := app.MigrateGrafana(command.Context(), paths, app.GrafanaOptions{
				OutputDirectory:      outputDirectory,
				RateInterval:         rateInterval,
				Interval:             interval,
				Range:                queryRange,
				TargetURL:            targetURL,
				APIKey:               resolvedAPIKey,
				Validate:             validate,
				Variables:            variableValues,
				MetricNameMap:        metricNames,
				RuleFiles:            ruleFiles,
				ValidationWorkers:    workers,
				DryRun:               dryRun,
				SourceNamespace:      sourceNamespace,
				DashboardIdentity:    sourceIdentity,
				AllowInsecureHTTP:    allowInsecureHTTP,
				ContinueOnInputError: continueOnInputError,
				OverridesFile:        overridesFile,
				FidelityThreshold:    fidelityThreshold,
				EmitV6:               emitV6,
				ProtectedInputs: []app.ProtectedInputPath{
					{Path: apiKeyFile, Purpose: "API-key file"},
					{Path: metricNameMapPath, Purpose: "metric-name map"},
				},
			})
			if err := writeGrafanaResults(outputWriter, results, jsonOutput(command.Flags())); err != nil {
				return err
			}
			if migrateErr != nil {
				return migrateErr
			}
			return dashboardReviewStatus(results)
		},
	}
	command.Flags().StringVarP(&outputDirectory, "out", "o", environmentDefault("PROMCAST_OUT", "out"), "output directory")
	command.Flags().DurationVar(&rateInterval, "rate-interval", 5*time.Minute, "replacement for Grafana rate intervals")
	command.Flags().DurationVar(&interval, "interval-default", time.Minute, "default panel interval")
	command.Flags().DurationVar(&interval, "interval", time.Minute, "alias for --interval-default")
	_ = command.Flags().MarkDeprecated("interval", "use --interval-default")
	command.Flags().DurationVar(&queryRange, "range", time.Hour, "default query range")
	command.Flags().StringVar(&targetURL, "target", environmentDefault("SIGNOZ_URL", ""), "SigNoz base URL for idempotent import")
	command.Flags().StringVar(&apiKey, "api-key", "", "SigNoz API key (prefer SIGNOZ_API_KEY)")
	command.Flags().StringVar(&apiKeyFile, "api-key-file", "", "file containing the SigNoz API key")
	command.Flags().BoolVar(&validate, "validate", true, "preview and execute queries before importing to a target")
	command.Flags().StringArrayVar(&variables, "var", nil, "dashboard variable override in name=value form (used as the generated default and for validation)")
	command.Flags().StringArrayVar(&ruleFiles, "rules", nil, "Prometheus rule file used to inline recording rules (repeatable)")
	command.Flags().IntVar(&workers, "workers", 4, "maximum concurrent target validation requests")
	command.Flags().BoolVar(&offline, "offline", false, "disable all network access")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "validate against SigNoz without importing")
	command.Flags().StringVar(&metricNameMapPath, "metric-name-map", "", "YAML mapping of source metric names to target metric names")
	command.Flags().StringVar(&sourceNamespace, "source-namespace", environmentDefault("PROMCAST_SOURCE_NAMESPACE", ""), "stable source estate or Grafana organization identifier used for target IDs")
	command.Flags().BoolVar(&continueOnInputError, "continue-on-input-error", false, "migrate every parseable dashboard instead of aborting the batch when one input fails")
	command.Flags().StringVar(&overridesFile, "overrides", "", "YAML file of operator/agent-provided Builder queries (verified live before emitting natively)")
	command.Flags().Float64Var(&fidelityThreshold, "fidelity", 0.05, "maximum relative deviation a live native promotion will accept (0.05 = 5%)")
	command.Flags().BoolVar(&emitV6, "emit-v6", false, "also write the SigNoz v6 (Perses) dashboard shape as a sibling <base>.v6.json")
	command.Flags().StringVar(&sourceIdentity, "source-identity", "", "stable logical identity for a UID-less dashboard (defaults to its input path)")
	command.Flags().BoolVar(&allowInsecureHTTP, "allow-insecure-http", false, "explicitly allow credentials over non-loopback plaintext HTTP")
	return command
}

func resolveAPIKey(flagValue, path string) (string, error) {
	if flagValue != "" && path != "" {
		return "", cliInputError(fmt.Errorf("--api-key and --api-key-file cannot be used together"))
	}
	if path != "" {
		value, err := readBoundedSecretFile(path)
		if err != nil {
			return "", cliInputError(fmt.Errorf("read API key file %q: %w", path, err))
		}
		return value, nil
	}
	if flagValue != "" {
		return strings.TrimSpace(flagValue), nil
	}
	return strings.TrimSpace(os.Getenv("SIGNOZ_API_KEY")), nil
}

func parseVariables(values []string) (map[string]string, error) {
	variables := make(map[string]string, len(values))
	for _, value := range values {
		name, variableValue, ok := strings.Cut(value, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, cliInputError(fmt.Errorf("invalid --var %q: expected name=value", value))
		}
		if _, exists := variables[name]; exists {
			return nil, cliInputError(fmt.Errorf("duplicate --var %q", name))
		}
		variables[name] = variableValue
	}
	return variables, nil
}
