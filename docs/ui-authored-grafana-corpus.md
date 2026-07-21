# UI-authored Grafana audit fixtures

This additive audit suite exercises real dashboard JSON maintained by upstream
projects for Grafana 10 and 11. It is separate from the externally assembled
151-dashboard corpus described in [corpus.md](corpus.md): these fixtures do not
change that corpus's frozen dashboard, panel, query, feature, or classification
totals.

“UI-authored” is used narrowly here. These are upstream-maintained classic
Grafana dashboard models with the structures produced and consumed by Grafana's
dashboard UI; no local generator or hand-built synthetic equivalent produced
the fixture bytes. Git history cannot prove which editor supplied every byte,
so the stronger, auditable claims are that the exact JSON is used by an
upstream Grafana runtime, comes from immutable upstream revisions, and has
recognizable dashboard-model fingerprints such as grid positions, plugin
versions, field configuration, transformations, templating state, and
annotations.

This matches Grafana's own documentation: a dashboard is represented by a
[JSON model](https://grafana.com/docs/grafana/latest/visualizations/dashboards/build-dashboards/view-dashboard-json-model/),
the UI exposes [dashboard JSON export](https://grafana.com/docs/grafana/latest/visualizations/dashboards/build-dashboards/create-dashboard/),
and file [provisioning](https://grafana.com/docs/grafana/latest/administration/provisioning/)
loads dashboard JSON into a running Grafana instance.

The machine-readable source of truth is
[manifest.json](../internal/integration/testdata/ui-authored-grafana/manifest.json).
It pins every source commit, raw URL, SHA-256 digest, license, Grafana runtime
version evidence, source inventory, and exercised feature family.

## Vendored Apache-2.0 fixtures

The OpenTelemetry Demo provisions these files directly into Grafana through an
editable file provider. The pinned
[Grafana provider configuration](https://github.com/open-telemetry/opentelemetry-demo/blob/8072c5bd72e15b2b1787fae0d1c09defec3b4f13/src/grafana/provisioning/dashboards/demo.yaml)
uses `type: file`, `editable: true`, and the directory containing the
dashboard models. Their history includes the upstream
[Grafana dashboard update PR #1150](https://github.com/open-telemetry/opentelemetry-demo/pull/1150)
and [dashboard cleanup PR #2085](https://github.com/open-telemetry/opentelemetry-demo/pull/2085).
The repository and all three pinned revisions are
[Apache-2.0 licensed](https://github.com/open-telemetry/opentelemetry-demo/blob/8072c5bd72e15b2b1787fae0d1c09defec3b4f13/LICENSE).

| Fixture | Immutable source | Runtime evidence | Raw inventory | Principal coverage |
|---|---|---|---:|---|
| `otel-demo-grafana-10.1-demo` | [`demo-dashboard.json` at `fc01d8f`](https://github.com/open-telemetry/opentelemetry-demo/blob/fc01d8f46f9d2a1cac6a4e674662fbfe8b66f3c4/src/grafana/provisioning/dashboards/demo/demo-dashboard.json) | [Grafana 10.1.2 image](https://github.com/open-telemetry/opentelemetry-demo/blob/fc01d8f46f9d2a1cac6a4e674662fbfe8b66f3c4/docker-compose.yml#L633-L647) | 8 panels, 11 queries, 1 variable, 106 source features | annotations, field configuration, object-shaped variable query, row, transformations |
| `otel-demo-grafana-11.5-demo` | [`demo-dashboard.json` at `8072c5b`](https://github.com/open-telemetry/opentelemetry-demo/blob/8072c5bd72e15b2b1787fae0d1c09defec3b4f13/src/grafana/provisioning/dashboards/demo/demo-dashboard.json) | [Grafana 11.5.2 image](https://github.com/open-telemetry/opentelemetry-demo/blob/8072c5bd72e15b2b1787fae0d1c09defec3b4f13/.env#L16) | 15 panels, 11 queries, 1 variable, 126 source features | four rows, four transformations, Prometheus/Jaeger/OpenSearch sources, field overrides |
| `otel-demo-grafana-11.5-spanmetrics` | [`spanmetrics-dashboard.json` at `8072c5b`](https://github.com/open-telemetry/opentelemetry-demo/blob/8072c5bd72e15b2b1787fae0d1c09defec3b4f13/src/grafana/provisioning/dashboards/demo/spanmetrics-dashboard.json) | [Grafana 11.5.2 image](https://github.com/open-telemetry/opentelemetry-demo/blob/8072c5bd72e15b2b1787fae0d1c09defec3b4f13/.env#L16) | 11 panels, 14 queries, 2 variables, 173 source features | multi/All variables, `query_result`, table and time-series formats, rows, transformations |

The upstream files do not end in a newline. The repository's patch transport
adds one final LF to each vendored file. Both identities are therefore pinned:

| Fixture | Upstream SHA-256 | Vendored SHA-256 |
|---|---|---|
| `otel-demo-grafana-10.1-demo` | `9a68502bc797fa5c99cbdd054621e51f1189f0898a0d7d21037b5e9141b5b062` | `ba5bd378f50985a0dbfe04e7df9fdfa3fe79e3c14ff3c153ba5f76921f8dfa99` |
| `otel-demo-grafana-11.5-demo` | `f7943992fc39384024e77bf29e41433e4a458f81c5b763335c1ba3bb31a7a077` | `d9e2621922377c8c4e5867fc2967b5740a2504a57aca095df4be5ba531e2d18d` |
| `otel-demo-grafana-11.5-spanmetrics` | `d61bcf3538de4e9c4ada8a65c274dd85470e614bd59748de30beed7cbcd0262c` | `e676ebf27c2cc415052d80730c8a7c045d6843a7ce0c86285b6cdeaef898c7c4` |

The normal test gate verifies the vendored digest, removes exactly the declared
final LF, reconstructs the upstream digest, parses the dashboard, checks the
raw inventory, reconciles every source object, and checks deterministic target
emission.

## Hash-verified Grafana AGPL fetch fixtures

Grafana's official development dashboards add feature shapes absent from the
Apache-licensed set. At the
[`v11.6.16` commit](https://github.com/grafana/grafana/tree/a26b9d592b2d3309834ae1e59f061fe4d10508f2/devenv/dev-dashboards),
Grafana's development build
[embeds the JSON directory](https://github.com/grafana/grafana/blob/a26b9d592b2d3309834ae1e59f061fe4d10508f2/devenv/dev-dashboards/dashboards.go)
as runtime fixtures. The source repository identifies itself as
[`AGPL-3.0-only`](https://github.com/grafana/grafana/blob/a26b9d592b2d3309834ae1e59f061fe4d10508f2/LICENSE).

To keep the repository's redistribution boundary conservative, no Grafana
AGPL dashboard payload is vendored. The manifest retains immutable URLs and
hashes. The explicit online gate downloads each payload into memory, verifies
its digest before parsing, and does not write it into the workspace. Generic
`go test ./...` and `make gold` do not fetch these upstream payloads. The
`test-upstream-fixtures` Make target enables this test, and the CI workflow
runs that target as a separate job.

| Fetch-only fixture | Immutable source | SHA-256 | Exercised behavior |
|---|---|---|---|
| `grafana-11.6-elasticsearch-expressions` | [Elasticsearch migration dashboard](https://github.com/grafana/grafana/blob/a26b9d592b2d3309834ae1e59f061fe4d10508f2/devenv/dev-dashboards/datasource-elasticsearch/elasticsearch_migration.json) | `65ed8e1b80e8b9852be76eb855c0d903ee4fa36c6b645a20548a94839c62e99c` | 39 Grafana expression targets, rows, transformations, mixed Elasticsearch/expression sources |
| `grafana-11.6-library-panels` | [panel-library dashboard](https://github.com/grafana/grafana/blob/a26b9d592b2d3309834ae1e59f061fe4d10508f2/devenv/dev-dashboards/panel-library/panel-library.json) | `12fe4eee3416d25f00afbb287861d725da438aca1a024f5fe3b657543f637975` | two external library-panel references |
| `grafana-11.6-repeating-kitchen-sink` | [repeating kitchen sink](https://github.com/grafana/grafana/blob/a26b9d592b2d3309834ae1e59f061fe4d10508f2/devenv/dev-dashboards/e2e-repeats/Repeating-Kitchen-Sink.json) | `d4d83a9a6e4530a2cc9588f01973da2f2fdd8426b6aa510b51e7776dbea0414a` | row, horizontal, and vertical repeats with multi/All variables |
| `grafana-11.6-dashboard-links` | [dashboard links and variables](https://github.com/grafana/grafana/blob/a26b9d592b2d3309834ae1e59f061fe4d10508f2/devenv/dev-dashboards/feature-templating/templating-dashboard-links-and-variables.json) | `41e679a9efefc31360e8fd09340bae45fc972d0f17429265cabdbb09dbe8cec9` | three dashboard links and templating state |

These files retain their historical classic schema and plugin revisions; the
claim is that they are official dashboards embedded at the pinned Grafana
11.6 revision, not that Grafana 11.6 rewrote every model to schema version 41.

Run the local and network-backed gates separately:

```sh
go test ./internal/integration -run 'Test(UIAuthoredGrafanaManifestContract|VendoredUIAuthoredGrafanaFixtures)$' -count=1

make test-upstream-fixtures
```

The network gate fails closed on a non-200 response, an object larger than 64
MiB, a digest mismatch, parse/inventory drift, incomplete accounting, loss of
declared feature coverage, or nondeterministic emission. `make gold-online`
runs the offline gold gate first and then this network-backed gate; it requires
the same external research corpus configured for `make gold`.
