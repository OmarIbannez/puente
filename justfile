# Puente task runner. `just` lists recipes.

default:
    @just --list

# Single quality gate: Go and (once it exists) the web app. Runs everything, reports all failures.
ci:
    #!/usr/bin/env bash
    set -o pipefail
    failed=0
    step() { echo "==> $*"; "$@" || failed=1; }
    unformatted="$(gofmt -l cmd internal 2>/dev/null)"
    if [ -n "$unformatted" ]; then echo "gofmt needed:"; echo "$unformatted"; failed=1; fi
    step go vet ./...
    step golangci-lint run ./...
    step go test -race ./...
    if [ -f web/package.json ]; then
        step npm --prefix web run lint
        step npm --prefix web run test -- --run
        step npm --prefix web run build
    fi
    exit $failed

install:
    go mod download
    if [ -f web/package.json ]; then npm --prefix web ci; fi

# Postgres in Docker on :5435, migrated, API on :8000
dev:
    #!/usr/bin/env bash
    set -euo pipefail
    export DATABASE_URL="postgres://puente:puente@localhost:${PUENTE_DB_PORT:-5435}/puente?sslmode=disable"
    docker compose up -d --wait db
    go run ./cmd/puente migrate
    go run ./cmd/puente serve

# Vite dev server on :5173, proxying /api to :8000
web:
    npm --prefix web run dev

# Everything in containers, seeded, on http://localhost:8000 (PUENTE_PORT to change it)
up:
    docker compose up --build

# Stop the containers and drop the local database volume
down:
    docker compose down -v

# Build and start the whole stack in Docker, then run the end-to-end smoke test
smoke:
    docker compose up -d --build --wait
    ./scripts/smoke.sh http://localhost:${PUENTE_PORT:-8000}

# Recompile the test ERC-20 used by the EVM adapter tests (needs Docker)
contracts:
    docker run --rm --platform linux/amd64 -v "$PWD/contracts:/s" ethereum/solc:0.8.28 --bin --optimize -o /s --overwrite /s/TestToken.sol
