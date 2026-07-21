# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

Initial public release candidate.

### Added

- Grafana dashboard JSON → SigNoz v5 dashboard migration with full source
  inventory reconciliation, stable source-namespaced identities, and idempotent
  live import.
- Prometheus rule YAML → SigNoz alert-rule candidates with pinned `rulefmt`
  validation, threshold extraction, fail-closed group semantics, and safe
  recording-rule inlining.
- Live native-promotion differential: Builder/formula candidates are emitted as
  `native` only after proving numerical **and temporal** equivalence to their
  own PromQL passthrough on the live target (`NATIVE_DIFFERENTIAL_VERIFIED`).
  Temporal phase shifts — magnitude-equivalent series offset one step in time,
  the signature of `latest` bucket labeling on moving gauges — are detected and
  rejected (`BUILDER_TEMPORAL_PHASE_SHIFT`, fidelity band `phase_shift`). See
  [docs/deep-audit-findings.md](docs/deep-audit-findings.md) for the audit that
  motivated this.
- `verify` command: live fidelity check for a single proposed Builder/formula
  candidate against source PromQL, with machine-readable bands.
- `--overrides` flow: operator- or agent-proposed Builder queries are
  re-verified live on every emission before they may ship natively.
- Agent Skill (`skills/promcast-assist/`) encoding the
  CLI-first → propose → verify → adopt loop for any coding agent.
- `--emit-v6`: sibling SigNoz v6 (Perses) dashboard shape derived from the
  verified v5 output.
- `diff` command: source-vs-target series comparison with label-aware matching,
  bounded skew alignment, and fail-closed report attachment.
- MCP server exposing `migrate_dashboard`, `explain_verdict`, and
  `validate_queries` over the same application layer, with bounded output
  quota and process-death-recoverable publication.
- Self-contained JSON + HTML evidence reports with reason-code taxonomy
  ([docs/reason-codes.md](docs/reason-codes.md)) and crash-safe artifact
  generations.
