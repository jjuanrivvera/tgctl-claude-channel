BINARY := tgctl-claude-channel

.PHONY: build vet test fmt tidy verify clean

build:
	go build -o bin/$(BINARY) .

vet:
	go vet ./...

test:
	go test ./...

fmt:
	gofmt -l -w .

tidy:
	go mod tidy

# Acceptance gate: compiles, passes vet, and all tests are green.
verify: build vet test
	@echo "verify: OK"

clean:
	rm -rf bin
