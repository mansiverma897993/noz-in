package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mansiverma897993/signoz/internal/app"
	"github.com/mansiverma897993/signoz/internal/metricmap"
	reportpkg "github.com/mansiverma897993/signoz/internal/report"
	"github.com/mansiverma897993/signoz/internal/safeoutput"
	"github.com/spf13/cobra"
)

func newDiffCommand() *cobra.Command {
	var sourceURL string
	var sourceBearerTokenFile string
	var targetURL string
	var targetAPIKey string
	var targetAPIKeyFile string
	var targetProvenance string
	var outputPath string
	var sourceVariableFlags []string
	var targetVariableFlags []string
	var rateInterval time.Duration
	var interval time.Duration
	var queryRange time.Duration
	var step time.Duration
	var timestampTolerance time.Duration
	var relativeTolerance float64
	var absoluteTolerance float64
	var minimumCoverage float64
	var workers int
	var maxQueries int
	var metricNameMapPath string
	var migrationReportPath string
	var allowInsecureHTTP bool

	command := &cobra.Command{
		Use:   "diff <dashboard.json>",
		Short: "Compare live Prometheus and SigNoz results for a Grafana dashboard",
		Args:  exactArgs(1),
		RunE: func(command *cobra.Command, paths []string) error {
			if outputPath != "" {
				protectedInputs := []safeoutput.ProtectedPath{
					{Path: paths[0], Purpose: "source dashboard"},
					{Path: sourceBearerTokenFile, Purpose: "source bearer-token file"},
					{Path: targetAPIKeyFile, Purpose: "target API-key file"},
					{Path: metricNameMapPath, Purpose: "metric-name map"},
				}
				if err := safeoutput.RejectAliases(outputPath, protectedInputs...); err != nil {
					return cliInputError(err)
				}
				if migrationReportPath != "" {
					if err := reportpkg.ValidateDashboardOutputPath(migrationReportPath, outputPath); err != nil {
						return cliInputError(fmt.Errorf("validate differential output path: %w", err))
					}
				}
			}
			sourceVariables, err := parseVariables(sourceVariableFlags)
			if err != nil {
				return cliInputError(fmt.Errorf("source variables: %w", err))
			}
			targetVariables, err := parseVariables(targetVariableFlags)
			if err != nil {
				return cliInputError(fmt.Errorf("target variables: %w", err))
			}
			sourceToken, err := resolveOptionalSecret(sourceBearerTokenFile, "PROMETHEUS_BEARER_TOKEN")
			if err != nil {
				return err
			}
			apiKey, err := resolveAPIKey(targetAPIKey, targetAPIKeyFile)
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
			report, err := app.ValidateGrafanaDifferential(command.Context(), paths[0], app.DifferentialOptions{
				SourceURL:           sourceURL,
				SourceBearerToken:   sourceToken,
				TargetURL:           targetURL,
				TargetAPIKey:        apiKey,
				TargetProvenance:    targetProvenance,
				SourceVariables:     sourceVariables,
				TargetVariables:     targetVariables,
				MetricNameMap:       metricNames,
				RateInterval:        rateInterval,
				Interval:            interval,
				Range:               queryRange,
				Step:                step,
				TimestampTolerance:  timestampTolerance,
				RelativeTolerance:   relativeTolerance,
				AbsoluteTolerance:   absoluteTolerance,
				MinimumCoverage:     minimumCoverage,
				Workers:             workers,
				MaxQueries:          maxQueries,
				MigrationReportPath: migrationReportPath,
				AllowInsecureHTTP:   allowInsecureHTTP,
			})
			if err != nil {
				return err
			}
			if outputPath != "" {
				if err := app.WriteDifferentialReport(outputPath, report); err != nil {
					return err
				}
			}
			if migrationReportPath != "" {
				if err := app.AttachDifferentialEvidence(migrationReportPath, report); err != nil {
					return err
				}
			}
			if err := writeDifferentialSummary(outputWriter, report.Summary, jsonOutput(command.Flags())); err != nil {
				return err
			}
			return differentialReviewStatus(report.Summary)
		},
	}
	command.Flags().StringVar(&sourceURL, "source", "", "Prometheus base URL")
	command.Flags().StringVar(&sourceBearerTokenFile, "source-bearer-token-file", "", "file containing an optional Prometheus bearer token")
	command.Flags().StringVar(&targetURL, "target", "", "SigNoz base URL")
	command.Flags().StringVar(&targetAPIKey, "api-key", "", "SigNoz API key (prefer SIGNOZ_API_KEY)")
	command.Flags().StringVar(&targetAPIKeyFile, "api-key-file", "", "file containing the SigNoz API key")
	command.Flags().StringVar(
		&targetProvenance,
		"target-provenance",
		"",
		"explicit target ingestion provenance (supported: otel_prometheus_receiver; empty keeps exact labels)",
	)
	command.Flags().StringVarP(&outputPath, "out", "o", "differential-report.json", "differential evidence report path")
	command.Flags().StringArrayVar(&sourceVariableFlags, "source-var", nil, "source Grafana variable in name=value form")
	command.Flags().StringArrayVar(&targetVariableFlags, "target-var", nil, "target SigNoz variable in name=value form")
	command.Flags().DurationVar(&rateInterval, "rate-interval", 5*time.Minute, "replacement for Grafana rate intervals")
	command.Flags().DurationVar(&interval, "interval", time.Minute, "replacement for Grafana panel intervals")
	command.Flags().DurationVar(&queryRange, "range", 30*time.Minute, "live comparison range")
	command.Flags().DurationVar(&step, "step", time.Minute, "query step")
	command.Flags().DurationVar(&timestampTolerance, "timestamp-tolerance", time.Minute, "maximum source/target sample time difference")
	command.Flags().Float64Var(&relativeTolerance, "relative-tolerance", 0.15, "maximum relative numeric error")
	command.Flags().Float64Var(&absoluteTolerance, "absolute-tolerance", 1e-9, "maximum absolute numeric error")
	command.Flags().Float64Var(&minimumCoverage, "minimum-coverage", 0.8, "minimum matched source-point fraction")
	command.Flags().IntVar(&workers, "workers", 4, "maximum concurrent query comparisons")
	command.Flags().IntVar(&maxQueries, "max-queries", 0, "maximum visible queries to compare (0 means all)")
	command.Flags().StringVar(&metricNameMapPath, "metric-name-map", "", "YAML mapping of source metric names to target metric names")
	command.Flags().StringVar(&migrationReportPath, "migration-report", "", "attach comparisons to an existing dashboard report and regenerate its HTML")
	command.Flags().BoolVar(&allowInsecureHTTP, "allow-insecure-http", false, "explicitly allow credentials over non-loopback plaintext HTTP")
	_ = command.MarkFlagRequired("source")
	_ = command.MarkFlagRequired("target")
	return command
}

func resolveOptionalSecret(path, environmentName string) (string, error) {
	if path == "" {
		return strings.TrimSpace(os.Getenv(environmentName)), nil
	}
	value, err := readBoundedSecretFile(path)
	if err != nil {
		return "", cliInputError(fmt.Errorf("read secret file %q: %w", path, err))
	}
	return value, nil
}
