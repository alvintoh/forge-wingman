# Forge Wingman

An unattended delivery tool: it picks up Linear tickets delegated to it, plans and
builds each one in an isolated run, and leaves a pull request for review.

**The design lives in `docs/tech-design-v1.md` and `docs/adr/`.** They are copied here from the
forge-vault vault, which is where they are edited — never edit them in this repo.

## Shape

One repository, one product, two projects — the Go module and `web/` — and three
deployables sharing one store. The projects share no packages across the language
line, so there is no workspace and no `apps/` level:

| Path | Deployable | Trigger |
|---|---|---|
| `cmd/dispatcher` | Cloud Run job | Cloud Scheduler, every 15 min |
| `cmd/runner` | Go binary in a per-repo GitHub Actions workflow | dispatched per run |
| `cmd/surface` | Cloud Run service behind IAP, serving `web/` | HTTP |
| `web/` | Vite + React + TanStack Router/Form SPA, embedded in `surface` | — |

## Develop

```sh
cd web && bun install     # once
make dev                  # backend :8080 + frontend http://localhost:5173, Ctrl-C stops both
make ui                   # the same, in a process-compose UI: a pane per process
```

For the single binary exactly as it ships: `cd web && bun run build && cd .. && go run ./cmd/surface`,
then http://localhost:8080. `make ui` needs `brew install f1bonacc1/tap/process-compose`.

## Checks

```sh
make check
cd web && bun run lint && bun run fmt:check && bun run build
```

Go's `go.mod` carries a **1.22 floor** (stdlib `ServeMux` routing) and the **toolchain**
that builds it; raise the toolchain deliberately.
