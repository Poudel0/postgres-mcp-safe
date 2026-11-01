test:
	go test -count=1 ./...

golden:
	go test -count=1 -run TestCompat -update ./...

build:
	CGO_ENABLED=0 go build -trimpath -o postgres-mcp-safe .

.PHONY: test golden build
