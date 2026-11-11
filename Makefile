VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/lamisator/herakles/internal/cli.Version=$(VERSION)
PLATFORMS := linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/arm64

.PHONY: build test race vet dist clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o herakles ./cmd/herakles

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...
	GOOS=windows go vet ./...

dist:
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/herakles-$(VERSION)-$$os-$$arch$$ext ./cmd/herakles || exit 1; \
	done

clean:
	rm -f herakles herakles.exe
	rm -rf dist
