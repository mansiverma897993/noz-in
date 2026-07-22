# Example: real local migration evidence

`node-exporter-full.report.html` is the untouched evidence report from a real
run of `promcast` on this repository's pinned local stack
(`deploy/local/`): SigNoz v0.133.0 + ClickHouse + signoz-otel-collector +
node_exporter, running in Docker, scraping live machine metrics.

Every chart in the report draws the actual series the live SigNoz API
returned during validation — no synthetic data. Reproduce it with the
commands in [deploy/local/README.md](../../deploy/local/README.md).
