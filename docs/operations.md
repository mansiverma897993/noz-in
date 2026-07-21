# Operational contracts

Durability, concurrency, and artifact-retention behavior for automation
operators. The README carries only the summary; this page is the contract.

## Import concurrency

Stable target IDs make sequential retries and updates idempotent after the
previous writer has stopped. They do not make concurrent imports atomic. Two
CLI or MCP processes — even on different nodes or with different output
directories — can race the target inventory and create/update requests.

Before every non-dry-run import, the operator must hold an external,
single-writer lease keyed by the canonical SigNoz target, source namespace, and
logical source identity. The identity is the Grafana UID (or explicit
`--source-identity`) for a dashboard and the stable group/rule identity for an
alert. A command that imports several identities must lock all of them in a
deterministic order; a coarser target-and-namespace lease is also safe. Hold the
lease through preflight, target mutation, and final evidence publication, and
retry only after the prior owner has conclusively stopped. `promcast`
does not acquire or validate a distributed lease.

## Artifact generations

The stable output filenames (`<name>.signoz.json`, `<name>.report.json`,
`<name>.report.html`) are compatibility facades over hidden immutable artifact
generations. The report is published last and binds its exact generation,
primary size, and SHA-256, so an interrupted final publication leaves either the
previous attempted checkpoint or the new final checkpoint readable. Hidden
storage is bounded to the pointer-bound current and recoverable previous
generations. A crash before pointer publication can leave one additional
fully-written orphan; the next writer validates and removes that orphan before
publishing, while multiple or malformed extras fail closed. Successful commits
prune all unreferenced generations only after the pointer, stable facades, and
report are durable. MCP relocation verifies every generation but copies and
charges quota only for current and previous. Archive an output snapshot before
an older generation falls outside that two-generation recovery window.

## Evidence field semantics

Dashboard evidence uses `dataPresentPercent` for the share of live-validation-
eligible queries that returned at least one result. Panel states ending in
`and-data-present` make the same bounded claim. Neither field asserts semantic
equivalence; only an attached differential comparison can provide observed
numerical evidence for its recorded window and tolerances.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Clean run |
| `2` | Completed run with review items (PromQL passthrough alone is not an error) |
| `3` | Invalid input |
| `4` | Target or authentication failure |
| `1` | Internal failure |
