# 0009 — Go for all three components; Rust is admissible here, and not indicated

- **Date:** 2026-09-21
- **Status:** accepted

## Context

This record exists because *"Go is the default"* is not an argument, and the owner
asked for the two to be weighed evenly rather than for a default to be applied.
It was argued at length and reached Rust once on the way; what follows is the
evidence that settled it, including the claim that was wrong.

`engineering-defaults` makes Go the default by the seam test — *"a standalone
service, webhook receiver, media/PDF processor, **scheduled job** or proxy has no
seam to lose, so Go is its plain merit"* — and `ROADMAP.md` §6 routes Rust through
three narrow doors. Both are cited; neither settles it.

### Performance decides nothing here, and that is established first

Every component is **I/O-bound** — the CPU sits idle waiting on a network response.
One dispatcher poll:

| Step | Wall clock | CPU busy |
|---|---|---|
| HTTP to Linear | ~200ms | idle |
| parse the response | ~1ms | busy |
| Firestore read | ~30ms | idle |
| sizing rules | ~1ms | busy |
| Firestore write | ~50ms | idle |
| **total** | **~280ms** | **~2ms** |

Rust is genuinely 1.5-3x faster than Go on CPU-bound work. Applied to ~2ms that
returns under 1.5ms of a 280ms poll. **And Cloud Run bills vCPU-seconds of wall
clock, not CPU cycles**, so a process blocked on an HTTP response bills identically
in either language. The runner is the same shape at a larger scale — it spawns
`opencode run` and waits minutes for a remote model, because NFR-6 forbids local
inference.

⚠️ **The table closes the performance door in BOTH directions.** It is why no
profiler-proven escalation to Rust exists, and equally why Go is not being claimed
as fast enough by luck. Nothing here is CPU-bound or memory-bound.

### ⚠️ The ECOSYSTEM objection was wrong, and it is recorded because a draft relied on it

A first pass rejected Rust on ecosystem, contrasting Google's
`google-cloud-firestore-admin-v1` at *"~790 recent downloads"* against *"the
community `firestore` crate"*. **That compared a number against an adjective, and
the missing number reverses it.** Measured on crates.io, 2026-09-21:

| Crate | Recent downloads | Total | Latest |
|---|---|---|---|
| **`firestore`** (`abdolence/firestore-rs`) | **289,052** | 1,149,952 | 0.55.0, **2026-09-20** |
| `google-cloud-firestore-admin-v1` (Google) | ~790 | — | — |

The community crate is ~350x more used than Google's own — and the admin crate is
the **wrong plane** anyway. It ships ADC and the GCP metadata server, transactions,
`count`/`sum`/`avg`, and emulator support: everything a hand-rolled client would
owe. **`google-cloud-storage` is first-party Rust at v1.18.0.** Rust's HTTP story
(`axum`) is arguably better than Go's stdlib.

**So Rust is ADMISSIBLE here. It is not blocked by the ecosystem, and anyone
re-reading this record must not re-raise that objection.** The distinction that
took an afternoon to see: **admissible is not indicated.**

### What actually decides it — four things, none about capability

**1 — The workload sits in Go's column on every axis.** The published split is by
*shape*: cloud infrastructure, microservices, REST APIs, CLIs, DevOps tooling,
scheduled jobs and rapid iteration go left; systems, embedded, WASM, blockchain,
game engines and latency-SLO network paths go right. **This product is a poller, a
REST surface behind IAP and a subprocess runner.** Nothing it does appears in
Rust's column.

**2 — The canonical Go→Rust migration does not apply, and citing it would be an
error.** Discord's Read States rewrite required a hot path hit on every connect,
send and read; a large in-memory LRU cache; a latency SLO; and **GC pauses measured
as the cause** (10-40ms spikes every two minutes). **This product wakes 96 times a
day, caches nothing and has no latency SLO. Zero of four preconditions hold.**

**3 — The compile loop, and its timing.** Go rebuilds incrementally in **under a
second** against Rust's ~8s, or ~3-5s via `cargo check`; clean builds are 2-10s
against 30-120s, and a ~14k-LOC service with ~500 crates measured ~10 min cold in
Docker. **Compile times are the #1 sustained complaint in the State of Rust survey
— >27% of 7,156 respondents — and velocity drops during adoption are reported
consistently enough to be a pattern.** The owner's own weighting, 2026-09-21: *"i
will probably be tuning like mad for the first few tickets."* **That is exactly the
window the cost lands in.**

**4 — Rust would be a FOURTH ecosystem maintained solo**, after TypeScript, Python
and Go — the cost `engineering-defaults` names as the one that *"bites hardest
solo"*. `ROADMAP.md` §6's narrow Rust doors exist for this reason and no other.

**The counter-example worth keeping, because it cuts against the obvious:** the
TypeScript team ported `tsc` to **Go**, not Rust, despite a compiler being
CPU-bound. It was a **port**, and semantic closeness to the original outranked peak
throughput. **The shape of the work can outrank the shape of the workload.**

### What Rust would genuinely have won, so it is not rediscovered as a smear

1. **Exhaustive `match` on the gate classes.** FR-7 through FR-12 are
   safety-critical, and a Go `switch` on a string can fall through silently where a
   Rust `match` on an enum will not compile.
2. **Unignorable errors.** A swallowed error in an unattended dispatcher is a
   *silent* failure — what FR-23 exists to catch. `Result` and `?` make ignoring one
   harder than handling it.

**Both are real, and both are the THIRD layer rather than the only one.** The gates
are already enforced by opencode's per-agent `permission` block and by the withheld
`GITHUB_TOKEN` scopes, and `exhaustive` + `errcheck` cover most of the language
gap. That is what makes them insufficient to outweigh the four findings above — not
that they are wrong.

## Decision

**Go for the dispatcher, the runner and the surface** — on **workload fit and
ecosystem cost**, with performance explicitly not relied upon and the ecosystem
objection against Rust explicitly withdrawn as mismeasured.

## Consequences

- ⚠️ **`exhaustive` and `errcheck` are REQUIRED CI gates, not optional lint.** They
  are standing in for a type system, which is the honest cost of this decision, and
  they are the mitigation the reasoning above depends on. Treat a failure as a
  build break.
- **One Go module, two entrypoints** (`cmd/dispatcher`, `cmd/surface`) plus the
  runner binary, sharing the data layer and the run-record types. One Firestore
  binding, first-party.
- **The compile loop is the win being banked, so protect it** — sub-second
  incremental rebuilds through the tuning phase are a stated reason for this
  decision, and a build that creeps toward tens of seconds has eroded it.
- **`adr/0006`'s Vite SPA and `embed.FS` stand unchanged**, as do `adr/0003`,
  `adr/0004`, `adr/0007` and `adr/0008`.

### The Rust port is a named later option, not a consolation

**This decision is reversible per component, and cheaply:** the three communicate
through Firestore and process boundaries, never shared types, so any one can become
a Rust binary without touching the other two.

⚠️ **That is the RIGHT way to reach Rust here, and it is better than starting in
it.** `rust.md` says *learn it on a PORT, never a greenfield*, because an existing
implementation is an **oracle** — you can diff the new component's behaviour
against the old one and know immediately when you are wrong. **Building in Go first
does not defer Rust; it creates the conditions under which Rust can be learned
safely.** The natural candidate is the **surface**: least unattended, smallest, and
`axum` suits it.

### Revisit triggers

| Revisit | When |
|---|---|
| this record | a **profiler** proves a Go bottleneck, per `ROADMAP.md` §6 — the wall-clock table above is why no candidate exists |
| the CI gates | never relax them; they are load-bearing for this decision rather than hygiene |
| the Rust port | after the first phase ships, on the surface, against the working Go implementation as the oracle |
| **not** this record | the ecosystem. That objection was measured, found wrong, and is recorded above so it is not raised again |
