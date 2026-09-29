# Local development. `make dev` needs nothing beyond Go and Bun; `make ui` adds a
# process-compose terminal UI (a pane per process, restart one on its own).

.PHONY: dev ui check

# A fresh clone has no web/node_modules, so `bun run dev` fails with `vite: command
# not found`. Install on first use, and again whenever the manifest or lockfile changes.
web/node_modules: web/package.json web/bun.lock
	cd web && bun install --frozen-lockfile
	@touch web/node_modules

dev ui: web/node_modules

# Backend on :8080, frontend on :5173 with /api proxied to it. Ctrl-C stops both.
dev:
	@trap 'kill 0' EXIT; \
	go run ./cmd/surface 2>&1 | sed 's/^/[be] /' & \
	(cd web && bun run dev) 2>&1 | sed 's/^/[fe] /' & \
	wait

ui:
	process-compose up

# The whole Go gate, as CI runs it.
check:
	test -z "$$(gofmt -l .)"
	go vet ./...
	golangci-lint run
	go test -race -shuffle=on -cover ./...
