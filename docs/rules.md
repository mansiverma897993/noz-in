# Rule migration contract

`promcast rules` converts Prometheus alerting and recording rules into
SigNoz alert-rule candidates. This page holds the full contract; the README
carries only the quickstart.

```sh
promcast rules rules/*.yaml \
  --target http://localhost:8080 \
  --api-key-file /run/secrets/signoz-api-key \
  --source-namespace prometheus:production \
  --out out
```

## Translation semantics

One numeric root comparison is separated into a query and SigNoz threshold.
Group labels are applied first and rule labels override them, matching
Prometheus. The original group and rule maps remain separate in evidence.
Prometheus `for` and immediate state transitions do not have an exact SigNoz
equivalent, so alert candidates are emitted disabled with an explicit
approximation reason; minimum-point settings reduce early firing but are not
treated as equivalence proof. Nonzero group `query_offset`, a positive `limit`,
and a valid positive group interval longer than the candidate evaluation window
also fail closed into disabled review. Interval zero selects the Prometheus
global/default cadence, nonpositive limits mean no limit, and zero `for` or
`keep_firing_for` values retain their upstream no-op/immediate meanings.
Recording rules are inventoried and may be inlined into dashboards or alerts
only when expansion is unlabeled, unconstrained, and acyclic. Notification
routing remains delegated to SigNoz policies; the tool does not invent channel
identifiers.

## Disabled-candidate safety on SigNoz v0.133

Pinned SigNoz v0.133 starts a rule executor during `POST /api/v2/rules` even
when the request says `disabled: true`. The importer therefore never creates a
missing disabled candidate and never uses a create-then-disable sequence. It
records `not_created_disabled` with requested/attempted/succeeded set to
`true/false/false`; an all-skipped run is `review_only`, and the CLI exits 2 for
review rather than 4 for target failure. If the same managed rule already
exists, the importer may safely update it with `PUT`, whose v0.133 path honors
the disabled state before scheduling. The generated `*.signoz-rules.json` is a
review artifact, not a curl script: do not blindly POST its standalone objects
to SigNoz v0.133, because they may evaluate and notify despite being marked
disabled.

## Input validation

Each plain document, `PrometheusRule`, `List`, or `PrometheusRuleList` item is
unwrapped and validated with the pinned
`github.com/prometheus/prometheus/model/rulefmt` v0.311.3 contract before the
output directory is created or any target metadata request is sent. That
contract enforces nonempty object-scoped unique group names, exactly one of
`alert` or `record`, a present parseable expression, recording-rule field and
name restrictions, valid labels and annotations, and valid alert templates.
Syntactically invalid durations are input errors; defensive duration reason
codes remain only for programmatic model callers. Unknown group/rule keys,
duplicate literal keys in labels or annotations, empty objects, and typo-only
documents/items are also rejected. Document-scoped YAML anchors are
materialized before validating extracted `spec.groups`, including anchors
declared in Kubernetes metadata; recursive or bounded-expansion violations are
rejected. A multi-file invocation is an all-batch input preflight: one invalid
source prevents metadata requests, artifacts, output-directory creation, and
target writes for every sibling.

Kubernetes type metadata, rich object metadata, and a PrometheusRule `status`
object are accepted as non-semantic envelope data and intentionally omitted
from the normalized rule model; the source SHA-256 still binds their exact
bytes. This parser contract is distinct from the separately runtime-tested
Prometheus 3.5.0 service used by live acceptance.

## Identity and namespacing

For rule imports, `--source-namespace` identifies the logical rule collection,
not a checkout path. Namespaced rule IDs use the stable group and alert names,
so file moves and group/rule reordering update the existing target. If two
rules intentionally share both names, give each a distinct, persistent
`promcast_source_id` source label; ambiguous identities fail before any
target request or artifact publication. The value must already be canonical:
nonempty, valid UTF-8, free of control/formatting characters and surrounding
whitespace, and at most 1,024 bytes. Source alerts may not define generated
target labels `prometheus_alertname`, `prometheus_rule_group`, or
`promcast_id`. They also may not define `prometheus_severity` when a
noncanonical `severity` requires that preservation label; collisions are input
errors rather than silent overwrites. Pinned SigNoz v0.133 runtime keys
`alertname`, `threshold.name`, `ruleId`, `ruleSource`, and `nodata` are reserved
as well; every alert candidate records their unavoidable label/fingerprint
effect.

A live rule target, including `--dry-run`, requires `--source-namespace` before
source parsing, output creation, or network access. Offline rule review may use
the legacy path-derived identity, but that identity is nonportable across file
moves and must not be treated as an import plan.

## Labels and templates

Configured alert-label keys use the same receiver mapping as PromQL: `job`
becomes `service.name`, and `instance` becomes `service.instance.id`. Preflight
rejects configured or dynamic collisions instead of overwriting a source label.
Prometheus UTF-8 rule keys are accepted at source parsing, then checked against
SigNoz v0.133's narrower `[A-Za-z_][A-Za-z0-9_.]*` target contract.

Label and annotation templates rewrite `$value` plus the Prometheus
`$labels.foo`, `.Labels.foo`, and `index` label-access forms. Prometheus-only or
target-runtime-invalid actions are replaced with an explicit
`[unsupported Prometheus template omitted]` literal in the disabled candidate,
never left behind as a payload the SigNoz API would reject. Literal dollars
outside template actions are escaped from SigNoz's bare-variable preprocessor.

Use `--alert-on-absent` only when no-data is itself an incident condition; the
default preserves SigNoz's normal no-data behavior.
