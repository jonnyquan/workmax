SHELL := /bin/bash

.DEFAULT_GOAL := help

.PHONY: help doctor source-baseline-self-test source-baseline-audit secret-audit license-audit baseline-audit bootstrap test-boundaries test-config test-go test-go-desktop test-agent-platform test-agent-platform-mysql check-agent-worker-db test-shell test-desktop verify-core fmt-audit build-server build-desktop

AGENT_WORKER_CONFIG ?= $(abspath server/config.yaml)
HOST_GOOS := $(shell go env GOHOSTOS)
HOST_GOARCH := $(shell go env GOHOSTARCH)
HOST_GO_ENV := GOOS="$(HOST_GOOS)" GOARCH="$(HOST_GOARCH)"
HERMETIC_TEST_ENV := env -u BODO_CONFIG -u WORKMAX_TEST_MYSQL_DSN -u WORKMAX_TEST_MYSQL_DSN_ADMIN -u WORKMAX_TEST_MYSQL_DSN_APP -u WORKMAX_AGENTTURN_MYSQL_DSN -u WORKMAX_AGENTTURN_MYSQL_DSN_ADMIN -u WORKMAX_AGENTTURN_MYSQL_DSN_APP -u WORKMAX_AGENTTURN_MYSQL_CONFIG -u WORKMAX_AGENTTURN_MYSQL_CONTRACT -u WORKMAX_AGENTTURN_MYSQL_ALLOW_DIRECT_DSN -u WORKMAX_AGENTTURN_MYSQL_ALLOW_PLAINTEXT -u WORKMAX_AGENTTURN_MYSQL_ALLOW_INSECURE_TLS

help:
	@echo "WorkMax development targets"
	@echo "  doctor             read-only tool, dependency and boundary checks"
	@echo "  source-baseline-self-test  exercise source path policy fixtures"
	@echo "  source-baseline-audit  classify untracked source paths without staging"
	@echo "  secret-audit       scan source candidates without printing secret values"
	@echo "  license-audit      verify project and distributable dependency licenses"
	@echo "  baseline-audit     run path policy and known-pattern secret gates"
	@echo "  bootstrap          install the renderer's build-time npm dependency"
	@echo "  verify-core        Server, Desktop Go, shell and boundary tests"
	@echo "  test-desktop       unified Desktop verification"
	@echo "  test-config        parse and validate sanitized Server examples"
	@echo "  test-agent-platform  focused credential, Durable Turn and migration contracts"
	@echo "  test-agent-platform-mysql  opt-in SQL contract; writes/cleans owned rows, never migrates"
	@echo "  check-agent-worker-db  Worker DB/schema preflight (no persistent mutation or Worker start)"
	@echo "  fmt-audit          report imported Go formatting debt"

doctor:
	@./scripts/doctor.sh

source-baseline-self-test:
	@./scripts/source-baseline-audit.sh --self-test

source-baseline-audit: source-baseline-self-test
	@./scripts/source-baseline-audit.sh

secret-audit:
	@./scripts/secret-audit.sh

license-audit:
	@./scripts/license-audit.sh

baseline-audit: source-baseline-audit secret-audit

bootstrap:
	@cd desktop/renderer && npm install

test-boundaries:
	@node desktop/scripts/check-desktop-boundaries.mjs
	@node desktop/scripts/check-bundled-renderer-behavior.mjs

test-config:
	@cd server && $(HERMETIC_TEST_ENV) $(HOST_GO_ENV) go test ./config -run TestSanitizedConfigExamplesParseAndValidate

test-go:
	@cd server && $(HERMETIC_TEST_ENV) $(HOST_GO_ENV) go test ./...

test-go-desktop:
	@cd server && $(HERMETIC_TEST_ENV) $(HOST_GO_ENV) CGO_ENABLED=1 go test -tags desktop ./desktop/... ./api/desktop/... ./service/desktop/... ./router/desktop/... ./middleware/...
	@cd server && $(HOST_GO_ENV) CGO_ENABLED=0 go build -tags desktop ./...

test-agent-platform:
	@cd server && $(HERMETIC_TEST_ENV) $(HOST_GO_ENV) go test ./contracts/agent/v1 ./contracts/credential/v1 ./service/agentturn ./api/agent/v1 ./cmd/agent-worker ./service/desktop/oauth ./api/desktop/oauth ./middleware ./config ./migrations ./scripts/guard

test-agent-platform-mysql:
	@cd server && $(HOST_GO_ENV) WORKMAX_AGENTTURN_MYSQL_CONTRACT=1 WORKMAX_AGENTTURN_MYSQL_CONFIG="$(abspath server/config.yaml)" go test -count=1 ./service/agentturn -run 'Test(SQL(Store|ExecutionStore)|PluginScopedExecutionStore)MySQLContract'

check-agent-worker-db:
	@cd server && GOOS="$(HOST_GOOS)" GOARCH="$(HOST_GOARCH)" go run ./cmd/agent-worker -check-database -c "$(AGENT_WORKER_CONFIG)"

# The desktop shell's own tests: UI origin capability, privileged-route
# refusal, containment headers. Replaced the Electron npm suite.
test-shell:
	@cd desktop/wails && $(HOST_GO_ENV) CGO_ENABLED=1 go test -tags desktop ./...

test-desktop:
	@./desktop/scripts/test-desktop.sh

verify-core: baseline-audit test-boundaries test-config test-go test-go-desktop test-shell

fmt-audit:
	@cd server && gofmt -l .

build-server:
	@cd server && go build ./...

build-desktop:
	@cd desktop/wails && CGO_ENABLED=1 go build -tags desktop -o /private/tmp/workmax-desktop .
