#!/bin/sh
set -eu

container_engine=${CONTAINER_ENGINE:-docker}
image=${PROMCAST_SMOKE_IMAGE:-promcast:mcp-smoke}

"$container_engine" build --tag "$image" .

runtime_user=$("$container_engine" image inspect --format '{{.Config.User}}' "$image")
test "$runtime_user" = "65532:65532"
"$container_engine" image inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$image" |
	grep -Fx 'TMPDIR=/tmp/promcast' >/dev/null

response=$(
	printf '%s\n%s\n%s\n' \
		'{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"container-smoke","version":"1"}}}' \
		'{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}' \
		'{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"migrate_dashboard","arguments":{"grafana_json":"{\"uid\":\"container-smoke\",\"title\":\"Container smoke\",\"schemaVersion\":39,\"panels\":[]}"}}}' |
		"$container_engine" run --rm -i "$image" mcp \
			--transport stdio \
			--root /workspace \
			--out /workspace/out
)

printf '%s\n' "$response" | grep -F '"id":1,"result"' >/dev/null
printf '%s\n' "$response" | grep -F '"id":2,"result"' >/dev/null
printf '%s\n' "$response" | grep -F '"dashboard_title":"Container smoke"' >/dev/null

printf '%s\n' 'container MCP migration smoke test passed'
