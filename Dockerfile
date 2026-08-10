# cube-cos-driver server image: SPA build -> static Go binary -> alpine.
FROM node:24-alpine AS web
WORKDIR /src
COPY . .
RUN corepack enable && pnpm install --frozen-lockfile && pnpm -C web build

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY . .
COPY --from=web /src/web/dist internal/webui/dist
# Version stamp: passed as a build-arg by CI (git describe); "dev" for a plain
# docker build. Injected into main.version so /api/v1/version reports the build.
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/cube-cos-driver ./cmd/cube-cos-driver && \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/phone-home-agent ./cmd/phone-home-agent

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=build /out/cube-cos-driver /usr/local/bin/cube-cos-driver
# Served at /api/v1/agents/binary for installer hot-update (found next to the
# driver binary) — keeps the agent the image was built from, not a stale commit.
COPY --from=build /out/phone-home-agent /usr/local/bin/phone-home-agent
ENV DATA_DIR=/var/lib/cube-cos-driver
VOLUME /var/lib/cube-cos-driver
EXPOSE 3001
ENTRYPOINT ["/usr/local/bin/cube-cos-driver"]
