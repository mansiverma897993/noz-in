# Local reproduction

This is the single-machine path. It stands up the pinned SigNoz stack with a
node_exporter sidecar, so a reviewer can migrate a real Grafana dashboard and
inspect the result without provisioning anything. The two-node AWS topology used
for the recorded validation is described in [../README.md](../README.md).

`casting.yaml` and `casting.yaml.lock` are both committed. Foundry's deploy step
reads the lock rather than the casting, so the committed lock is what actually
gets deployed; `forge` regenerates it deterministically from the casting.

## Prerequisites

- Docker Engine 20.10+ or Docker Desktop, with Compose v2 (`docker compose version`)
- At least 4 GiB allocated to Docker; ClickHouse restart-loops below that
- Ports 8080, 4317, and 4318 free
- Outbound HTTPS for image pulls

## Stand up the stack

```sh
curl -fsSL https://signoz.io/foundry.sh | FOUNDRY_VERSION=v0.2.13 bash
cd deploy/local
foundryctl cast -f casting.yaml
```

`cast` runs gauge, forge, and deploy. It rewrites `pours/` and the lock from
`casting.yaml`; on an unmodified checkout the regenerated lock matches the
committed one.

Wait for the API to answer, then create the admin user and a service-account key:

```sh
curl -fsS http://127.0.0.1:8080/api/v1/health
PROMCAST_STATE_DIR=./state SIGNOZ_URL=http://127.0.0.1:8080 ../destination/bootstrap.sh
```

The script is idempotent and writes credentials with mode `0600` beneath the
state directory. Point `PROMCAST_STATE_DIR` somewhere writable if you do not want
the default `/var/lib/promcast`.

## Migrate a dashboard

The collector scrapes the node_exporter sidecar over `signoz-network`, so metrics
begin arriving immediately. **Let the stack ingest for a few minutes before
migrating.** Metric names must already exist on the target for a query to be
eligible for native promotion, so an immediate run reports far more
`MISSING_METRIC_IN_TARGET` than a run a few minutes later.

```sh
go build -o bin/promcast ./cmd/promcast

./bin/promcast grafana deploy/source/grafana/dashboards/node-exporter-full.json \
  --target http://127.0.0.1:8080 \
  --api-key-file deploy/local/state/api-key \
  --allow-insecure-http \
  --source-namespace judge-local \
  --var job=node-exporter \
  --var node=local-node \
  --out out/
```

This writes the emitted dashboard, a JSON evidence report, and a self-contained
HTML report into `out/`, and creates the dashboard on the target. Re-running the
same command updates the same dashboard rather than creating a second one.

Open <http://127.0.0.1:8080>, sign in with the credentials in the state
directory, and the migrated dashboard appears as **Node Exporter Full**.

## What to expect

Every query migrates and every panel renders, but on this dashboard nothing is
promoted to `native`: the summary line reports review-scoped queries, not native
ones. That is the honest result, and it matches the recorded AWS validation.
Native is only granted when a Builder candidate is executed against the live
target and numerically agrees with its own PromQL passthrough, and this
dashboard's queries do not clear that bar. See
[../../docs/guarantees.md](../../docs/guarantees.md).

Two environment-specific notes:

- On macOS, node_exporter reports the Docker Desktop LinuxKit VM rather than the
  host, and roughly 70 of the dashboard's series have no counterpart there. Those
  queries report `MISSING_METRIC_IN_TARGET` on any run length. This is a property
  of the platform, not of the migration.
- `pid: host` and the root bind mount are Linux semantics. The metrics are real
  and fully labelled either way.

## Tear down

Foundry has no uninstall command.

```sh
docker compose -f pours/deployment/compose.yaml down -v
```
