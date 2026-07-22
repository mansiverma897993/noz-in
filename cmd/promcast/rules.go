package main

import (
	"fmt"

	"github.com/mansiverma897993/noz-in/internal/app"
	"github.com/mansiverma897993/noz-in/internal/metricmap"
	"github.com/spf13/cobra"
)

func newPrometheusRulesCommand() *cobra.Command {
	var outputDirectory string
	var targetURL string
	var apiKey string
	var apiKeyFile string
	var validate bool
	var workers int
	var offline bool
	var dryRun bool
	var metricNameMapPath string
	var alertOnAbsent bool
	var sourceNamespace string
	var allowInsecureHTTP bool
	command := &cobra.Command{
		Use:     "rules <rules.yaml>...",
		Aliases: []string{"prometheus-rules"},
		Short:   "Migrate Prometheus alerting rules",
		Args:    minimumArgs(1),
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
			metricNames := map[string]string(nil)
			if metricNameMapPath != "" {
				metricNames, err = metricmap.Load(metricNameMapPath)
				if err != nil {
					return &app.Error{Kind: app.ErrorInput, Err: err}
				}
			}
			results, migrateErr := app.MigratePrometheusRules(command.Context(), paths, app.RuleOptions{
				OutputDirectory:   outputDirectory,
				TargetURL:         targetURL,
				APIKey:            resolvedAPIKey,
				Validate:          validate,
				ValidationWorkers: workers,
				MetricNameMap:     metricNames,
				AlertOnAbsent:     alertOnAbsent,
				DryRun:            dryRun,
				SourceNamespace:   sourceNamespace,
				AllowInsecureHTTP: allowInsecureHTTP,
				ProtectedInputs: []app.ProtectedInputPath{
					{Path: apiKeyFile, Purpose: "API-key file"},
					{Path: metricNameMapPath, Purpose: "metric-name map"},
				},
			})
			if err := writeRuleResults(outputWriter, results, jsonOutput(command.Flags())); err != nil {
				return err
			}
			if migrateErr != nil {
				return migrateErr
			}
			return ruleReviewStatus(results)
		},
	}
	command.Flags().StringVarP(&outputDirectory, "out", "o", environmentDefault("PROMCAST_OUT", "out"), "output directory")
	command.Flags().StringVar(&targetURL, "target", environmentDefault("SIGNOZ_URL", ""), "SigNoz base URL for idempotent import")
	command.Flags().StringVar(&apiKey, "api-key", "", "SigNoz API key (prefer SIGNOZ_API_KEY)")
	command.Flags().StringVar(&apiKeyFile, "api-key-file", "", "file containing the SigNoz API key")
	command.Flags().BoolVar(&validate, "validate", true, "preview and execute queries before importing to a target")
	command.Flags().IntVar(&workers, "workers", 4, "maximum concurrent target validation requests")
	command.Flags().BoolVar(&offline, "offline", false, "disable all network access")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "validate against SigNoz without importing")
	command.Flags().StringVar(&metricNameMapPath, "metric-name-map", "", "YAML mapping of source metric names to target metric names")
	command.Flags().BoolVar(&alertOnAbsent, "alert-on-absent", false, "alert when the query returns no data")
	command.Flags().StringVar(&sourceNamespace, "source-namespace", environmentDefault("PROMCAST_SOURCE_NAMESPACE", ""), "stable logical rule collection identifier used for target rule IDs")
	command.Flags().BoolVar(&allowInsecureHTTP, "allow-insecure-http", false, "explicitly allow credentials over non-loopback plaintext HTTP")
	return command
}
