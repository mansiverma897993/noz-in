# Verified versions

The local toolchain and AWS integration environment were last exercised with:

| Component | Version |
|---|---|
| Go | 1.25.12 |
| golangci-lint | 2.12.2 |
| GoReleaser | 2.17.0 |
| Prometheus | 3.5.0 |
| Grafana | 12.1.0 |
| node_exporter | 1.11.1 |
| Foundry | 0.2.13 |
| SigNoz | 0.133.0 |
| SigNoz OpenTelemetry Collector | 0.144.6 |

Application dependencies are pinned by `go.mod` and `go.sum`. Deployment images
are pinned in `deploy/source/compose.yaml` and
`deploy/destination/casting.yaml.tmpl`. This table records compatibility
evidence; it is not a claim that newer versions are unsupported.
