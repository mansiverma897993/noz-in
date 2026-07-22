package model

// Verdict describes the safest migration outcome for a query or panel.
type Verdict string

const (
	VerdictNative      Verdict = "native"
	VerdictPassthrough Verdict = "passthrough"
	VerdictNeedsReview Verdict = "needs_review"
)

// ReasonCode is a stable explanation for a non-native or qualified outcome.
type ReasonCode string

const (
	ReasonVectorMatching                 ReasonCode = "VECTOR_MATCHING"
	ReasonWithoutClause                  ReasonCode = "WITHOUT_CLAUSE"
	ReasonNonstandardQuantile            ReasonCode = "NONSTANDARD_QUANTILE"
	ReasonTopKSemantics                  ReasonCode = "TOPK_SEMANTICS"
	ReasonRecordingRuleMetric            ReasonCode = "RECORDING_RULE_METRIC"
	ReasonRangeStepMismatch              ReasonCode = "RANGE_STEP_MISMATCH"
	ReasonSubquery                       ReasonCode = "SUBQUERY"
	ReasonComparisonInFormula            ReasonCode = "COMPARISON_IN_FORMULA"
	ReasonNonPromDatasource              ReasonCode = "NON_PROM_DATASOURCE"
	ReasonTextPanel                      ReasonCode = "TEXT_PANEL"
	ReasonUnsupportedPanel               ReasonCode = "UNSUPPORTED_PANEL"
	ReasonTableJoinTransform             ReasonCode = "TABLE_JOIN_TRANSFORM"
	ReasonRepeatPanel                    ReasonCode = "REPEAT_PANEL"
	ReasonQueryResultVariable            ReasonCode = "QUERY_RESULT_VARIABLE"
	ReasonChainedVariable                ReasonCode = "CHAINED_VARIABLE"
	ReasonRateIntervalRewrite            ReasonCode = "RATE_INTERVAL_REWRITE"
	ReasonMetricNameRemap                ReasonCode = "METRIC_NAME_REMAP"
	ReasonResourceLabelRemap             ReasonCode = "PROMETHEUS_RESOURCE_LABEL_REMAP"
	ReasonMissingMetric                  ReasonCode = "MISSING_METRIC_IN_TARGET"
	ReasonMetricMetadataUnavailable      ReasonCode = "METRIC_METADATA_UNAVAILABLE"
	ReasonHiddenTarget                   ReasonCode = "HIDDEN_TARGET"
	ReasonEmptyExpression                ReasonCode = "EMPTY_EXPRESSION"
	ReasonParseError                     ReasonCode = "PROMQL_PARSE_ERROR"
	ReasonUnsupportedFunction            ReasonCode = "UNSUPPORTED_FUNCTION"
	ReasonUnsupportedOperator            ReasonCode = "UNSUPPORTED_OPERATOR"
	ReasonUnsupportedModifier            ReasonCode = "UNSUPPORTED_MODIFIER"
	ReasonNonExactMetricSelector         ReasonCode = "NON_EXACT_METRIC_SELECTOR"
	ReasonRegexVariable                  ReasonCode = "REGEX_VARIABLE_SEMANTICS"
	ReasonMetricTypeRequired             ReasonCode = "METRIC_TYPE_REQUIRED"
	ReasonDynamicStructure               ReasonCode = "DYNAMIC_PROMQL_STRUCTURE"
	ReasonDynamicRewriteConflict         ReasonCode = "DYNAMIC_PROMQL_REWRITE_REQUIRED"
	ReasonLabelRemapCollision            ReasonCode = "PROMETHEUS_LABEL_REMAP_COLLISION"
	ReasonMetricRemapCollision           ReasonCode = "METRIC_NAME_REMAP_COLLISION"
	ReasonMixedPanelQueries              ReasonCode = "MIXED_PANEL_QUERY_TYPES"
	ReasonBuilderValueGroupBy            ReasonCode = "BUILDER_VALUE_GROUP_BY_UNSUPPORTED"
	ReasonTargetVectorMatching           ReasonCode = "TARGET_RESOURCE_VECTOR_MATCHING"
	ReasonTargetVectorMatchingUnresolved ReasonCode = "TARGET_RESOURCE_VECTOR_MATCHING_UNRESOLVED"
	ReasonRecordingRule                  ReasonCode = "RECORDING_RULE_DEFINITION"
	ReasonAlertThreshold                 ReasonCode = "ALERT_THRESHOLD_NOT_EXTRACTED"
	ReasonAlertForDefault                ReasonCode = "ALERT_IMMEDIATE_WINDOW_APPROXIMATION"
	ReasonAlertForWindow                 ReasonCode = "PROMETHEUS_FOR_WINDOW_APPROXIMATION"
	ReasonAlertForInvalid                ReasonCode = "ALERT_FOR_DURATION_UNREPRESENTABLE"
	ReasonRuleGroupInterval              ReasonCode = "RULE_GROUP_INTERVAL_UNREPRESENTABLE"
	ReasonRuleGroupQueryOffset           ReasonCode = "RULE_GROUP_QUERY_OFFSET_UNSUPPORTED"
	ReasonRuleGroupLimit                 ReasonCode = "RULE_GROUP_LIMIT_UNSUPPORTED"
	ReasonKeepFiringFor                  ReasonCode = "KEEP_FIRING_FOR_UNSUPPORTED"
	ReasonSeverityNormalized             ReasonCode = "SEVERITY_NORMALIZED"
	ReasonAnnotationFormatting           ReasonCode = "ANNOTATION_TEMPLATE_FORMATTING_DROPPED"
	ReasonAlertLabelFormatting           ReasonCode = "ALERT_LABEL_TEMPLATE_FORMATTING_DROPPED"
	ReasonTargetAlertRuntimeLabels       ReasonCode = "TARGET_ALERT_RUNTIME_LABELS"
	ReasonAlertNameDisambiguated         ReasonCode = "ALERT_NAME_DISAMBIGUATED"
	ReasonPanelTypeDowngrade             ReasonCode = "PANEL_TYPE_DOWNGRADE_PROMQL"
	ReasonGrafanaExpression              ReasonCode = "GRAFANA_EXPRESSION_TARGET"
	ReasonInstantQuery                   ReasonCode = "INSTANT_QUERY_UNSUPPORTED"
	ReasonGrafanaIntervalControl         ReasonCode = "GRAFANA_INTERVAL_CONTROL"
	ReasonGrafanaQueryFormat             ReasonCode = "GRAFANA_QUERY_FORMAT"
	ReasonGrafanaVariableFormat          ReasonCode = "GRAFANA_VARIABLE_FORMAT"
	ReasonGrafanaVariableLegend          ReasonCode = "GRAFANA_VARIABLE_LEGEND_SEMANTICS"
	ReasonGrafanaVariablePanelTitle      ReasonCode = "GRAFANA_VARIABLE_PANEL_TITLE_SEMANTICS"
	ReasonGrafanaPanelDescription        ReasonCode = "GRAFANA_PANEL_DESCRIPTION_SEMANTICS"
	ReasonTargetResponseLabelStripped    ReasonCode = "TARGET_PROMQL_RESPONSE_LABEL_STRIPPED"
	ReasonTargetOnlyLabelSemanticUse     ReasonCode = "TARGET_ONLY_LABEL_SEMANTIC_USE"
	ReasonMetricNameSemanticUse          ReasonCode = "METRIC_NAME_SEMANTIC_USE_AFTER_REMAP"
	ReasonTargetNativeHistogramDropped   ReasonCode = "TARGET_PROMQL_NATIVE_HISTOGRAM_DROPPED"
	ReasonGrafanaTimeseriesPointMode     ReasonCode = "GRAFANA_TIMESERIES_POINT_MODE_SEMANTICS"
	ReasonGrafanaGraphRenderingDefaults  ReasonCode = "GRAFANA_GRAPH_RENDERING_DEFAULTS"
	ReasonGrafanaConstantVariable        ReasonCode = "GRAFANA_CONSTANT_VARIABLE_MUTABILITY"
	ReasonMissingVariableValue           ReasonCode = "MISSING_VARIABLE_VALUE"
	ReasonMultiVariableValue             ReasonCode = "MULTI_VARIABLE_VALUE_UNRESOLVED"
	ReasonVariableValueEscaping          ReasonCode = "VARIABLE_VALUE_ESCAPING_UNRESOLVED"
	ReasonCustomVariableReload           ReasonCode = "CUSTOM_VARIABLE_RELOAD_SEMANTICS"
	ReasonPanelTimeOverride              ReasonCode = "PANEL_TIME_OVERRIDE"
	ReasonVariableRegex                  ReasonCode = "VARIABLE_REGEX_FILTER"
	ReasonVariableAllValue               ReasonCode = "VARIABLE_ALL_VALUE_SEMANTICS"
	ReasonDatasourceVariable             ReasonCode = "DATASOURCE_VARIABLE_OMITTED"
	ReasonUnsupportedVariable            ReasonCode = "UNSUPPORTED_VARIABLE"
	ReasonRecordingRuleInlined           ReasonCode = "RECORDING_RULE_INLINED"
	ReasonMetricTypeIncompatible         ReasonCode = "METRIC_TYPE_INCOMPATIBLE"
	ReasonGrafanaTransformation          ReasonCode = "GRAFANA_TRANSFORMATION"
	ReasonRefIDNormalized                ReasonCode = "QUERY_REFID_NORMALIZED"
	ReasonNoQueryTargets                 ReasonCode = "NO_QUERY_TARGETS"
	ReasonAllTargetsDisabled             ReasonCode = "ALL_TARGETS_DISABLED"
	ReasonPanelOmitted                   ReasonCode = "PANEL_OMITTED"
	ReasonLegacyPanelAlert               ReasonCode = "LEGACY_PANEL_ALERT"
	ReasonAnnotationQuery                ReasonCode = "ANNOTATION_QUERY"
	ReasonDashboardLink                  ReasonCode = "DASHBOARD_LINK"
	ReasonPanelLink                      ReasonCode = "PANEL_LINK"
	ReasonFieldThresholds                ReasonCode = "FIELD_THRESHOLDS"
	ReasonFieldOverrides                 ReasonCode = "FIELD_OVERRIDES"
	ReasonLibraryPanel                   ReasonCode = "LIBRARY_PANEL_REFERENCE"
	ReasonQueryNameCollision             ReasonCode = "QUERY_NAME_COLLISION"
	ReasonFormulaLabelMismatch           ReasonCode = "FORMULA_LABEL_SET_MISMATCH"
	ReasonHistogramBucketFilter          ReasonCode = "HISTOGRAM_BUCKET_FILTER"
	ReasonBuilderRateIncrease            ReasonCode = "BUILDER_RATE_INCREASE_SEMANTICS"
	ReasonBuilderLatestLookback          ReasonCode = "BUILDER_LATEST_LOOKBACK_SEMANTICS"
	ReasonBuilderHistogramPercentile     ReasonCode = "BUILDER_HISTOGRAM_PERCENTILE_SEMANTICS"
	ReasonBuilderFormulaEvaluation       ReasonCode = "BUILDER_FORMULA_EVALUATION_SEMANTICS"
	ReasonVariableSelectorScope          ReasonCode = "VARIABLE_SELECTOR_SCOPE_DROPPED"
	ReasonVisualizationDowngrade         ReasonCode = "VISUALIZATION_TYPE_DOWNGRADE"
	ReasonUnmappedVisualization          ReasonCode = "UNMAPPED_VISUALIZATION_CONFIG"
	ReasonUnmappedQueryConfig            ReasonCode = "UNMAPPED_QUERY_CONFIG"
	ReasonUnmappedDashboardConfig        ReasonCode = "UNMAPPED_DASHBOARD_CONFIG"
	ReasonUnmappedVariableConfig         ReasonCode = "UNMAPPED_VARIABLE_CONFIG"
	ReasonRowPanelTarget                 ReasonCode = "ROW_PANEL_TARGET_UNSUPPORTED"
	ReasonNativeDifferentialVerified     ReasonCode = "NATIVE_DIFFERENTIAL_VERIFIED"
	ReasonOperatorOverride               ReasonCode = "OPERATOR_OVERRIDE"
	ReasonBuilderOverTime                ReasonCode = "BUILDER_OVER_TIME_SEMANTICS"
	ReasonBuilderTemporalPhaseShift      ReasonCode = "BUILDER_TEMPORAL_PHASE_SHIFT"
)

// Decision records a migration outcome and its evidence.
type Decision struct {
	Verdict Verdict      `json:"verdict"`
	Reasons []ReasonCode `json:"reasonCodes,omitempty"`
	Notes   []string     `json:"notes,omitempty"`
}

var reasonDescriptions = map[ReasonCode]string{
	ReasonVectorMatching:                 "PromQL vector matching has no equivalent Builder semantics.",
	ReasonWithoutClause:                  "The aggregation uses a without clause.",
	ReasonNonstandardQuantile:            "The histogram quantile is not supported by SigNoz Builder.",
	ReasonTopKSemantics:                  "PromQL and SigNoz apply top-k over different evaluation scopes.",
	ReasonRecordingRuleMetric:            "The query references a recording-rule metric that may not exist in the target.",
	ReasonRangeStepMismatch:              "The PromQL range differs from the target evaluation step.",
	ReasonSubquery:                       "PromQL subqueries are preserved as passthrough queries.",
	ReasonComparisonInFormula:            "A comparison cannot be represented safely as a Builder formula.",
	ReasonNonPromDatasource:              "The source query does not use a Prometheus-compatible datasource.",
	ReasonTextPanel:                      "The text panel has no query migration path.",
	ReasonUnsupportedPanel:               "The panel visualization is not supported by the target emitter.",
	ReasonTableJoinTransform:             "The panel depends on a Grafana table join transformation.",
	ReasonRepeatPanel:                    "The panel uses Grafana repeat behavior.",
	ReasonQueryResultVariable:            "The variable uses query_result semantics.",
	ReasonChainedVariable:                "The variable query depends on another variable.",
	ReasonRateIntervalRewrite:            "A Grafana rate interval was replaced with a fixed duration.",
	ReasonMetricNameRemap:                "The target metric name differs from the source name.",
	ReasonResourceLabelRemap:             "Prometheus job and instance labels were mapped to OpenTelemetry resource attributes.",
	ReasonMissingMetric:                  "The referenced metric was not found in the target.",
	ReasonMetricMetadataUnavailable:      "The target could not provide the metric metadata required to qualify a Builder candidate.",
	ReasonHiddenTarget:                   "The source target is disabled.",
	ReasonEmptyExpression:                "The source target has no PromQL expression.",
	ReasonParseError:                     "The PromQL expression could not be parsed.",
	ReasonUnsupportedFunction:            "The PromQL function has no supported Builder candidate mapping.",
	ReasonUnsupportedOperator:            "The PromQL operator has no supported SigNoz Builder formula candidate mapping.",
	ReasonUnsupportedModifier:            "The query uses an offset or at modifier that is not represented safely.",
	ReasonNonExactMetricSelector:         "The query does not select one metric by exact name and cannot be represented by Builder.",
	ReasonRegexVariable:                  "A Grafana variable inside a regex matcher cannot be proven equivalent in a Builder filter.",
	ReasonMetricTypeRequired:             "Builder candidate qualification requires metric metadata from the target.",
	ReasonDynamicStructure:               "A variable changes the PromQL grammar rather than a matcher value.",
	ReasonDynamicRewriteConflict:         "A grammar-changing variable prevents a required target-specific PromQL rewrite from being applied safely.",
	ReasonLabelRemapCollision:            "Distinct source labels would collapse to the same target label during Prometheus receiver remapping.",
	ReasonMetricRemapCollision:           "Distinct source metrics would collapse to the same target metric within one PromQL expression.",
	ReasonMixedPanelQueries:              "The panel mixes native and passthrough queries and must use one target mode.",
	ReasonBuilderValueGroupBy:            "A grouped Builder scalar response cannot be rendered reliably by the target value panel.",
	ReasonTargetVectorMatching:           "PromQL vector matching was rewritten over logical source labels to exclude known receiver-only attributes.",
	ReasonTargetVectorMatchingUnresolved: "Known target-only labels require a PromQL vector-matching rewrite, but the operand output labels could not be proven exactly, so no executable query was emitted.",
	ReasonRecordingRule:                  "The source is a recording rule definition, not an alert rule.",
	ReasonAlertThreshold:                 "A single numeric root threshold could not be separated from the PromQL expression.",
	ReasonAlertForDefault:                "An immediate Prometheus alert was mapped to the target's minimum rolling evaluation window.",
	ReasonAlertForWindow:                 "A Prometheus for state transition was approximated with a trailing SigNoz evaluation window.",
	ReasonAlertForInvalid:                "The Prometheus alert for duration is invalid or cannot be represented by the target evaluation window.",
	ReasonRuleGroupInterval:              "The explicit Prometheus rule-group interval is invalid or exceeds the emitted target evaluation window.",
	ReasonRuleGroupQueryOffset:           "The Prometheus rule group uses query_offset, which has no target alert-rule equivalent.",
	ReasonRuleGroupLimit:                 "The Prometheus rule group uses a positive series limit, which has no target alert-rule equivalent.",
	ReasonKeepFiringFor:                  "A positive Prometheus keep_firing_for duration has no equivalent target rule field.",
	ReasonSeverityNormalized:             "The source severity was normalized to a target severity tier.",
	ReasonAnnotationFormatting:           "Prometheus-only annotation formatting was reduced to a supported target variable, or an unsupported action was replaced with an explicit omission sentinel.",
	ReasonAlertLabelFormatting:           "Prometheus-only alert-label formatting was reduced to a supported target variable, or an unsupported action was replaced with an explicit omission sentinel.",
	ReasonTargetAlertRuntimeLabels:       "Pinned SigNoz v0.133 owns alertname and injects threshold.name, ruleId, ruleSource, and conditional nodata runtime labels that alter the alert label set and fingerprint.",
	ReasonAlertNameDisambiguated:         "A duplicate source alert name received a deterministic suffix.",
	ReasonPanelTypeDowngrade:             "The visualization was changed to a graph because its SigNoz form cannot safely render PromQL.",
	ReasonGrafanaExpression:              "The target is a Grafana expression rather than a Prometheus query.",
	ReasonInstantQuery:                   "Grafana evaluated this query at a single instant; the migrated panel executes it as a range query and renders the latest sample, which review should confirm is acceptable for this expression.",
	ReasonGrafanaIntervalControl:         "Grafana query interval controls were materialized into an explicit target step and require review.",
	ReasonGrafanaQueryFormat:             "The Grafana query requests a non-default result format whose target shaping is not proven equivalent.",
	ReasonGrafanaVariableFormat:          "A Grafana variable formatter has no proven executable SigNoz equivalent.",
	ReasonGrafanaVariableLegend:          "Grafana query legends can use smart __auto naming, dashboard-variable interpolation, or missing-label placeholder behavior that pinned SigNoz does not reproduce exactly.",
	ReasonGrafanaVariablePanelTitle:      "Grafana and pinned SigNoz resolve dashboard variables in panel titles with different syntax and multi-value formatting.",
	ReasonGrafanaPanelDescription:        "Grafana template-interpolates and renders panel descriptions as Markdown, while pinned SigNoz displays the stored description as plain tooltip text.",
	ReasonTargetResponseLabelStripped:    "Pinned SigNoz strips fingerprint and non-name double-underscore labels from PromQL responses, changing explicit output labels.",
	ReasonTargetOnlyLabelSemanticUse:     "The expression semantically consumes receiver-only labels before response normalization, so target-added values can change filtering, grouping, matching, label creation, series selection, or classic histogram evaluation.",
	ReasonMetricNameSemanticUse:          "The expression semantically reads __name__ while one of its metric selectors is renamed, so the target would observe a different label value.",
	ReasonTargetNativeHistogramDropped:   "Pinned SigNoz serializes only float samples from PromQL matrices and silently drops native-histogram samples.",
	ReasonGrafanaTimeseriesPointMode:     "Grafana time-series panels default to density-aware Auto points, while pinned SigNoz exposes only a fixed showPoints boolean.",
	ReasonGrafanaGraphRenderingDefaults:  "Grafana graph and time-series line, fill, and tooltip defaults are not all representable by the pinned SigNoz graph widget.",
	ReasonGrafanaConstantVariable:        "Grafana constants are hidden immutable bindings, while pinned SigNoz represents them as visible editable textbox variables.",
	ReasonMissingVariableValue:           "An emitted query references a dashboard variable without a resolved value, so live validation was skipped.",
	ReasonMultiVariableValue:             "The variable uses Grafana multi/All interpolation whose source-side escaping is not proven exactly; a declared target selection remains preserved, while a contradictory single-select array is omitted.",
	ReasonVariableValueEscaping:          "Grafana Prometheus interpolation and pinned SigNoz raw variable, reserved-time, or Go-template rendering cannot be proven to materialize the same query bytes.",
	ReasonCustomVariableReload:           "Pinned SigNoz rebuilds a custom variable from customValue on reload and ignores selectedValue; the emitted option set was reduced to a proven current selection or the variable was omitted.",
	ReasonPanelTimeOverride:              "The panel uses a source-specific time override.",
	ReasonVariableRegex:                  "The variable applies a Grafana regex filter that is not reproduced by a dynamic SigNoz variable.",
	ReasonVariableAllValue:               "The variable uses a custom Grafana All value whose matcher semantics differ from SigNoz matcher removal.",
	ReasonDatasourceVariable:             "The datasource variable is omitted because the target has one SigNoz backend.",
	ReasonUnsupportedVariable:            "The variable has no equivalent target representation and needs review.",
	ReasonRecordingRuleInlined:           "A Prometheus recording-rule expression was safely inlined for the target query.",
	ReasonMetricTypeIncompatible:         "The target metric type or temporality is incompatible with the proposed Builder operation.",
	ReasonGrafanaTransformation:          "The panel depends on a Grafana transformation that is preserved for review but not reimplemented.",
	ReasonRefIDNormalized:                "A missing or duplicate Grafana query reference was replaced with a deterministic unique name.",
	ReasonNoQueryTargets:                 "The source visualization has no executable query targets.",
	ReasonAllTargetsDisabled:             "Every executable source target is disabled, which the target API cannot import safely.",
	ReasonPanelOmitted:                   "The panel was deliberately omitted because emitting it would be invalid or misleading.",
	ReasonLegacyPanelAlert:               "The panel contains a legacy Grafana alert block that dashboard migration does not convert.",
	ReasonAnnotationQuery:                "The dashboard contains a Grafana annotation definition that is not emitted.",
	ReasonDashboardLink:                  "The dashboard contains a link that is not emitted.",
	ReasonPanelLink:                      "The panel contains a link that is not emitted.",
	ReasonFieldThresholds:                "The panel contains Grafana field thresholds that are not emitted.",
	ReasonFieldOverrides:                 "The panel contains Grafana field overrides that are not emitted.",
	ReasonLibraryPanel:                   "The panel is a library-panel reference whose external model is unavailable in the export.",
	ReasonQueryNameCollision:             "Expanded Builder query names collide within the panel, so it uses PromQL instead.",
	ReasonFormulaLabelMismatch:           "Builder formula operands expose different label sets and cannot preserve PromQL matching.",
	ReasonHistogramBucketFilter:          "A classic histogram bucket-label filter cannot be represented on a native histogram metric.",
	ReasonBuilderRateIncrease:            "SigNoz Builder rate and increase use step-bucket deltas and do not preserve PromQL range extrapolation and reset handling.",
	ReasonBuilderLatestLookback:          "SigNoz Builder latest uses a bucket-local last sample and does not preserve PromQL selector lookback and stale-marker semantics.",
	ReasonBuilderHistogramPercentile:     "SigNoz Builder percentile evaluation does not preserve PromQL classic-histogram rate or increase and histogram_quantile semantics.",
	ReasonBuilderFormulaEvaluation:       "SigNoz Builder formula evaluation uses different label matching, missing-series defaults, and non-finite handling than PromQL.",
	ReasonVariableSelectorScope:          "A metric or matcher scope in a Grafana variable cannot be encoded by the target dynamic variable.",
	ReasonVisualizationDowngrade:         "The closest target visualization does not preserve the source visualization type exactly.",
	ReasonUnmappedVisualization:          "A source visualization setting is inventoried but has no target representation.",
	ReasonUnmappedQueryConfig:            "A source query setting is inventoried but has no proven target representation.",
	ReasonUnmappedDashboardConfig:        "A source dashboard or legacy-row setting is inventoried but has no target representation.",
	ReasonUnmappedVariableConfig:         "A source variable setting is inventoried but has no target representation.",
	ReasonRowPanelTarget:                 "A Grafana row carries stale query targets; the row is emitted as structural layout only.",
	ReasonNativeDifferentialVerified:     "The Builder query was confirmed numerically equivalent to its PromQL passthrough on the live target and emitted natively.",
	ReasonOperatorOverride:               "An operator-provided Builder query replaced the generated translation; it is still verified live before it can be emitted natively.",
	ReasonBuilderOverTime:                "The candidate maps a PromQL *_over_time range function to a SigNoz Builder time aggregation; equivalence is confirmed by the live differential.",
	ReasonBuilderTemporalPhaseShift:      "The Builder result matched the PromQL passthrough in magnitude but was offset by one step in time (e.g. SigNoz `latest` labels its bucket at the start); it is not temporally equivalent and was held at review rather than promoted natively.",
}

// ReasonCodes returns every supported reason code in stable order.
func ReasonCodes() []ReasonCode {
	return []ReasonCode{
		ReasonVectorMatching,
		ReasonWithoutClause,
		ReasonNonstandardQuantile,
		ReasonTopKSemantics,
		ReasonRecordingRuleMetric,
		ReasonRangeStepMismatch,
		ReasonSubquery,
		ReasonComparisonInFormula,
		ReasonNonPromDatasource,
		ReasonTextPanel,
		ReasonUnsupportedPanel,
		ReasonTableJoinTransform,
		ReasonRepeatPanel,
		ReasonQueryResultVariable,
		ReasonChainedVariable,
		ReasonRateIntervalRewrite,
		ReasonMetricNameRemap,
		ReasonResourceLabelRemap,
		ReasonMissingMetric,
		ReasonMetricMetadataUnavailable,
		ReasonHiddenTarget,
		ReasonEmptyExpression,
		ReasonParseError,
		ReasonUnsupportedFunction,
		ReasonUnsupportedOperator,
		ReasonUnsupportedModifier,
		ReasonNonExactMetricSelector,
		ReasonRegexVariable,
		ReasonMetricTypeRequired,
		ReasonDynamicStructure,
		ReasonDynamicRewriteConflict,
		ReasonLabelRemapCollision,
		ReasonMetricRemapCollision,
		ReasonMixedPanelQueries,
		ReasonBuilderValueGroupBy,
		ReasonTargetVectorMatching,
		ReasonTargetVectorMatchingUnresolved,
		ReasonRecordingRule,
		ReasonAlertThreshold,
		ReasonAlertForDefault,
		ReasonAlertForWindow,
		ReasonAlertForInvalid,
		ReasonRuleGroupInterval,
		ReasonRuleGroupQueryOffset,
		ReasonRuleGroupLimit,
		ReasonKeepFiringFor,
		ReasonSeverityNormalized,
		ReasonAnnotationFormatting,
		ReasonAlertLabelFormatting,
		ReasonTargetAlertRuntimeLabels,
		ReasonAlertNameDisambiguated,
		ReasonPanelTypeDowngrade,
		ReasonGrafanaExpression,
		ReasonInstantQuery,
		ReasonGrafanaIntervalControl,
		ReasonGrafanaQueryFormat,
		ReasonGrafanaVariableFormat,
		ReasonGrafanaVariableLegend,
		ReasonGrafanaVariablePanelTitle,
		ReasonGrafanaPanelDescription,
		ReasonTargetResponseLabelStripped,
		ReasonTargetOnlyLabelSemanticUse,
		ReasonMetricNameSemanticUse,
		ReasonTargetNativeHistogramDropped,
		ReasonGrafanaTimeseriesPointMode,
		ReasonGrafanaGraphRenderingDefaults,
		ReasonGrafanaConstantVariable,
		ReasonMissingVariableValue,
		ReasonMultiVariableValue,
		ReasonVariableValueEscaping,
		ReasonCustomVariableReload,
		ReasonPanelTimeOverride,
		ReasonVariableRegex,
		ReasonVariableAllValue,
		ReasonDatasourceVariable,
		ReasonUnsupportedVariable,
		ReasonRecordingRuleInlined,
		ReasonMetricTypeIncompatible,
		ReasonGrafanaTransformation,
		ReasonRefIDNormalized,
		ReasonNoQueryTargets,
		ReasonAllTargetsDisabled,
		ReasonPanelOmitted,
		ReasonLegacyPanelAlert,
		ReasonAnnotationQuery,
		ReasonDashboardLink,
		ReasonPanelLink,
		ReasonFieldThresholds,
		ReasonFieldOverrides,
		ReasonLibraryPanel,
		ReasonQueryNameCollision,
		ReasonFormulaLabelMismatch,
		ReasonHistogramBucketFilter,
		ReasonBuilderRateIncrease,
		ReasonBuilderLatestLookback,
		ReasonBuilderHistogramPercentile,
		ReasonBuilderFormulaEvaluation,
		ReasonVariableSelectorScope,
		ReasonVisualizationDowngrade,
		ReasonUnmappedVisualization,
		ReasonUnmappedQueryConfig,
		ReasonUnmappedDashboardConfig,
		ReasonUnmappedVariableConfig,
		ReasonRowPanelTarget,
		ReasonNativeDifferentialVerified,
		ReasonOperatorOverride,
		ReasonBuilderOverTime,
		ReasonBuilderTemporalPhaseShift,
	}
}

// IsBuilderCandidateSemanticReason reports whether a reason applies only to a
// structurally representable Builder or Builder-formula candidate. These
// reasons require dashboard emission to use the canonical PromQL alternative;
// they do not make an already-PromQL alert query unsafe.
func IsBuilderCandidateSemanticReason(code ReasonCode) bool {
	switch code {
	case ReasonBuilderRateIncrease,
		ReasonBuilderLatestLookback,
		ReasonBuilderHistogramPercentile,
		ReasonBuilderOverTime,
		ReasonBuilderFormulaEvaluation:
		return true
	default:
		return false
	}
}

// ReasonDescription returns the human-readable meaning of a reason code.
func ReasonDescription(code ReasonCode) (string, bool) {
	description, ok := reasonDescriptions[code]
	return description, ok
}
