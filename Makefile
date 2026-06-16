GO ?= go

.PHONY: build install test test-fresh vet clean

build:
	$(GO) build -o tidemark ./cmd/tidemark

install:
	$(GO) install ./cmd/tidemark

test:
	$(GO) test ./...

test-fresh:
	$(GO) test ./... -v -count=1

vet:
	$(GO) vet ./...

clean:
	rm -f tidemark
