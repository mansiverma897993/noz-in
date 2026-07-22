package app

import (
	"bytes"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/mansiverma897993/noz-in/internal/model"
	"github.com/mansiverma897993/noz-in/internal/target/signoz"
	"github.com/mansiverma897993/noz-in/internal/transpile"
	"github.com/mansiverma897993/noz-in/pkg/reporttypes"
)

func validateDifferentialLabelValueAliases(aliases map[string]map[string]string) error {
	for label, mappings := range aliases {
		if strings.TrimSpace(label) == "" {
			return fmt.Errorf("label name is empty")
		}
		if len(mappings) == 0 {
			return fmt.Errorf("label %q has no value mappings", label)
		}
		for targetValue, sourceValue := range mappings {
			if strings.TrimSpace(targetValue) == "" || strings.TrimSpace(sourceValue) == "" {
				return fmt.Errorf("label %q contains an empty target or source value", label)
			}
			if targetValue == "__all__" {
				return fmt.Errorf(
					"label %q records dynamic matcher-removal syntax __all__ as a literal target value",
					label,
				)
			}
			if targetValue == sourceValue {
				return fmt.Errorf("label %q records identity alias %q", label, targetValue)
			}
		}
	}
	return nil
}

func validateDifferentialLabelValueAliasBindings(
	evidence reporttypes.Report,
	primaryDashboard signoz.DashboardV5,
	query reporttypes.QueryRecord,
	comparison DifferentialQuery,
) error {
	if len(comparison.LabelValueAliases) == 0 && len(comparison.LabelValueAliasBindings) == 0 {
		return nil
	}
	if len(bytes.TrimSpace(comparison.TargetArtifact)) == 0 {
		return fmt.Errorf(
			"differential query %q has label-value aliases without an exact target request artifact",
			comparison.SourcePath,
		)
	}
	targetRequest, err := decodeTargetArtifact(comparison.TargetArtifact)
	if err != nil {
		return fmt.Errorf(
			"differential query %q cannot validate label-value alias bindings against its target artifact: %w",
			comparison.SourcePath, err,
		)
	}
	_, materializationOptions, err := differentialMaterializationFromEvidence(evidence)
	if err != nil {
		return fmt.Errorf(
			"differential query %q cannot validate label-value alias bindings against migration materialization: %w",
			comparison.SourcePath, err,
		)
	}
	analyzer := transpile.NewAnalyzer(materializationOptions)
	requestValues := make(targetVariableValues, len(targetRequest.Variables))
	for name, variable := range targetRequest.Variables {
		requestValues[name] = variable.Value
	}
	sourceValues := make(sourceVariableValues, len(requestValues))
	for name, value := range requestValues {
		if scalar, ok := value.(string); ok {
			sourceValues[name] = scalar
		}
	}
	var comparisonStart, comparisonEnd time.Time
	if comparison.Window != nil {
		comparisonStart = comparison.Window.Start
		comparisonEnd = comparison.Window.End
	}
	bindingSourceValues := make(map[string]string, len(comparison.LabelValueAliasBindings))
	for index, binding := range comparison.LabelValueAliasBindings {
		if err := validateDifferentialLabelValueAliasBindingShape(binding); err != nil {
			return fmt.Errorf(
				"differential query %q label-value alias binding %d is invalid: %w",
				comparison.SourcePath, index, err,
			)
		}
		if existing, found := bindingSourceValues[binding.VariableName]; found && existing != binding.SourceValue {
			return fmt.Errorf(
				"differential query %q label-value alias bindings record conflicting source values %q and %q for variable %q",
				comparison.SourcePath, existing, binding.SourceValue, binding.VariableName,
			)
		}
		bindingSourceValues[binding.VariableName] = binding.SourceValue
		sourceValues[binding.VariableName] = binding.SourceValue
	}
	materializedSource, missingSource := analyzer.MaterializeSourceQueryForWindow(model.Query{
		RefID:          query.RefID,
		OriginalRefID:  query.OriginalRefID,
		Expression:     query.Original,
		Step:           query.Step,
		Interval:       query.Interval,
		IntervalFactor: query.IntervalFactor,
		MaxDataPoints:  query.MaxDataPoints,
		SourcePath:     query.SourcePath,
	}, map[string]string(sourceValues), nil, comparisonStart, comparisonEnd)
	if len(missingSource) > 0 || materializedSource != comparison.SourceExpression {
		return fmt.Errorf(
			"differential query %q exact materialized source expression does not match its persisted alias bindings",
			comparison.SourcePath,
		)
	}

	recomputed := make(map[string]map[string]string)
	origins := make(map[string]map[string]string)
	seenBindings := make(map[string]struct{}, len(comparison.LabelValueAliasBindings))
	for _, binding := range comparison.LabelValueAliasBindings {
		bindingKey := binding.VariableName + "\x00" + binding.SourceLabel + "\x00" + binding.TargetLabel
		if _, duplicate := seenBindings[bindingKey]; duplicate {
			return fmt.Errorf(
				"differential query %q contains duplicate label-value alias binding for variable %q and label %q",
				comparison.SourcePath, binding.VariableName, binding.SourceLabel,
			)
		}
		seenBindings[bindingKey] = struct{}{}

		if err := validateDifferentialAliasBindingArtifacts(
			evidence, primaryDashboard, targetRequest, binding,
		); err != nil {
			return fmt.Errorf(
				"differential query %q label-value alias binding for variable %q: %w",
				comparison.SourcePath, binding.VariableName, err,
			)
		}

		sentinel := differentialAliasSentinel(comparison.SourcePath, binding.VariableName, binding.SourceLabel)
		if differentialAliasSentinelCollides(sentinel, query, comparison, sourceValues, requestValues, binding) {
			return fmt.Errorf(
				"differential query %q label-value alias binding for variable %q collides with its provenance probe",
				comparison.SourcePath, binding.VariableName,
			)
		}
		sourceProbeValues := make(sourceVariableValues, len(sourceValues))
		maps.Copy(sourceProbeValues, sourceValues)
		sourceProbeValues[binding.VariableName] = sentinel
		sourceProbe, missing := analyzer.MaterializeSourceQueryForWindow(
			model.Query{
				RefID:          query.RefID,
				OriginalRefID:  query.OriginalRefID,
				Expression:     query.Original,
				Step:           query.Step,
				Interval:       query.Interval,
				IntervalFactor: query.IntervalFactor,
				MaxDataPoints:  query.MaxDataPoints,
				SourcePath:     query.SourcePath,
			},
			map[string]string(sourceProbeValues),
			nil,
			comparisonStart,
			comparisonEnd,
		)
		if len(missing) > 0 || !promQLAliasLabelProof(
			sourceProbe, binding.SourceLabel, sentinel, binding.SourceValue,
		) {
			return fmt.Errorf(
				"differential query %q label-value alias binding for variable %q is not proven by the migration source expression",
				comparison.SourcePath, binding.VariableName,
			)
		}
		if !promQLResolvedAliasLabelProof(
			comparison.SourceExpression, binding.SourceLabel, binding.SourceValue,
		) {
			return fmt.Errorf(
				"differential query %q label-value alias binding for variable %q is not present in the exact materialized source expression",
				comparison.SourcePath, binding.VariableName,
			)
		}

		targetProbeValues := maps.Clone(requestValues)
		targetProbeValues[binding.VariableName] = sentinel
		if !targetArtifactAliasLabelProof(
			targetRequest,
			comparison.TargetKind,
			comparison.TargetQueryName,
			binding.TargetLabel,
			sentinel,
			binding.TargetValue,
			targetProbeValues,
		) {
			return fmt.Errorf(
				"differential query %q label-value alias binding for variable %q is not proven by the exact target request artifact",
				comparison.SourcePath, binding.VariableName,
			)
		}

		if recomputed[binding.SourceLabel] == nil {
			recomputed[binding.SourceLabel] = make(map[string]string)
			origins[binding.SourceLabel] = make(map[string]string)
		}
		if existing, found := recomputed[binding.SourceLabel][binding.TargetValue]; found && existing != binding.SourceValue {
			return fmt.Errorf(
				"differential query %q alias bindings for variables %q and %q conflict on label %q target value %q",
				comparison.SourcePath,
				origins[binding.SourceLabel][binding.TargetValue], binding.VariableName,
				binding.SourceLabel, binding.TargetValue,
			)
		}
		recomputed[binding.SourceLabel][binding.TargetValue] = binding.SourceValue
		origins[binding.SourceLabel][binding.TargetValue] = binding.VariableName
	}

	if !maps.EqualFunc(
		comparison.LabelValueAliases,
		recomputed,
		func(left, right map[string]string) bool { return maps.Equal(left, right) },
	) {
		return fmt.Errorf(
			"differential query %q label-value aliases do not match their exact persisted bindings",
			comparison.SourcePath,
		)
	}
	return nil
}

func validateDifferentialAliasBindingArtifacts(
	evidence reporttypes.Report,
	primaryDashboard signoz.DashboardV5,
	targetRequest signoz.QueryRangeRequest,
	binding DifferentialLabelValueAliasBinding,
) error {
	variableEvidence, err := exactDifferentialVariableEvidence(evidence, binding.VariableName)
	if err != nil {
		return fmt.Errorf("is not bound to migration variable evidence: %w", err)
	}
	if variableEvidence.SourceKind != string(model.VariableKindQuery) ||
		variableEvidence.EmittedKind != "dynamic" ||
		variableEvidence.Attribute != binding.SourceLabel {
		return fmt.Errorf("does not match its migration variable evidence")
	}

	dashboardVariable, err := exactDifferentialDashboardVariable(primaryDashboard, binding.VariableName)
	if err != nil {
		return fmt.Errorf("is not bound to the primary dashboard: %w", err)
	}
	if dashboardVariable.Type != "DYNAMIC" ||
		dashboardVariable.DynamicVariablesAttribute != binding.TargetLabel {
		return fmt.Errorf("does not match the primary dashboard dynamic variable")
	}

	requestVariable, found := targetRequest.Variables[binding.VariableName]
	requestValue, scalar := requestVariable.Value.(string)
	if !found || !scalar || requestVariable.Type != "dynamic" || requestValue != binding.TargetValue {
		return fmt.Errorf("does not match the exact target request variable")
	}
	return nil
}

func validateDifferentialLabelValueAliasBindingShape(binding DifferentialLabelValueAliasBinding) error {
	if strings.TrimSpace(binding.VariableName) == "" {
		return fmt.Errorf("variable name is empty")
	}
	if strings.TrimSpace(binding.SourceLabel) == "" || strings.TrimSpace(binding.TargetLabel) == "" {
		return fmt.Errorf("source or target label is empty")
	}
	if strings.TrimSpace(binding.SourceValue) == "" || strings.TrimSpace(binding.TargetValue) == "" {
		return fmt.Errorf("source or target value is empty")
	}
	if binding.TargetValue == "__all__" {
		return fmt.Errorf("target value __all__ is dynamic matcher-removal syntax, not a literal label value")
	}
	if binding.SourceValue == binding.TargetValue {
		return fmt.Errorf("source and target values are identical")
	}
	if binding.TargetLabel != differentialTargetLabel(binding.SourceLabel) {
		return fmt.Errorf(
			"target label %q is not the emitted label for source label %q",
			binding.TargetLabel, binding.SourceLabel,
		)
	}
	return nil
}

func exactDifferentialVariableEvidence(
	evidence reporttypes.Report,
	variableName string,
) (reporttypes.VariableRecord, error) {
	var matches []reporttypes.VariableRecord
	for _, variable := range evidence.Variables {
		if variable.Name == variableName {
			matches = append(matches, variable)
		}
	}
	if len(matches) != 1 {
		return reporttypes.VariableRecord{}, fmt.Errorf(
			"expected exactly one variable named %q, found %d", variableName, len(matches),
		)
	}
	return matches[0], nil
}

func exactDifferentialDashboardVariable(
	dashboard signoz.DashboardV5,
	variableName string,
) (signoz.VariableV5, error) {
	var matches []signoz.VariableV5
	for _, variable := range dashboard.Variables {
		if variable.Name == variableName {
			matches = append(matches, variable)
		}
	}
	if len(matches) != 1 {
		return signoz.VariableV5{}, fmt.Errorf(
			"expected exactly one variable named %q, found %d", variableName, len(matches),
		)
	}
	return matches[0], nil
}

func differentialAliasSentinelCollides(
	sentinel string,
	query reporttypes.QueryRecord,
	comparison DifferentialQuery,
	sourceValues sourceVariableValues,
	requestValues targetVariableValues,
	binding DifferentialLabelValueAliasBinding,
) bool {
	if strings.Contains(query.Original, sentinel) ||
		strings.Contains(comparison.SourceExpression, sentinel) ||
		strings.Contains(comparison.TargetExpression, sentinel) ||
		strings.Contains(binding.SourceValue, sentinel) ||
		strings.Contains(binding.TargetValue, sentinel) {
		return true
	}
	for _, value := range sourceValues {
		if strings.Contains(value, sentinel) {
			return true
		}
	}
	for _, value := range requestValues {
		if variableValueContains(value, sentinel) {
			return true
		}
	}
	return false
}
