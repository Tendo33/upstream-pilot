.DEFAULT_GOAL := help
.PHONY: help web build test integration run demo-upstream release
help:
	@printf '%s\n' 'make build       Build UI and executable' 'make test        Build UI and run Go tests' 'make integration Run PostgreSQL integration and race tests' 'make run         Run with exported S2AM_* configuration' 'make demo-upstream Start local upstream simulator' 'make release     Linux release (TARGET_ARCH=amd64 or arm64)'
web:
	npm --prefix web ci
	npm --prefix web run build
build: web
	go build -trimpath -o bin/upstream-manager ./cmd/upstream-manager
test: web
	go test ./...
integration:
	@test -n "$$SUB2UPSTREAM_TEST_DATABASE_URL" || (printf 'Set SUB2UPSTREAM_TEST_DATABASE_URL to an isolated PostgreSQL database\n' >&2; exit 1)
	go test -race ./...
run:
	go run ./cmd/upstream-manager
demo-upstream:
	go run ./qa/fake-sub2api
release:
	bash scripts/build-release.sh
