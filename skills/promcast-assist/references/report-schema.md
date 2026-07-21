# Report schema (`out/<name>.report.json`)

Table of contents: top-level keys · panel record · query record · summary · run flags.

## Top-level keys

- `summary` — aggregate counts (see below).
- `panels[]` — one record per source panel.
- `variables[]` — dashboard variable outcomes.
- `sourceFeatures[]` — captured but unmapped source features (alerts, annotations, links, overrides).
- `reasonCodeIndex` — human descriptions for every reason code used.
- `run.flags` — includes `nativeCandidates` and `nativePromoted` when a live run promoted queries.

## Panel record (`panels[]`)

- `id`, `title`, `kind`, `emittedKind`, `sourcePath`.
- `emittedMode` — `builder` or `promql` (the panel's final query mode).
- `verdict` — `native`, `passthrough`, or `needs_review` (panel-level).
- `queries[]` — the per-query records.

## Query record (`panels[].queries[]`)

- `refId`, `sourcePath` — the sourcePath is the key you use in `overrides.yaml`.
- `original` — the source Grafana PromQL. This is the `--source` you pass to `verify`.
- `verdict` — `native`, `passthrough`, or `needs_review`. **Work only `needs_review` and `passthrough`.**
- `candidateKind`, `emittedKind` — `builder`, `formula`, or `promql`.
- `reasonCodes[]` — why the deterministic engine could not prove native. Codes that mean "not yours
  to fix": `MISSING_METRIC_IN_TARGET`, `NON_PROM_DATASOURCE`, `GRAFANA_EXPRESSION_TARGET`,
  `NON_EXACT_METRIC_SELECTOR`, `DYNAMIC_PROMQL_STRUCTURE`. Codes that are good candidates for an
  override: `METRIC_TYPE_REQUIRED`, `RATE_INTERVAL_REWRITE`, `RANGE_STEP_MISMATCH`,
  `BUILDER_RATE_INCREASE_SEMANTICS`, `BUILDER_LATEST_LOOKBACK_SEMANTICS`, `VECTOR_MATCHING`.
- `promql` — the emitted passthrough (what ships if you do not adopt an override).
- `builder`/`formula` — present when the engine already built a candidate you can refine.
- `validation` — `{previewed, previewOk, metricFound, executed, dataPresent}`. If `dataPresent` is
  false the metric has no live data; a candidate cannot be verified, so leave it passthrough.

## Summary

`native`, `passthrough`, `needsReview`, `panelsAccounted`, `queriesAccounted`. Compare `native`
before and after your overrides to report how much you raised.
