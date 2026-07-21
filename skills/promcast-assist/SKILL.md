---
name: promcast-assist
description: >-
  Assist migrating Grafana dashboards and Prometheus alert rules to SigNoz. Use when the user asks
  to migrate a Grafana dashboard to SigNoz, convert PromQL panels to SigNoz Builder queries, raise
  the native-conversion rate of a promcast run, or resolve the needs-review and passthrough
  queries left by promcast. Runs the deterministic promcast CLI first, then proposes and
  live-verifies Builder queries only for the residual. Requires the promcast binary on PATH and
  a reachable SigNoz URL.
compatibility: Requires the `promcast` CLI on PATH and a live SigNoz URL with an API key for verification.
allowed-tools: "Bash(promcast:*)"
license: Apache-2.0
metadata:
  version: "0.1.0"
---

# SigNoz migration assistant

You help a user migrate Grafana dashboards and Prometheus rules to SigNoz. The deterministic
`promcast` CLI does the migration and is the **only** thing allowed to decide a query is
correct. Your job is narrow: for the queries the CLI could not prove native, propose a SigNoz
Builder query, and **verify every proposal with the CLI before adopting it**. Never hand-write a
whole dashboard, never mark a query correct yourself, and never adopt an unverified proposal.

## Non-negotiable rule

Every Builder query you propose MUST pass `promcast verify` on the live target before it is
written to the overrides file. If `verify` does not report `ADOPTED`, leave the query as the CLI's
honest passthrough and explain why. This is what keeps the migration trustworthy.

## Workflow

### Step 1 — Deterministic migration (always first)

Run the CLI. It parses every panel, emits SigNoz v5, live-verifies what it can prove, and writes an
evidence report. Nothing you do replaces this step.

```
promcast grafana <dashboard.json> \
  --target "$SIGNOZ_URL" --api-key-file <key> --source-namespace <estate-id> \
  --var <name=value> ... \
  --out out/
```

### Step 2 — Read the report, target only the residual

Read `out/<name>.report.json`. Its structure is documented in `references/report-schema.md`. Work
**only** the queries whose `verdict` is `needs_review` or `passthrough` AND that reference a metric
that exists on the target (skip `needs_review` caused by missing metrics, non-Prometheus datasources,
or Grafana expressions — those are not yours to fix). Ignore everything already `native`.

### Step 3 — Propose a Builder candidate for each residual query

For each residual query, read its `original` (the source PromQL) and write a candidate SigNoz Builder
query as JSON. Use the mapping in `references/promql-to-builder.md` and heed the equivalence limits in
`references/gotchas.md` — especially: SigNoz Builder rate/increase is not numerically identical to
PromQL rate, so many rate panels will legitimately fail verification and should stay passthrough.

Write each candidate to a file, for example `candidate.json`:

```json
{ "builder": { "name": "A", "metricName": "node_memory_MemTotal_bytes",
  "timeAggregation": "latest", "spaceAggregation": "sum", "stepSeconds": 60 } }
```

### Step 4 — Verify the candidate live (mandatory gate)

```
promcast verify \
  --source '<the emitted PromQL from the report>' \
  --candidate candidate.json \
  --target "$SIGNOZ_URL" --api-key-file <key> \
  --var <name=value> ... \
  --fidelity 0.05
```

For `--source`, use the query's **emitted `promql` from the report**, not the
Grafana `original`. On a target whose resource labels are remapped (Prometheus
`job`/`instance` → `service.name`/`service.instance.id`), the original's label
matchers select nothing and every verify returns `no_data`; the emitted PromQL
carries the remapped, correctly quoted matchers.

Read the result: `ADOPTED` with a fidelity band (`exact`, `within_1pct`, `within_5pct`) means the
candidate reproduces the source within tolerance and is safe to adopt. `REJECTED` (`phase_shift`,
`diverged`, `series_mismatch`, `no_data`) means do not adopt it — leave the query as passthrough.
`phase_shift` specifically means the candidate matched in magnitude but was offset one step in time
(common for `latest` on a moving gauge); it will never adopt, and no fidelity widening fixes it.
Use `--json` if you want the machine-readable band and `maxRelativeError`.

### Step 5 — Record only adopted candidates, then re-emit deterministically

Append each ADOPTED candidate to `overrides.yaml`, keyed by the query's `sourcePath` from the report:

```yaml
overrides:
  - sourcePath: /panels/2/targets/0
    builder:
      name: A
      metricName: node_memory_MemTotal_bytes
      timeAggregation: latest
      spaceAggregation: sum
      stepSeconds: 60
```

Then re-run the migration with the overrides. The CLI re-verifies every override live and only emits
it natively if it still passes, so the final dashboard is reproducible and honest:

```
promcast grafana <dashboard.json> \
  --target "$SIGNOZ_URL" --api-key-file <key> --source-namespace <estate-id> \
  --var <name=value> ... --overrides overrides.yaml --out out/
```

### Step 6 — Report the residual honestly

Tell the user: how many queries were native before your help, how many you raised to native via
verified overrides, and which queries remain passthrough or needs-review and **why** (e.g. "rate
panels: SigNoz Builder rate is not equivalent within tolerance", "metric absent on target",
"Grafana expression"). Never claim a query converted that `verify` did not adopt.

## What you must not do

- Do not write SigNoz dashboard JSON directly — always go through the CLI and overrides.
- Do not adopt a candidate that `verify` did not report `ADOPTED`.
- Do not touch queries already `native`, or `needs_review` queries whose reason is a missing metric,
  a non-Prometheus datasource, or a Grafana expression.
- Do not loosen `--fidelity` past what the user approves to force an adoption.

## References

- `references/report-schema.md` — the fields of `out/<name>.report.json`.
- `references/promql-to-builder.md` — how PromQL shapes map to SigNoz Builder queries.
- `references/gotchas.md` — equivalence limits: rate algorithm, dot-metrics, temporality, matching.
