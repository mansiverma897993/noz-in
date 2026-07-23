# noz-in

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/mansiverma897993/noz-in.svg)](https://pkg.go.dev/github.com/mansiverma897993/noz-in)

> **Migrate every Grafana dashboard to SigNoz immediately through safe PromQL
> passthrough — then automatically promote only the queries proven equivalent
> to native SigNoz Builder queries.**

**noz-in** is a deterministic query-compatibility and migration engine that
moves observability estates *into* SigNoz. It ships as the **`promcast`** CLI,
which converts Grafana dashboards and Prometheus alerting rules into SigNoz
artifacts, validates the exact target queries against live SigNoz APIs, and
explains every compatibility decision in JSON and self-contained HTML.

This is an independent community project, not affiliated with or endorsed by
SigNoz, Inc. The code is organized so proven adapters or compatibility rules
can be proposed to SigNoz later without coupling its core model to Grafana
internals.

## How it works

Every migration rests on one floor and one invariant:

- **The floor:** every query always migrates. When a PromQL query cannot be
  proven equivalent to a SigNoz Builder query, the verbatim PromQL is emitted
  and SigNoz executes it natively. 100% of dashboards come out the other side
  rendering, with every decision explained by a
  [reason code](docs/reason-codes.md).
- **The invariant:** nothing is emitted as `native` (a SigNoz Builder query,
  which restores drilldown and click-to-filter) without passing a live
  differential against its own PromQL passthrough on the target — including a
  temporal phase-shift check. That gate applies equally to deterministic rules,
  operator overrides, and agent proposals.

The full contract — verdicts, fidelity bands, and what is deliberately not
claimed — is in [docs/guarantees.md](docs/guarantees.md). How we know the gate
is honest (an adversarial live audit that found and fixed a real temporal
defect) is in [docs/deep-audit-findings.md](docs/deep-audit-findings.md).
How the transpiler turns PromQL ASTs into Builder queries — and how the
bundled agent skill layers on top of it for the most accurate conversion —
is illustrated step by step in [docs/transpiler.md](docs/transpiler.md).

## What it handles

| Source | Target | Behavior |
|---|---|---|
| Grafana dashboard JSON | SigNoz dashboard v5 | Recursive rows, layouts, variables, visualizations, canonical PromQL execution, and live-verified Builder/formula promotion |
| Prometheus rule YAML | SigNoz alert-rule candidates | Pinned Prometheus `rulefmt` validation, `PrometheusRule`, Kubernetes lists, multi-document YAML, threshold extraction, fail-closed group semantics, and safe recording-rule inlining |
| Prometheus and SigNoz APIs | Differential evidence | Label-aware series matching, timestamp alignment, explicit tolerances, empty-result classification, and non-finite samples |

Dashboard reconciliation compares an inventory taken from the raw export with
the parsed panels, targets, variables, and source-only features. Unsupported
behavior is retained as an explicit review record or a deliberate omission,
never counted as a successful conversion. Repeated live runs reconcile through
stable identities: dashboards use source-namespaced UUIDs and alerts use a
source-namespaced `promcast_id` label.

## Install

Go 1.25 or newer is required.

```sh
go install github.com/mansiverma897993/noz-in/cmd/promcast@latest
promcast version
```

From source: `make build`, then `./bin/promcast version`. Tagged releases
produce checksum-protected Linux, macOS, and Windows archives for amd64 and
arm64. A container image is also available:

```sh
docker build -t promcast:local .
docker run --rm -v "$PWD:/workspace" promcast:local \
  grafana dashboard.json --offline --out out
```

## Migrate a dashboard

An offline run performs no network access:

```sh
promcast grafana node-exporter-full.json --offline --out out
```

A live run resolves target metric metadata, previews and executes every enabled
query, promotes provably-equivalent Builder candidates to native, then creates
or updates the dashboard only when preflight succeeds:

```sh
promcast grafana node-exporter-full.json \
  --target http://localhost:8080 \
  --api-key-file /run/secrets/signoz-api-key \
  --source-namespace grafana:production \
  --var job=node-exporter \
  --var node=source-node \
  --rules rules/*.yaml \
  --out out
```

Use `--dry-run` with a live target to perform metadata, preview, and execution
checks without importing. `SIGNOZ_URL`, `SIGNOZ_API_KEY`, and
`PROMCAST_OUT` provide automation-friendly defaults.

Credential-bearing non-loopback endpoints require HTTPS by default. For an
isolated private test network that intentionally uses plaintext HTTP, pass
`--allow-insecure-http`; the acknowledgement is recorded in evidence. Loopback
development endpoints remain available without the flag.

Set `--source-namespace` (or `PROMCAST_SOURCE_NAMESPACE`) to a stable
identifier for the source Grafana organization or observability estate. Grafana
UIDs are scoped to an organization, so this prevents two source organizations
that reuse a UID from overwriting one SigNoz dashboard. For a UID-less export,
`--source-identity` preserves the same target across input-path moves and title
edits.

If a target stores a different OpenTelemetry metric name, provide a
deterministic YAML map and pass it with `--metric-name-map metrics.yaml`:

```yaml
http_requests_total: http.server.request.count
```

## Raising the native rate

A live run promotes a Builder or formula candidate to native only when it is
confirmed numerically and temporally equivalent to its own PromQL passthrough
on the target, within `--fidelity` (default 5%). Anything that cannot be proven
ships as honest passthrough. Coverage is metric-shape dependent: stable gauges,
exact aggregations, and `agg(*_over_time(m[range]))` convert; rate and
histogram shapes often differ from PromQL and stay passthrough; slow-moving
`latest` gauges that match in magnitude but trail one step in time are rejected
with `BUILDER_TEMPORAL_PHASE_SHIFT`.

To raise coverage further without weakening that guarantee, a human or a coding
agent can propose Builder queries for the residual and verify each one live
before it is adopted:

```sh
# 1) verify a single proposed candidate against the source PromQL
promcast verify \
  --source 'sum(rate(http_requests_total[5m]))' \
  --candidate candidate.json \
  --target http://localhost:8080 --api-key-file /run/secrets/signoz-api-key \
  --fidelity 0.05

# 2) record adopted candidates in overrides.yaml, then re-emit; each override is
#    re-verified live and only emitted natively if it still passes
promcast grafana dashboard.json \
  --target http://localhost:8080 --api-key-file /run/secrets/signoz-api-key \
  --source-namespace grafana:production \
  --overrides overrides.yaml --out out
```

The `verify` command prints a fidelity band (`exact`, `within_1pct`,
`within_5pct`) with the measured maximum relative error, and exits non-zero for
`phase_shift`, `diverged`, `series_mismatch`, or `no_data`. The bundled
[Agent Skill](skills/promcast-assist/) drives this loop for any coding
agent; the CLI remains the sole authority on whether a conversion is correct.
The full propose → verify → adopt sequence is diagrammed in
[docs/transpiler.md](docs/transpiler.md).

## Emit the SigNoz v6 (Perses) shape

Add `--emit-v6` to also write the SigNoz v6 (Perses) dashboard shape as a
sibling `<base>.v6.json`, transformed from the verified v5 output for the v2
dashboard API. The v5 file remains the verified primary import target;
reconcile the v6 plugin-kind strings against the pinned SigNoz release before
importing v6.

## Migrate rules

```sh
promcast rules rules/*.yaml \
  --target http://localhost:8080 \
  --api-key-file /run/secrets/signoz-api-key \
  --source-namespace prometheus:production \
  --out out
```

Alert candidates are translated conservatively: thresholds are extracted,
Prometheus `for` semantics are approximated with an explicit reason, unsafe
shapes fail closed into disabled review, and identity collisions are rejected
before any target write. The full contract — validation, disabled-candidate
safety on pinned SigNoz v0.133, identity rules, and template rewriting — is in
[docs/rules.md](docs/rules.md).

## Compare source and target data

```sh
promcast diff node-exporter-full.json \
  --source http://prometheus:9090 \
  --target http://signoz:8080 \
  --target-provenance otel_prometheus_receiver \
  --api-key-file /run/secrets/signoz-api-key \
  --source-var node=source-node \
  --target-var node=source-node \
  --range 15m --step 1m \
  --migration-report out/node-exporter-full.report.json \
  --out differential-report.json
```

The comparator maps Prometheus resource labels, requires exact logical label
identity, aligns the nearest samples within a bounded skew, and reports
coverage and absolute/relative error separately. `--migration-report` attaches
each measured comparison to the matching query record fail-closed: the exact
primary artifact is reloaded and verified by size and SHA-256 before anything
is attached. Preview success, data presence, and numerical equivalence remain
distinct claims.

## Reports, exit codes, automation

Dashboard runs write:

- `<name>.signoz.json` — importable SigNoz v5 dashboard;
- `<name>.report.json` — stable machine-readable evidence;
- `<name>.report.html` — self-contained review report with the review queue first.

Rule runs write equivalent payload, JSON report, and HTML report files.
Regenerate HTML from stored JSON with `promcast report <report.json>`.
Use `--json` for newline-delimited records on stdout.

| Exit code | Meaning |
|---|---|
| `0` | Clean run |
| `2` | Completed run with review items (passthrough alone is not an error) |
| `3` | Invalid input |
| `4` | Target or authentication failure |
| `1` | Internal failure |

The stable filenames are facades over hidden immutable artifact generations
with crash-safe publication; the durability, retention, and import-concurrency
contracts are in [docs/operations.md](docs/operations.md).

## MCP server

The MCP adapter exposes `migrate_dashboard`, `explain_verdict`, and
`validate_queries` over the same application layer as the CLI:

```sh
promcast mcp --transport stdio --root /workspace --out /workspace/out
```

Credentials are server configuration rather than tool arguments; HTTP mode is
loopback-only with a bearer token. Quota, crash recovery, and the container
smoke test are documented in [docs/mcp.md](docs/mcp.md).

## Agent skill: promcast-assist

[`skills/promcast-assist`](skills/promcast-assist/) is a packaged Agent Skill
that any coding agent (Claude Code, or anything that reads `SKILL.md`) can load
to run migrations conversationally and raise the native-conversion rate safely.
The division of labor is strict and is what keeps agent involvement
trustworthy:

1. **Deterministic first.** The agent always runs the `promcast` CLI, which
   migrates everything and live-verifies what it can prove. This step alone
   yields a complete, rendering dashboard.
2. **Agent proposes, never decides.** For queries the CLI left as passthrough,
   the agent proposes Builder candidates using the bundled reference material
   ([PromQL→Builder mapping](skills/promcast-assist/references/promql-to-builder.md),
   [gotchas](skills/promcast-assist/references/gotchas.md),
   [report schema](skills/promcast-assist/references/report-schema.md)).
3. **Every proposal passes the same live gate.** `promcast verify` executes the
   candidate and the source PromQL against the live target and only reports
   `ADOPTED` inside the fidelity tolerance — a hallucinated query cannot pass a
   numeric differential it never satisfies.
4. **Adoption is re-verified.** Adopted overrides go into `overrides.yaml`, and
   re-emitting re-verifies each one live before it is written as `native`.

The skill requires the `promcast` binary on `PATH` and a reachable SigNoz URL
with an API key. The full propose → verify → adopt sequence is diagrammed in
[docs/transpiler.md](docs/transpiler.md).

## Evidence

The frozen corpus currently asserts 151 dashboards, 3,186 recursive panels,
4,973 queries, 51,745 inventoried source-only dashboard, variable,
visualization, and query features, 244 disabled alert-rule candidates, 51
additional alert records without a safe executable payload, and 250 recording
rules retained for review. In the deliberately metadata-free offline gate,
query decisions are 0 native, 73 passthrough, and 4,900 needs-review with zero
parser errors — offline runs cannot promote, because promotion requires the
live differential. See [docs/corpus.md](docs/corpus.md) for provenance and
replay details.

Correctness of the promotion gate itself was adversarially audited against a
live SigNoz deployment across 23 query shapes, 13 panel types, a 140-panel
real-world dashboard, and 41 kube-prometheus alerts; the audit found one real
defect (a temporal phase shift invisible to magnitude-only comparison), which
was fixed and re-proven live. The method and results are in
[docs/deep-audit-findings.md](docs/deep-audit-findings.md).

The reference topology uses separate nodes for the Prometheus/Grafana source
and SigNoz destination; versions and reproduction steps are in
[VERSIONS.md](VERSIONS.md) and [deploy/README.md](deploy/README.md). A passing
comparison is observed evidence for that query and fixed window; it does not
auto-promote a candidate or prove all inputs.

## Design and development

Read [docs/architecture.md](docs/architecture.md) (including the package map
and suggested reading order), [docs/guarantees.md](docs/guarantees.md),
[docs/compatibility.md](docs/compatibility.md), and
[docs/reason-codes.md](docs/reason-codes.md) before changing translation
behavior. Observed SigNoz API behavior is recorded separately in
[docs/integration-findings.md](docs/integration-findings.md) so a future SigNoz
change has one place to update.

```sh
make fmt vet test-race lint build
```

`make gold` is the reproducible local gate (with
`PROMCAST_RESEARCH_ROOT` pointing at the external corpus; corpus tests
skip cleanly when it is unset) and does not fetch upstream Grafana payloads.
`make test-upstream-fixtures` is the explicit online gate for the hash-pinned
official Grafana fixtures. `make gold-online` runs both. Tagged publication
additionally requires `make release-gold`; see
[docs/releasing.md](docs/releasing.md).

The contribution checklist requires focused fixtures, compatibility evidence,
and reason-code documentation for behavior changes; see
[CONTRIBUTING.md](CONTRIBUTING.md). Generated credentials, deployment
inventory, and live tenant data must never enter the project.

Licensed under the Apache License 2.0. SigNoz is a trademark of SigNoz, Inc.
