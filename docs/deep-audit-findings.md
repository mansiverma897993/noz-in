# Adversarial audit findings

An adversarial live audit of the native-promotion gate, run against
a real SigNoz deployment with live `node_exporter` metrics: every query shape,
every panel type, a 140-panel real-world dashboard, and 41 kube-prometheus
alerts. It found one real defect — a temporal phase shift invisible to
magnitude-only comparison — which is fixed and re-proven live below.

## Result

The workflow was honest and safe across everything tested: no crashes, no
silently dropped queries, and every emitted `native` was numerically correct on
the test data, independently re-verified through a separate query path (raw v5
API, independent value extraction — not the tool's own differential). But the
`native` certification proved *magnitude* equivalence only, not *temporal*
equivalence.

## Coverage evidence

**23 query shapes**, all accounted for, each verdict independently checked:

| Shape | Verdict | Independently correct? |
|---|---|---|
| gauge sum (constant) | native | ✓ exact |
| regex filter (count) | native | ✓ exact, regex anchored `^(?:…)$` |
| count | native | ✓ exact |
| scalar math (formula) | native → **review after fix** | phase-shifted (see defect) |
| gauge avg / groupby (moving) | needs_review | ✓ |
| rate / increase / rate-formula | needs_review | ✓ Builder rate ≠ PromQL rate |
| `*_over_time` family | needs_review | ✓ candidates recognised, unproven |
| histogram_quantile, filters, var filter, offset | needs_review | ✓ |
| topk, subquery, group_left, without, quantile agg, nonstandard quantile | passthrough | ✓ genuinely non-convertible |

**13 panel types**: every graphable panel emitted Builder or PromQL
passthrough; `row`/`text` correctly omitted as structural. Grafana schema v14
parses and migrates.

**Node Exporter Full (140 panels)**: 140/140 accounted for; all 161 native
candidates correctly *refused* promotion with "probe returned no data" because
the dashboard's `$job`/`$instance` filters did not match the live series — the
no-data → no-native fail-safe working as designed.

**41 kube-prometheus alerts**: all conservatively held at review with precise
reasons (recording rules, `predict_linear` → `UNSUPPORTED_FUNCTION`, for-window
approximation). The tool also detected that `NodeFilesystemSpaceFillingUp`
warning + critical share an alert name and refused to mint colliding IDs until
disambiguated.

## The defect — `latest` ≠ PromQL instant

SigNoz Builder `latest` buckets time as `toStartOfInterval(step)` taking the
newest sample in the bucket, **labeled at the bucket start**. PromQL evaluates
at the step boundary. Every moving gauge is therefore offset one step.
Reproduced independently:

```
builder[t] == promql[t+60s]   for 10/10 points   (0/10 match same-slot)
```

The pre-fix gate compared `builder[t]` vs `passthrough[t]` with a magnitude
tolerance (5%) and a 60s fuzzy timestamp match. On a slow gauge, one bucket of
drift is a sub-tolerance magnitude change (~0.006% for `MemAvailable`), so a
phase-shifted pair passed. Impact bounds:

- constant gauges: genuinely exact, unaffected;
- fast gauges: exceed magnitude tolerance and were already rejected;
- the masked band: gauges that move slowly enough to stay under tolerance per
  step but whose rendering trails PromQL by one bucket — visible when
  co-plotted with a PromQL panel or when alert timing must line up.

The defect also explained an inconsistency: structurally identical queries got
different verdicts depending only on whether the metric happened to be flat
during the audit window. The verdict tracked "was the data flat," not "is the
translation correct."

## The fix (shipped, `internal/validate/promote_compare.go`)

- **Phase-aware differential.** `seriesPhaseShifted` compares each Builder
  point against its passthrough counterpart at the same slot and one step
  either side. If the shifted fit is an order of magnitude better and the
  same-slot error is real (not floating-point noise), the pair is a temporal
  offset: `equivalent` is forced false and the translation is annotated
  `BUILDER_TEMPORAL_PHASE_SHIFT`. Constant and truly aligned series are never
  flagged.
- **Nearest-match with a capped tolerance.** The two probes align to different
  epochs (Builder to the wall clock, PromQL to the window start), so their
  grids sit a sub-step apart and never share exact timestamps. Both the
  magnitude and phase checks pair points by nearest-within-half-a-step, capped
  below half a step so the fuzzy match can never absorb a full-step offset.
  (A first implementation used exact-timestamp matching, silently found zero
  overlap on real data, and was caught only by live re-verification —
  regression test: `TestCompareRejectsPhaseShiftAcrossSubStepGrids`.)
- **New surfaces.** Reason code `BUILDER_TEMPORAL_PHASE_SHIFT`; `verify`
  fidelity band `phase_shift`, which never adopts.

## Post-fix live re-run (same target)

- `verify sum(node_memory_MemAvailable_bytes)` (moving) → `phase_shift`,
  `pass:false`; `verify sum(node_memory_MemTotal_bytes)` (constant) → `exact`,
  `pass:true`.
- Shape matrix: 4 native → **3 native**. The constant/count natives stay native
  and still independently reproduce exactly; the moving-gauge formula drops to
  review with the precise phase-shift reason. The flat-data inconsistency is
  gone: constant gauges promote, moving `latest` gauges do not.

`native` now means numerically **and temporally** equivalent — see
[guarantees.md](guarantees.md).
