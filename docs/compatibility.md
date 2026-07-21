# Compatibility policy

The project optimizes for semantic fidelity and inspectability, not for the highest possible native-conversion percentage.

## Dashboard decisions

Every Grafana target receives one of three verdicts:

- `native`: represented by a target construct whose behavior is proven equivalent for the classified source shape;
- `passthrough`: represented by canonical PromQL because Builder would change behavior;
- `needs_review`: preserved, but a source or target feature prevents an automatic safety claim.

SigNoz widgets select one query mode. If one target in a panel needs PromQL, all targets in that panel use their canonical PromQL form. This prevents a mixed panel from silently reverting native targets to the original, unremapped Grafana expression.

Builder analysis uses live metric metadata when a target is supplied. Counter `rate` and `increase` candidates require a monotonic cumulative sum, and selector candidates use `latest`; discovered attributes are retained as candidate groupings. Metadata proves that a Builder operation is type-compatible, not that its evaluation is PromQL-equivalent.

Metric-name mappings may be many-to-one across a migration corpus because legacy aliases do not necessarily occur together. They must be injective within each expression, however. If two distinct source metrics used by one expression would become the same target metric, or one mapped name would collide with another source metric already using the target spelling, the query is non-executable and carries `METRIC_NAME_REMAP_COLLISION` rather than silently becoming a self-comparison or self-arithmetic expression.

Structurally representable Builder queries and formulas remain in the report as candidates, but they are not emitted by default. The dashboard emits their canonical PromQL and records the precise boundary: `BUILDER_RATE_INCREASE_SEMANTICS` for PromQL extrapolation/reset behavior, `BUILDER_LATEST_LOOKBACK_SEMANTICS` for selector lookback/staleness, `BUILDER_HISTOGRAM_PERCENTILE_SEMANTICS` for classic-histogram percentile evaluation, or `BUILDER_FORMULA_EVALUATION_SEMANTICS` for formula label matching, missing operands, and non-finite values. Such candidates have a `needs_review` verdict and cannot contribute to native or emitted-Builder counts.

Current SigNoz value panels cannot reliably render grouped scalar Builder tables because label columns precede the numeric column. A grouped Builder or formula candidate in a value panel therefore uses its canonical PromQL fallback. The report records `BUILDER_VALUE_GROUP_BY_UNSUPPORTED`.

Pinned SigNoz v0.133 also reduces PromQL value/stat responses through a scalar path that can select the first series and its oldest point rather than Grafana's last-value result. Any value panel that executes PromQL is therefore emitted as a graph, with `PANEL_TYPE_DOWNGRADE`, so the complete time-series envelope remains visible. Table and pie panels that require PromQL use the same conservative graph downgrade. Grafana heatmap and histogram panels are always emitted as graphs: the legacy v5 histogram request path asks for `distribution`, while the PromQL response is a nested time-series envelope that the histogram converter does not consume and the panel renders blank. Grafana bar charts are also emitted as graphs because SigNoz cannot reproduce Grafana's Auto orientation and conditional value labels. The migrator does not publish those non-working or misleading artifacts as native visualizations.

Every emitted widget explicitly sets `spanGaps:false`, `lineInterpolation:"linear"`, and `showPoints:false` instead of relying on target defaults. Grafana's time-series `showPoints:"auto"` remains a review boundary because its density-aware behavior cannot be represented by SigNoz's boolean setting; such panels carry `GRAFANA_TIMESERIES_POINT_MODE_SEMANTICS`. Legacy graph axes retain Grafana's default `short` K/Mil/Bil formatter. Line width, legacy fill, and tooltip aggregation still differ, so graph and time-series panels also carry `GRAFANA_GRAPH_RENDERING_DEFAULTS` rather than being counted as visually native.

## Prometheus and OpenTelemetry labels

Receiver-only labels are guarded at the evaluation boundary, not merely stripped from returned series. Explicit matchers, grouping, vector matching/includes, label joins/replacements, and label-ordering operations are non-executable when they consume one of these target-owned values. Classic `histogram_quantile` and `histogram_fraction` also group bucket series by the full remaining label set; a raw bucket input is therefore omitted when `fingerprint` survives, while an inner aggregation that provably removes target-only labels remains eligible. A non-bucket histogram input is treated as native only when live metadata proves native histogram samples rather than classic `_bucket`, `.bucket`, or `le`-bearing series.

The OpenTelemetry Prometheus receiver represents Prometheus `job` and `instance` as `service.name` and `service.instance.id`. The analyzer rewrites selectors, aggregation groupings, explicit vector matching, and label arguments in functions such as `label_replace`, `label_join`, and `count_values`. If one expression uses distinct source labels that resolve to the same target spelling, remapping can collapse them across selectors, an enclosing aggregation, vector matching, or a label-producing function. Collision analysis is therefore expression-wide and fail-closed: the query is non-executable and carries `PROMETHEUS_LABEL_REMAP_COLLISION`.

The receiver may also add target-only resource, scope, and transport attributes: `server.address`, `server.port`, `url.scheme`, `__scope.name__`, `__scope.schema_url__`, `__scope.version__`, `__temporality__`, and `fingerprint`. These attributes can break binary vector matching even when the logical Prometheus series match. Pinned SigNoz adds `fingerprint` to every stored series before PromQL evaluation even though the metric-attribute endpoint does not report it, so selector analysis always includes it as target-only, including when metadata is unavailable. Live metadata labels are normalized through the configured label map, and the complete target-only inventory is then removed from the logical label set. Implicit arithmetic, comparison, `and`, `or`, and `unless` matching receives an explicit `on(...)` over the exact logical output-label universe; set operators retain Prometheus's many-to-many cardinality. An explicit `ignoring(...)` list is extended with the same target-only inventory. The source expression remains unchanged.

Output-label inference is deliberately bounded. Selectors, subqueries, scalar/vector arithmetic, exact vector-matching cardinalities (including `group_left` and `group_right` includes), ordinary and selection aggregators, `count_values`, and proven label-preserving functions are modeled. The function inventory includes `changes`, `last_over_time`, and the common `avg`, `count`, `min`, `max`, `present`, `quantile`, `stddev`, `stdvar`, and `sum` over-time functions. If target-only labels—including the universal `fingerprint`—require a matching correction but either operand's output labels cannot be established exactly, the query is non-executable with `TARGET_RESOURCE_VECTOR_MATCHING_UNRESOLVED`; an unmodeled function is never guessed to be label-preserving. An enclosing aggregation that provably removes the target-only labels remains executable. The differential validator records an emitted rewrite's observed behavior over its fixed window; it does not turn that metadata snapshot into a universal proof.

Differential series matching is exact after the documented `job` → `service.name` and `instance` → `service.instance.id` remaps (and the Builder-only metric-name normalization). It does not treat arbitrary target label supersets as equivalent. With no `--target-provenance` assertion, no target-only label is ignored. Only the known Prometheus-receiver resource, scope, and transport labels `server.address`, `server.port`, `url.scheme`, `__scope.name__`, `__scope.schema_url__`, `__scope.version__`, `__temporality__`, and `fingerprint` may be ignored, and only when the operator explicitly supplies `--target-provenance otel_prometheus_receiver` and the comparison is also bound to a concrete emitted target kind. The assertion and every ignored label name are retained in evidence. An unknown extra label, or multiple target series that collapse to one logical source series after an allowed label is removed, remains non-equivalent.

The pinned SigNoz PromQL response converter strips `fingerprint` and non-`__name__` labels whose names begin with `__`. A query that explicitly produces one of those labels is omitted with `TARGET_PROMQL_RESPONSE_LABEL_STRIPPED`; preserving its expression while changing its observable series identity is not accepted as passthrough. The proof covers selectors, grouping, vector cardinality and includes, `label_replace`, `label_join`, `count_values`, histogram functions, and label-dropping outer aggregations, and fails closed when it cannot establish the output label set.

The same converter serializes float samples but drops native-histogram samples. When live metadata identifies a selector input as `histogram` or `exponential_histogram`, an expression that can still return native histograms is omitted with `TARGET_PROMQL_NATIVE_HISTOGRAM_DROPPED`. Operations proven to return floats, including `histogram_count`, `histogram_sum`, `histogram_avg`, `histogram_stddev`, `histogram_stdvar`, `histogram_fraction`, `histogram_quantile`, `count`, `group`, `count_over_time`, `present_over_time`, and `scalar`, remain eligible.

Source and target variable overrides can intentionally select different names
for the same logical label value. Differential validation records a value alias
per measured query only when the variable is defined by
`label_values(..., label)`, is referenced by both exact query artifacts, is
the complete positive
matcher value for that same logical label on both sides, and syntax analysis
proves the label survives into the result. The proof follows label-preserving
PromQL functions and aggregations and requires an exact emitted Builder filter
plus group-by; formulas and unknown transformations fail closed. An unrelated
panel, a variable used against another matcher label, a multi-value regex, a
label rewrite, or conflicting variables for one target value cannot authorize
normalization. SigNoz's dynamic-variable value `__all__` is matcher-removal
control syntax, not a literal target label value, so it never authorizes a
value alias (an exact source/target All match needs no alias). The exact
per-query target-to-source alias map and its per-variable source/target binding
are retained in differential evidence. During attachment, the alias map is
recomputed from those bindings; the migration variable record, primary
dashboard dynamic variable, exact target request variable and query, and the
fully rematerialized source expression must all agree. Missing or altered
bindings fail closed. Aliases that collapse multiple target series remain
ambiguous rather than equivalent.

SigNoz Builder formulas cannot specify an `on(...)` key. A formula with at least two vector dependencies is therefore emitted as an explicit-matching PromQL query with a `needs_review` verdict when any dependency groups by a known target-only resource attribute. Other structurally representable formulas are also candidate-only: SigNoz's subset label matching, default-zero operands, and dropped non-finite results are not PromQL formula semantics.

`without(...)` aggregations need the inverse treatment. SigNoz series contain collector-only scope, temporality, and fingerprint labels that Prometheus never evaluated. The target expression adds those transport labels and the known target-only resource attributes to the exclusion list, preventing duplicate logical series from surviving an aggregation.

Grafana duration globals such as `$__rate_interval`, `$__interval`, and `$__range` receive deterministic durations. Unformatted `$__from` and `$__to`, including their braced and legacy spellings, become SigNoz's `$SIGNOZ_START_TIME` and `$SIGNOZ_END_TIME`; source differential requests materialize the exact epoch-millisecond boundaries from the persisted comparison window. Formatted or field-qualified time globals are not guessed. Any other `$__*` built-in or macro is non-executable because leaving it for SigNoz would turn Grafana syntax into an undefined or differently substituted target reference. Variables that alter PromQL grammar rather than matcher values remain PromQL and carry a structural-variable reason only when the canonical target expression needs no target-specific rewrite. If label remapping, metric remapping, regex anchoring, target-only `without` exclusions, or explicit target vector matching would also be required, the query is non-executable and carries `DYNAMIC_PROMQL_REWRITE_REQUIRED`; the migration never imports an unrevised source expression as though those rewrites had occurred.

Quoted strings are not automatically value positions under extended PromQL identifiers. Selector label names, grouping and `without` lists, vector-matching and group-include lists, and specific arguments to `label_replace`, `label_join`, `count_values`, `sort_by_label`, and `sort_by_label_desc` all name labels and participate in receiver mapping. A dashboard variable in any mapped label-name role is non-executable under `DYNAMIC_PROMQL_REWRITE_REQUIRED`: its runtime value might be a legacy spelling such as `job`, and the target variable contract cannot conditionally remap that value. The same rule applies to a variable metric-name identifier whenever a nonidentity metric-name map is configured. Variables used as ordinary matcher values remain supported under the separate variable rules below.

The pinned PromQL parser runs with experimental options disabled. The experimental `anchored` and `smoothed` range modifiers are rejected as `PROMQL_PARSE_ERROR`; the same words remain valid metric and grouping-label identifiers.

Grafana variables preserve their full current selection and custom `allValue` in the neutral model. For target import and live target validation, a recognized Grafana All selection is encoded as SigNoz's `__all__` sentinel only for an emitted dynamic variable. The pinned SigNoz frontend takes that path only for a `DYNAMIC` variable with `allSelected`, `showALLOption`, and `multiSelect`. Non-dynamic target variables therefore require an exact selected scalar list. An explicit normalized scalar list is preserved unchanged, but the migrator does not guess that list by splitting a CUSTOM definition: SigNoz's parser supports escaped commas, display-label/value pairs, and numeric conversion. When the complete list is unavailable, the variable and every panel query that references it are omitted with `MISSING_VARIABLE_VALUE` and `VARIABLE_ALL_VALUE_SEMANTICS`, rather than persisting an ignored `allSelected` flag, sending `__all__` as a literal value, or importing a runnable query with an undefined selection. A command-line target override for a real dashboard variable replaces the unresolved All selection before translation and restores the explicit scalar path; a multi-variable override is persisted and executed as the same one-element array. An unknown override cannot invent a variable absent from the imported dashboard. Differential validation bound to an already committed migration report does not rewrite that artifact, so reviving a previously omitted selection requires rerunning migration with the override. Source-side differential validation is stricter: an All selection is expanded only from a non-empty explicit source `allValue` or a source-side override; an unset or empty `allValue` is unresolved and no source comparison request is issued. At dashboard-definition level, custom All values other than empty or `.*`, including `.+`, also carry `VARIABLE_ALL_VALUE_SEMANTICS` because removing a matcher can include series where the label is absent.

Pinned SigNoz v0.133 has a separate CUSTOM reload path: dashboard initialization rebuilds the selection from `customValue` and does not use persisted `selectedValue` to choose it. The migrator therefore serializes `customValue` from the exact current selection that live validation executes, even when that deliberately reduces the original option list. That reduction is explicit `CUSTOM_VARIABLE_RELOAD_SEMANTICS` review evidence. MCP revalidation derives the runtime value from the stored `customValue` and rejects an artifact whose `selectedValue` disagrees. Empty or whitespace-lossy strings, display-label syntax, numeric-coercible values, control characters, and ECMAScript's U+FEFF whitespace are outside the proven string-only round-trip subset; such a variable and every dependent query are omitted with `MISSING_VARIABLE_VALUE` and `CUSTOM_VARIABLE_RELOAD_SEMANTICS` instead of validating one value and loading another after refresh.

The pinned SigNoz v5 live-query variable contract accepts strings, numbers, booleans, and lists of those scalar values. Except for the CUSTOM reload qualification above, target import, preview, execution, MCP revalidation, and differential target artifacts therefore preserve a declared Grafana multi-selection as an array; they never select `current[0]` or flatten the target list into a synthetic string. A hostile or inconsistent export with several current values but `multi:false` is different: persisting one scalar would truncate validation state, while forcing multi-select would invent source configuration, so the variable and every dependent query are omitted with explicit review evidence. A missing or blank current selection is likewise omitted instead of allowing either frontend to choose an unvalidated load-time default. The emitter also has a defensive boundary for hand-built migrations and will preserve the full array instead of truncating it.

Grafana constant variables are hidden immutable bindings. Pinned SigNoz v5 has no constant type, so the preserved initial value is emitted as a visible editable textbox and carries `GRAFANA_CONSTANT_VARIABLE_MUTABILITY`; it is never classified as a native variable conversion.

Dynamic All uses SigNoz's `__all__` matcher-removal sentinel. Matcher removal is equivalent only when Grafana declares the explicit custom All value `.*` and every occurrence of the variable is the complete value of a positive `=~` matcher. Default option-list All, `!~`, equality, partial matcher values, and non-matcher uses are omitted; removing those expressions changes their meaning. A legitimate selected dynamic-label value whose literal text is `__all__` is also omitted unless it is that proven explicit-All path, because pinned SigNoz treats the value as control syntax even when Grafana's All option is disabled.

Source-side differential evaluation follows the pinned Grafana Prometheus distinction: both `multi:true` and `includeAll:true` use regex interpolation, even when one ordinary value is selected. For the exact cross-version-safe subset, one safe value stays scalar, several safe values become a parenthesized regex alternation, `regex` has the same form, and `pipe` omits parentheses. Multi/All values containing quotes, backslashes, or regex metacharacters remain unresolved because Grafana's recorded export does not identify which escaping feature mode produced the live query. For a regular scalar, backslash doubling is invariant and is reproduced, while either quote remains unresolved because the two modes disagree. An explicit source override can supply the exact expansion for either case.

Emitted query syntax normalizes unformatted `${name}` and `[[name]]` references to `$name` only when that dashboard variable is actually defined. Undefined braced or legacy references are omitted because Grafana preserves their bytes while normalization would change them; field paths and unsupported formatters also fail closed. Grafana replacement captures such as `${1}` are left untouched. `regex` and `pipe` formatters are normalized only when the variable is the complete value of a regex matcher and retain `REGEX_VARIABLE_SEMANTICS`; all other formatted references carry `GRAFANA_VARIABLE_FORMAT` and are not emitted as executable queries.

Before an emitted PromQL artifact is accepted, the migrator checks the pinned SigNoz renderer itself, including canonical PromQL fallbacks selected for Builder and formula candidates. SigNoz injects reserved runtime variables, sorts names by descending length, performs boundary-free raw `ReplaceAll` substitutions, and then evaluates the result as a Go template. Queries are omitted when a dashboard variable collides with a reserved name, a longer identifier begins with a runtime name and would be partially replaced, a target-reserved literal has no corresponding source global, or the expression contains a Go-template action. Selected values are checked too: after one value is inserted, a later shorter or equal-length `$name` or `[[name]]` replacement, or the final Go-template pass, must not reinterpret it. The same checks run when stored evidence is replayed. This prevents target-side substitution from changing an expression or selected value that Grafana would execute literally.

Grafana query result format is preserved in migration evidence. An empty format or `time_series` is the default Prometheus shape. `table`, `heatmap`, and other non-default or unknown values retain the safest existing query translation but carry `GRAFANA_QUERY_FORMAT`, because equivalent target response shaping has not been proven.

Every explicit Grafana target `step` retains its raw value and source path, so absent, `null`, empty-string, zero, negative, and positive values remain distinguishable. A positive value is also normalized to seconds and carries `GRAFANA_INTERVAL_CONTROL`; it does not silently override `interval`, `intervalFactor`, or `maxDataPoints`, because Grafana exports do not provide a reliable precedence contract among those controls. Explicit nonpositive, empty, or null values carry `UNMAPPED_QUERY_CONFIG` instead of being silently clamped into absence. Target-level `range` and `exemplar` settings, including explicit `false`, are likewise inventoried at their source paths and carry `UNMAPPED_QUERY_CONFIG` until equivalent target behavior is proven.

The same presence-preserving inventory applies to otherwise-unmodeled dashboard, legacy-row, variable, panel, and target properties. Explicit `false`, zero, `null`, empty strings, arrays, and objects remain visible at their exact JSON paths under `UNMAPPED_DASHBOARD_CONFIG`, `UNMAPPED_VARIABLE_CONFIG`, `UNMAPPED_VISUALIZATION_CONFIG`, or `UNMAPPED_QUERY_CONFIG`. Only proven artifact metadata such as dashboard revision identity and panel plugin version is excluded. Variable labels are preserved as target descriptions even when other variable properties require review.

Visualization text is accounted for separately from query syntax. A Grafana legend using `__auto`, a dashboard variable, or any `{{label}}` placeholder carries `GRAFANA_VARIABLE_LEGEND_SEMANTICS`; SigNoz does not reproduce all of Grafana's naming and missing-label behavior. A variable-bearing panel title carries `GRAFANA_VARIABLE_PANEL_TITLE_SEMANTICS`. Every nonempty panel description carries `GRAFANA_PANEL_DESCRIPTION_SEMANTICS` because Grafana interpolates templates and renders Markdown while pinned SigNoz exposes the stored text as a plain tooltip. The source text remains in the artifact and report, but none of these differences is counted as native fidelity.

Grafana row panels are structural containers. Some modern exports retain stale `targets` on a row header; those targets are accounted for as `ROW_PANEL_TARGET_UNSUPPORTED` evidence but are never emitted or executed. Queries on the row's child panels remain independently eligible for migration.

## Alert rules

One numeric comparison at the root is separated into a query and target
threshold. This does not make the Prometheus alert state machine equivalent to
SigNoz. Prometheus tracks a pending state across repeated instant evaluations;
SigNoz evaluates threshold matches over a trailing range. Therefore both
source shapes are candidate-only:

- a nonempty, valid `for: T` becomes an `evalWindow` of `T` and an
  `all_the_times` threshold, but carries
  `PROMETHEUS_FOR_WINDOW_APPROXIMATION` and remains disabled;
- an absent or explicit zero `for` becomes the target's one-minute window and an
  `at_least_once` threshold, but carries
  `ALERT_IMMEDIATE_WINDOW_APPROXIMATION` and remains disabled.

SigNoz v0.133 does not make disabled creation atomic: its create path installs
an executor after storing the rule even when `disabled` is true. A missing
disabled candidate is therefore review-only and is never POSTed by this tool;
the per-rule write outcome is `not_created_disabled` with no mutation attempt.
An already managed rule can be PUT to the disabled state because the update
path synchronizes task state. Do not POST an object copied from the standalone
rules candidate JSON. That file preserves the proposed payload for review, but
direct creation can evaluate or notify before a later update or restart.

Rule YAML is first checked by the pinned Prometheus Go `rulefmt` v0.311.3
contract. A syntactically invalid `for` is therefore an input error and never
becomes a CLI candidate. `ALERT_FOR_DURATION_UNREPRESENTABLE` remains a
defensive classification for callers that construct the neutral Go model
directly.

SigNoz's pinned PromQL rule evaluator requests points at a fixed 60-second
step. For a valid `for: T`, the candidate sets `requireMinPoints` only when
`floor(T / 60s) - 2` is positive, and sets `requiredNumPoints` to that value.
The two-point allowance tolerates scrape-boundary jitter. It reduces the risk
of a newly appearing series firing early; it is not proof of continuous
Prometheus truth and never clears the review gate. Windows below three minutes
omit the setting because SigNoz rejects a nonpositive required point count.

An explicit positive group `interval` is preserved as target frequency only
when it does not exceed the emitted evaluation window. A valid longer interval
carries `RULE_GROUP_INTERVAL_UNREPRESENTABLE`; the disabled candidate uses
`min(evalWindow, 1m)` so the artifact remains structurally valid without
claiming that the faster cadence is equivalent. Syntactically invalid duration
strings are rejected by rulefmt before classification. An explicit zero
interval has Prometheus's global-default meaning and is treated like an absent
interval; a negative programmatic value remains unrepresentable. A nonzero
`query_offset` and positive `limit` have no target rule equivalent. Every rule in
such a group carries `RULE_GROUP_QUERY_OFFSET_UNSUPPORTED` or
`RULE_GROUP_LIMIT_UNSUPPORTED` and remains in review. An explicit zero offset
and every nonpositive limit are semantic no-ops under the pinned Prometheus
contract. Likewise, `for: 0s` is an immediate alert and
`keep_firing_for: 0s` is a no-op; only a positive `keep_firing_for` carries
`KEEP_FIRING_FOR_UNSUPPORTED`.

Prometheus group labels are merged before rule labels, with the rule value
winning on a duplicate key. The emitted target receives the complete effective
map, while the report retains the source group map and source rule map
separately. Group `query_offset`, `limit`, labels, interval, and source path are
also machine-report fields.

Configured alert-label keys follow the same receiver mapping as query labels:
`job` becomes `service.name`, and `instance` becomes
`service.instance.id`. Input preflight rejects source/target key pairs that
would collapse into one configured key. It also fails closed when a configured
target-spelling key can overwrite a dynamic legacy query label, or a configured
legacy key would be remapped over an already target-spelled dynamic query
label. Only scalar or root aggregation shapes that positively prove the
conflicting label is dropped are exempt. Selection aggregators (`topk`,
`bottomk`, `limitk`, and
`limit_ratio`), `count_values`, functions, subqueries, and unknown vector
shapes are not granted that exemption.

Prometheus's UTF-8 label-name contract is broader than the pinned SigNoz v0.133
rule API. The source parser accepts the upstream contract, then target
compatibility preflight requires emitted label and annotation keys to use ASCII
letters, digits after the first byte, underscore, or dot. The error reports the
source path and key before output creation or target access.

Every extracted groups object is checked by Prometheus's pinned Go `rulefmt`
v0.311.3 contract before normalization. The input preflight rejects empty or
duplicate group names within that object, missing or ambiguous rule kinds,
missing or unparsable expressions, invalid recording-rule fields and names,
invalid label/annotation names or values, invalid templates, unknown group or
rule keys, and duplicate literal label/annotation keys. Plain rule files,
multi-document YAML, `PrometheusRule`, Kubernetes `List`, and
`PrometheusRuleList` envelopes remain accepted. Kubernetes type metadata,
object metadata, and PrometheusRule status are deliberately ignored as
non-semantic envelope data while the source digest continues to bind all input
bytes. YAML aliases are materialized within their original document before the
extracted groups object is handed to rulefmt, including anchors declared in
Kubernetes metadata and referenced from `spec.groups`; recursive aliases or
bounded-expansion violations fail input preflight. A semantically empty or
typo-only document/list item is not ignored.

Generated alert provenance labels are tool-owned. Source alerts that would
overwrite `prometheus_alertname`, `prometheus_rule_group`,
`promcast_id`, or the conditionally generated `prometheus_severity`
preservation label fail input preflight. Pinned SigNoz v0.133 also owns
`alertname`, `threshold.name`, `ruleId`, `ruleSource`, and conditional `nodata`;
configured source values for those keys are rejected. Plain `threshold` is not reserved.
Every alert candidate carries `TARGET_ALERT_RUNTIME_LABELS`, because these
runtime additions change the label set and fingerprint. Expressions that
explicitly reference `severity`, `alertname`, or `threshold.name` retain an
additional note. Templates that read `severity` or `threshold.name`, which
SigNoz mutates before expansion, are replaced with an omission sentinel instead
of importing misleading behavior. An explicit
`promcast_source_id` must be nonempty and already trimmed; it is validated
before identity calculation rather than silently canonicalized.

Input validation is collection-atomic for one command: if any file fails, no
valid sibling reaches target metadata, artifact publication, output-directory
creation, or target mutation. The rulefmt module version is not a claim about
the deployed server version; live acceptance separately exercises Prometheus
3.5.0.

Rules also remain disabled when a threshold cannot be separated safely or when
a positive `keep_firing_for` is present. Duplicate alert names receive
deterministic group/severity suffixes while the source name remains in labels. Annotation
and alert-label variables are converted only when the pinned target execution
path has a direct counterpart. `$labels.foo`, `.Labels.foo`,
`index $labels "foo"`, and `index .Labels "foo"` are normalized to the target
label syntax after the `job`/`instance` remap; `$value` is supported directly.
Prometheus-only functions such as `query`, `graphLink`, `tableLink`,
`parseDuration`, `stripPort`, and `stripDomain`, external URL/label data, and
all unverified actions are replaced by a deterministic
`[unsupported Prometheus template omitted]` literal in a disabled candidate.
This guarantees that review artifacts remain accepted by target template
validation. Bare dollar text outside a Prometheus action is encoded as a
literal dollar so SigNoz's custom bare-variable preprocessor cannot reinterpret
source text such as `owner $job`.

A live rule target, including `--dry-run`, requires a nonempty stable
`--source-namespace` before source parsing, output-directory creation, or any
network request. Offline runs may retain the legacy path-derived identity for
local review, but that identity is nonportable across checkout or file moves
and must not be used as an import plan.

Recording rules are accounted for in reports but are not emitted as alert rules. Their series can be ingested separately and referenced by migrated alerts.

## Validation levels

Validation is deliberately layered:

1. parse and account for every source object;
2. classify with deterministic reason codes;
3. validate the strict v5 request with SigNoz preview;
4. execute it and distinguish valid-empty from data-bearing results;
5. compare Prometheus and SigNoz series over the same fixed window.

A preview success does not prove that a query selects data. A data-bearing result does not prove numerical equivalence. Reports keep those claims separate.

A passing differential window is observed evidence for the exact effective query and window; it does not silently promote a Builder candidate to `native` or change dashboard emission away from PromQL. A report-bound run reloads the exact primary dashboard file named by the migration report and verifies its byte size, SHA-256, strict v5 shape, effective widget set, and one-to-one query envelopes before any network request. Attachment also binds the source bytes, migrated target endpoint, migration-time Grafana macro settings, comparison endpoints, request artifacts, window, tolerances, and summary. Runtime timestamps remain explicit provenance rather than part of a static query-envelope identity.

## Known limits

- Grafana transformations, repeated panels, query-result variables, and grammar-changing variables need review.
- PromQL subqueries, `without`, top-k scope differences, offsets, and `@` modifiers remain PromQL.
- The differential comparator tolerates bounded scrape-time skew; volatile rates can fail a whole-query threshold even when most points agree.
- Notification routing is represented through SigNoz policies, not invented channel names.
- Recording-rule evaluation and Alertmanager configuration are not reimplemented.
