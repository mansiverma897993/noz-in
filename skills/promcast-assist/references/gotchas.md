# Equivalence limits (why a candidate fails `verify`)

These are the reasons a well-formed Builder candidate legitimately fails verification. When you see
them, the right answer is to leave the query as honest passthrough, not to force an adoption.

## Rate/increase algorithm difference (the big one)

SigNoz Builder computes rate as a counter-reset-aware per-step delta (`lagInFrame`) with **no**
PromQL-style boundary extrapolation. PromQL `rate()`/`increase()` extrapolate at series edges. For
steady counters over a calm window they land within a few percent; for volatile counters (context
switches, network errors, interrupts) they diverge well past 5%. Result: most `rate()` panels on a
real infrastructure dashboard will not adopt, and that is correct. Do not widen `--fidelity` to make
them pass — the numbers really do differ.

## Gauges: latest vs instant, and the phase-shift rejection

A Builder gauge uses `latest` (the newest sample in each step bucket, labeled at the bucket start);
PromQL returns the value at the step timestamp. This has two distinct consequences:

- **Constant gauges** (memory total, filesystem size, counts of stable series) match exactly and
  adopt cleanly.
- **Moving gauges** are systematically offset one step: `builder[t] == promql[t+step]`. On a
  slow-moving gauge the magnitude difference is tiny, but `verify` detects the temporal offset and
  returns the `phase_shift` band — REJECTED, always. Do not retry with a wider `--fidelity`; the
  band is not a tolerance question. On a fast-moving gauge (load average) the same mismatch shows up
  as `diverged` instead. Either way the honest answer is passthrough.

## Temporality

Delta-temporality counters are invisible to the PromQL passthrough path but parameterized on the
Builder path. If a metric is delta, the Builder form may return data where the passthrough returns
none, producing `series_mismatch`. Trust `verify`.

## Dot-normalized metric names

Histogram components and some OTel-native metrics are stored with dot suffixes (`m.bucket`, `m.sum`,
`m.count`). If a candidate returns `no_data` or `series_mismatch`, try the dot-suffixed `metricName`.

## Resource-label remapping

Prometheus `job`/`instance` become `service.name`/`service.instance.id` under the OpenTelemetry
Prometheus receiver. Filter and group on the resource-attribute names, not the Prometheus names, or
the series will not match.

## Vector matching

`on()/ignoring()/group_left/right` joins have no exact Builder form. A formula can approximate simple
cases, but if `verify` reports `series_mismatch` or `diverged`, leave it as passthrough.

## The rule of thumb

`verify` is the oracle. If it says `ADOPTED`, adopt. If it says anything else, the deterministic
passthrough is the honest answer — say so in your summary and move on.
