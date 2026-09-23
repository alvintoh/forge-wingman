# 0007 — Identity-Aware Proxy, not an auth library

- **Date:** 2026-09-21
- **Status:** accepted

## Context

`engineering-defaults` records *"Personal → Better Auth — TS-native, Drizzle
adapter, own Cloud SQL Postgres, no MAU pricing or lock-in"*, with sessions in
httpOnly cookies.

Every premise of that pick is absent here. The product serves **exactly one
person** (`prd-v1.md` Summary), *"multi-user, tenancy, or auth beyond a single
operator"* is explicitly out of scope, there is no Cloud SQL instance for an
adapter to use (`adr/0004`), and the surface's client is a Go-served SPA rather
than a TS server runtime.

The PRD had already settled this in passing rather than as a decision — FR-23's
note reads *"every write action lives on the FR-20 surface behind IAP"*. This
record exists so the choice has a status and can be superseded, which a clause
inside another requirement cannot.

## Decision

**Identity-Aware Proxy in front of the surface's Cloud Run service**, with the
operator's Google account as the only authorised principal.

## Consequences

- **There is no authentication code.** Not less of it — none. No session store, no
  cookie handling, no token rotation, no password reset, no login screen, and no
  library to keep patched. `/tech-design` §1a records this as the highest-value
  reason to settle hosting early: *"platforms solve whole problems that look like
  application work from inside the PRD."*
- **The surface receives a verified identity header** and does not have to trust
  anything the browser sends about who the caller is.
- **It composes correctly with FR-20's actor rule.** IAP establishes *who*; the
  surface then acts on GitHub with the **operator's** credential, because FR-20
  requires each write be performed *as me, not as the runner*. Those are two
  separate concerns and IAP owns only the first.
- **The runner is unaffected and gains nothing.** It holds no inbound surface, so
  FR-11's credential gate remains its only identity control.
- **Lock-in is real and small.** IAP is GCP-specific, so moving the surface means
  implementing auth for the first time. Against a single-operator internal tool
  that is a better trade than maintaining an auth stack now — and the SPA, the Go
  handlers and the data all port regardless.
- **Superseding trigger, named so the deferral ends:** a second human needing
  access. IAP handles that too, by adding a principal; an auth library only becomes
  necessary if access must be granted to someone without a Google account.
