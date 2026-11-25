SRC=*.go internal/.

default: fmt lint install generate

build:
	go build -v ./...

install: build
	go install -v ./...

generate:
	cd tools && go generate ./...

lint:
	golangci-lint run

fmt:
	gofmt -s -w -e $(SRC)

test:
	go test -v -race -cover -timeout=120s -parallel=10 ./...

testacc:
	TF_ACC=1 go test -v -cover -timeout 120m ./...

.PHONY: fmt lint test testacc build install generate
