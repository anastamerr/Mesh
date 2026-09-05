# Native components

Go is the agreed long-term language. This is a skeleton, not an installed Windows service.

With Go 1.23 or newer, from this directory:

```sh
go run ./cmd/mesh-agent info
go run ./cmd/mesh-executor version
go vet ./...
go test ./...
GOOS=windows GOARCH=amd64 go build -o bin/mesh-agent.exe ./cmd/mesh-agent
```

Next: enrollment and secure credential storage, durable heartbeat sequence, Windows service lifecycle, then a separately authenticated WSL executor. The module name is intentionally local until the repository's public location is decided.
