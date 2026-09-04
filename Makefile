.DEFAULT_GOAL := help

.PHONY: help web test build-release clean

help:
	@printf '%s\n' \
	  'make web           Install locked dependencies and build the embedded UI' \
	  'make test          Build the UI and run all Go tests' \
	  'make build-release Build only the Linux amd64 release and SHA-256 file' \
	  'make clean         Remove generated release artifacts'

web:
	npm --prefix web ci
	npm --prefix web run build

test: web
	go test ./...

build-release:
	bash ./scripts/build-release.sh

clean:
	rm -f dist/s2am-go-linux-amd64 dist/s2am-go-linux-amd64.sha256
