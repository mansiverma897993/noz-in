# Architecture

`promcast` is a translation and verification pipeline, not a dashboard
JSON field copier. Source formats are parsed into a neutral model, compatibility
is decided once, and every downstream surface consumes the same decision.

```text
Grafana JSON ──> source adapter ──> neutral model ──> analyzer ──> SigNoz v5
Prometheus YAML ────────────────┘         │              │          │
                                         │              └─> report ├─> live APIs
Prometheus API ───────────────────────────┴─> differential compare <─┘
```

## Reading order for reviewers

To understand the system, read the packages in this order — each layer only
depends on the ones before it:

1. `internal/model` — the neutral vocabulary: dashboards, queries, rules,
   verdicts, and reason codes. Start with `verdict.go`.
2. `internal/source/grafana` — Grafana JSON in, neutral model out
   (`ingest.go` entry points).
3. `internal/transpile` — PromQL AST → Builder/formula candidates and the
   semantic reasons that hold them at review (`transpile.go`, `builder.go`).
4. `internal/validate` — the live gate: metadata/preview/execution checks and
   the native-promotion differential (`promote.go`, `promote_compare.go`,
   `verify.go`). This is where the guarantees in
   [guarantees.md](guarantees.md) are enforced.
5. `internal/target/signoz` — SigNoz payloads, API envelopes, probe requests,
   stable-ID upserts.
6. `internal/app` + `cmd/promcast` — orchestration and CLI surface.

Everything else is support: `internal/rules` (Prometheus rule translation),
`internal/diff` (source-vs-target series comparison), `internal/report`
(JSON/HTML evidence), `internal/target/perses` (v6 emitter),
`internal/mcpserver` (optional MCP adapter over the same app layer), and the
infrastructure packages below.

## Boundaries

- `internal/source` owns Grafana, Prometheus YAML, and Prometheus HTTP details.
- `internal/model` contains source-neutral dashboards, queries, rules, verdicts,
  and reason codes. Lint prevents it importing source or target adapters.
- `internal/transpile` parses PromQL, recognizes structurally representable
  Builder/formula candidates, performs deterministic label and metric rewrites,
  and records the semantic boundary that keeps those candidates on canonical
  PromQL execution.
- `internal/target/signoz` owns SigNoz v5 payloads, API envelopes, retries, and
  stable-ID dashboard/rule upserts. `internal/target/perses` derives the v6
  (Perses) shape from verified v5 output.
- `internal/validate` runs bounded-concurrency metadata, preview, and execution
  checks without containing translation rules, and owns the live
  native-promotion differential (magnitude, series identity, and temporal
  phase).
- `internal/diff` compares already-fetched series. It has no HTTP or dashboard
  parsing logic.
- `internal/app` orchestrates those packages and writes artifacts.
- `pkg/reporttypes` is the only public library package because reports are the
  stable integration surface for the CLI, MCP server, and other tooling.

Infrastructure packages, each single-purpose: `internal/artifactset`
(crash-safe artifact generations), `internal/artifactbind` (report-to-artifact
binding), `internal/atomicfile`, `internal/safeoutput` (rooted output-path
confinement), `internal/stableidentity` (namespaced target IDs),
`internal/transportpolicy` (HTTPS/loopback policy), `internal/httpdetail`,
`internal/metricmap`, `internal/releasegate` (release acceptance checks),
`internal/integration` (hostile-fixture and invariant tests),
`internal/migrate`, `internal/version`.

## Compatibility invariant

Every source panel, query, variable, alert, and recording rule must be present
in evidence. A native verdict requires a proven target representation. A
passthrough verdict means canonical PromQL is safer than Builder. A review
verdict preserves the source and explains the unresolved semantic constraint.

A Builder or formula object in a translation is candidate evidence, not an
execution decision. Candidate-engine reason codes force the panel to PromQL;
the report retains the candidate for future SigNoz work but counts the emitted
query as review, never native Builder. Live metadata can establish type
compatibility, and a differential can establish an observed result for one
window, but neither automatically promotes the candidate.

SigNoz widgets select one query mode. If any query in a panel needs PromQL, the
whole panel uses canonical PromQL so a mixed-mode widget cannot silently execute
different expressions from those in the report.

## Live validation

A target-backed dashboard run proceeds in this order:

1. resolve metric name, type, temporality, monotonicity, and attributes;
2. re-run translation using that metadata to qualify candidate structure and type;
3. preview the exact target request;
4. execute each enabled query and distinguish empty data from invalid data;
5. import the exact primary artifact only after validation; an isolated widget
   failure may be removed from that primary artifact when every remaining
   widget is independently valid, while the complete candidate artifact and
   rejected source paths remain in evidence. Non-isolatable failures block the
   entire target mutation. A `--dry-run` never imports either artifact.

The migration report binds that primary artifact by portable filename, byte
size, and SHA-256. A report-bound differential reloads and strictly decodes
those exact bytes instead of regenerating a candidate dashboard. It verifies
the stored widget/query mapping, excludes validation-pruned queries, restores
the original source paths, and uses the migration run's Grafana macro settings
independently of the comparison window. The attached evidence binds both
endpoints, every target request artifact, the window, tolerances, summary, and
the same primary artifact identity. Target ingestion provenance is never
inferred by the application: the default permits no target-only label, while
an explicit `--target-provenance otel_prometheus_receiver` operator assertion
is retained before its narrow receiver-label policy can apply.

Before any dashboard write, the JSON and HTML evidence paths are published with
an explicit pending outcome. The target response then replaces that preflight
record with requested/attempted/succeeded flags, action, dashboard ID, and any
error. This prevents an unwritable report destination from being discovered
only after SigNoz was already modified.

Dashboard and rule batches have an input-atomic preflight boundary. Every
source is read and parsed, assigned its final namespace and identity, and
checked for duplicate stable target IDs before the output directory is created
or a target request is made; rule inputs also complete their deterministic
translation preflight at this boundary. Every prospective stable and hidden
artifact path is checked against every dashboard, rule, API-key, and metric-map
file, including symbolic-link and hard-link aliases. One
malformed later input or unusable later destination therefore rejects the
whole batch without publishing or importing an earlier object. Target API
failures after that boundary retain their own durable outcome evidence;
dashboard batches may continue to later preflighted dashboards, while the rule
writer stops after the first target write failure.

Output roots are created from an identity-pinned existing ancestor. Every new
directory component is created relative to that handle and its parent entry is
flushed before the next component is used. Artifact-set locks and standalone
JSON/HTML/diff replacements likewise operate through pinned roots, so a parent
renamed or replaced by a symlink after preflight cannot redirect a lock,
temporary file, atomic rename, or directory-sync barrier.

Rule writes use two barriers. First, a complete locked-inventory plan records
every pending create, pending update, and disabled candidate that will not be
created. Immediately before each actual POST or PUT, a second generation marks
that rule attempted with an unknown outcome and carries forward every earlier
completed outcome. A crash or lost response can therefore leave conservative
`attempting_create`/`attempting_update` evidence, never a false claim that no
request was sent. Failure to publish either barrier prevents the next request.
Missing disabled candidates cross only the plan barrier and remain explicitly
unattempted because SigNoz v0.133 cannot create them safely.

The API client retries only safe reads, idempotent writes, and query previews on
429 or 5xx responses. Create-only POST requests are never blindly repeated.
Response bodies and source artifacts have explicit size limits.

## Idempotency

Dashboard UUIDs derive from a stable source namespace plus the Grafana UID.
UID-less dashboards use a separate stable source identity and never hash their
mutable title or contents into the target UUID.
Alert rules carry a `promcast_id` derived from the stable logical rule
collection namespace plus group and alert names. Mutable expressions,
filesystem paths, and positional source paths are deliberately excluded.
Duplicate names require an explicit, persistent `promcast_source_id`
source label and fail preflight otherwise. Repeating an edited migration
updates the same target objects, while identical upstream IDs from different
source estates do not collide.

The rule preflight owns the generated `prometheus_alertname`,
`prometheus_rule_group`, and `promcast_id` labels, plus
`prometheus_severity` when severity normalization needs to preserve the source
tier. A source collision is rejected instead of overwritten. Explicit
`promcast_source_id` values must already be nonempty, trimmed, bounded,
and safe stable-identity components.

This is sequential idempotency, not a distributed transaction. For each target
mutation, the single-writer key is the canonical SigNoz target endpoint, the
source namespace, and the logical source identity used to derive the stable
target ID. A caller that may overlap another CLI process, MCP server, node, or
automation run must acquire an external lease for that key before a non-dry-run
import and retain it through preflight, target mutation, and evidence
publication. Multi-object imports acquire every key in deterministic order or
use a coarser lease that covers the target and namespace. Once the previous
owner has conclusively stopped, a retry reconciles through the same stable IDs.
The application has no cross-process or cross-node lock and does not assert
atomicity for concurrent writers.

Artifact writes use deterministic names in CLI mode and isolated migration
directories in MCP mode. Every dashboard or rule artifact set is first written
as a complete immutable generation beneath a hidden, manifest-specific
directory. Each member is limited to 64 MiB and one generation to just under
256 MiB. Files and the generation directory are flushed before an atomic
current-pointer replacement can expose that generation. The stable primary,
candidate, HTML, and manifest facades are then refreshed, with the stable JSON
report replaced last. A report therefore never names a partially written
generation.

Committed readers resolve the generation declared by the report. A pointer and
report generation mismatch is a recoverable interrupted-facade state only when
the report names the pointer's hash-bound previous generation: the old
attempted report remains readable without permitting rollback to an arbitrary
older generation. When the pointer and report agree, all stable facades must
match the pointer and manifest, so an out-of-band edit still fails closed. Flat
artifact sets from older releases remain readable and are snapshotted before
their first update.

Retention is part of the same locked transaction. Before publishing a new
generation, the writer strictly decodes the pointer and fully verifies its
current and optional previous generation. A single fully valid unreferenced
generation is the mechanically unambiguous residue of an interrupted
pre-pointer publication and is removed before another generation is created.
More than one unreferenced generation, an invalid generation member, or a
malformed or unknown pointer fails closed instead of guessing from random
generation IDs or filesystem timestamps. After the new pointer, stable
facades, and report have all been flushed, the writer re-verifies them and
removes every generation not named by the pointer. Generation-container entry
changes are flushed after each removal on platforms that support directory
handle synchronization. Pruning first atomically renames a fully verified,
unreferenced generation to a distinct tombstone. A bounded ownership record
lists every removable regular file; cleanup removes only those members, keeps
the marker until last, and never recursively deletes an unverified directory.
An interruption after the rename or during deletion is therefore recoverable
on the next locked preflight instead of leaving a partial valid generation that
blocks all future commits. Generation and member enumeration reads at most one
entry beyond the valid topology before rejecting the tree; output-alias checks
reuse this closed inventory instead of recursively walking hidden storage.

Publication stages also use random nonces and an exact ownership record. Startup
cleanup scans a bounded number of root entries and removes only marker-bound
regular members from a strictly shaped stage. User files or directories that
merely collide with a stage prefix, including a correctly shaped name without
the matching record, are preserved.

An absent pointer is not treated as an empty history when the stable manifest
still exists. Before pruning in that state, the writer requires a valid bound
stable report, verifies every stable member against the manifest, and requires
an existing immutable copy of that generation to carry identical manifest
bytes. Missing, malformed, unbound, or mismatched stable reports fail before
any generation is removed. A true pre-artifact-set legacy output has no stable
manifest and therefore remains distinguishable from this interrupted state.

The steady-state bound is therefore the current generation plus the
hash-bound recoverable previous generation. Only an in-flight or interrupted
pre-pointer publication can temporarily add one validated orphan. Private MCP
relocation verifies the closed set including that one possible orphan, rejects
multiple or untrusted extras, and copies and charges quota only for the pointer-
bound current and previous directories. CLI users that need older forensic
history must archive a complete output snapshot before it falls outside this
two-generation window.

On Unix, already-opened directory handles provide persistence barriers for file
and directory renames. On Windows, each replacement file is flushed and rooted
renames remain confined, but Go cannot flush the containing directory handle;
the design guarantees process-crash consistency there, while sudden power-loss
durability remains subject to the filesystem and operating system.

## MCP

MCP is a protocol adapter over the same application layer. It exposes migration,
verdict explanation, and live revalidation; it does not contain an alternate
translator. Credentials are process configuration, never tool arguments. Local
path reads use Go's descriptor-backed `os.Root` confinement, stored manifests
reject traversal, and HTTP transport binds to loopback. HTTP MCP requests additionally require a
local bearer token, an exact loopback Host and same-origin browser Origin, and
pass bounded header, body, read-time, concurrency, and process-wide body-budget
gates. Stateless HTTP
disables long-lived GET streams so they cannot retain the request budget.
MCP calls require an explicit source namespace whenever the server has a
SigNoz target configured, including validation-only runs, because an inline
JSON payload cannot reveal its Grafana organization safely and its generated
target ID must match a later import. UID-less inline payloads also require an
explicit logical source identity.

Output retention uses a non-destructive admission quota. Before every rooted
directory or file publication, MCP re-accounts all files and directories below
the pinned output root without following symlinks. Logical file sizes are used,
so hard links and sparse files are counted conservatively. Migration
generations and repeated validation runs consume the same entry and byte
budgets. In-flight publication metadata and payload copies live under the one
reserved `.promcast-mcp-work-v1` root and consume those budgets too.
Every operation has a random 128-bit token, immutable plan, atomic phase name,
and exact bounded payload inventory. Raw rule inputs also carry persisted
SHA-256 and size bindings in migration-state schema v2. Schema-v1 migrations
remain readable by normal consumers; crash recovery fails closed only when a
legacy migration has rule inputs that cannot be integrity-bound. Work-plan v1
is accepted conservatively without guessing its old unrecorded temporary
location, while current work is written as plan v2. The renderer's private
temporary container is tied to the operation token and output-root identity;
its canonical parent is recorded in the plan before staging begins, rather than
being rediscovered from a possibly changed `TMPDIR`. The work root is removed
when empty, but interrupted output or private staging remains discoverable at
the next startup.

No migration directory becomes visible while translation or private copying is
incomplete. A non-import run is exposed by one rooted directory rename. An
import exposes the complete `attempt` generation and its root `migration.json`
before the dashboard request can start. The complete final generation is then
renamed into place with a prewritten `migration-result.json`; publishing the
root result pointer is a same-filesystem file rename and requires no new quota
admission after the target outcome is known. Validation uses the same rule: a
complete run directory, or the first complete `validations` container, appears
through one rooted rename.

Startup recovery enumerates at most 64 owned operations and 512 entries in any
payload. Each operation directory is identity-pinned across `Lstat`, rooted
open, and `Stat` before its contents are trusted. Recovery verifies an installed
attempted or final committed artifact set before retaining it, completes a
verified installed result-pointer rename, and removes only token-owned workspace
members named by the exact inventory. A pre-attempt payload, an unpublished
validation payload, or an inventory persisted before its payload directory was
created is reclaimed. An unknown entry, substituted directory, malformed
marker, symlink, special file, excessive inventory, or corrupt installed result
stops startup and is preserved for operator inspection.
Attempted evidence is never discarded after a target request may have started;
if the process died in flight or after a response but before final publication,
the durable attempt remains an explicit unknown outcome.

Before cleanup, recovery validates a legal matrix across plan kind, inventory
set, payload set, and phase. Migration and validation stages cannot be mixed,
and result publication requires both the initial inventory and an import plan.
After visible evidence has been verified, the phase is atomically renamed to
`cleaning`; payloads, inventories, plan, phase, and owner are then removed in a
restartable order with the owner last. A kill anywhere in that sequence resumes
cleanup instead of reclassifying committed evidence from whichever metadata
happened to remain.

Before relocating private staging, MCP verifies a closed inventory:
the exact report-bound stable facades, explicitly declared source inputs, the
hash-derived pointer and its current and optional previous generation roots,
and every manifest-listed immutable member in those roots. One verified crash
orphan is classified but not copied; multiple or untrusted extras fail closed.
Extra visible files, unrelated but valid-shaped hidden trees, stages, symlinks,
special files, and unmanifested nested members also fail closed. The exported
tree's entries, logical bytes, and publication ceiling are admitted before any
child is copied, then the published tree is reopened and verified again. The
process-local admission lock makes concurrent tools in one server fail before
crossing a limit; operators must not share an output root between multiple
server processes or external writers because there is no distributed
filesystem lock. Existing artifacts are never selected or deleted by the
server. The only automatic deletion is strictly inventoried tool-owned recovery
workspace; committed migrations, validation runs, and operator files are never
selected for eviction.

Credential-bearing non-loopback source and target URLs require HTTPS. The
explicit `--allow-insecure-http` acknowledgement exists for isolated private
test networks and is retained in generated evidence; it is never inferred from
RFC1918 addressing.

## Deliberate scope

The project emits the current SigNoz v5 dashboard contract. It does not invent a
v6 format, a LogQL-to-metrics conversion, notification channel identifiers, or
an LLM rewrite path without a verified SigNoz API contract. Recording rules are
inlined only when expansion is unconstrained, unlabeled, and acyclic; otherwise
they remain visible for review.

Terminal output is deliberately plain text and diagnostics are returned as
typed errors. There is no decorative color layer or unused log-level switch.
