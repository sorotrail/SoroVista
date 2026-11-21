.PHONY: build run vet test test-db lint fmt ci up down clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

build:
	go build -ldflags "-X github.com/sorotrail/sorovista/internal/buildinfo.Version=$(VERSION) -X github.com/sorotrail/sorovista/internal/buildinfo.Commit=$(COMMIT) -X github.com/sorotrail/sorovista/internal/buildinfo.Date=$(DATE)" -o bin/sorovista ./cmd/sorovista

run: build
	./bin/sorovista

vet:
	go vet ./...

test:
	@if [ -z "$${TEST_DATABASE_URL}" ]; then \
		echo "note: TEST_DATABASE_URL is unset; the Postgres integration tests in internal/store will skip (run make up then make test-db for the database pass)"; \
	fi
	go test ./...

# Run every test including the Postgres integration tests, against the
# docker-compose database (make up first, or point at your own Postgres).
test-db:
	TEST_DATABASE_URL=$${TEST_DATABASE_URL:-postgres://sorovista:sorovista@localhost:5432/sorovista?sslmode=disable} go test ./...

lint:
	golangci-lint run

fmt:
	gofmt -w .

# ci mirrors the CI pipeline: build, vet, test, lint, in CI's order,
# stopping at the first failure, so "did I break CI" is one command. It
# composes the targets above rather than duplicating their commands, so the
# two cannot drift. The lint step needs golangci-lint v2.12.2, the version
# CI pins. The test step prints a note when TEST_DATABASE_URL is unset: the
# Postgres integration tests then skip, and `make ci` is not proof the store
# works without a database. Run `make up` first if you want the database
# pass.
ci: build vet test lint

up:
	docker compose up --build -d

down:
	docker compose down

clean:
	rm -rf bin
