#!/usr/bin/env bash

set -euo pipefail

goreleaser=${GORELEASER:-goreleaser}
if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  "$goreleaser" check
  "$goreleaser" release --snapshot --clean
else
  # GoReleaser supports snapshot builds from exported source trees, but its
  # standalone `check` command requires SCM state. The release command still
  # parses and defaults the complete configuration before building. Supplying
  # empty release notes prevents changelog discovery from inventing a git
  # requirement, and the explicit synthetic tag makes archive names stable.
  printf 'release snapshot is validating an exported source tree without git metadata\n'
  GORELEASER_CURRENT_TAG=v0.0.0 \
    GORELEASER_PREVIOUS_TAG=v0.0.0 \
    "$goreleaser" release --snapshot --clean --release-notes /dev/null
fi

shopt -s nullglob
archives=(dist/promcast_*.tar.gz dist/promcast_*.zip)
if ((${#archives[@]} != 6)); then
  printf 'release snapshot produced %d archives; expected 6\n' "${#archives[@]}" >&2
  exit 1
fi

for target in \
  darwin_amd64.tar.gz \
  darwin_arm64.tar.gz \
  linux_amd64.tar.gz \
  linux_arm64.tar.gz \
  windows_amd64.zip \
  windows_arm64.zip; do
  matches=(dist/promcast_*_"$target")
  if ((${#matches[@]} != 1)); then
    printf 'release snapshot expected exactly one archive for %s\n' "$target" >&2
    exit 1
  fi
done

if [[ ! -f dist/checksums.txt ]]; then
  printf 'release snapshot did not produce checksums.txt\n' >&2
  exit 1
fi
checksum_lines=$(wc -l <dist/checksums.txt | tr -d '[:space:]')
if [[ $checksum_lines != 6 ]]; then
  printf 'release snapshot checksum manifest has %s lines; expected 6\n' "$checksum_lines" >&2
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  (cd dist && sha256sum --check checksums.txt)
else
  (cd dist && shasum -a 256 --check checksums.txt)
fi

for archive in "${archives[@]}"; do
  if [[ $archive == *.zip ]]; then
    contents=$(unzip -Z1 "$archive")
    binary=promcast.exe
  else
    contents=$(tar -tzf "$archive")
    binary=promcast
  fi
  content_lines=$(printf '%s\n' "$contents" | wc -l | tr -d '[:space:]')
  if [[ $content_lines != 4 ]]; then
    printf '%s contains %s entries; expected 4\n' "$archive" "$content_lines" >&2
    exit 1
  fi
  for required in "$binary" LICENSE NOTICE README.md; do
    if ! printf '%s\n' "$contents" | grep -Fx -- "$required" >/dev/null; then
      printf '%s is missing %s\n' "$archive" "$required" >&2
      exit 1
    fi
  done
done

printf 'GoReleaser snapshot and archive contract passed\n'
