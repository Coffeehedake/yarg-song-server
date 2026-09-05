BINARY  := yarg-song-server
PKG     := ./cmd/yarg-song-server
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test lint tidy clean release docker

build:
	go build -ldflags '$(LDFLAGS)' -o bin/$(BINARY) $(PKG)

test:
	go test ./... -race -count=1

lint:
	go vet ./...
	gofmt -l .

tidy:
	go mod tidy

clean:
	rm -rf bin dist

## release: every target platform the project promises to support
release:
	@mkdir -p dist
	GOOS=linux   GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-linux-amd64   $(PKG)
	GOOS=linux   GOARCH=arm64 go build -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-linux-arm64   $(PKG)
	GOOS=linux   GOARCH=arm GOARM=7 go build -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-linux-armv7 $(PKG)
	GOOS=darwin  GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-darwin-amd64  $(PKG)
	GOOS=darwin  GOARCH=arm64 go build -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-darwin-arm64  $(PKG)
	GOOS=windows GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/$(BINARY)-windows-amd64.exe $(PKG)

docker:
	docker buildx build --platform linux/amd64,linux/arm64 -t yarg-song-server:$(VERSION) .
