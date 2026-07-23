#!/usr/bin/env bash

set -euo pipefail

temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT

for target in \
  darwin/amd64 \
  darwin/arm64 \
  linux/amd64 \
  linux/arm64 \
  windows/amd64 \
  windows/arm64; do
  goos=${target%/*}
  goarch=${target#*/}
  suffix=
  if [[ $goos == windows ]]; then
    suffix=.exe
  fi
  output="$temporary/promcast-${goos}-${goarch}${suffix}"
  CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch \
    go build -trimpath -o "$output" ./cmd/promcast
  test -s "$output"
done

# Linux/amd64 tests execute in the release runner. Compile the complete test
# tree for the other implementation build tags: Windows and Unix on arm64.
go list ./... >"$temporary/packages"
for target in windows/amd64 linux/arm64; do
  goos=${target%/*}
  goarch=${target#*/}
  suffix=
  if [[ $goos == windows ]]; then
    suffix=.exe
  fi
  index=0
  while IFS= read -r package; do
    index=$((index + 1))
    CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch \
      go test -c -o "$temporary/${goos}-${goarch}-${index}.test${suffix}" "$package"
  done <"$temporary/packages"
done

printf 'cross-platform build and test compilation passed\n'
