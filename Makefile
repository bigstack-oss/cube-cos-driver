# cube-cos-snapshot — single Go binary embedding the COS UI SPA.
#
# `make all`   : build SPA + embed + binary (release artifact)
# `make build` : Go binary only (placeholder UI unless `make web` ran)
# `make test`  : Go vet + tests
# `make web`   : build the SPA and copy it into the embed dir

BIN := bin/cube-cos-snapshot
GOFLAGS := -trimpath -ldflags '-s -w'

.PHONY: all build test web ensure-dist clean

all: web build

build: ensure-dist
	CGO_ENABLED=0 go build $(GOFLAGS) -o $(BIN) ./cmd/cube-cos-snapshot

test: ensure-dist
	go vet ./...
	go test ./...

# The embed dir is gitignored; provide a placeholder so `go build` works
# without a web build.
ensure-dist:
	@mkdir -p internal/webui/dist
	@[ -f internal/webui/dist/index.html ] || printf '<!doctype html><title>cube-cos-snapshot</title><p>UI not built — run make web\n' > internal/webui/dist/index.html

web:
	pnpm install
	pnpm -C web build
	rm -rf internal/webui/dist
	cp -r web/dist internal/webui/dist

clean:
	rm -rf bin internal/webui/dist web/dist
