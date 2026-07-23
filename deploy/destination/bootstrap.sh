#!/usr/bin/env bash

set -euo pipefail

base_url=${SIGNOZ_URL:-http://127.0.0.1:8080}
state_dir=${PROMCAST_STATE_DIR:-/var/lib/promcast}
admin_name=${SIGNOZ_BOOTSTRAP_NAME:-Mansi Verma}
admin_email=${SIGNOZ_BOOTSTRAP_EMAIL:-mansiverma@local.signoz}
service_account=${SIGNOZ_SERVICE_ACCOUNT:-promcast}

for command in curl jq openssl; do
  command -v "$command" >/dev/null || {
    printf '%s is required\n' "$command" >&2
    exit 1
  }
done

install -d -m 0700 "$state_dir"
umask 077
curl_config=$(mktemp "$state_dir/curl-config.XXXXXX")
trap 'rm -f "$curl_config"' EXIT

set_api_key_header() {
  printf 'header = "SIGNOZ-API-KEY: %s"\n' "$1" >"$curl_config"
}

set_bearer_header() {
  printf 'header = "Authorization: Bearer %s"\n' "$1" >"$curl_config"
}

if [[ -s "$state_dir/api-key" ]]; then
  key=$(<"$state_dir/api-key")
  set_api_key_header "$key"
  if curl -fsS --config "$curl_config" "$base_url/api/v1/dashboards" >/dev/null; then
    printf 'SigNoz credentials are already initialized in %s\n' "$state_dir"
    exit 0
  fi
fi

for _ in {1..60}; do
  if curl -fsS "$base_url/api/v1/health" >/dev/null; then
    break
  fi
  sleep 2
done
curl -fsS "$base_url/api/v1/health" >/dev/null

password="$(openssl rand -hex 20)Aa1!"
register_payload=$(printf '%s' "$password" | jq -Rsc \
  --arg name "$admin_name" \
  --arg email "$admin_email" \
  '{name:$name,orgName:"promcast",email:$email,password:.}')
printf '%s' "$register_payload" | curl -fsS -X POST "$base_url/api/v1/register" \
  -H 'Content-Type: application/json' \
  --data-binary @- >/dev/null

org_id=$(curl -fsSG "$base_url/api/v2/sessions/context" \
  --data-urlencode "email=$admin_email" \
  --data-urlencode "ref=$base_url" | jq -er '.data.orgs[0].id')
login_payload=$(printf '%s' "$password" | jq -Rsc \
  --arg email "$admin_email" \
  --arg orgId "$org_id" \
  '{email:$email,password:.,orgId:$orgId}')
token=$(printf '%s' "$login_payload" | curl -fsS -X POST "$base_url/api/v2/sessions/email_password" \
  -H 'Content-Type: application/json' \
  --data-binary @- | jq -er '.data.accessToken')

set_bearer_header "$token"

service_account_id=$(curl -fsS -X POST "$base_url/api/v1/service_accounts" \
  --config "$curl_config" \
  -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg name "$service_account" '{name:$name}')" | jq -er '.data.id')
role_id=$(curl -fsS "$base_url/api/v1/roles" \
  --config "$curl_config" | jq -er '.data[] | select(.name=="signoz-admin").id')
curl -fsS -X POST "$base_url/api/v1/service_accounts/$service_account_id/roles" \
  --config "$curl_config" \
  -H 'Content-Type: application/json' \
  -d "$(jq -nc --arg id "$role_id" '{id:$id}')" >/dev/null

key=$(curl -fsS -X POST "$base_url/api/v1/service_accounts/$service_account_id/keys" \
  --config "$curl_config" \
  -H 'Content-Type: application/json' \
  -d '{"name":"migrate-key","expiresAt":0}' | jq -er '.data.key')
printf '%s\n' "$admin_email" >"$state_dir/admin-email"
printf '%s\n' "$password" >"$state_dir/admin-password"
printf '%s\n' "$key" >"$state_dir/api-key"

set_api_key_header "$key"
curl -fsS --config "$curl_config" "$base_url/api/v1/dashboards" >/dev/null
printf 'SigNoz admin and service account initialized in %s\n' "$state_dir"
