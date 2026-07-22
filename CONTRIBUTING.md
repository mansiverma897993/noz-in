# Contributing

Thank you for improving `promcast`.

## Development setup

```sh
git clone https://github.com/mansiverma897993/noz-in
cd promcast
make build          # ./bin/promcast
make test           # unit tests; corpus tests skip unless the env var below is set
make gold           # the full reproducible local gate (fmt, vet, race, cover, lint, vuln, build)
```

The frozen research corpus is external; set
`PROMCAST_RESEARCH_ROOT=/path/to/fixtures` to enable the corpus-scale
tests (`make test-corpus`). Without it those tests skip cleanly — a plain
`make test` needs nothing but Go.

A live end-to-end lab (Prometheus + Grafana source, SigNoz destination) is
described in [deploy/README.md](deploy/README.md). Start with
[docs/architecture.md](docs/architecture.md) for the package map and suggested
reading order, and [docs/guarantees.md](docs/guarantees.md) for the invariants
any change must preserve.

## Proposing a change

Before proposing a change:

1. Explain the user problem and the compatibility behavior being changed.
2. Add a focused fixture or golden test that demonstrates it.
3. Run `make fmt vet test-race lint build`.
4. Update the reason-code or compatibility documentation when behavior changes.

For query compatibility changes, test all three contracts where relevant:

- the stored dashboard shape consumed by the SigNoz frontend;
- the strict v5 preview/execution shape consumed by the backend;
- source/target numerical behavior in the differential comparator.

An API preview alone is not sufficient evidence for a dashboard change. Check
scalar rendering, variable defaults, empty results, and non-finite samples.

Keep changes small and cohesive. The neutral model must not import source or target packages. Grafana-specific behavior belongs in `internal/source/grafana`; SigNoz wire formats and API behavior belong in `internal/target/signoz`.

Never include credentials, live tenant data, generated build output, or local debugging artifacts.

Use `PROMCAST_RESEARCH_ROOT=/path/to/fixtures make test-corpus` when the
frozen corpus is available. Changes to parsing or classification must continue
to account for every fixture rather than improving a percentage by dropping
unsupported inputs.

Use conventional commit titles when the project is eventually placed under
version control. Keep product changes focused; separate broad scaffolding,
behavior, and end-to-end evidence when a change would otherwise be difficult to
review.

This standalone project intentionally uses idiomatic Go wrapped errors instead
of importing SigNoz's internal error package. It follows SigNoz's package
boundaries, focused-change discipline, structured review template, and strict
lint posture without creating a fake compatibility dependency.
