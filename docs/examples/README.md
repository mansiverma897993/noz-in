# Examples: real local migration evidence

Both files here are untouched output from real `promcast` runs against this
repository's pinned local stack (`deploy/local/`): SigNoz v0.133.0 +
ClickHouse + signoz-otel-collector + node_exporter in Docker, scraping live
machine metrics, with a Grafana/Prometheus source stack alongside it
(`deploy/source/`).

- **`node-exporter-full.report.html`** — dashboard migration evidence for
  Grafana's Node Exporter Full: 140/140 panels accounted for, zero omitted,
  every executed query charted from the actual series the live SigNoz API
  returned. No synthetic data anywhere in the file.
- **`kube-prometheus-rules.report.html`** — alert-rule migration evidence for
  the kube-prometheus core rule file, including the candidates held for review
  rather than written.

Reproduce both with the commands in
[deploy/local/README.md](../../deploy/local/README.md).
