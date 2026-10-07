BINARY := bin/gofetch

.PHONY: build test clean

build:
	go build -trimpath -ldflags="-s -w" -o $(BINARY) ./cmd/gofetch

test:
	go test ./...

clean:
	rm -f $(BINARY)
