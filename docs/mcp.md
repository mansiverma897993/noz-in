# MCP server contract

The MCP adapter uses the same migration application layer as the CLI. This
page holds the full operational contract; the README carries only the
quickstart.

```sh
promcast mcp --transport stdio --root /workspace --out /workspace/out
```

It provides `migrate_dashboard`, `explain_verdict`, and `validate_queries`, plus
the reason-code resource. Credentials are server configuration rather than tool
arguments. HTTP mode binds only to `127.0.0.1` and exposes `/mcp`, `/livez`, and
`/readyz`. `/mcp` is non-streaming and requires a bearer token; provide a token
of at least 32 characters through `SIGNOZ_MCP_HTTP_TOKEN` or
`--http-token-file`. If neither is set, the server generates a token and prints
it once to stderr at startup. Health endpoints remain unauthenticated on the
strictly loopback listener.

When the MCP server has a SigNoz target configured, every `migrate_dashboard`
call must provide `source_namespace`, including validation-only calls. This
keeps the generated dashboard identity identical to a later import and fails
missing provenance before target access. A UID-less inline dashboard must
additionally provide `source_identity`; rooted paths and grafana.com IDs supply
their own stable identity.

Start the MCP server with `--allow-insecure-http` only when its configured
SigNoz target is an intentionally plaintext private test endpoint.

## Output quota

MCP output admission is bounded without automatic eviction. The defaults are
10,000 retained entries and 10 GiB of logical file data under `--out`; existing
files, migration generations, validation directories, validation artifacts,
and in-flight recovery markers and payloads all count. Set
`--max-output-entries` / `SIGNOZ_MCP_MAX_OUTPUT_ENTRIES` and
`--max-output-bytes` / `SIGNOZ_MCP_MAX_OUTPUT_BYTES` to lower or raise those
limits. A write that would exceed either limit fails before creating that entry.
Archive or remove old artifacts explicitly after reviewing them — the server
never chooses evidence to evict. Do not run multiple MCP server processes
against the same output root: quota admission is serialized inside one process,
not by a distributed filesystem lock.

Quota values supplied through flags or the environment must be positive base-10
integers. Invalid, zero, negative, and out-of-range values stop startup as
invalid CLI input (exit code `3`); they are never replaced silently with a more
permissive default.

## Crash recovery

Migration and validation publication is process-death recoverable. Incomplete
work is kept beneath one reserved, marker-owned work root and a complete payload
becomes visible only through a rooted rename. For imports, `migration.json`
with the conservative attempted outcome is durable before the target dashboard
request; a final generation carries a prewritten result pointer that startup
can finish publishing without another quota admission. Startup removes only
strictly inventoried tool-owned work, never committed evidence or unrelated
files. An inventory made durable before its payload directory exists is treated
as a bounded pre-publication interruption and reclaimed. Recovery pins each
operation directory across `Lstat` and rooted open, so a substituted directory
is rejected and preserved. The renderer's private temporary directory is bound
to the same operation token; its canonical parent is persisted before staging,
so restart cleanup still finds it if `TMPDIR` changes. Cleanup itself advances
through a durable cleaning phase and can resume after any metadata deletion.
Malformed, structurally impossible, unowned, or oversized recovery state fails
startup closed and is left untouched for inspection.

## Container smoke test

The scratch container image includes a private writable staging directory for
the non-root runtime user. Exercise the packaged binary through a real MCP
initialize and `migrate_dashboard` exchange with:

```sh
make container-mcp-smoke
```

Set `CONTAINER_ENGINE=podman` to run the same smoke check with Podman. The test
builds the pinned image, performs an offline dashboard migration over MCP
stdio, and fails unless the migration result is returned. This specifically
covers temporary staging and durable artifact publication inside the image.
