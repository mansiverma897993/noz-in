# Reason codes

Reason codes are part of the report contract. They are stable identifiers; human descriptions may improve without changing the code.

| Code | Meaning |
|---|---|
| `VECTOR_MATCHING` | The query uses explicit PromQL vector matching. |
| `WITHOUT_CLAUSE` | The aggregation uses `without`. |
| `NONSTANDARD_QUANTILE` | The requested histogram percentile is unavailable in Builder. |
| `TOPK_SEMANTICS` | PromQL and SigNoz evaluate top-k over different scopes. |
| `RECORDING_RULE_METRIC` | The metric may only exist as a Prometheus recording rule. |
| `RANGE_STEP_MISMATCH` | The source range and target step may produce different values. |
| `SUBQUERY` | The expression contains a PromQL subquery. |
| `COMPARISON_IN_FORMULA` | A formula comparison cannot be represented safely. |
| `NON_PROM_DATASOURCE` | The target is not backed by Prometheus-compatible data. |
| `TEXT_PANEL` | The source panel contains text rather than a query. |
| `UNSUPPORTED_PANEL` | The visualization has no target representation. |
| `TABLE_JOIN_TRANSFORM` | A Grafana table join is required. |
| `REPEAT_PANEL` | The panel depends on Grafana repeat behavior. |
| `QUERY_RESULT_VARIABLE` | The variable uses `query_result`. |
| `CHAINED_VARIABLE` | The variable depends on another variable. |
| `RATE_INTERVAL_REWRITE` | A Grafana global interval was replaced deterministically. |
| `METRIC_NAME_REMAP` | The target metric name differs from the source name. |
| `PROMETHEUS_RESOURCE_LABEL_REMAP` | Prometheus `job` and `instance` labels became OpenTelemetry resource attributes. |
| `MISSING_METRIC_IN_TARGET` | The metric was not found during live validation. |
| `METRIC_METADATA_UNAVAILABLE` | The target could not provide the metadata required to qualify a Builder candidate. |
| `HIDDEN_TARGET` | The source target is disabled. |
| `EMPTY_EXPRESSION` | The source target contains no PromQL. |
| `PROMQL_PARSE_ERROR` | The canonical parser rejected the expression. |
| `UNSUPPORTED_FUNCTION` | No supported Builder candidate mapping exists for a function or shape. |
| `UNSUPPORTED_OPERATOR` | No supported Builder formula candidate mapping exists for the PromQL operator. |
| `UNSUPPORTED_MODIFIER` | The expression uses an offset or `@` modifier. |
| `NON_EXACT_METRIC_SELECTOR` | The selector does not identify exactly one metric name, so Builder cannot represent it. |
| `REGEX_VARIABLE_SEMANTICS` | A variable-bearing regex cannot be proven equivalent in a Builder filter. |
| `METRIC_TYPE_REQUIRED` | Builder candidate qualification requires live metric metadata. |
| `DYNAMIC_PROMQL_STRUCTURE` | A variable changes a metric name, duration, grouping label, or another grammar element. |
| `DYNAMIC_PROMQL_REWRITE_REQUIRED` | A grammar-changing variable prevents a required target-specific rewrite from being applied safely, so no executable query is emitted. |
| `PROMETHEUS_LABEL_REMAP_COLLISION` | Distinct source labels would collapse to one target label during receiver remapping, so no executable query is emitted. |
| `METRIC_NAME_REMAP_COLLISION` | Distinct source metrics in one expression would collapse to the same target metric, so no executable query is emitted. |
| `MIXED_PANEL_QUERY_TYPES` | One panel contains both native and passthrough queries, while the target panel selects one query mode. |
| `BUILDER_VALUE_GROUP_BY_UNSUPPORTED` | A grouped Builder scalar result cannot be rendered reliably by the target value panel, so the panel uses PromQL. |
| `TARGET_RESOURCE_VECTOR_MATCHING` | Matching was rewritten with a logical `on(...)` key or an expanded `ignoring(...)` list to exclude known receiver-only labels. |
| `TARGET_RESOURCE_VECTOR_MATCHING_UNRESOLVED` | Known target-only labels require a matching rewrite, but an operand's exact output labels cannot be proven; no executable target query is emitted. |
| `RECORDING_RULE_DEFINITION` | The source is a recording rule and is reported separately from alert migration. |
| `ALERT_THRESHOLD_NOT_EXTRACTED` | The source expression has no separable numeric root comparison; the emitted rule remains disabled for review. |
| `ALERT_IMMEDIATE_WINDOW_APPROXIMATION` | A source alert without `for`, or with an explicit zero duration, uses the target's minimum one-minute rolling window. |
| `PROMETHEUS_FOR_WINDOW_APPROXIMATION` | A Prometheus pending-state `for` duration is represented only as a trailing SigNoz evaluation-window candidate; the emitted rule remains disabled. |
| `ALERT_FOR_DURATION_UNREPRESENTABLE` | A programmatically constructed source model has an invalid `for`; YAML/CLI inputs are rejected earlier by pinned Prometheus rulefmt validation. |
| `RULE_GROUP_INTERVAL_UNREPRESENTABLE` | A valid explicit group interval is longer than the emitted evaluation window; invalid/negative programmatic model values also fail defensively, while zero selects the Prometheus global/default cadence and invalid YAML syntax is rejected earlier. |
| `RULE_GROUP_QUERY_OFFSET_UNSUPPORTED` | A nonzero or invalid group `query_offset` has no target alert-rule equivalent. |
| `RULE_GROUP_LIMIT_UNSUPPORTED` | A positive group series `limit` has no target alert-rule equivalent; nonpositive values mean no limit. |
| `KEEP_FIRING_FOR_UNSUPPORTED` | The source uses a positive `keep_firing_for`, for which there is no equivalent target field; zero is a no-op. |
| `SEVERITY_NORMALIZED` | A missing or noncanonical source severity was mapped to a supported target tier. |
| `ANNOTATION_TEMPLATE_FORMATTING_DROPPED` | A Prometheus-only annotation formatter was reduced to a supported target variable, or an unsupported action was replaced with an explicit omission sentinel. |
| `ALERT_LABEL_TEMPLATE_FORMATTING_DROPPED` | A Prometheus-only alert-label formatter was reduced to a supported target variable, or an unsupported action was replaced with an explicit omission sentinel. |
| `TARGET_ALERT_RUNTIME_LABELS` | Pinned SigNoz v0.133 owns `alertname` and injects `threshold.name`, `ruleId`, `ruleSource`, and conditional `nodata`, changing the target alert label set and fingerprint. |
| `ALERT_NAME_DISAMBIGUATED` | A duplicate source alert name received a deterministic group/severity suffix. |
| `PANEL_TYPE_DOWNGRADE_PROMQL` | The visualization became a graph because its target form cannot safely render PromQL. |
| `GRAFANA_EXPRESSION_TARGET` | The target is a Grafana expression, not a Prometheus query. |
| `INSTANT_QUERY_UNSUPPORTED` | Grafana instant-query evaluation has no proven equivalent in the target dashboard query model. |
| `GRAFANA_INTERVAL_CONTROL` | Source interval, interval-factor, or max-data-point controls were materialized into an explicit target step and require review. |
| `GRAFANA_QUERY_FORMAT` | A non-default Grafana query result format has no proven equivalent target response shaping. |
| `GRAFANA_VARIABLE_FORMAT` | A Grafana variable formatter has no proven executable SigNoz equivalent. |
| `GRAFANA_VARIABLE_LEGEND_SEMANTICS` | Grafana legend `__auto`, dashboard-variable interpolation, and absent-label placeholder behavior do not have exact pinned SigNoz equivalents. |
| `GRAFANA_VARIABLE_PANEL_TITLE_SEMANTICS` | Grafana and pinned SigNoz interpolate dashboard variables in panel titles with different syntax and multi-value formatting. |
| `GRAFANA_PANEL_DESCRIPTION_SEMANTICS` | Grafana template-interpolates and renders panel descriptions as Markdown; pinned SigNoz displays the stored value as plain tooltip text. |
| `TARGET_PROMQL_RESPONSE_LABEL_STRIPPED` | Pinned SigNoz removes the exact `fingerprint` label and every non-`__name__` label prefixed `__` while serializing PromQL results. Queries that explicitly retain, group by, or create those output labels are omitted. |
| `TARGET_ONLY_LABEL_SEMANTIC_USE` | The expression explicitly reads a receiver-only label, or implicitly consumes the full label set while selecting series or combining classic histogram buckets. Target-added values can change cardinality or values before response normalization, so the query is omitted; corrective `without(...)` and `ignoring(...)` exclusions remain allowed. |
| `METRIC_NAME_SEMANTIC_USE_AFTER_REMAP` | The expression reads `__name__` through grouping, matching, label functions, or ordering while a selected metric is renamed. The target would observe the remapped value, so no executable query is emitted. |
| `TARGET_PROMQL_NATIVE_HISTOGRAM_DROPPED` | Pinned SigNoz serializes only float points from PromQL matrix results and drops native-histogram points. Queries with known native-histogram inputs are omitted unless their top-level operation is statically proven to return floats. |
| `GRAFANA_TIMESERIES_POINT_MODE_SEMANTICS` | Grafana time-series panels default to density-aware Auto points, while pinned SigNoz has a fixed `showPoints` boolean. The target is emitted with linear interpolation, disconnected null gaps, and points disabled, but Auto point visibility remains review-only. |
| `GRAFANA_GRAPH_RENDERING_DEFAULTS` | Grafana graph/time-series line width, legacy fill, and tooltip defaults are not all representable by the pinned SigNoz graph widget. Legacy `short` axis formatting is preserved, but the remaining presentation differences stay review-only. |
| `GRAFANA_CONSTANT_VARIABLE_MUTABILITY` | Grafana constant variables are hidden immutable bindings, while pinned SigNoz can represent the value only as a visible editable textbox. The initial value is preserved but the variable remains review-only. |
| `MISSING_VARIABLE_VALUE` | A query references a dashboard variable without a resolved value. Live validation is skipped; when a non-dynamic All selection lacks its exact scalar list or a CUSTOM selection cannot survive target reload exactly, the stored variable and dependent queries are omitted. |
| `MULTI_VARIABLE_VALUE_UNRESOLVED` | A variable uses Grafana's multi/All Prometheus formatter and its source expansion is outside the exact cross-mode subset. A declared target selection remains preserved; if several values exist while `multi` is false, the contradictory variable and dependent queries are omitted. |
| `VARIABLE_VALUE_ESCAPING_UNRESOLVED` | Grafana interpolation and pinned SigNoz raw variable, reserved-time, or Go-template rendering cannot be proven to produce the same PromQL. Unsafe values, prefix collisions, target-only reserved substitutions, Go-template actions, and non-equivalent Dynamic-All uses are omitted. |
| `CUSTOM_VARIABLE_RELOAD_SEMANTICS` | Pinned SigNoz rebuilds a CUSTOM selection from `customValue` on reload. The option list is reduced to the exact validated current selection, or the variable and dependent queries are omitted when that selection is outside the proven string-only parser subset. |
| `PANEL_TIME_OVERRIDE` | The panel uses a source-specific time window or time shift. |
| `VARIABLE_REGEX_FILTER` | A Grafana variable regex cannot be reproduced by a native dynamic variable. |
| `VARIABLE_ALL_VALUE_SEMANTICS` | Grafana All semantics cannot be preserved exactly. SigNoz matcher removal is executable only for an explicit Grafana `.*` All value used as the complete value of every positive regex matcher; default option-list All, negative/equality/partial uses, other custom values, and incomplete non-dynamic lists are omitted. |
| `DATASOURCE_VARIABLE_OMITTED` | A Grafana datasource selector is unnecessary in the single-backend target. |
| `UNSUPPORTED_VARIABLE` | The variable type has no safe target representation. |
| `RECORDING_RULE_INLINED` | An unconstrained, unlabeled, acyclic recording rule was expanded into its source expression. |
| `METRIC_TYPE_INCOMPATIBLE` | Live type or temporality metadata makes the proposed Builder operation unsafe. |
| `GRAFANA_TRANSFORMATION` | The panel relies on a Grafana transformation that is not reimplemented. |
| `QUERY_REFID_NORMALIZED` | A missing or duplicate source query reference received a deterministic unique name. |
| `NO_QUERY_TARGETS` | The source visualization has no executable query targets. |
| `ALL_TARGETS_DISABLED` | Every executable target is disabled, which cannot be imported safely. |
| `PANEL_OMITTED` | The panel was deliberately left out of the payload rather than emitted invalidly or misleadingly. |
| `LEGACY_PANEL_ALERT` | A legacy panel alert block is present but not converted by dashboard migration. |
| `ANNOTATION_QUERY` | A dashboard annotation definition is present but not emitted. |
| `DASHBOARD_LINK` | A dashboard link is present but not emitted. |
| `PANEL_LINK` | A panel link is present but not emitted. |
| `FIELD_THRESHOLDS` | Grafana field thresholds are present but not emitted. |
| `FIELD_OVERRIDES` | Grafana field overrides are present but not emitted. |
| `LIBRARY_PANEL_REFERENCE` | The export refers to an external library panel whose model is unavailable. |
| `QUERY_NAME_COLLISION` | Expanded Builder dependency names collide within the panel, forcing PromQL fallback. |
| `FORMULA_LABEL_SET_MISMATCH` | Formula operands expose different label sets and cannot preserve PromQL matching. |
| `HISTOGRAM_BUCKET_FILTER` | A classic histogram `le` matcher cannot be carried onto a native histogram metric. |
| `BUILDER_RATE_INCREASE_SEMANTICS` | The structurally representable Builder candidate uses step-bucket deltas, which do not preserve PromQL range extrapolation and reset handling; canonical PromQL is emitted. |
| `BUILDER_LATEST_LOOKBACK_SEMANTICS` | The structurally representable Builder candidate uses bucket-local `latest`, which does not preserve PromQL lookback and stale-marker behavior; canonical PromQL is emitted. |
| `BUILDER_HISTOGRAM_PERCENTILE_SEMANTICS` | The structurally representable percentile candidate does not preserve the complete PromQL classic-histogram `rate`/`increase` plus `histogram_quantile` evaluation; canonical PromQL is emitted. |
| `BUILDER_FORMULA_EVALUATION_SEMANTICS` | Builder formula label matching, missing-series defaults, and non-finite handling differ from PromQL; the formula remains report evidence while canonical PromQL is emitted. |
| `VARIABLE_SELECTOR_SCOPE_DROPPED` | A metric or matcher scope in `label_values` cannot be encoded by the target dynamic variable. |
| `VISUALIZATION_TYPE_DOWNGRADE` | The closest target panel does not preserve the source visualization type exactly. |
| `UNMAPPED_VISUALIZATION_CONFIG` | A source visualization setting is inventoried but has no target representation. |
| `UNMAPPED_QUERY_CONFIG` | A source query setting is inventoried but has no proven target representation. |
| `UNMAPPED_DASHBOARD_CONFIG` | A source dashboard or legacy-row setting is inventoried but has no target representation. |
| `UNMAPPED_VARIABLE_CONFIG` | A source variable setting is inventoried but has no target representation. |
| `ROW_PANEL_TARGET_UNSUPPORTED` | A Grafana row contains stale targets; they are accounted for but the emitted row remains structural and query-free. |
| `NATIVE_DIFFERENTIAL_VERIFIED` | A Builder or formula candidate was confirmed numerically equivalent to its PromQL passthrough on the live target and emitted as a native Builder query, restoring drilldown. |
| `OPERATOR_OVERRIDE` | An operator-provided Builder query replaced the generated translation; it is still verified live before it can be emitted natively. |
| `BUILDER_OVER_TIME_SEMANTICS` | The candidate maps a PromQL *_over_time range function to a SigNoz Builder time aggregation; equivalence is confirmed by the live differential. |
| `BUILDER_TEMPORAL_PHASE_SHIFT` | The Builder result matched the PromQL passthrough in magnitude but was offset by one step in time (e.g. SigNoz `latest` labels its bucket at the start); it is not temporally equivalent and was held at review rather than promoted natively. |
