# Corpus replay

The full regression corpus is intentionally not redistributed inside this
repository because its dashboards and rule files come from projects with their
own licenses. Tests consume a separately assembled, frozen fixture directory
through `PROMCAST_RESEARCH_ROOT`.

The current baseline contains:

- 151 Grafana dashboards;
- 3,186 recursive panels and 4,973 query targets;
- 51,745 inventoried source-only dashboard, variable, visualization, and query features;
- 295 alerting rules and 250 recording rules.

All 17 rule files pass the pinned Prometheus Go `rulefmt` v0.311.3 input
contract before translation. Enabling that upstream validation changed no
frozen rule count; malformed syntax is rejected before it can enter the
classification baseline.

The metadata-free dashboard translation baseline is 0 native, 73 passthrough, and 4,900
needs-review query decisions with zero PromQL parse errors. Zero offline native
decisions is deliberate. Target metric type, temporality, monotonicity, and
attributes are required to construct qualified Builder/formula candidates, and
promotion to native emission additionally requires the live differential (see
[guarantees.md](guarantees.md)); neither is available offline. The rules baseline emits 244 alert payloads as
disabled candidates, retains 51 additional alert records whose PromQL cannot
be executed safely, and leaves all 545 alert or recording-rule records in
explicit review. Of the alerting rules, 264 carry
`PROMETHEUS_FOR_WINDOW_APPROXIMATION` and 31 carry
`ALERT_IMMEDIATE_WINDOW_APPROXIMATION` (including explicit zero durations).
This fail-closed baseline reflects the real state-machine boundary:
SigNoz rolling range evaluation is not Prometheus pending-state evaluation.
All 295 alert candidates also carry `TARGET_ALERT_RUNTIME_LABELS`, accounting
for pinned SigNoz v0.133's target-owned `alertname`, `threshold.name`,
`ruleId`, `ruleSource`, and conditional `nodata` label/fingerprint effects.
Minimum-point settings harden eligible candidates but do not enable them.
Alerts also inherit every PromQL-relevant query review decision and remain
disabled until a maintainer reviews the complete candidate. Builder-engine-only
reasons are still omitted from PromQL alert payloads because those payloads do
not execute Builder; the state-machine approximation independently keeps every
alert in review.

The dashboard gate also freezes 2 Grafana expression targets, 502 instant
targets, 2,108 interval-controlled targets, 359 non-default query-format
targets (338 `table` and 21 `heatmap`), and 6,368 target-level configuration
features. Those include 1,084 explicit `step` values, 656 `range` values, and
555 `exemplar` values; the remaining fields are preserved as exact raw-config
review evidence. It also freezes 3,007 review panels and 1,978 deliberately
omitted panels. Of those omissions, 297 panels (879 queries) require a
target-specific rewrite that cannot be applied through a grammar-changing
runtime variable, 384 panels (811 queries and 30 directly unresolved variables)
use an All selection outside the only proven matcher-removal form, and 1,299
panels are omitted for other reasons. The increase is intentional: variables
without a nonblank current selection no longer allow either frontend to choose
an unvalidated load-time default. Two panels occur in both the dynamic and All
categories. Only an explicit `.*` All value used as every complete positive
regex matcher remains executable; default option-list All and negative,
equality, or partial uses are omitted. Vector matching is independently pinned at 281 executable
PromQL rewrites and 128 fail-closed unresolved shapes. Every one of the
151 reports must pass raw-source reconciliation.

Run it with an absolute path:

```sh
PROMCAST_RESEARCH_ROOT=/absolute/path/to/fixtures make test-corpus
```

Exact counts are assertions in the corpus tests. A compatibility change must
update the relevant assertion and explain why the changed classification is
safer; merely preserving a total is not sufficient.

The 51,745-feature baseline includes 1,970 query-legend semantics, 765 panel
descriptions, and 66 variable-bearing panel titles in addition to field-config properties outside
`defaults` and `overrides`, non-emitted y-axis properties and secondary axis
formats, transformation options, nested annotation settings, container
configuration, structured variable state, datasource extensions, and one
additional explicit barchart-to-graph visualization downgrade. Unknown
properties are counted by presence even when their value is `null`, `{}`, or
`[]`; those values are source evidence rather than absence.

The fixture manifest pins upstream revisions for Grafana Mimir, Loki, Thanos,
Istio, Strimzi, CockroachDB, Karpenter, kube-prometheus, and public Grafana.com
dashboard revisions. Keep that manifest beside the external fixture directory.

An additive, repository-local audit suite covers upstream Grafana 10/11
dashboard models without changing these frozen totals. Its Apache-vendored and
AGPL hash-fetch provenance boundary is documented in
[ui-authored-grafana-corpus.md](ui-authored-grafana-corpus.md).
