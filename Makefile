BINARY := kubox
VERSION ?= dev

.PHONY: build test vet lint fmt clean tidy install

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/$(BINARY) ./cmd/kubox

test:
	go test ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -w cmd internal

tidy:
	go mod tidy

install: build
	install -m 0755 bin/$(BINARY) $(or $(KUBOX_INSTALL_DIR),/usr/local/bin)/$(BINARY)

clean:
	rm -rf bin
