# SigNoz integration findings

This file records behavior verified against the reference SigNoz deployment.
It separates observed API behavior from compatibility policy so a future SigNoz
change has one place to update.

## Verified target contract

- Dashboard storage uses the v5 dashboard shape and accepts deterministic UUIDs.
- Query preflight uses `/api/v5/query_range/preview`; execution uses
  `/api/v5/query_range`.
- Metric metadata and attribute endpoints expose the information needed to
  decide whether `rate`, `increase`, `latest`, and percentile Builder operations
  are type-compatible candidates. They do not establish PromQL-equivalent
  evaluation semantics.
- PromQL widgets and Builder widgets use different persisted envelopes. A widget
  selects one query mode, so mixed representations must be normalized per panel.
- Rule writes use the v2alpha1 alert schema with a v5 composite query.
- Rule list/create/update operations support sequential reconciliation through
  the stable `promcast_id` label. They are not a concurrent-writer
  transaction.
- In pinned SigNoz v0.133, the
  [create path](https://github.com/SigNoz/signoz/blob/v0.133.0/pkg/query-service/rules/manager.go#L546-L619)
  stores the rule and installs its executor even when `disabled` is true.
  Restart loading and the
  [update task synchronization](https://github.com/SigNoz/signoz/blob/v0.133.0/pkg/query-service/rules/manager.go#L919-L937)
  honor disabled, but POST does not provide an atomic disabled-create
  operation. The importer consequently performs zero POSTs for missing
  disabled candidates, records `not_created_disabled`, and only uses PUT to
  disable an already identified managed rule. Standalone candidate JSON must
  not be blindly POSTed.

## Prometheus receiver behavior

The reference deployment ingests the same exporter targets through the SigNoZ
OpenTelemetry Collector Prometheus receiver. Prometheus `job` and `instance`
become `service.name` and `service.instance.id`. Collector-added resource,
scope, temporality, and fingerprint fields can change vector matching and
`without(...)` behavior unless the target PromQL is made explicit.

The differential comparator requires exact logical label identity after the
documented receiver remaps. Its narrow exception list contains only the
receiver-added resource and transport labels observed above. It is disabled by
default and enabled only when the operator supplies
`--target-provenance otel_prometheus_receiver`; that assertion, the emitted
target kind, and every ignored label name are recorded in evidence. Tenant, cluster, or other
unknown target-only labels cannot pass as equivalent. Receiver labels are not
allowed to hide a cardinality split: two target series collapsing onto one
source label set are reported as ambiguous and non-equivalent.

Metric storage may retain Prometheus underscore names or expose normalized
OpenTelemetry names. Live runs probe exact names and the known histogram
component suffix variants. Operators can pin any remaining mapping with
`--metric-name-map`; explicit mappings are validated against target metadata.

## Rendering constraints

- Table, pie, and value visualizations cannot safely hold a PromQL-mode widget
  in the same way as a graph. Such panels are downgraded and reported.
- A grouped Builder scalar may return label columns before the numeric column;
  value panels therefore use PromQL and graph rendering when grouped scalar
  rendering is ambiguous.
- The pinned legacy histogram converter does not consume the nested PromQL
  time-series envelope and renders blank, so Grafana heatmap/histogram panels
  use a graph. Grafana bar charts also use a graph because the target cannot
  preserve Auto orientation and conditional value labels.
- Emitted graph defaults are explicit. Legacy `short` axis formatting is
  retained, while line/fill and tooltip differences remain review evidence.
- Grafana transformations, repeat behavior, panel-local time overrides, and
  expression targets are not assumed to have direct SigNoz equivalents.

## Validation interpretation

Preview success proves the request shape is accepted. It does not prove the
metric exists or that the query returns data. A data-bearing result still does
not prove numerical equivalence with Prometheus. Reports retain those as three
separate checks. The differential command records bounded numerical evidence
for the exact emitted query and fixed window; it does not promote a
Builder/formula candidate or prove equivalence for other inputs.

The reproducible environment and pinned versions are described in
[`deploy/README.md`](../deploy/README.md) and [`VERSIONS.md`](../VERSIONS.md).

## Superseded reference run

The AWS run used separate source and destination nodes and
the exact release binary identified by SHA-256
`e7a0cf4f548b51a3732d7d27348624b1ea75ec6c1446a1726f63ff4958eaae14`.
That binary is no longer acceptance evidence. An adversarial live audit proved
that it could classify offset, range-window, exponentiation, regex-anchor, and
non-exact metric-selector conversions as native while changing their numeric
meaning. Its differential command compared the canonical PromQL fallback
instead of the emitted Builder/formula envelope. Therefore every native-fidelity
and numerical-equivalence conclusion below is withdrawn; the figures remain
only as a reproducible record of the flawed run.

Node Exporter Full accounted for all 140 panels and 284 queries: 151 native
queries (131 Builder and 20 formulas), 89 PromQL passthrough queries, and 44
review outcomes. SigNoz accepted all 284 preview requests; 236 of 273 enabled
queries returned data on the reference host.

The uninterrupted 15-minute differential window matched 6,416 source and
target points. Of those, 5,927 were within the fixed tolerance and 133 queries
were exact. The query summary was 179 equivalent, 57 value mismatches, 37 empty
on both sides, and 11 intentionally skipped, with no request, decoding,
coverage, missing-target, or series-matching failures. Most out-of-tolerance
points were volatile independently scraped gauges and exporter self-scrape
durations; the result is retained as measured rather than normalized into a
success claim.

The live rule run parsed 41 definitions in two groups, emitted all 26 alerts,
and retained all 15 recording rules in the report. All alert queries passed
preview and execution; 16 alerts were enabled and ten uncertain rules were
imported disabled. A repeat run updated the same 26 target IDs and created none.

A replacement reference run is acceptable only after the current code passes
the hostile fixtures, executes exact emitted target envelopes, reports every
inconclusive comparison as non-success, imports the generated dashboard, and
is inspected in the SigNoz UI. Until then this project has no current live
acceptance claim.

## Remediation: differentially verified native emission

The transpiler no longer claims a native conversion on structure alone. A
Builder or formula candidate is emitted as verbatim PromQL passthrough unless a
live promotion gate proves it. During a live run the gate executes both the
Builder envelope and the candidate's own PromQL passthrough against the target
over the same window, and promotes the candidate to a native verdict only when
their series match within tolerance. A promoted query carries the
`NATIVE_DIFFERENTIAL_VERIFIED` reason, so "native" implies a passing differential
by construction; offline runs, which cannot execute the proof, emit zero native
queries. The differential comparator drops `__name__` for every target kind so a
PromQL passthrough series reconciles with its source instead of reporting a
spurious no-series-match. Recording-rule inlining is refused on dynamic queries
so a variable can never be baked into a literal, and range windows are preserved
for passthroughs (the Builder step is aligned to the source range) so a faithful
passthrough is never review-gated for a range-step difference.
