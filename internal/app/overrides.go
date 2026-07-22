package app

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/mansiverma897993/noz-in/internal/model"
	"gopkg.in/yaml.v3"
)

// DashboardOverride replaces the translation of one source query with an
// operator-provided (typically agent-proposed and separately verified) SigNoz
// Builder or formula query, keyed by the query's source path in the report. The
// builder/formula shapes use the same field names as the verify candidate and
// the report, so an agent can copy a verified candidate straight into an
// override.
type DashboardOverride struct {
	SourcePath string              `json:"sourcePath"`
	Builder    *model.BuilderQuery `json:"builder,omitempty"`
	Formula    *model.Formula      `json:"formula,omitempty"`
}

type overridesFile struct {
	Overrides []DashboardOverride `json:"overrides"`
}

// loadOverrides reads and validates an overrides YAML file. It decodes through a
// YAML->JSON round-trip so the builder/formula fields bind by their JSON tags
// (the same camelCase names used everywhere else in the tool). Every entry must
// name a source path and carry exactly one of a builder or a formula.
func loadOverrides(path string) ([]DashboardOverride, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, inputError(fmt.Errorf("read overrides %q: %w", path, err))
	}
	var raw any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, inputError(fmt.Errorf("parse overrides %q: %w", path, err))
	}
	jsonData, err := json.Marshal(raw)
	if err != nil {
		return nil, inputError(fmt.Errorf("normalize overrides %q: %w", path, err))
	}
	var file overridesFile
	if err := json.Unmarshal(jsonData, &file); err != nil {
		return nil, inputError(fmt.Errorf("decode overrides %q: %w", path, err))
	}
	for index, override := range file.Overrides {
		if strings.TrimSpace(override.SourcePath) == "" {
			return nil, inputError(fmt.Errorf("override %d is missing a sourcePath", index))
		}
		if (override.Builder == nil) == (override.Formula == nil) {
			return nil, inputError(fmt.Errorf("override %q must have exactly one of builder or formula", override.SourcePath))
		}
	}
	return file.Overrides, nil
}

// applyOverrides swaps each named query's translation for its override candidate,
// preserving the original PromQL as the differential's source of truth and
// marking the translation as a Builder candidate at needs-review. The live
// promotion gate then verifies the candidate exactly like any other, so an
// override only becomes native if it is proven equivalent on the target; offline
// it stays a review item and ships as the original passthrough.
func applyOverrides(migration model.Migration, overrides []DashboardOverride) []string {
	applied := make([]string, 0, len(overrides))
	for _, override := range overrides {
		existing, found := migration.Translations[override.SourcePath]
		if !found {
			continue
		}
		replacement := existing
		if override.Formula != nil {
			replacement.Kind = model.TranslationFormula
			replacement.Formula = override.Formula
			replacement.Builder = nil
			replacement.Decision.Reasons = overrideReasons(model.ReasonBuilderFormulaEvaluation)
		} else {
			replacement.Kind = model.TranslationBuilder
			replacement.Builder = override.Builder
			replacement.Formula = nil
			replacement.Decision.Reasons = overrideReasons(builderSemanticReason(override.Builder))
		}
		// Preserve the original passthrough so the differential compares the
		// override against what the source query actually produces.
		if strings.TrimSpace(replacement.PromQL) == "" {
			replacement.PromQL = existing.PromQL
		}
		replacement.Decision.Verdict = model.VerdictNeedsReview
		migration.Translations[override.SourcePath] = replacement
		applied = append(applied, override.SourcePath)
	}
	return applied
}

func overrideReasons(semantic model.ReasonCode) []model.ReasonCode {
	return []model.ReasonCode{model.ReasonOperatorOverride, semantic}
}

// builderSemanticReason mirrors the transpiler's builder-candidate classification
// so an overridden query is recognized as a promotable candidate.
func builderSemanticReason(builder *model.BuilderQuery) model.ReasonCode {
	if builder == nil {
		return model.ReasonBuilderLatestLookback
	}
	if strings.HasPrefix(builder.SpaceAggregation, "p") {
		return model.ReasonBuilderHistogramPercentile
	}
	switch builder.TimeAggregation {
	case "rate", "increase":
		return model.ReasonBuilderRateIncrease
	default:
		return model.ReasonBuilderLatestLookback
	}
}
