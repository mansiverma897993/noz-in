#!/usr/bin/env bash

set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
source_ip=${SOURCE_PRIVATE_IP:-}
output=${1:-"$script_dir/casting.yaml"}

if [[ ! $source_ip =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
  printf 'SOURCE_PRIVATE_IP must be an IPv4 address\n' >&2
  exit 1
fi

IFS=. read -r -a octets <<<"$source_ip"
for octet in "${octets[@]}"; do
  if ((10#$octet > 255)); then
    printf 'SOURCE_PRIVATE_IP contains an invalid octet\n' >&2
    exit 1
  fi
done

temporary=$(mktemp "${output}.tmp.XXXXXX")
trap 'rm -f "$temporary"' EXIT
sed "s/__SOURCE_PRIVATE_IP__/$source_ip/g" "$script_dir/casting.yaml.tmpl" >"$temporary"
chmod 0600 "$temporary"
mv "$temporary" "$output"
trap - EXIT
printf 'Rendered %s\n' "$output"
