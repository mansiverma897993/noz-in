package app

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mansiverma897993/signoz/internal/model"
	"github.com/mansiverma897993/signoz/internal/target/signoz"
	"github.com/mansiverma897993/signoz/internal/validate"
)

// CandidateSpec is an agent-proposed SigNoz query to verify against a source
// PromQL query. Exactly one of Builder or Formula must be set.
type CandidateSpec struct {
	Builder *model.BuilderQuery `json:"builder,omitempty"`
	Formula *model.Formula      `json:"formula,omitempty"`
}

// VerifyOptions parameterizes a single candidate verification.
type VerifyOptions struct {
	SourcePromQL      string
	Candidate         CandidateSpec
	TargetURL         string
	APIKey            string
	HTTPClient        *http.Client
	AllowInsecureHTTP bool
	Variables         map[string]string
	Range             time.Duration
	FidelityThreshold float64
}

// VerifyCandidate runs the live differential between an agent-proposed Builder
// query and a source PromQL query, returning the measured fidelity band. It is
// the public safety gate for skill-driven, agent-proposed conversions: propose
// anything, adopt only what verifies within the operator's threshold.
func VerifyCandidate(ctx context.Context, options VerifyOptions) (validate.FidelityResult, error) {
	if strings.TrimSpace(options.SourcePromQL) == "" {
		return validate.FidelityResult{}, inputError(fmt.Errorf("--source PromQL is required"))
	}
	if options.Candidate.Builder == nil && options.Candidate.Formula == nil {
		return validate.FidelityResult{}, inputError(fmt.Errorf("--candidate must contain a builder or formula query"))
	}
	if strings.TrimSpace(options.TargetURL) == "" {
		return validate.FidelityResult{}, inputError(fmt.Errorf("--target is required to verify against live data"))
	}
	if options.Range <= 0 {
		options.Range = time.Hour
	}
	client, err := signoz.NewClientWithOptions(
		options.TargetURL, options.APIKey, options.HTTPClient,
		signoz.ClientOptions{AllowInsecureHTTP: options.AllowInsecureHTTP},
	)
	if err != nil {
		return validate.FidelityResult{}, targetError(err)
	}
	values := make(map[string]any, len(options.Variables))
	for name, value := range options.Variables {
		values[name] = value
	}
	items, err := signoz.VariableItems(values, nil)
	if err != nil {
		return validate.FidelityResult{}, inputError(err)
	}
	candidate := candidateTranslation(options.Candidate, options.SourcePromQL)
	result := validate.VerifyCandidate(ctx, client, candidate, options.SourcePromQL, validate.PromoteOptions{
		Now:                time.Now().UTC(),
		Window:             options.Range,
		Variables:          items,
		RelativeTolerance:  options.FidelityThreshold,
		AbsoluteTolerance:  1e-9,
		TimestampTolerance: time.Minute,
		MinimumPoints:      3,
	})
	return result, nil
}

func candidateTranslation(candidate CandidateSpec, sourcePromQL string) model.Translation {
	if candidate.Formula != nil {
		return model.Translation{Kind: model.TranslationFormula, Formula: candidate.Formula, PromQL: sourcePromQL}
	}
	return model.Translation{Kind: model.TranslationBuilder, Builder: candidate.Builder, PromQL: sourcePromQL}
}
