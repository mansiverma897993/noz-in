# Migration guarantees

This page is the contract. Every claim the tool makes is one of the three
verdicts below, and one invariant governs all of them.

## The invariant

> **Nothing is emitted as `native` without passing a live differential against
> its own PromQL passthrough on the target — whether the Builder query was
> produced by a deterministic rule, proposed by a coding agent, or written by a
> human operator.**

All three sources flow through the same gate
(`internal/validate`). The gate compares the candidate Builder/formula result
with the verbatim PromQL result over the same window on the same target, series
by series and point by point, and rejects on magnitude divergence, series
mismatch, missing data, **and temporal phase shift** (see below). There is no
trusted path around it.

## The three verdicts

| Verdict | Claim | Guarantee |
|---|---|---|
| `native` | The emitted SigNoz Builder/formula query was proven numerically and temporally equivalent to the source PromQL on this target | Live differential passed within the operator's `--fidelity` tolerance; reason `NATIVE_DIFFERENTIAL_VERIFIED` records it |
| `passthrough` | The verbatim source PromQL is emitted and executed by SigNoz's PromQL engine | Faithful by construction — the query text is unchanged; SigNoz executes exactly what Grafana executed |
| `needs_review` | The query migrated as PromQL, and a semantic concern is recorded | Nothing was guessed; every concern is a documented [reason code](reason-codes.md) |

The important consequence: **100% of dashboards migrate faithfully**, because
passthrough is always available as the floor. `native` is a promotion above
that floor, never a precondition for a working panel.

"100% native" is not a goal and not claimed: some PromQL genuinely has no
Builder equivalent (per-timestamp `topk`, subqueries, `group_left`/`group_right`
vector matching, `predict_linear`, …). Those stay passthrough, and the panel
still renders correctly.

## What the differential actually proves

For each candidate the gate executes two requests against the live target —
the Builder envelope and the verbatim PromQL at the same step over the same
window — then requires:

- every Builder series pairs with a passthrough series by exact label identity;
- at least the minimum number of matched points;
- every matched point within the relative tolerance (`--fidelity`, default 5%)
  or absolute tolerance;
- **no temporal phase shift**: a series whose values fit the passthrough one
  step over markedly better than at the same timestamps (the signature of
  SigNoz's `latest` bucket labeling) is rejected with reason
  `BUILDER_TEMPORAL_PHASE_SHIFT`, even when every same-slot point is inside the
  magnitude tolerance. Constant series are unaffected. The two probes align to
  different epochs, so the check pairs points by nearest-within-half-a-step;
  the fuzzy tolerance is capped below half a step so it can never absorb a
  full-step offset.

The phase-shift check exists because a magnitude-only gate certifies
slow-moving gauges whose SigNoz rendering trails PromQL by one bucket — found
by adversarial audit and reproduced independently before being fixed. The full
account is in [deep-audit-findings.md](deep-audit-findings.md).

A passing differential is observed evidence for that query, that target, and
that window. It is re-established on every live run; overrides are re-verified
each time they are emitted.

## Fidelity bands (`verify` command)

| Band | Meaning | Adopts? |
|---|---|---|
| `exact` | Every matched point identical | yes |
| `within_1pct` | Max relative error ≤ 1% | yes |
| `within_5pct` | Max relative error ≤ 5% | if within `--fidelity` |
| `phase_shift` | Magnitude match but offset one step in time | **never** |
| `diverged` | Error beyond 5% on ≥ 1 point | never |
| `series_mismatch` | Different series sets | never |
| `no_data` | One or both probes returned nothing | never |
| `probe_failed` | Probe could not be constructed/executed | never |

## The three ways to use the tool

1. **CLI alone (deterministic).** `promcast grafana … --target …`
   migrates everything, promotes what it can prove, and ships the rest as
   passthrough with reason codes. Reproducible in CI; no agent, no LLM, no
   trust required.
2. **CLI + human overrides.** An operator writes Builder queries for the
   panels they care about in `overrides.yaml`; each is verified live before it
   can be emitted natively (`OPERATOR_OVERRIDE` + differential).
3. **CLI + coding agent.** The bundled skill
   (`skills/promcast-assist/`) lets any coding agent propose Builder
   queries for the residual. The agent is never trusted: only proposals that
   pass `promcast verify` are adopted. A wrong proposal cannot enter the
   output — the worst case is a query that stays passthrough, which is still
   correct.

## What is deliberately not claimed

- No equivalence claim for `rate()`/`increase()` shapes: SigNoz Builder rate is
  a reset-aware per-step delta without PromQL's boundary extrapolation, so it
  genuinely differs on volatile counters. These stay passthrough unless a
  specific series proves equivalent live.
- Preview success and data presence are distinct from numerical equivalence;
  only an attached differential comparison asserts observed values.
- A passing run does not prove future runs: metric shape, temporality, and
  target versions can change. The evidence report records the exact window and
  tolerances of every claim.
