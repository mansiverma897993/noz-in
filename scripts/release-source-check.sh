#!/usr/bin/env bash

set -euo pipefail

tag=${RELEASE_TAG:-}
event_sha=${RELEASE_SHA:-}
if [[ -z $tag ]]; then
  printf 'RELEASE_TAG is required\n' >&2
  exit 1
fi
if [[ ! $tag =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?(\+[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$ ]]; then
  printf 'release tag is not a canonical v-prefixed semantic version: %s\n' "$tag" >&2
  exit 1
fi

tag_commit=$(git rev-parse --verify "refs/tags/${tag}^{commit}")
head_commit=$(git rev-parse --verify HEAD)
if [[ $tag_commit != "$head_commit" ]]; then
  printf 'release tag %s does not resolve to checked-out HEAD\n' "$tag" >&2
  exit 1
fi
if [[ -n $event_sha && $event_sha != "$head_commit" ]]; then
  printf 'release event SHA does not match checked-out HEAD\n' >&2
  exit 1
fi
if ! git diff --quiet --ignore-submodules -- || ! git diff --cached --quiet --ignore-submodules --; then
  printf 'release checkout contains tracked changes\n' >&2
  exit 1
fi

printf 'release tag %s resolves to checked-out HEAD\n' "$tag"
