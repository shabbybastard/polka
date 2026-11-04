.PHONY: all web build run test clean

# An exact tag wins; otherwise the fork version from VERSION is used.
TAG_VERSION := $(shell git describe --tags --exact-match 2>/dev/null | sed 's/^v//')
VERSION ?= $(if $(TAG_VERSION),$(TAG_VERSION),$(shell cat VERSION 2>/dev/null || git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo dev))
# Keep build metadata free of the tag's "v" prefix even when VERSION is
# passed explicitly (e.g. VERSION=v0.1.22-fork.4).
override VERSION := $(patsubst v%,%,$(VERSION))
# deb/rpm treat "-fork.1" as a prerelease ("0.1.22~fork.1"), which sorts
# below 0.1.22. Package metadata uses "+fork.1" so fork builds are newer.
PKG_VERSION ?= $(subst -fork.,+fork.,$(VERSION))
LDFLAGS := -s -w -X main.version=$(VERSION)

# nodynamic: decode JPEG XL covers via the pure-Go wazero backend (no
# purego/CGO), so every target stays CGO-free.
export GOFLAGS := -tags=nodynamic

all: build

web:
	cd web && npm install && npm run build

build: web
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/polka ./cmd/polka
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/polka-desktop ./cmd/polka-desktop

# Installers: deb+rpm (nfpm) and the Windows installer (makensis)
package: build
	mkdir -p dist/windows
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/windows/polka.exe ./cmd/polka
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS) -H=windowsgui' -o dist/windows/polka-desktop.exe ./cmd/polka-desktop
	VERSION=$(PKG_VERSION) go run github.com/goreleaser/nfpm/v2/cmd/nfpm@latest package -f packaging/nfpm.yaml -p deb -t dist/
	VERSION=$(PKG_VERSION) go run github.com/goreleaser/nfpm/v2/cmd/nfpm@latest package -f packaging/nfpm.yaml -p rpm -t dist/
	cd packaging && makensis -DVERSION=$(VERSION) installer.nsi

run: build
	./bin/polka

test:
	go test ./...

clean:
	rm -rf bin web/dist web/node_modules
