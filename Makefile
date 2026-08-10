# cube-cos-driver — two static Go binaries + the embedded COS UI SPA.
#
# `make all`   : build SPA + embed + both binaries (release artifacts)
# `make build` : Go binaries only (placeholder UI unless `make web` ran)
# `make test`  : Go vet + tests
# `make web`   : build the SPA and copy it into the embed dir
#
# Binaries: bin/cube-cos-driver (server) + bin/phone-home-agent (node agent).

BIN := bin/cube-cos-driver
AGENT := bin/phone-home-agent
# Build-time version stamp: git describe (tag+commit), or the short SHA, with a
# -dirty suffix for uncommitted trees. Overridable: `make build VERSION=…`.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOFLAGS := -trimpath -ldflags '-s -w -X main.version=$(VERSION)'

.PHONY: all build test web ensure-dist clean

all: web build

build: ensure-dist
	CGO_ENABLED=0 go build $(GOFLAGS) -o $(BIN) ./cmd/cube-cos-driver
	CGO_ENABLED=0 go build $(GOFLAGS) -o $(AGENT) ./cmd/phone-home-agent

test: ensure-dist
	go vet ./...
	go test ./...

# The embed dir is gitignored; provide a placeholder so `go build` works
# without a web build.
ensure-dist:
	@mkdir -p internal/webui/dist
	@[ -f internal/webui/dist/index.html ] || printf '<!doctype html><title>cube-cos-driver</title><p>UI not built — run make web\n' > internal/webui/dist/index.html

web:
	pnpm install
	pnpm -C web build
	rm -rf internal/webui/dist
	cp -r web/dist internal/webui/dist

clean:
	rm -rf bin internal/webui/dist web/dist
