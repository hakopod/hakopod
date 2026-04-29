SHELL := /bin/bash
.PHONY: build test check local-up local-down bootstrap api dashboard

build:
	GOMAXPROCS=2 go build -p 2 -trimpath -ldflags='-s -w' -o bin/hakopod ./cmd/hakopod
	GOMAXPROCS=2 go build -p 2 -trimpath -ldflags='-s -w' -o bin/hakopod-server ./cmd/hakopod-server

test:
	GOMAXPROCS=2 go test -p 2 ./...
	pnpm --dir web test

check: test
	GOMAXPROCS=2 go vet -p 2 ./...
	pnpm --dir web typecheck
	pnpm --dir web build

local-up:
	./scripts/local-up.sh

local-down:
	./scripts/local-down.sh

bootstrap: build
	source .local/env && bin/hakopod-server bootstrap --key-file .local/admin-key

api:
	source .local/env && exec bin/hakopod-server

dashboard:
	HAKOPOD_API_URL=http://127.0.0.1:8080 pnpm --dir web dev --port 3001
