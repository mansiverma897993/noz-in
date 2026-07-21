.PHONY: build container-mcp-smoke cross-check docs-check fmt fmt-check gold gold-online lint mod-check release-gold release-snapshot-check shell-check shell-lint test test-corpus test-cover test-race test-upstream-fixtures vet vuln workflow-check

ACTIONLINT ?= $(shell command -v actionlint 2>/dev/null || printf '%s/bin/actionlint\n' "$$(go env GOPATH)")
GOLANGCI_LINT ?= $(shell command -v golangci-lint 2>/dev/null || printf '%s/bin/golangci-lint\n' "$$(go env GOPATH)")
GORELEASER ?= $(shell command -v goreleaser 2>/dev/null || printf '%s/bin/goreleaser\n' "$$(go env GOPATH)")
GOVULNCHECK ?= $(shell command -v govulncheck 2>/dev/null || printf '%s/bin/govulncheck\n' "$$(go env GOPATH)")
CONTAINER_ENGINE ?= docker
SHELLCHECK_IMAGE ?= koalaman/shellcheck@sha256:61862eba1fcf09a484ebcc6feea46f1782532571a34ed51fedf90dd25f925a8d

build:
	go build -o bin/promcast ./cmd/promcast

container-mcp-smoke:
	./scripts/container-mcp-smoke.sh

cross-check:
	./scripts/cross-check.sh

# Every doc under docs/ must be referenced from at least one other markdown
# file or Go source, and every relative markdown link must resolve. Orphaned
# or dangling documentation fails the gate instead of accumulating.
docs-check:
	@fail=0; \
	for doc in docs/*.md; do \
		name=$$(basename "$$doc"); \
		if ! grep -rql --include='*.md' --include='*.go' "$$name" --exclude-dir=bin --exclude-dir=.git . 2>/dev/null || \
		   [ "$$(grep -rl --include='*.md' --include='*.go' "$$name" . 2>/dev/null | grep -cv "^\./docs/$$name$$")" -eq 0 ]; then \
			echo "orphaned doc: $$doc"; fail=1; \
		fi; \
	done; \
	for link in $$(grep -ohE '\]\((docs/[A-Za-z0-9_.-]+\.md|[A-Z]+[A-Za-z_.-]*\.md|deploy/README\.md)' README.md docs/*.md 2>/dev/null | tr -d ']('); do \
		[ -e "$$link" ] || { echo "dangling link: $$link"; fail=1; }; \
	done; \
	exit $$fail

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

fmt-check:
	@test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './vendor/*'))" || \
		{ gofmt -l $$(find . -name '*.go' -not -path './vendor/*'); exit 1; }

mod-check:
	go mod verify
	go mod tidy -diff

lint:
	$(GOLANGCI_LINT) run

release-snapshot-check:
	GORELEASER="$(GORELEASER)" ./scripts/release-snapshot-check.sh

shell-check:
	bash -n deploy/destination/bootstrap.sh deploy/destination/render-casting.sh scripts/cross-check.sh scripts/prepare-release-corpus.sh scripts/release-snapshot-check.sh scripts/release-source-check.sh
	sh -n scripts/container-mcp-smoke.sh

shell-lint:
	$(CONTAINER_ENGINE) run --rm -v "$(CURDIR):/mnt:ro" -w /mnt $(SHELLCHECK_IMAGE) \
		scripts/container-mcp-smoke.sh scripts/cross-check.sh scripts/prepare-release-corpus.sh \
		scripts/release-snapshot-check.sh scripts/release-source-check.sh \
		deploy/destination/bootstrap.sh deploy/destination/render-casting.sh

test:
	go test -count=1 ./...

test-corpus:
	test -n "$(PROMCAST_RESEARCH_ROOT)"
	go test -count=1 ./internal/integration ./internal/source/grafana ./internal/source/prometheus ./internal/transpile ./internal/rules

test-cover:
	go test -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | awk '/^total:/ { gsub("%", "", $$3); if ($$3 < 70) { print "coverage " $$3 "% is below 70%"; exit 1 } }'

test-race:
	go test -race -count=1 ./...

test-upstream-fixtures:
	PROMCAST_FETCH_UPSTREAM_GRAFANA_FIXTURES=1 go test -count=1 ./internal/integration -run '^(TestUIAuthoredGrafanaManifestContract|TestFetchOnlyUIAuthoredGrafanaFixtures)$$'

vet:
	go vet ./...

vuln:
	$(GOVULNCHECK) ./...

workflow-check:
	$(ACTIONLINT) .github/workflows/ci.yml .github/workflows/release.yml

gold: fmt-check mod-check vet test test-race test-cover test-corpus lint vuln docs-check build

gold-online: gold
	$(MAKE) test-upstream-fixtures

release-gold: gold-online shell-check shell-lint workflow-check cross-check container-mcp-smoke release-snapshot-check
