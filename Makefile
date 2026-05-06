.PHONY: test lint build vet fmt

build:
	go build ./...

test:
	go test -race -count=1 ./...

vet:
	go vet ./...

fmt:
	gofmt -s -w .

lint: vet
	gofmt -l . | tee /dev/stderr | (! read)
