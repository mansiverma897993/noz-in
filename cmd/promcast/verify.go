package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/mansiverma897993/signoz/internal/app"
	"github.com/mansiverma897993/signoz/internal/validate"
	"github.com/spf13/cobra"
)

func newVerifyCommand() *cobra.Command {
	var sourcePromQL string
	var candidatePath string
	var targetURL string
	var apiKey string
	var apiKeyFile string
	var variableFlags []string
	var queryRange time.Duration
	var fidelityThreshold float64
	var allowInsecureHTTP bool

	command := &cobra.Command{
		Use:   "verify",
		Short: "Verify an agent-proposed SigNoz Builder query against a source PromQL query on the live target",
		Long: "Runs a live differential between a candidate SigNoz Builder/formula query and a source PromQL query, " +
			"and reports the measured fidelity band. This is the safety gate for agent-proposed conversions: propose " +
			"anything, adopt only what verifies within --fidelity.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if candidatePath == "" {
				return cliInputError(fmt.Errorf("--candidate is required"))
			}
			candidateData, err := os.ReadFile(candidatePath)
			if err != nil {
				return cliInputError(fmt.Errorf("read candidate %q: %w", candidatePath, err))
			}
			var candidate app.CandidateSpec
			if err := json.Unmarshal(candidateData, &candidate); err != nil {
				return cliInputError(fmt.Errorf("parse candidate %q: %w", candidatePath, err))
			}
			resolvedAPIKey, err := resolveAPIKey(apiKey, apiKeyFile)
			if err != nil {
				return err
			}
			variables, err := parseVariables(variableFlags)
			if err != nil {
				return err
			}
			result, err := app.VerifyCandidate(command.Context(), app.VerifyOptions{
				SourcePromQL:      sourcePromQL,
				Candidate:         candidate,
				TargetURL:         targetURL,
				APIKey:            resolvedAPIKey,
				AllowInsecureHTTP: allowInsecureHTTP,
				Variables:         variables,
				Range:             queryRange,
				FidelityThreshold: fidelityThreshold,
			})
			if err != nil {
				return err
			}
			if err := writeVerifyResult(outputWriter, result, jsonOutput(command.Flags())); err != nil {
				return err
			}
			if !result.Pass {
				// Exit 2 marks a completed run whose candidate was not adopted, the
				// same review-status code the grafana verb uses.
				return statusError{code: 2}
			}
			return nil
		},
	}
	command.Flags().StringVar(&sourcePromQL, "source", "", "source Grafana PromQL query to reproduce")
	command.Flags().StringVar(&candidatePath, "candidate", "", "JSON file with the candidate SigNoz query: {\"builder\":{...}} or {\"formula\":{...}}")
	command.Flags().StringVar(&targetURL, "target", environmentDefault("SIGNOZ_URL", ""), "SigNoz base URL")
	command.Flags().StringVar(&apiKey, "api-key", "", "SigNoz API key (prefer SIGNOZ_API_KEY)")
	command.Flags().StringVar(&apiKeyFile, "api-key-file", "", "file containing the SigNoz API key")
	command.Flags().StringArrayVar(&variableFlags, "var", nil, "dashboard variable value in name=value form (used in both probes)")
	command.Flags().DurationVar(&queryRange, "range", time.Hour, "comparison window")
	command.Flags().Float64Var(&fidelityThreshold, "fidelity", 0.05, "maximum relative deviation to accept (0.05 = 5%)")
	command.Flags().BoolVar(&allowInsecureHTTP, "allow-insecure-http", false, "explicitly allow credentials over non-loopback plaintext HTTP")
	return command
}

func writeVerifyResult(writer interface{ Write([]byte) (int, error) }, result validate.FidelityResult, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(writer).Encode(result)
	}
	verdict := "REJECTED"
	if result.Pass {
		verdict = "ADOPTED"
	}
	_, err := fmt.Fprintf(writer,
		"%s\tfidelity=%s\tmaxRelErr=%.4f\tthreshold=%.4f\tseries=%d\tpoints=%d\t%s\n",
		verdict, result.Band, result.MaxRelative, result.Threshold, result.MatchedSeries, result.MatchedPoints, result.Detail,
	)
	return err
}
