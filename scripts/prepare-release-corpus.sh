#!/usr/bin/env bash

set -euo pipefail
export LC_ALL=C

destination=${1:-}
url=${RELEASE_CORPUS_URL:-}
expected_sha256=${RELEASE_CORPUS_SHA256:-}
maximum_archive_bytes=$((128 * 1024 * 1024))
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repository_root=$(dirname -- "$script_dir")

if [[ -z $destination || $destination != /* ]]; then
  printf 'usage: prepare-release-corpus.sh /absolute/destination\n' >&2
  exit 1
fi
if [[ -z $url ]]; then
  printf 'RELEASE_CORPUS_URL is required; release validation is fail-closed\n' >&2
  exit 1
fi
if [[ $url != https://* ]]; then
  printf 'RELEASE_CORPUS_URL must use HTTPS\n' >&2
  exit 1
fi
if [[ ! $expected_sha256 =~ ^[0-9a-f]{64}$ ]]; then
  printf 'RELEASE_CORPUS_SHA256 must be a lowercase SHA-256 digest\n' >&2
  exit 1
fi
if [[ -e $destination || -L $destination ]]; then
  printf 'release corpus destination already exists: %s\n' "$destination" >&2
  exit 1
fi

parent=$(dirname -- "$destination")
mkdir -p -- "$parent"
archive=$(mktemp "$parent/release-corpus.archive.XXXXXX")
staging=
cleanup() {
  rm -f -- "$archive"
  if [[ -n $staging ]]; then
    rm -rf -- "$staging"
  fi
}
trap cleanup EXIT

curl \
  --fail \
  --silent \
  --show-error \
  --location \
  --proto '=https' \
  --proto-redir '=https' \
  --max-redirs 3 \
  --connect-timeout 15 \
  --max-time 300 \
  --max-filesize "$maximum_archive_bytes" \
  --retry 3 \
  --retry-all-errors \
  --output "$archive" \
  "$url"

archive_bytes=$(wc -c <"$archive" | tr -d '[:space:]')
if ((archive_bytes == 0 || archive_bytes > maximum_archive_bytes)); then
  printf 'release corpus archive size is outside the accepted range\n' >&2
  exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
  actual_sha256=$(sha256sum "$archive" | awk '{print $1}')
else
  actual_sha256=$(shasum -a 256 "$archive" | awk '{print $1}')
fi
if [[ $actual_sha256 != "$expected_sha256" ]]; then
  printf 'release corpus SHA-256 mismatch\n' >&2
  exit 1
fi

staging=$(mktemp -d "$parent/release-corpus.extract.XXXXXX")
(cd "$repository_root" && go run ./internal/releasegate/cmd/corpus-archive "$archive" "$staging")

mv -- "$staging" "$destination"
staging=
printf 'prepared hash-verified release corpus at %s\n' "$destination"
