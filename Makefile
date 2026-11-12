VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/lamisator/herakles/internal/cli.Version=$(VERSION)
PLATFORMS := linux/amd64 linux/arm64 linux/arm windows/amd64 windows/arm64 darwin/arm64

.PHONY: build test race vet dist clean winres

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o herakles ./cmd/herakles

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...
	GOOS=windows go vet ./...

# Windows resources (icon, version, manifest) for herakles.exe; generated,
# not committed.
WINRES := go run github.com/tc-hib/go-winres@v0.3.3
NUMVER = $(shell echo $(VERSION) | sed -E 's/^v?([0-9]+\.[0-9]+\.[0-9]+).*/\1/;t;s/.*/0.0.0/')

winres:
	$(WINRES) simply --arch amd64,arm64 --out cmd/herakles/rsrc --icon packaging/windows/herakles.ico \
		--product-version $(NUMVER).0 --file-version $(NUMVER).0 --product-name Herakles \
		--file-description "Herakles Backup" --original-filename herakles.exe --copyright "Marius Klöck"

dist: winres
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch GOARM=7 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/herakles-$(VERSION)-$$os-$$arch$$ext ./cmd/herakles || exit 1; \
	done

clean:
	rm -f herakles herakles.exe cmd/herakles/rsrc_windows_*.syso
	rm -rf dist
