# Security policy

Please report a suspected vulnerability privately to the project maintainer.
Do not open a public issue containing API keys, tenant data, exploit details, or
the address of a reachable SigNoz deployment.

The CLI accepts SigNoz keys through `SIGNOZ_API_KEY`, `--api-key`, or
`--api-key-file`. Prefer a mode-restricted key file for automation. Generated
payloads and reports do not include the key.

Credential-bearing non-loopback URLs require HTTPS. `--allow-insecure-http`
is an explicit acknowledgement for an isolated private test network; it is
recorded in evidence and must not be used across an untrusted network. Human
terminal output neutralizes control and formatting characters from upstream
content, while machine-readable JSON preserves the original text.

Generated artifacts are mode `0600` and MCP migration directories are mode
`0700`. MCP path reads use descriptor-backed root confinement, reject root
replacement and manifest identity mismatches, and never reopen a path after
verifying its bound hash.

MCP output is admitted under configurable total-entry and logical-byte quotas.
Accounting includes existing content and validation artifacts, refuses
symlinks and special files, and does not auto-delete evidence. Admission is
serialized within one MCP server process; using the same output root from
another process or an external writer is unsupported because the quota is not
a distributed lock. Invalid, non-positive, or out-of-range quota flags and
environment values abort startup instead of falling back to defaults.

The scratch container runs as `65532:65532` and sets `TMPDIR` to a private
mode-`0700` directory owned by that user. MCP dashboard and validation staging
must succeed without root privileges or a writable filesystem-global `/tmp`.

Before sharing evidence artifacts, review source expressions, dashboard titles,
labels, and endpoint URLs for organization-specific information. The reference
AWS topology uses security-group-only east/west traffic over private addresses,
no public ingress rules, IMDSv2, encrypted volumes, and Systems Manager instead
of public SSH. The reference subnet assigned public addresses, so the absence
of inbound permissions—not subnet classification—is the security boundary.
