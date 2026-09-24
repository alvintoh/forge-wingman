# acctsvc

Collects account records from the upstream directory and resolves them for
downstream consumers.

    go build ./...
    go test ./...
    go run ./cmd/collector

## Layout

| Path | Holds |
|---|---|
| `cmd/collector` | the entry point |
| `internal/collect` | the collector and its request pacing |
| `internal/directory` | the upstream account service |
| `internal/store` | the lookup path |
