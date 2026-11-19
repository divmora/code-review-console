.PHONY: web web-dev build clean test test-db test-e2e fmt lint docker-build run

BINARY  ?= code-review-console
IMAGE   ?= ghcr.io/divmora/code-review-console
VERSION ?= 0.0.0-dev
LDFLAGS := -s -w -X github.com/divmora/code-review-console/internal/version.Version=$(VERSION)
BOT_DIR ?= ../code-review-bot

# Admin UI: web/ (React + Vite) builds into internal/admin/ui, which the
# binary embeds. The build output is committed so `go build` alone works.
web:
	@cd web && npm ci --no-audit --no-fund && npm run build

# Vite dev server on :5173, proxying the API to a console on :8080.
web-dev:
	@cd web && npm run dev

build:
	@go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/console

clean:
	@rm -rf bin/ dist/ coverage.out coverage.html

test:
	@go test -race ./...

# PostgreSQL integration tests against a throwaway container.
test-db:
	@docker run -d --rm --name crc-test-pg -e POSTGRES_PASSWORD=test -e POSTGRES_DB=console -p 127.0.0.1:55434:5432 postgres:17 >/dev/null
	@until docker exec crc-test-pg pg_isready -U postgres -q; do sleep 1; done
	@CODEREVIEW_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55434/console go test -race -count=1 ./... ; s=$$?; docker stop crc-test-pg >/dev/null; exit $$s

# End-to-end with the real codereview CLI (local + docker executors).
test-e2e:
	@cd $(BOT_DIR) && go build -o bin/codereview ./cmd/codereview && docker build -q -t code-review-bot:e2e . >/dev/null
	@CRB_E2E_BIN=$(abspath $(BOT_DIR))/bin/codereview go test -count=1 -run TestEndToEnd ./internal/server/
	@CRB_E2E_IMAGE=code-review-bot:e2e go test -count=1 -run TestEndToEnd ./internal/server/

fmt:
	@gofmt -w .

lint:
	@golangci-lint run ./... || go vet ./...

docker-build:
	@docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):dev .

run: build
	@./bin/$(BINARY) serve
