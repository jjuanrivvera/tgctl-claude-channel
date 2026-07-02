BINARY    := tgctl-claude-channel
COVER_MIN ?= 80

.PHONY: build vet lint test cover fmt fmtcheck tidy verify hooks clean

build:
	go build -o bin/$(BINARY) .

vet:
	go vet ./...

lint:
	golangci-lint run

test:
	go test -race ./...

cover:
	./scripts/cover-check.sh $(COVER_MIN)

fmt:
	gofmt -w .

fmtcheck:
	@out=$$(gofmt -l .); [ -z "$$out" ] || { echo "gofmt needed:"; echo "$$out"; exit 1; }

tidy:
	go mod tidy

# Acceptance gate: formatted, vets, lints, tests pass, coverage floor held.
verify: fmtcheck vet lint cover
	@echo "verify: OK"

hooks:
	./scripts/install-hooks.sh

clean:
	rm -rf bin cover.out
