# Release gate

Tagged releases are fail-closed. The `Release` workflow first runs a read-only
acceptance job, and only a separate job that depends on that result receives
`contents: write`. The publishing job checks out the same immutable tag and
reruns the GoReleaser source hooks before creating a GitHub release.

## Repository configuration

The full regression corpus is intentionally not redistributed in this
repository, but it remains a release requirement. Configure both of these
repository settings before pushing a tag:

- secret `RELEASE_CORPUS_URL`: an HTTPS URL that returns a gzip-compressed tar
  archive containing only `corpus/` and `corpus-complex/`;
- variable `RELEASE_CORPUS_SHA256`: the lowercase SHA-256 digest of those exact
  archive bytes.

The archive must contain `corpus/top`, `corpus/mixin`, and `corpus-complex`.
The workflow rejects redirects away from HTTPS, oversized or mismatched
archives, paths outside those roots, links, devices, and other non-regular
entries before extraction. It also rejects duplicate paths, more than 4,096
entries, or more than 64 MiB of expanded regular-file data. A missing setting
is an acceptance failure, not a reason to skip corpus tests. Keep the object
private when its source licenses or storage policy require that; the URL secret
may be a read-only signed URL.

To prepare an archive from the separately assembled fixture root, then record
the digest of the finished bytes:

```sh
COPYFILE_DISABLE=1 tar -czf promcast-release-corpus.tar.gz \
  -C /absolute/path/to/fixtures corpus corpus-complex
sha256sum promcast-release-corpus.tar.gz
```

`COPYFILE_DISABLE=1` prevents macOS `tar` from adding AppleDouble metadata
outside the two accepted roots; it is harmless on Linux.

Upload those exact bytes and update the repository variable only as an
explicit corpus-baseline change. The corpus tests still assert the frozen
counts, so replacing an archive cannot silently relax the contract.

## What blocks publication

`make release-gold` covers formatting, module integrity, vet, normal and race
tests, the 70% coverage floor, the external corpus, strict lint, vulnerability
scanning, the hash-pinned upstream Grafana fixtures, shell and workflow
validation, all six release builds, Windows and arm64 test compilation, the
non-root container MCP smoke test, and a GoReleaser snapshot whose six archives
and checksums are inspected. ShellCheck runs from a digest-pinned container.
The release workflow pins every action to a full commit and pins the lint,
vulnerability, actionlint, and GoReleaser versions.

Run the same gate locally with the corpus already assembled:

```sh
PROMCAST_RESEARCH_ROOT=/absolute/path/to/fixtures make release-gold
```

The snapshot archive check also supports an exported source tree without a
`.git` directory. In that mode GoReleaser receives deterministic synthetic
snapshot metadata and empty release notes, then parses the same configuration,
runs its hooks, and builds and inspects the same six archives. Publication still
requires the real tagged repository checks described below.

Before tagging, create a canonical `vMAJOR.MINOR.PATCH` tag (SemVer prerelease
and build suffixes are accepted) at the exact commit intended for publication.
Missing corpus configuration, a tag/checkout mismatch, or any failed gate
prevents the write-privileged job from starting. Both jobs check out the push
event SHA directly and require the tag to still resolve to it, so moving a tag
during a run cannot change the source that gets published.
