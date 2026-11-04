GO := $(shell command -v go)
BIN := bin/moolah
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X moolah/app.Version=$(VERSION)

.PHONY: build check fmt vet staticcheck test golden install clean

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/moolah

check: fmt vet staticcheck test

fmt:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	$(GO) vet ./...

staticcheck:
	staticcheck ./...

test:
	$(GO) test ./...

golden:
	$(GO) test ./cli/... -update

install: build
	install -m 755 $(BIN) $(HOME)/.local/bin/moolah

clean:
	rm -rf bin dist
