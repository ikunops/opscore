.PHONY: build run test clean

BINARY=opscore
CMD_DIR=cmd/opscore

build:
	go build -o $(BINARY) ./$(CMD_DIR)

run:
	go run ./$(CMD_DIR)

test:
	go test ./...

clean:
	rm -f $(BINARY)

fmt:
	go fmt ./...

vet:
	go vet ./...
