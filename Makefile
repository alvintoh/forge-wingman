# Local development. `make dev` needs nothing beyond Go and Bun; `make ui` adds a
# process-compose terminal UI (a pane per process, restart one on its own).

.PHONY: dev ui check

# A fresh clone has no web/node_modules, so `bun run dev` fails with `vite: command
# not found`. Install on first use, and again whenever the manifest or lockfile changes.
web/node_modules: web/package.json web/bun.lock
	cd web && bun install --frozen-lockfile
	@touch web/node_modules

# Backend on :8080, frontend on :5173 with /api proxied to it. Ctrl-C stops both.
dev: web/node_modules
	@trap 'kill 0' EXIT; \
	go run ./cmd/surface 2>&1 | sed 's/^/[be] /' & \
	(cd web && bun run dev) 2>&1 | sed 's/^/[fe] /' & \
	wait

# process-compose serves its own HTTP API on :8080 by default, the port the backend needs,
# so the backend could never bind and the frontend waited on its health check forever.
ui: web/node_modules
	process-compose up --no-server

# The whole Go gate, as CI runs it.
check:
	test -z "$$(gofmt -l .)"
	@! git ls-files '*.go' | xargs grep -nE '//.*\bFRG-[0-9]+' | grep -v 'TODO(' || { echo 'Go comment names a ticket; say what the code does (TODO(FRG-n) only for unresolved work)'; exit 1; }
	go vet ./...
	golangci-lint run
	go test -race -shuffle=on -cover ./...
