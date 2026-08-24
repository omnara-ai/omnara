GO ?= $(shell if [ -x .tools/go/bin/go ]; then printf '%s' .tools/go/bin/go; else printf '%s' go; fi)
GO_BIN_DIR := $(dir $(GO))
GO_PATH_PREFIX := $(if $(filter ./,$(GO_BIN_DIR)),,$(GO_BIN_DIR):)
export PATH := $(GO_PATH_PREFIX)/opt/homebrew/bin:/usr/local/bin:$(PATH)
CI_TOOL_MOD := tools/ci/go.mod
CI_TOOL = $(GO) tool -modfile=$(CI_TOOL_MOD)
SQLC ?= $(CI_TOOL) github.com/sqlc-dev/sqlc/cmd/sqlc
GOOSE_TOOL_MOD := tools/goose/go.mod
GOOSE_OFFLINE_BUILD_TAGS := no_clickhouse,no_libsql,no_mssql,no_mysql,no_postgres,no_sqlite3,no_vertica,no_ydb
GOOSE ?= GOFLAGS='-tags=$(GOOSE_OFFLINE_BUILD_TAGS)' $(GO) tool -modfile=$(GOOSE_TOOL_MOD) github.com/pressly/goose/v3/cmd/goose
REPO_ROOT := $(CURDIR)
REACT_DOCTOR_BASE ?= origin/main
GOLANGCI_LINT ?= $(REPO_ROOT)/.tools/custom-golangci-lint
GOVULNCHECK ?= $(CI_TOOL) golang.org/x/vuln/cmd/govulncheck
OAPI_CODEGEN ?= $(CI_TOOL) github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen
OASDIFF ?= $(CI_TOOL) github.com/oasdiff/oasdiff
OASDIFF_FORMAT ?= $(if $(CI),githubactions,text)
OASDIFF_BREAKING = $(OASDIFF) breaking --allow-external-refs=false --fail-on WARN \
	--severity-levels tools/ci/openapi-compat/severity-levels.txt
COMPAT_REMOTE ?= origin
AIR ?= $(CI_TOOL) github.com/air-verse/air
MIGRATION_CHECK ?= $(GO) -C tools/ci run ./migrationcheck
OMNARAD_VERSION ?= 0.0.0-dev
MIGRATION_DIRS := migrations internal/machinedaemon/statedb/migrations
SQLC_OWNED_PATHS := internal/storage/internal/dbsqlc internal/storage/queries \
	internal/machinedaemon/statedb/internal/dbsqlc internal/machinedaemon/statedb/queries
OMNARALINT_SOURCES := $(shell find tools/omnaralint -name '*.go' -print)
GO_MODULE_DIRS := . tools/ci tools/goose tools/omnaralint
INTEGRATION_STORAGE_PACKAGES := \
	./internal/storage \
	./internal/storage/executionstore \
	./internal/storage/identitystore \
	./internal/testutil/storagetest
INTEGRATION_HTTPAPI_PACKAGES := \
	./internal/httpapi \
	./internal/httpapi/auth
INTEGRATION_RUNTIME_PACKAGES := \
	./internal/harness/kernel \
	./internal/harness/tools \
	./internal/harness/worker \
	./internal/dbmigrate \
	./internal/machinepool \
	./internal/modelprovider \
	./internal/notifications \
	./internal/redistore
INTEGRATION_PACKAGES := $(INTEGRATION_STORAGE_PACKAGES) $(INTEGRATION_HTTPAPI_PACKAGES) $(INTEGRATION_RUNTIME_PACKAGES)
POSTGRES_HOST_PORT ?= 55432
REDIS_HOST_PORT ?= 6379
TEST_DATABASE_URL ?= postgres://omnara:omnara@127.0.0.1:$(POSTGRES_HOST_PORT)/omnara?sslmode=disable
TEST_REDIS_URL ?= redis://127.0.0.1:$(REDIS_HOST_PORT)/0
TEST_DB_ENV = OMNARA_TEST_DATABASE_URL=$${OMNARA_TEST_DATABASE_URL:-$(TEST_DATABASE_URL)}
TEST_REDIS_ENV = OMNARA_TEST_REDIS_URL=$${OMNARA_TEST_REDIS_URL:-$(TEST_REDIS_URL)}
TEST_INFRA_ENV = $(TEST_DB_ENV) $(TEST_REDIS_ENV)
SERVICE_E2E_ENV = OMNARA_REQUIRE_SERVICE_E2E=1 $(TEST_INFRA_ENV)
SERVICE_E2E_SHARD_INDEX ?= 0
SERVICE_E2E_SHARD_TOTAL ?= 1
LOAD_DOTENV = set -a; [ ! -f .env ] || . ./.env; set +a

.DEFAULT_GOAL := help

.PHONY: \
	help ci test-all test verify verify-go verify-static fmt-check golangci-version-check golangci-lint govulncheck race-machinedaemon \
	go-modules-check integration-packages-check tagged-packages-check \
	openapi-generate openapi-check openapi-compat-fixture-check openapi-compat-check compatibility-check \
	migration-create state-migration-create migration-fix migration-check migration-compat-check goose-version-check sqlite-libc-check \
	sqlc-generate sqlc-check sql-rules sqlc-vet migrate-test-db sqlc-vet-db sqlc-vet-local-db \
	unit coverage test-database-contracts test-integration test-integration-storage test-integration-httpapi test-integration-runtime clean-integration-dbs db-up db-down stack-up stack-down fmt run-migrate run-api run-worker run-maintenance \
	test-service-e2e \
	web-install web-generate web-generate-check build-web build-api build-api-from-dist build-omnarad web-lint web-doctor web-check web-check-all web-e2e run-web \
	test-live-web test-live-openai-responses test-live-openai-chat-completions test-live-openrouter test-live-anthropic \
	test-live-api-format-switching test-live-sandbox-providers test-live \
	docs-openapi docs-openapi-check

help:
	@printf 'Usage:\n  make <target>\n\nCommon targets:\n'
	@grep -E '^[a-zA-Z0-9_-]+:.*## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*## "}; {printf "  %-20s %s\n", $$1, $$2}'

ci: test-all ## Run the complete suite, including live provider tests

test-all: verify web-e2e test-integration test-service-e2e migrate-test-db sqlc-vet-db govulncheck test-live

test: sqlc-check sql-rules sqlc-vet unit test-integration

verify: verify-go web-check ## Run the fast repository gate

verify-go: verify-static unit race-machinedaemon

verify-static: fmt-check go-modules-check golangci-version-check goose-version-check golangci-lint integration-packages-check tagged-packages-check openapi-check openapi-compat-fixture-check docs-openapi-check migration-check sqlite-libc-check sqlc-check sql-rules sqlc-vet

fmt-check:
	@files="$$(find . \( -path './.tools' -o -path './.cache' -o -path '*/node_modules' -o -path './frontend/apps/web/dist' \) -prune -o -name '*.go' -print | xargs gofmt -l)"; \
	test -z "$$files" || { printf 'gofmt needed:\n%s\n' "$$files"; exit 1; }

golangci-version-check:
	@config_version="$$(awk '$$1 == "version:" { print $$2; exit }' .custom-gcl.yml)"; \
	tool_version="$$(GOFLAGS= $(GO) list -m -modfile=$(CI_TOOL_MOD) -f '{{.Version}}' github.com/golangci/golangci-lint/v2)"; \
	test "$$config_version" = "$$tool_version" || { \
		printf 'golangci-lint version mismatch: .custom-gcl.yml=%s tools/ci=%s\n' "$$config_version" "$$tool_version"; \
		exit 1; \
	}

go-modules-check:
	@expected="$$(printf '%s\n' $(GO_MODULE_DIRS) | sort)"; \
	actual="$$(git ls-files ':(glob)**/go.mod' | xargs -n1 dirname | sort)"; \
	test "$$expected" = "$$actual" || { \
		printf 'GO_MODULE_DIRS is out of date.\n\nExpected from Makefile:\n%s\n\nTracked Go modules:\n%s\n' "$$expected" "$$actual"; \
		exit 1; \
	}

golangci-lint: $(GOLANGCI_LINT)
	@set -e; for module in $(GO_MODULE_DIRS); do \
		packages="$$( $(GO) -C "$$module" list ./... 2>/dev/null )"; \
		test -n "$$packages" || continue; \
		printf 'golangci-lint (%s)\n' "$$module"; \
		(cd "$$module" && "$(GOLANGCI_LINT)" run --config "$(REPO_ROOT)/.golangci.yml" ./...); \
	done

govulncheck:
	@set -e; for module in $(GO_MODULE_DIRS); do \
		packages="$$( $(GO) -C "$$module" list ./... 2>/dev/null )"; \
		tools="$$( $(GO) -C "$$module" list tool 2>/dev/null )"; \
		patterns=''; \
		test -z "$$packages" || patterns='./...'; \
		test -z "$$tools" || patterns="$$patterns $$tools"; \
		test -n "$$patterns" || continue; \
		printf 'govulncheck (%s)\n' "$$module"; \
		$(GOVULNCHECK) -C "$$module" $$patterns; \
	done

$(GOLANGCI_LINT): .custom-gcl.yml tools/ci/go.mod tools/ci/go.sum tools/omnaralint/go.mod tools/omnaralint/go.sum $(OMNARALINT_SOURCES)
	@mkdir -p .tools
	$(CI_TOOL) github.com/golangci/golangci-lint/v2/cmd/golangci-lint custom

race-machinedaemon:
	$(GO) test -race ./internal/machinedaemon/...

openapi-generate:
	$(OAPI_CODEGEN) -config api/openapi/oapi-codegen.yaml api/openapi/openapi.yaml

openapi-check:
	@$(OAPI_CODEGEN) -config api/openapi/oapi-codegen.yaml api/openapi/openapi.yaml; \
	untracked="$$(git ls-files --others --exclude-standard api/openapi internal/httpapi/openapi)"; \
	test -z "$$untracked" || { printf 'untracked openapi-owned files:\n%s\n' "$$untracked"; exit 1; }; \
	git diff --exit-code -- api/openapi internal/httpapi/openapi

openapi-compat-fixture-check:
	@tools/ci/openapi-compat/check-fixtures.sh $(OASDIFF_BREAKING)

openapi-compat-check:
	@test -n "$(COMPAT_BASE_SHA)" || { printf 'COMPAT_BASE_SHA is required\n'; exit 2; }
	$(OASDIFF_BREAKING) \
		--err-ignore tools/ci/openapi-compat/approved-breaking-changes.txt \
		--warn-ignore tools/ci/openapi-compat/approved-breaking-changes.txt \
		--format $(OASDIFF_FORMAT) \
		"$(COMPAT_BASE_SHA):api/openapi/openapi.yaml" api/openapi/openapi.yaml

migration-create:
	@test -n "$(NAME)" || { printf 'NAME is required\n'; exit 1; }
	$(GOOSE) -env=none -dir migrations create "$(NAME)" sql

state-migration-create:
	@test -n "$(NAME)" || { printf 'NAME is required\n'; exit 1; }
	$(GOOSE) -env=none -dir internal/machinedaemon/statedb/migrations create "$(NAME)" sql

migration-fix:
	@for dir in $(MIGRATION_DIRS); do \
		$(GOOSE) -env=none -dir "$$dir" fix || exit $$?; \
		for file in "$$dir"/[0-9][0-9][0-9][0-9][0-9]_*.sql \
			"$$dir"/[0-9][0-9][0-9][0-9][0-9]_*.go; do \
			test -e "$$file" || continue; \
			mv "$$file" "$$dir/0$$(basename "$$file")" || exit $$?; \
		done; \
	done

migration-check:
	@for dir in $(MIGRATION_DIRS); do \
		$(GOOSE) -env=none -dir "$$dir" validate || exit $$?; \
		if down_annotations="$$(grep -niE '^[[:space:]]*--.*[+]goose.*down.*$$' "$$dir"/*.sql)"; then \
			printf '%s\n' "$$down_annotations"; \
			printf '%s contains a Down migration; committed migrations are forward-only\n' "$$dir"; \
			exit 1; \
		else \
			grep_status=$$?; \
			test "$$grep_status" -eq 1 || exit "$$grep_status"; \
		fi; \
		if test "$$dir" = "internal/machinedaemon/statedb/migrations"; then \
			if no_transaction="$$(grep -niE '^[[:space:]]*--[[:space:]]*[+]goose[[:space:]]+no[[:space:]]+transaction[[:space:]]*$$' "$$dir"/*.sql)"; then \
				printf '%s\n' "$$no_transaction"; \
				printf '%s migrations must be transactional\n' "$$dir"; \
				exit 1; \
			else \
				grep_status=$$?; \
				test "$$grep_status" -eq 1 || exit "$$grep_status"; \
			fi; \
		fi; \
	done
	$(MIGRATION_CHECK) check

migration-compat-check:
	$(MIGRATION_CHECK) compare-releases $(if $(MIGRATION_RELEASE_REF_ROOT),--release-ref-root "$(MIGRATION_RELEASE_REF_ROOT)")

compatibility-check: ## Check API and released migrations after syncing with origin/main
	@tools/ci/compatibility-check.sh "$(COMPAT_BASE_SHA)" "$(COMPAT_REMOTE)" $(MAKE)

goose-version-check:
	@root="$$(GOFLAGS= $(GO) list -m -f '{{.Version}}' github.com/pressly/goose/v3)" || exit $$?; \
	tool="$$(GOFLAGS= $(GO) list -m -modfile=$(GOOSE_TOOL_MOD) -f '{{.Version}}' github.com/pressly/goose/v3)" || exit $$?; \
	test -n "$$root" && test -n "$$tool" || { \
		printf 'cannot resolve goose versions\n'; \
		exit 1; \
	}; \
	test "$$root" = "$$tool" || { \
		printf 'goose version mismatch: go.mod=%s tools/goose=%s\n' "$$root" "$$tool"; \
		exit 1; \
	}

sqlite-libc-check:
	@sqlite="$$($(GO) list -m -f '{{.Path}}@{{.Version}}' modernc.org/sqlite)"; \
	want="$$($(GO) mod graph | awk -v sqlite="$$sqlite" \
		'$$1 == sqlite && index($$2, "modernc.org/libc@") == 1 { \
			sub("^modernc[.]org/libc@", "", $$2); print $$2; exit \
		}')"; \
	got="$$($(GO) list -m -f '{{.Version}}' modernc.org/libc)"; \
	test -n "$$want" || { printf 'cannot read modernc.org/sqlite libc requirement\n'; exit 1; }; \
	test "$$got" = "$$want" || { \
		printf 'modernc.org/sqlite requires libc %s exactly; module graph selects %s\n' "$$want" "$$got"; \
		exit 1; \
	}

sqlc-generate:
	$(SQLC) generate -f sqlc.yaml

sqlc-check:
	@set -e; $(SQLC) diff -f sqlc.yaml; \
	untracked="$$(git ls-files --others --exclude-standard $(SQLC_OWNED_PATHS))"; \
	test -z "$$untracked" || { printf 'untracked sqlc-owned files:\n%s\n' "$$untracked"; exit 1; }

sql-rules:
	CGO_ENABLED=0 $(GO) -C tools/ci run ./sqlrules ../../internal/storage/queries

sqlc-vet:
	$(SQLC) vet -f sqlc.yaml

migrate-test-db:
	$(TEST_DB_ENV) $(GO) test -count=1 -run '^TestMigrateLocalDatabase$$' ./internal/testutil/integrationdb

sqlc-vet-db:
	SQLC_DATABASE_URL=$${SQLC_DATABASE_URL:-$${OMNARA_TEST_DATABASE_URL:-$(TEST_DATABASE_URL)}} $(SQLC) vet -f sqlc.vet-db.yaml

sqlc-vet-local-db: db-up migrate-test-db sqlc-vet-db

test-database-contracts:
	$(MAKE) migrate-test-db
	$(MAKE) sqlc-vet-db

unit:
	@set -e; for module in $(GO_MODULE_DIRS); do \
		packages="$$( $(GO) -C "$$module" list ./... 2>/dev/null )"; \
		test -n "$$packages" || continue; \
		printf 'go test ./... (%s)\n' "$$module"; \
		$(GO) -C "$$module" test ./...; \
	done

coverage:
	$(GO) test -covermode=atomic -coverprofile=coverage.out ./...

test-integration: clean-integration-dbs ## Run database-backed integration tests
	@run_id="$$(perl -MTime::HiRes=time -e 'printf "%x\n", int(time() * 1000000000)')"; \
	status=0; cleanup_status=0; \
	$(TEST_INFRA_ENV) OMNARA_TEST_DATABASE_RUN_ID="$$run_id" $(GO) test -count=1 -p $${INTEGRATION_TEST_P:-4} -parallel $${INTEGRATION_TEST_PARALLEL:-4} -tags=integration $(INTEGRATION_PACKAGES) || status=$$?; \
	$(TEST_DB_ENV) OMNARA_TEST_DATABASE_RUN_ID="$$run_id" OMNARA_TEST_DATABASE_CLEAN_RUN_ID="$$run_id" OMNARA_TEST_DATABASE_CLEAN_OLDER_THAN=0s $(GO) test -count=1 -run '^TestCleanStaleGeneratedDatabases$$' ./internal/testutil/integrationdb || cleanup_status=$$?; \
	test $$status -eq 0 || exit $$status; \
	exit $$cleanup_status

test-integration-storage:
	$(MAKE) test-integration INTEGRATION_TEST_PARALLEL=8 INTEGRATION_PACKAGES="$(INTEGRATION_STORAGE_PACKAGES)"

test-integration-httpapi:
	$(MAKE) test-integration INTEGRATION_PACKAGES="$(INTEGRATION_HTTPAPI_PACKAGES)"

test-integration-runtime:
	$(MAKE) test-integration INTEGRATION_PACKAGES="$(INTEGRATION_RUNTIME_PACKAGES)"

clean-integration-dbs:
	$(TEST_DB_ENV) OMNARA_TEST_DATABASE_CLEAN_OLDER_THAN=$${STALE_INTEGRATION_DB_AGE:-1h} $(GO) test -count=1 -run '^TestCleanStaleGeneratedDatabases$$' ./internal/testutil/integrationdb

# Keep INTEGRATION_PACKAGES equal to packages that gain test files when the
# integration build tag is enabled. go list evaluates build constraints for us.
integration-packages-check:
	@expected="$$( \
		$(GO) list -f '{{.ImportPath}}' $(INTEGRATION_PACKAGES) \
			| sort -u \
	)"; \
	without="$$(mktemp)"; \
	with="$$(mktemp)"; \
	trap 'rm -f "$$without" "$$with"' EXIT; \
	$(GO) list -f '{{range .TestGoFiles}}{{$$.ImportPath}}/{{.}}{{"\n"}}{{end}}{{range .XTestGoFiles}}{{$$.ImportPath}}/{{.}}{{"\n"}}{{end}}' ./... \
		| sort -u > "$$without"; \
	$(GO) list -tags=integration -f '{{range .TestGoFiles}}{{$$.ImportPath}}/{{.}}{{"\n"}}{{end}}{{range .XTestGoFiles}}{{$$.ImportPath}}/{{.}}{{"\n"}}{{end}}' ./... \
		| sort -u > "$$with"; \
	actual="$$( \
		comm -13 "$$without" "$$with" \
			| sed 's|/[^/]*$$||' \
			| sort -u \
	)"; \
	test "$$expected" = "$$actual" || { \
		printf 'INTEGRATION_PACKAGES is out of date.\n\nExpected from Makefile:\n%s\n\nActual packages with integration tests:\n%s\n' "$$expected" "$$actual"; \
		exit 1; \
	}

tagged-packages-check:
	$(GO) test -run '^$$' -tags=integration $(INTEGRATION_PACKAGES)
	$(GO) test -run '^$$' -tags='integration servicee2e' ./internal/e2e
	$(GO) test -run '^$$' -tags='integration servicee2e webe2e' ./internal/e2e
	$(GO) test -run '^$$' -tags='integration servicee2e live' ./internal/e2e
	$(GO) test -run '^$$' -tags=live ./internal/model/openaichatcompletions ./internal/webaccess
	$(GO) test -run '^$$' -tags='integration live' ./internal/compaction
	@tmp_dir="$$(mktemp -d)"; trap 'rm -rf "$$tmp_dir"' EXIT; \
		$(GO) test -c -tags=blackbox -o "$$tmp_dir/blackbox.test" ./internal/blackbox

db-up:
	POSTGRES_HOST_PORT=$(POSTGRES_HOST_PORT) REDIS_HOST_PORT=$(REDIS_HOST_PORT) docker compose up -d --wait postgres redis minio
	docker compose run --rm minio-init

db-down:
	docker compose down

stack-up: ## Start the local development stack
	docker compose --profile app up -d --build --wait

stack-down: ## Stop the local development stack
	docker compose --profile app down

fmt:
	$(GO) fmt ./...

web-install:
	cd frontend && pnpm install

web-generate:
	cd frontend && pnpm run generate:api

web-generate-check:
	cd frontend && pnpm run generate:api
	@untracked="$$(git ls-files --others --exclude-standard frontend/packages/sdk/src/generated)"; \
	if [ -n "$$untracked" ]; then \
		echo "untracked generated files:"; echo "$$untracked"; exit 1; \
	fi
	git diff --exit-code -- frontend/packages/sdk/src/generated

build-web:
	cd frontend && pnpm install --frozen-lockfile && pnpm run generate:api && pnpm run typecheck && pnpm run build

build-api: build-web

build-api build-api-from-dist:
	mkdir -p bin
	$(GO) build -tags requirespa -o bin/omnara-api ./cmd/api

build-omnarad:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -ldflags "-X github.com/omnara-ai/omnara/internal/omnarad.version=$(OMNARAD_VERSION)" -o bin/omnarad ./cmd/daemon

web-lint:
	cd frontend && pnpm run lint

web-doctor: ## Run React Doctor against changes from REACT_DOCTOR_BASE
	cd frontend && pnpm run doctor --base "$(REACT_DOCTOR_BASE)"

web-check: ## Run the frontend test, typecheck, build, lint, and format gate
	cd frontend && pnpm install --frozen-lockfile && pnpm run generate:api
	@untracked="$$(git ls-files --others --exclude-standard frontend/packages/sdk/src/generated)"; \
	if [ -n "$$untracked" ]; then \
		echo "untracked generated files:"; echo "$$untracked"; exit 1; \
	fi
	git diff --exit-code -- frontend/packages/sdk/src/generated
	cd frontend && pnpm run test && pnpm run typecheck && pnpm run build && pnpm run lint && pnpm run format:check
	$(GO) test -run '^$$' -tags=requirespa ./cmd/api

web-check-all: web-check ## Run the frontend gate plus React Doctor locally
	$(MAKE) web-doctor REACT_DOCTOR_BASE="$(REACT_DOCTOR_BASE)"

web-e2e: web-check db-up
	cd frontend && pnpm --filter @omnara/web exec playwright install $${CI:+--with-deps} --only-shell chromium
	$(SERVICE_E2E_ENV) $(GO) test -count=1 -v -run '^TestWebE2E$$' -tags='integration servicee2e webe2e' ./internal/e2e

run-web:
	@set -a; [ ! -f .env ] || . ./.env; set +a; \
	cd frontend && pnpm run dev

run-migrate:
	@set -a; [ ! -f .env ] || . ./.env; set +a; \
	OMNARA_ALLOW_INSECURE_DEV_DEFAULTS=$${OMNARA_ALLOW_INSECURE_DEV_DEFAULTS:-1} $(GO) run ./cmd/migrate

# run-api/run-worker/run-maintenance watch cmd/ and internal/ and rebuild plus
# restart the service on change. SIGINT with a kill delay lets the service shut
# down gracefully before air kills it.
define RUN_SERVICE
	set -a; [ ! -f .env ] || . ./.env; set +a; \
	OMNARA_ALLOW_INSECURE_DEV_DEFAULTS=$${OMNARA_ALLOW_INSECURE_DEV_DEFAULTS:-1} \
	OMNARA_PUBLIC_URL=$${OMNARA_PUBLIC_URL:-http://localhost:5173} \
	$(AIR) --tmp_dir tmp \
	  --build.cmd "$(GO) build -o tmp/air-$(1) ./cmd/$(1)" \
	  --build.bin tmp/air-$(1) \
	  --build.log air-$(1)-errors.log \
	  --build.include_dir cmd,internal \
	  --build.include_ext go \
	  --build.send_interrupt true \
	  --build.kill_delay 5s
endef

run-api:
	@$(call RUN_SERVICE,api)

run-worker:
	@$(call RUN_SERVICE,worker)

run-maintenance:
	@$(call RUN_SERVICE,maintenance)

test-service-e2e: db-up ## Run deterministic service end-to-end tests
	@tests="$$( $(GO) test -tags='integration servicee2e' -list '^Test' ./internal/e2e \
		| awk -v shard="$(SERVICE_E2E_SHARD_INDEX)" -v total="$(SERVICE_E2E_SHARD_TOTAL)" \
			'/^Test/ { if ((count++ % total) == shard) { printf "%s%s", separator, $$0; separator = "|" } }' \
	)"; \
	test -n "$$tests" || { printf 'service E2E shard %s/%s has no tests\n' "$(SERVICE_E2E_SHARD_INDEX)" "$(SERVICE_E2E_SHARD_TOTAL)"; exit 1; }; \
	$(SERVICE_E2E_ENV) $(GO) test -count=1 -v -timeout=20m -tags='integration servicee2e' -run "^($$tests)$$" ./internal/e2e

test-live-web:
	@$(LOAD_DOTENV); \
	$(GO) test -count=1 -v -tags=live ./internal/webaccess

test-live-openai-responses:
	@$(LOAD_DOTENV); \
	: "$${OPENAI_API_KEY:?OPENAI_API_KEY is required for live OpenAI Responses tests}"; \
	$(SERVICE_E2E_ENV) $(GO) test -count=1 -v -timeout=25m -tags='integration servicee2e live' ./internal/e2e -run '^TestServiceE2ELiveOpenAIResponses(ModelTurn|CompactionRecall|DockerDaemonProcessTools)$$' && \
	$(TEST_DB_ENV) $(GO) test -count=1 -v -tags='integration live' ./internal/compaction -run '^TestRunnerLiveOpenAIResponsesCompactionCreatesCheckpoint$$'

test-live-openai-chat-completions:
	@$(LOAD_DOTENV); \
	: "$${OPENAI_API_KEY:?OPENAI_API_KEY is required for live OpenAI Chat Completions tests}"; \
	$(GO) test -count=1 -v -tags=live ./internal/model/openaichatcompletions -run '^TestLiveOpenAIChatCompletionsText$$' && \
	$(SERVICE_E2E_ENV) $(GO) test -count=1 -v -timeout=25m -tags='integration servicee2e live' ./internal/e2e -run '^TestServiceE2ELiveOpenAIChatCompletions(ModelTurn|CompactionRecall|DockerDaemonProcessTools)$$' && \
	$(TEST_DB_ENV) $(GO) test -count=1 -v -tags='integration live' ./internal/compaction -run '^TestRunnerLiveOpenAIChatCompletionsCompactionCreatesCheckpoint$$'

test-live-openrouter:
	@$(LOAD_DOTENV); \
	: "$${OPENROUTER_API_KEY:?OPENROUTER_API_KEY is required for live OpenRouter tests}"; \
	$(GO) test -count=1 -v -tags=live ./internal/model/openaichatcompletions -run '^TestLiveOpenRouterChatCompletions' && \
	$(SERVICE_E2E_ENV) $(GO) test -count=1 -v -timeout=25m -tags='integration servicee2e live' ./internal/e2e -run '^TestServiceE2ELiveOpenRouter(ModelTurn|CompactionRecall|DockerDaemonProcessTools)$$' && \
	$(TEST_DB_ENV) $(GO) test -count=1 -v -tags='integration live' ./internal/compaction -run '^TestRunnerLiveOpenRouterCompactionCreatesCheckpoint$$'

test-live-anthropic:
	@$(LOAD_DOTENV); \
	: "$${ANTHROPIC_API_KEY:?ANTHROPIC_API_KEY is required for live Anthropic tests}"; \
	$(SERVICE_E2E_ENV) $(GO) test -count=1 -v -timeout=25m -tags='integration servicee2e live' ./internal/e2e -run '^TestServiceE2ELiveAnthropic(ModelTurn|CompactionRecall|DockerDaemonProcessTools)$$' && \
	$(TEST_DB_ENV) $(GO) test -count=1 -v -tags='integration live' ./internal/compaction -run '^TestRunnerLiveAnthropicCompactionCreatesCheckpoint$$'

test-live-api-format-switching:
	@$(LOAD_DOTENV); \
	: "$${OPENAI_API_KEY:?OPENAI_API_KEY is required for live API-format switching tests}"; \
	: "$${OPENROUTER_API_KEY:?OPENROUTER_API_KEY is required for live API-format switching tests}"; \
	: "$${ANTHROPIC_API_KEY:?ANTHROPIC_API_KEY is required for live API-format switching tests}"; \
	$(SERVICE_E2E_ENV) $(GO) test -count=1 -v -timeout=20m -tags='integration servicee2e live' ./internal/e2e -run '^TestServiceE2ELiveAPIFormatSwitchingPreservesHistory$$'

test-live-sandbox-providers:
	@$(LOAD_DOTENV); \
	$(GO) test -count=1 -v \
		./internal/machinepool/providers/blaxel \
		./internal/machinepool/providers/daytona \
		./internal/machinepool/providers/unikraft \
		-run '^Test(Blaxel|Daytona|Unikraft)ProviderLiveSmoke$$'

test-live: test-live-web test-live-openai-responses test-live-openai-chat-completions test-live-openrouter test-live-anthropic test-live-api-format-switching test-live-sandbox-providers

# Black-box API suite against a deployed control plane.
# The -timeout must stay comfortably above the suite's worst-case waits (~9m of
# live-turn polling): a package timeout panics the process before TestMain can
# tear down the model stacks and scrub the live provider key.
test-blackbox:
	@$(LOAD_DOTENV); \
	: "$${OMNARA_BLACKBOX_API_URL:?OMNARA_BLACKBOX_API_URL is required (e.g. https://api.example.com)}"; \
	: "$${OMNARA_BLACKBOX_TOKEN:?OMNARA_BLACKBOX_TOKEN is required (a personal access token for the target)}"; \
	$(GO) test -count=1 -v -timeout=20m -tags=blackbox ./internal/blackbox $(if $(RUN),-run '$(RUN)')

docs-openapi:
	{ echo "# Published documentation view of api/openapi/openapi.yaml. Do not edit by hand; run 'make docs-openapi' after changing the canonical spec."; \
	  sed 's#^  /api/v1/#  /#' api/openapi/openapi.yaml; \
	  echo "# The hosted API exposes canonical /api/v1 routes at https://api.omnara.com/v1."; \
	  echo "servers:"; \
	  echo "  - url: https://api.omnara.com/v1"; \
	  echo "    description: Hosted Omnara"; } > docs/api-reference/openapi.yaml
	@echo "docs-openapi: spec copied. Mintlify auto-generates the Endpoints pages from it at build time."

docs-openapi-check:
	@tmp="$$(mktemp)"; \
	trap 'rm -f "$$tmp"' EXIT; \
	{ echo "# Published documentation view of api/openapi/openapi.yaml. Do not edit by hand; run 'make docs-openapi' after changing the canonical spec."; \
	  sed 's#^  /api/v1/#  /#' api/openapi/openapi.yaml; \
	  echo "# The hosted API exposes canonical /api/v1 routes at https://api.omnara.com/v1."; \
	  echo "servers:"; \
	  echo "  - url: https://api.omnara.com/v1"; \
	  echo "    description: Hosted Omnara"; } > "$$tmp"; \
	diff -u "$$tmp" docs/api-reference/openapi.yaml || { \
		printf '\ndocs/api-reference/openapi.yaml is stale; run make docs-openapi\n'; \
		exit 1; \
	}
