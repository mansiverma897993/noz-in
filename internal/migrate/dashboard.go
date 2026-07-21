package migrate

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/mansiverma897993/signoz/internal/model"
	"github.com/mansiverma897993/signoz/internal/target/signoz"
	"github.com/mansiverma897993/signoz/internal/transpile"
)

// Dashboard translates every query in a normalized dashboard.
func Dashboard(source model.Dashboard, analyzer *transpile.Analyzer) model.Migration {
	migration := model.Migration{
		Dashboard:            source,
		Translations:         make(map[string]model.Translation),
		VariableTranslations: make(map[string]model.VariableTranslation),
	}
	for _, panel := range source.Panels {
		for _, query := range panel.Queries {
			translation := analyzer.Analyze(query)
			if panel.Kind == model.PanelKindRow {
				translation.Kind = model.TranslationNone
				translation.Builder = nil
				translation.Formula = nil
				translation.PromQL = ""
				translation.Decision.Verdict = model.VerdictNeedsReview
				translation.Decision.Reasons = appendUniqueReason(translation.Decision.Reasons, model.ReasonRowPanelTarget)
			}
			migration.Translations[query.SourcePath] = translation
		}
	}
	unresolvedSelections := make(map[string][]model.ReasonCode)
	for _, variable := range source.Variables {
		translation := translateVariable(variable, analyzer)
		if translation.Kind != "none" && hasMissingCurrentSelection(variable.Current) {
			translation.Kind = "none"
			translation.Decision.Verdict = model.VerdictNeedsReview
			translation.Decision.Reasons = appendUniqueReason(translation.Decision.Reasons, model.ReasonMissingVariableValue)
			translation.Decision.Notes = append(translation.Decision.Notes, fmt.Sprintf(
				"Grafana variable %q has no nonblank current selection; the target variable and dependent queries were omitted rather than executing an unvalidated load-time default.",
				variable.Name,
			))
		}
		migration.VariableTranslations[variable.SourcePath] = translation
		if isUnresolvedNonDynamicAll(variable, translation) {
			unresolvedSelections[variable.Name] = appendUniqueReason(
				unresolvedSelections[variable.Name], model.ReasonMissingVariableValue,
			)
			unresolvedSelections[variable.Name] = appendUniqueReason(
				unresolvedSelections[variable.Name], model.ReasonVariableAllValue,
			)
		}
		if isContradictoryMultiSelection(variable, translation) {
			unresolvedSelections[variable.Name] = appendUniqueReason(
				unresolvedSelections[variable.Name], model.ReasonMissingVariableValue,
			)
			unresolvedSelections[variable.Name] = appendUniqueReason(
				unresolvedSelections[variable.Name], model.ReasonMultiVariableValue,
			)
		}
		if isUnresolvedCustomReload(variable, translation) {
			unresolvedSelections[variable.Name] = appendUniqueReason(
				unresolvedSelections[variable.Name], model.ReasonMissingVariableValue,
			)
			unresolvedSelections[variable.Name] = appendUniqueReason(
				unresolvedSelections[variable.Name], model.ReasonCustomVariableReload,
			)
		}
		if isUnresolvedEmptySelection(variable, translation) {
			unresolvedSelections[variable.Name] = appendUniqueReason(
				unresolvedSelections[variable.Name], model.ReasonMissingVariableValue,
			)
		}
	}
	omitQueriesWithUnresolvedSelections(&migration, unresolvedSelections)
	omitQueriesWithDivergentTargetVariableEscaping(&migration)
	return migration
}

var (
	labelValuesScopedPattern = regexp.MustCompile(`(?i)^\s*label_values\s*\((.+),\s*([A-Za-z_:][A-Za-z0-9_:.-]*)\s*\)\s*$`)
	labelValuesGlobalPattern = regexp.MustCompile(`(?i)^\s*label_values\s*\(\s*([A-Za-z_:][A-Za-z0-9_:.-]*)\s*\)\s*$`)
)

func translateVariable(variable model.Variable, analyzer *transpile.Analyzer) model.VariableTranslation {
	translation := model.VariableTranslation{Decision: model.Decision{Verdict: model.VerdictNative}}
	switch variable.Kind {
	case model.VariableKindQuery:
		if match := labelValuesScopedPattern.FindStringSubmatch(variable.Query); len(match) == 3 {
			translation.Kind = "dynamic"
			translation.Attribute = analyzer.TargetLabel(match[2])
			translation.Decision = model.Decision{Verdict: model.VerdictNeedsReview, Reasons: []model.ReasonCode{model.ReasonVariableSelectorScope}}
			if variable.Regex != "" {
				translation.Decision.Reasons = appendUniqueReason(translation.Decision.Reasons, model.ReasonVariableRegex)
			}
			if strings.Contains(variable.Query, "$") {
				translation.Decision.Reasons = appendUniqueReason(translation.Decision.Reasons, model.ReasonChainedVariable)
			}
			return qualifyVariableSemantics(variable, translation)
		}
		if match := labelValuesGlobalPattern.FindStringSubmatch(variable.Query); len(match) == 2 {
			translation.Kind = "dynamic"
			translation.Attribute = analyzer.TargetLabel(match[1])
			if variable.Regex != "" {
				translation.Decision = model.Decision{Verdict: model.VerdictNeedsReview, Reasons: []model.ReasonCode{model.ReasonVariableRegex}}
			}
			return qualifyVariableSemantics(variable, translation)
		}
		translation.Kind = "textbox"
		if strings.Contains(strings.ToLower(variable.Query), "query_result") {
			translation.Decision = model.Decision{Verdict: model.VerdictNeedsReview, Reasons: []model.ReasonCode{model.ReasonQueryResultVariable}}
		} else {
			translation.Decision = model.Decision{Verdict: model.VerdictNeedsReview, Reasons: []model.ReasonCode{model.ReasonUnsupportedVariable}}
		}
	case model.VariableKindCustom, model.VariableKindInterval:
		translation.Kind = "custom"
		translation = qualifyCustomVariableReload(variable, translation)
	case model.VariableKindConstant:
		translation.Kind = "textbox"
		translation.Decision = model.Decision{
			Verdict: model.VerdictNeedsReview,
			Reasons: []model.ReasonCode{model.ReasonGrafanaConstantVariable},
		}
	case model.VariableKindText:
		translation.Kind = "textbox"
	case model.VariableKindDatasource:
		translation.Kind = "none"
		translation.Decision = model.Decision{Verdict: model.VerdictNeedsReview, Reasons: []model.ReasonCode{model.ReasonDatasourceVariable}}
	default:
		translation.Kind = "textbox"
		translation.Decision = model.Decision{Verdict: model.VerdictNeedsReview, Reasons: []model.ReasonCode{model.ReasonUnsupportedVariable}}
	}
	return qualifyVariableSemantics(variable, translation)
}

func qualifyCustomVariableReload(
	variable model.Variable,
	translation model.VariableTranslation,
) model.VariableTranslation {
	encoded, ok := signoz.EncodeStableCustomSelection(variable.Current)
	if !ok {
		translation.Kind = "none"
		translation.Decision.Verdict = model.VerdictNeedsReview
		translation.Decision.Reasons = appendUniqueReason(
			translation.Decision.Reasons, model.ReasonMissingVariableValue,
		)
		translation.Decision.Reasons = appendUniqueReason(
			translation.Decision.Reasons, model.ReasonCustomVariableReload,
		)
		translation.Decision.Notes = append(translation.Decision.Notes, fmt.Sprintf(
			"Custom variable %q has no exact string selection that survives the pinned target reload parser; the variable and dependent queries were omitted.",
			variable.Name,
		))
		return translation
	}
	translation.CustomValue = encoded
	sourceOptions, sourceExact := signoz.DecodeStableCustomSelection(variable.Query)
	if !sourceExact || !slices.Equal(sourceOptions, variable.Current) {
		translation.Decision.Verdict = model.VerdictNeedsReview
		translation.Decision.Reasons = appendUniqueReason(
			translation.Decision.Reasons, model.ReasonCustomVariableReload,
		)
		translation.Decision.Notes = append(translation.Decision.Notes, fmt.Sprintf(
			"Custom variable %q options were reduced to the proven current selection so target reload executes the value that was validated.",
			variable.Name,
		))
	}
	return translation
}

func qualifyVariableSemantics(variable model.Variable, translation model.VariableTranslation) model.VariableTranslation {
	allValue := strings.TrimSpace(variable.AllValue)
	if variable.IncludeAll && allValue != "" && allValue != ".*" {
		translation.Decision.Verdict = model.VerdictNeedsReview
		translation.Decision.Reasons = appendUniqueReason(translation.Decision.Reasons, model.ReasonVariableAllValue)
		translation.Decision.Notes = append(translation.Decision.Notes, fmt.Sprintf(
			"Grafana allValue=%q differs from target All matcher removal; current=%q.",
			variable.AllValue,
			variable.Current,
		))
	}
	if translation.Kind != "dynamic" && translation.Kind != "none" && variable.IncludeAll && isAllVariableSelection(variable.Current) {
		translation.Kind = "none"
		translation.Decision.Verdict = model.VerdictNeedsReview
		translation.Decision.Reasons = appendUniqueReason(translation.Decision.Reasons, model.ReasonMissingVariableValue)
		translation.Decision.Reasons = appendUniqueReason(translation.Decision.Reasons, model.ReasonVariableAllValue)
		translation.Decision.Notes = append(translation.Decision.Notes, fmt.Sprintf(
			"Grafana All is selected for non-dynamic variable %q, but the normalized export has no proven complete option list; the target variable and dependent queries were omitted.",
			variable.Name,
		))
	}
	if !variable.Multi && len(variable.Current) > 1 {
		translation.Kind = "none"
		translation.Decision.Verdict = model.VerdictNeedsReview
		translation.Decision.Reasons = appendUniqueReason(translation.Decision.Reasons, model.ReasonMissingVariableValue)
		translation.Decision.Reasons = appendUniqueReason(translation.Decision.Reasons, model.ReasonMultiVariableValue)
		translation.Decision.Notes = append(translation.Decision.Notes, fmt.Sprintf(
			"Grafana variable %q has %d current values while multi is disabled; the contradictory target variable and dependent queries were omitted.",
			variable.Name,
			len(variable.Current),
		))
	}
	for _, feature := range variable.SourceFeatures {
		translation.Decision.Verdict = model.VerdictNeedsReview
		translation.Decision.Reasons = appendUniqueReason(translation.Decision.Reasons, feature.Reason)
	}
	return translation
}

func isAllVariableSelection(values []string) bool {
	if len(values) != 1 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(values[0])) {
	case "all", "$__all", "__all__":
		return true
	default:
		return false
	}
}

func isUnresolvedNonDynamicAll(variable model.Variable, translation model.VariableTranslation) bool {
	return translation.Kind == "none" &&
		variable.IncludeAll &&
		isAllVariableSelection(variable.Current) &&
		slices.Contains(translation.Decision.Reasons, model.ReasonMissingVariableValue) &&
		slices.Contains(translation.Decision.Reasons, model.ReasonVariableAllValue)
}

func isContradictoryMultiSelection(variable model.Variable, translation model.VariableTranslation) bool {
	return translation.Kind == "none" &&
		!variable.Multi &&
		len(variable.Current) > 1 &&
		slices.Contains(translation.Decision.Reasons, model.ReasonMissingVariableValue) &&
		slices.Contains(translation.Decision.Reasons, model.ReasonMultiVariableValue)
}

func isUnresolvedCustomReload(variable model.Variable, translation model.VariableTranslation) bool {
	return (variable.Kind == model.VariableKindCustom || variable.Kind == model.VariableKindInterval) &&
		translation.Kind == "none" &&
		slices.Contains(translation.Decision.Reasons, model.ReasonMissingVariableValue) &&
		slices.Contains(translation.Decision.Reasons, model.ReasonCustomVariableReload)
}

func isUnresolvedEmptySelection(variable model.Variable, translation model.VariableTranslation) bool {
	return translation.Kind == "none" &&
		hasMissingCurrentSelection(variable.Current) &&
		slices.Contains(translation.Decision.Reasons, model.ReasonMissingVariableValue)
}

func hasMissingCurrentSelection(values []string) bool {
	return len(values) == 0 || slices.ContainsFunc(values, func(value string) bool {
		return strings.TrimSpace(value) == ""
	})
}

func omitQueriesWithUnresolvedSelections(
	migration *model.Migration,
	unresolved map[string][]model.ReasonCode,
) {
	if len(unresolved) == 0 {
		return
	}
	for _, panel := range migration.Dashboard.Panels {
		for _, query := range panel.Queries {
			translation, ok := migration.Translations[query.SourcePath]
			if !ok {
				continue
			}
			for _, name := range unresolvedSelectionReferences(query.Expression, unresolved) {
				translation.Kind = model.TranslationNone
				translation.Builder = nil
				translation.Formula = nil
				translation.PromQL = ""
				translation.Decision.Verdict = model.VerdictNeedsReview
				for _, reason := range unresolved[name] {
					translation.Decision.Reasons = appendUniqueReason(translation.Decision.Reasons, reason)
				}
				note := unresolvedSelectionQueryNote(name, unresolved[name])
				if !slices.Contains(translation.Decision.Notes, note) {
					translation.Decision.Notes = append(translation.Decision.Notes, note)
				}
			}
			migration.Translations[query.SourcePath] = translation
		}
	}
}

func omitQueriesWithDivergentTargetVariableEscaping(migration *model.Migration) {
	variables := make(map[string]model.Variable, len(migration.Dashboard.Variables))
	variableNames := make([]string, 0, len(migration.Dashboard.Variables))
	for _, variable := range migration.Dashboard.Variables {
		variables[variable.Name] = variable
		variableNames = append(variableNames, variable.Name)
	}
	slices.Sort(variableNames)
	affectedVariables := make(map[string]bool)
	for _, panel := range migration.Dashboard.Panels {
		panelMode := migration.PanelMode(panel)
		for _, query := range panel.Queries {
			translation, ok := migration.Translations[query.SourcePath]
			if !ok || translation.Kind == model.TranslationNone {
				continue
			}
			if panelMode == model.TranslationPromQL && translation.PromQL != "" &&
				!transpile.TargetPromQLRuntimeSubstitutionExact(
					query.Expression, translation.PromQL, variableNames,
				) {
				translation.Kind = model.TranslationNone
				translation.Builder = nil
				translation.Formula = nil
				translation.PromQL = ""
				translation.Decision.Verdict = model.VerdictNeedsReview
				translation.Decision.Reasons = appendUniqueReason(
					translation.Decision.Reasons, model.ReasonVariableValueEscaping,
				)
				note := "Query omitted because pinned SigNoz runtime-variable or Go-template rendering would change bytes Grafana sends unchanged to Prometheus."
				if !slices.Contains(translation.Decision.Notes, note) {
					translation.Decision.Notes = append(translation.Decision.Notes, note)
				}
				migration.Translations[query.SourcePath] = translation
				continue
			}
			for _, name := range transpile.VariableNames(query.Expression) {
				variable, defined := variables[name]
				if !defined {
					continue
				}
				variableTranslation, translated := migration.VariableTranslations[variable.SourcePath]
				if targetVariableSelectionExact(
					query.Expression, name, variable, variableTranslation, translated, variableNames,
				) {
					continue
				}
				translation.Kind = model.TranslationNone
				translation.Builder = nil
				translation.Formula = nil
				translation.PromQL = ""
				translation.Decision.Verdict = model.VerdictNeedsReview
				translation.Decision.Reasons = appendUniqueReason(
					translation.Decision.Reasons, model.ReasonMissingVariableValue,
				)
				translation.Decision.Reasons = appendUniqueReason(
					translation.Decision.Reasons, model.ReasonVariableValueEscaping,
				)
				if isTargetDynamicAllControl(variable, variableTranslation, translated) {
					translation.Decision.Reasons = appendUniqueReason(
						translation.Decision.Reasons, model.ReasonVariableAllValue,
					)
				}
				note := fmt.Sprintf(
					"Query omitted because Grafana Prometheus interpolation of variable %q does not equal pinned SigNoz raw selectedValue substitution for the current selection.",
					name,
				)
				if !slices.Contains(translation.Decision.Notes, note) {
					translation.Decision.Notes = append(translation.Decision.Notes, note)
				}
				affectedVariables[variable.SourcePath] = true
			}
			migration.Translations[query.SourcePath] = translation
		}
	}
	for sourcePath := range affectedVariables {
		translation, ok := migration.VariableTranslations[sourcePath]
		if !ok {
			continue
		}
		translation.Decision.Verdict = model.VerdictNeedsReview
		translation.Decision.Reasons = appendUniqueReason(
			translation.Decision.Reasons, model.ReasonVariableValueEscaping,
		)
		note := "The current selection requires Grafana Prometheus escaping, but pinned SigNoz substitutes selectedValue raw; dependent non-pipe queries were omitted."
		if !slices.Contains(translation.Decision.Notes, note) {
			translation.Decision.Notes = append(translation.Decision.Notes, note)
		}
		migration.VariableTranslations[sourcePath] = translation
	}
}

func targetVariableSelectionExact(
	expression string,
	name string,
	variable model.Variable,
	translation model.VariableTranslation,
	translated bool,
	runtimeVariableNames []string,
) bool {
	if isTargetDynamicAllControl(variable, translation, translated) {
		return variable.IncludeAll && strings.TrimSpace(variable.AllValue) == ".*" &&
			transpile.TargetDynamicAllMatcherRemovalExact(expression, name)
	}
	return transpile.TargetRawVariableSubstitutionExact(
		expression, name, variable.Current, variable.Multi || variable.IncludeAll, runtimeVariableNames,
	)
}

func isTargetDynamicAllControl(
	variable model.Variable,
	translation model.VariableTranslation,
	translated bool,
) bool {
	return translated && translation.Kind == "dynamic" && (variable.IncludeAll && isAllVariableSelection(variable.Current) ||
		len(variable.Current) == 1 && variable.Current[0] == "__all__")
}

func unresolvedSelectionQueryNote(name string, reasons []model.ReasonCode) string {
	if slices.Contains(reasons, model.ReasonVariableAllValue) {
		return fmt.Sprintf(
			"Query omitted because non-dynamic variable %q has Grafana All selected without a proven complete target value list.",
			name,
		)
	}
	if slices.Contains(reasons, model.ReasonCustomVariableReload) {
		return fmt.Sprintf(
			"Query omitted because custom variable %q cannot reproduce its selected value after target dashboard reload.",
			name,
		)
	}
	if slices.Contains(reasons, model.ReasonMultiVariableValue) {
		return fmt.Sprintf(
			"Query omitted because variable %q has multiple current values while Grafana multi is disabled.",
			name,
		)
	}
	return fmt.Sprintf(
		"Query omitted because variable %q has no proven nonblank current selection.",
		name,
	)
}

func unresolvedSelectionReferences(expression string, unresolved map[string][]model.ReasonCode) []string {
	references := make([]string, 0)
	for _, name := range transpile.VariableNames(expression) {
		if _, affected := unresolved[name]; affected {
			references = append(references, name)
		}
	}
	// The pinned SigNoz PromQL renderer additionally recognizes both its
	// direct mustache form and Go-template dot form. Grafana seldom emits
	// these, but an imported expression that contains either still depends on
	// the variable and must share the same fail-closed boundary.
	for name := range unresolved {
		if strings.Contains(expression, "{{"+name+"}}") || strings.Contains(expression, "{{."+name+"}}") {
			references = append(references, name)
		}
	}
	slices.Sort(references)
	return slices.Compact(references)
}

func appendUniqueReason(reasons []model.ReasonCode, reason model.ReasonCode) []model.ReasonCode {
	if slices.Contains(reasons, reason) {
		return reasons
	}
	return append(reasons, reason)
}
