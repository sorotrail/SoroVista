.PHONY: build run test test-db lint fmt vet ci up down clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

build:
	go build -ldflags "-X github.com/sorotrail/sorovista/internal/buildinfo.Version=$(VERSION) -X github.com/sorotrail/sorovista/internal/buildinfo.Commit=$(COMMIT) -X github.com/sorotrail/sorovista/internal/buildinfo.Date=$(DATE)" -o bin/sorovista ./cmd/sorovista

run: build
	./bin/sorovista

# A successful `go test` discards the test binary's output, so the notice
# printed by internal/store's skip helper is invisible here. Print it from the
# make target instead, where a database-less run always shows it.
# -count=1 disables the test cache too: a cached result would never execute the
# store package at all.
test:
	@if [ -z "$$TEST_DATABASE_URL" ]; then \
		echo ""; \
		echo "[store] Postgres integration tests skipped: TEST_DATABASE_URL is not set."; \
		echo "[store] This run does NOT exercise internal/store. To run them: make up && make test-db"; \
		echo ""; \
	fi
	go test -count=1 ./...

vet:
	go vet ./...

# Everything CI runs. test-db is separate because it needs Postgres, and stays
# the authority for coverage of the store.
ci: build vet test lint

# Run every test including the Postgres integration tests, against the
# docker-compose database (make up first, or point at your own Postgres).
test-db:
	TEST_DATABASE_URL=$${TEST_DATABASE_URL:-postgres://sorovista:sorovista@localhost:5432/sorovista?sslmode=disable} go test ./...

lint:
	golangci-lint run

fmt:
	gofmt -w .

up:
	docker compose up --build -d

down:
	docker compose down

clean:
	rm -rf bin
