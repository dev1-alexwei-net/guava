.PHONY: fmt vet test lint

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './.git/*')

vet:
	go vet ./...

test:
	go test ./...

lint:
	go vet ./...
	@test -z "$$(gofmt -l .)"
