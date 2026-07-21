# PromQL → SigNoz Builder mapping

Table of contents: builder shape · aggregations · rate/increase · histograms · formulas · filters · step.

A SigNoz Builder query (the `builder` object in a candidate) has these fields:

```json
{
  "name": "A",
  "metricName": "<exact metric name>",
  "timeAggregation": "latest|rate|increase",
  "spaceAggregation": "sum|avg|min|max|count|p50|p75|p90|p95|p99",
  "filters": [{ "label": "<label>", "operator": "=|!=|REGEXP|NOT REGEXP", "value": "<value>" }],
  "groupBy": ["<label>", "..."],
  "stepSeconds": 60
}
```

## Aggregations

- `sum(m)` → `spaceAggregation: sum`, `timeAggregation: latest`.
- `avg/min/max/count(m)` → the matching `spaceAggregation`, `timeAggregation: latest`.
- `sum by (a,b) (m)` → `spaceAggregation: sum`, `groupBy: ["a","b"]`.
- `without(...)` has no exact Builder form — leave as passthrough.

## Rate and increase

- `sum(rate(m[5m]))` → `timeAggregation: rate`, `spaceAggregation: sum`, and set `stepSeconds` to the
  range in seconds (`[5m]` → `300`). **Expect many of these to fail `verify`**: SigNoz Builder rate
  is a per-step delta with no PromQL-style extrapolation, so it diverges from `rate()` on volatile or
  boundary-heavy series. Adopt only the ones `verify` reports `within_5pct` or better.
- `increase(m[R])` → `timeAggregation: increase`, `stepSeconds` = R in seconds. Same caveat.

## Histograms

- `histogram_quantile(0.95, sum by (le) (rate(m_bucket[5m])))` → `metricName: m_bucket`,
  `spaceAggregation: p95`, `groupBy` = the labels other than `le`. Only φ ∈ {0.5,0.75,0.9,0.95,0.99}.
  Histogram bucket metrics are often stored dot-suffixed on SigNoz (`m.bucket`) — if a candidate
  returns `series_mismatch`, try the dot-suffixed `metricName`.

## Formulas

For arithmetic across queries, use a `formula` instead of a `builder`:

```json
{ "formula": { "name": "A", "expression": "(100 - (A_1 * 100))",
  "queries": [ { "name": "A_1", "metricName": "node_cpu_seconds_total",
    "timeAggregation": "rate", "spaceAggregation": "avg" } ] } }
```

Every operand must be a Builder query; operand group-by sets must match for the formula to be
equivalent (mismatched grouping changes the join and will diverge).

## Filters

`{job="x", mode=~"user|system"}` → `filters: [{label:"job",operator:"=",value:"x"},
{label:"mode",operator:"REGEXP",value:"^(?:user|system)$"}]`. Anchor regexes with `^(?:...)$`.
Prometheus `job`/`instance` are stored on SigNoz as `service.name`/`service.instance.id` when
ingested via the OpenTelemetry Prometheus receiver — filter on those names.

## Step

Match `stepSeconds` to the source range for rate/increase; use the panel interval (usually 60) for
gauges. `verify` runs both probes at the Builder's step, so a wrong step causes false divergence.
