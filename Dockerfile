# cube-cos-driver server image: SPA build -> static Go binary -> alpine.
FROM node:24-alpine AS web
WORKDIR /src
COPY . .
RUN corepack enable && pnpm install --frozen-lockfile && pnpm -C web build

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY . .
COPY --from=web /src/web/dist internal/webui/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' \
      -o /out/cube-cos-driver ./cmd/cube-cos-driver

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=build /out/cube-cos-driver /usr/local/bin/cube-cos-driver
ENV DATA_DIR=/var/lib/cube-cos-driver
VOLUME /var/lib/cube-cos-driver
EXPOSE 3001
ENTRYPOINT ["/usr/local/bin/cube-cos-driver"]
