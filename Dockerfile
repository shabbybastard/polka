# syntax=docker/dockerfile:1
# Polka — home library server.
# Build: docker build -t polka .
# Run:   docker run -p 12791:12791 -v polka-data:/data -v /path/to/books:/books polka

# --- Frontend ---
# Build on the native build platform. Running npm/node under QEMU for the
# arm64 target is both slow and fragile (Node may hit an unsupported CPU
# instruction and leave BuildKit waiting for a dead process).
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# --- Server (pure Go, no CGO) ---
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=docker
# -tags nodynamic: decode JPEG XL covers via the pure-Go wazero backend
# (no purego/CGO), so the build stays CGO-free across all targets.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -tags nodynamic -ldflags "-s -w -X main.version=${VERSION}" -o /polka ./cmd/polka

# --- Runtime ---
FROM alpine:3.21
RUN adduser -D -H polka && mkdir -p /data /books && chown polka /data /books
COPY --from=build /polka /usr/local/bin/polka
USER polka
# Defaults via env so any subcommand (serve, import, passwd) run with
# `docker exec` uses the data/library volumes, not the user's home dir.
ENV POLKA_DATA_DIR=/data \
    POLKA_LIBRARY_DIR=/books \
    POLKA_ADDR=:12791
VOLUME ["/data", "/books"]
EXPOSE 12791
ENTRYPOINT ["polka"]
CMD ["serve"]
