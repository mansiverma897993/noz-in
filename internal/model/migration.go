package model

import "slices"

// Migration combines a normalized dashboard with per-query translations.
type Migration struct {
	Dashboard            Dashboard                      `json:"dashboard"`
	Translations         map[string]Translation         `json:"translations"`
	VariableTranslations map[string]VariableTranslation `json:"variableTranslations,omitempty"`
}

// VariableTranslation records the target-independent compatibility decision for a dashboard variable.
type VariableTranslation struct {
	Kind        string   `json:"kind"`
	Attribute   string   `json:"attribute,omitempty"`
	CustomValue string   `json:"customValue,omitempty"`
	Decision    Decision `json:"decision"`
}

// TranslationFor returns the translation associated with a query provenance path.
func (migration Migration) TranslationFor(query Query) (Translation, bool) {
	translation, ok := migration.Translations[query.SourcePath]
	return translation, ok
}

// VariableTranslationFor returns the decision associated with a variable provenance path.
func (migration Migration) VariableTranslationFor(variable Variable) (VariableTranslation, bool) {
	translation, ok := migration.VariableTranslations[variable.SourcePath]
	return translation, ok
}

// PanelDecision accounts for visualization-level behavior independently of query conversion.
func (migration Migration) PanelDecision(panel Panel) Decision {
	decision := Decision{Verdict: VerdictNative}
	addReview := func(reason ReasonCode) {
		decision.Verdict = VerdictNeedsReview
		decision.Reasons = appendReason(decision.Reasons, reason)
	}
	for _, reason := range basePanelReviewReasons(panel) {
		addReview(reason)
	}
	if panel.Repeat != "" {
		addReview(ReasonRepeatPanel)
	}
	if len(panel.Transforms) > 0 {
		addReview(ReasonGrafanaTransformation)
	}
	if panel.TimeFrom != "" || panel.TimeShift != "" {
		addReview(ReasonPanelTimeOverride)
	}
	for _, feature := range panel.SourceFeatures {
		addReview(feature.Reason)
	}
	if len(panel.Queries) == 0 && panel.Kind != PanelKindRow && panel.Kind != PanelKindText {
		addReview(ReasonNoQueryTargets)
	}
	allTargetsDisabled := len(panel.Queries) > 0
	for _, query := range panel.Queries {
		allTargetsDisabled = allTargetsDisabled && query.Hidden
		for _, feature := range query.SourceFeatures {
			addReview(feature.Reason)
		}
		translation, ok := migration.TranslationFor(query)
		if !ok || translation.Decision.Verdict == VerdictNeedsReview {
			decision.Verdict = VerdictNeedsReview
			if ok {
				for _, reason := range translation.Decision.Reasons {
					decision.Reasons = appendReason(decision.Reasons, reason)
				}
			}
		}
	}
	if builderNamesCollide(migration, panel) {
		addReview(ReasonQueryNameCollision)
	}
	if allTargetsDisabled {
		addReview(ReasonAllTargetsDisabled)
	}
	if !migration.PanelEmittable(panel) {
		addReview(ReasonPanelOmitted)
	}
	if migration.PanelMode(panel) == TranslationPromQL {
		if decision.Verdict == VerdictNative {
			decision.Verdict = VerdictPassthrough
		}
		if panel.Kind == PanelKindTable || panel.Kind == PanelKindPie || panel.Kind == PanelKindValue {
			addReview(ReasonPanelTypeDowngrade)
		}
	}
	return decision
}

func basePanelReviewReasons(panel Panel) []ReasonCode {
	reasons := make([]ReasonCode, 0, 3)
	switch panel.Kind {
	case PanelKindText:
		reasons = append(reasons, ReasonTextPanel)
	case PanelKindUnknown:
		reasons = append(reasons, ReasonUnsupportedPanel)
	case PanelKindBar, PanelKindHistogram:
		// The v5 emitter deliberately graph-downgrades these kinds because the
		// pinned native target visualizations are not semantically usable.
		reasons = append(reasons, ReasonVisualizationDowngrade)
	}
	if panel.Kind == PanelKindGraph && panel.SourceType == "timeseries" {
		reasons = append(reasons, ReasonGrafanaTimeseriesPointMode)
	}
	if panel.Kind == PanelKindGraph && (panel.SourceType == "graph" || panel.SourceType == "timeseries") {
		reasons = append(reasons, ReasonGrafanaGraphRenderingDefaults)
	}
	return reasons
}

func builderNamesCollide(migration Migration, panel Panel) bool {
	names := make(map[string]bool)
	for _, query := range panel.Queries {
		translation, ok := migration.TranslationFor(query)
		if !ok || translation.Kind == TranslationNone {
			continue
		}
		var candidates []string
		switch translation.Kind {
		case TranslationBuilder:
			candidates = []string{translation.Builder.Name}
		case TranslationFormula:
			for _, dependency := range translation.Formula.Queries {
				candidates = append(candidates, dependency.Name)
			}
			candidates = append(candidates, translation.Formula.Name)
		}
		for _, name := range candidates {
			if names[name] {
				return true
			}
			names[name] = true
		}
	}
	return false
}

// PanelEmittable reports whether a panel can be imported without inventing or
// silently dropping its visible result.
func (migration Migration) PanelEmittable(panel Panel) bool {
	if panel.Kind == PanelKindText || panel.Kind == PanelKindUnknown || hasPanelFeature(panel, ReasonLibraryPanel) {
		return false
	}
	if panel.Kind == PanelKindRow {
		return true
	}
	if len(panel.Queries) == 0 {
		return false
	}
	enabled := false
	for _, query := range panel.Queries {
		translation, ok := migration.TranslationFor(query)
		if !ok || translation.Kind == TranslationNone {
			if !query.Hidden {
				return false
			}
			continue
		}
		if !query.Hidden {
			enabled = true
		}
	}
	return enabled
}

func hasPanelFeature(panel Panel, reason ReasonCode) bool {
	for _, feature := range panel.SourceFeatures {
		if feature.Reason == reason {
			return true
		}
	}
	return false
}

func appendReason(reasons []ReasonCode, reason ReasonCode) []ReasonCode {
	if slices.Contains(reasons, reason) {
		return reasons
	}
	return append(reasons, reason)
}

// PanelMode returns the single query mode that can preserve a panel's targets.
func (migration Migration) PanelMode(panel Panel) TranslationKind {
	mode, _ := migration.panelMode(panel)
	return mode
}

// PanelFallbackReason explains why an otherwise-native panel must use PromQL.
func (migration Migration) PanelFallbackReason(panel Panel) ReasonCode {
	_, reason := migration.panelMode(panel)
	return reason
}

func (migration Migration) panelMode(panel Panel) (TranslationKind, ReasonCode) {
	if !migration.PanelEmittable(panel) {
		return TranslationNone, ReasonPanelOmitted
	}
	if panel.Kind == PanelKindRow {
		return TranslationBuilder, ""
	}
	if len(panel.Queries) == 0 {
		return TranslationBuilder, ""
	}
	if builderNamesCollide(migration, panel) {
		return TranslationPromQL, ReasonQueryNameCollision
	}
	for _, query := range panel.Queries {
		translation, ok := migration.TranslationFor(query)
		if ok && (translation.Kind == TranslationBuilder || translation.Kind == TranslationFormula) {
			// A Builder/formula candidate carries a semantic reason (rate/increase
			// window, latest lookback, histogram percentile) that is only a proven
			// equivalence once a live differential confirms it. Until promoted to a
			// native verdict the panel must ship as verbatim PromQL. A promoted
			// (native) translation has that live proof and emits as a Builder,
			// restoring drilldown.
			if translation.Decision.Verdict != VerdictNative {
				for _, reason := range translation.Decision.Reasons {
					if IsBuilderCandidateSemanticReason(reason) {
						return TranslationPromQL, reason
					}
				}
			}
		}
		if ok && translation.Kind == TranslationNone && query.Hidden {
			continue
		}
		if !ok || !translation.isBuilderCompatible() {
			return TranslationPromQL, ReasonMixedPanelQueries
		}
	}
	if panel.Kind == PanelKindValue {
		for _, query := range panel.Queries {
			translation, _ := migration.TranslationFor(query)
			if translation.Kind == TranslationBuilder && len(translation.Builder.GroupBy) > 0 {
				return TranslationPromQL, ReasonBuilderValueGroupBy
			}
			if translation.Kind == TranslationFormula {
				for _, builder := range translation.Formula.Queries {
					if len(builder.GroupBy) > 0 {
						return TranslationPromQL, ReasonBuilderValueGroupBy
					}
				}
			}
		}
	}
	return TranslationBuilder, ""
}

func (translation Translation) isBuilderCompatible() bool {
	switch translation.Kind {
	case TranslationBuilder:
		return translation.Builder != nil
	case TranslationFormula:
		return translation.Formula != nil && len(translation.Formula.Queries) > 0
	default:
		return false
	}
}
