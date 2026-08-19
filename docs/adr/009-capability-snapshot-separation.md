# ADR-009: Capability Snapshot Separation

**Status**: Accepted
**Date**: 2026-07-22

## Context

OpsCore needs to know "what can the target host do?" for two very different
reasons, and conflating them creates bugs and audit gaps:

1. **Decisioning** — a Handler's `Plan()` must choose *how* to execute an
   Operation: `systemctl restart` vs `service restart` vs "refuse, host cannot".
   This is a live, in-process question asked at plan time.
2. **Observation** — operators and audits want a *record* of what the host
   reported it could do at a given moment, with versions and details, so they
   can diff across time (capability drift) and replay "what was true when this
   ran?". This is a frozen, serializable artifact.

Two pre-existing constructs already embody these and must be kept deliberately
separate:

- `core.CapabilityContext` (a struct) — produced by `DetectCapability()` at
  `Context.Build()` time, consulted via `ctx.Capability()`. It is the **live
  decisioning surface**: booleans like `HasSystemctl`, `HasDocker`,
  `ServiceManager`, `IsRoot`.
- `capability.Snapshot(ctx) []CapabilityInfo` — produced by stateless probes
  (`systemdProbe`, `ufwProbe`, …), each carrying `Name`, `Available`,
  `Version`, `Details`. It is the **observed snapshot surface**, surfaced via
  the builtin `system.host.capability.list` (`CollectStep`) and
  `GET /api/capabilities`.

The risk of merging them: if Handlers branch on the rich snapshot, they couple
control-flow to probe versions/details that are noisy and platform-specific; if
the snapshot is treated as live state, it drifts from the real host and audits
lie. We need an explicit boundary.

This also reinforces ADR-004 ("Code Owns Capability, Database Owns
Assignment") and the architecture-review rule that **capability is kernel
state, not a business operation**: a probe is not a command to change the
system, so it must never become a `CommandStep` and must never flow through the
Operation → Handler → Plan → Executor(shell) chain.

## Decision

Keep two distinct capability surfaces with a hard separation:

### Surface A — Live CapabilityContext (decisioning)
- Struct `core.CapabilityContext`, attached to every `Context`.
- Built once at `Context.Build()` via `DetectCapability()` (or explicitly via
  `WithCapability` for tests/plugin hosts). It reflects the *live* target at
  plan time.
- The **only** surface Handlers may read to choose execution strategy.
  Membership is intentionally small and boolean — cheap to reason about.

### Surface B — CapabilitySnapshot (observation)
- `capability.Snapshot(ctx)` returns `[]CapabilityInfo`, sorted by name for
  deterministic output (tests/diffs). Each probe is stateless and cheap.
- Collected in-process by `capability.CollectStep` (no shell, no remote hop),
  never via `CommandStep`. This is the enforcement of the "kernel state, not an
  operation" rule.
- The **only** surface that carries versions/details and is serialized for the
  API/UI/audit. It is a value type — captured and then frozen.

### The boundary
- Surface A answers "**what should I do?**" (control-flow).
- Surface B answers "**what does this host report?**" (observation/audit).
- Handlers MUST NOT branch on Surface B. The API/UI MUST NOT mutate Surface A.
- A snapshot is captured at a point in time and persisted as-is; it is never
  re-derived lazily from a live host after the fact.

### Recommended follow-up (Phase 2.1.5, not yet implemented)
Freeze `capability.Snapshot(ctx)` into the `ExecutionRecord` (and the
`AuditEvent`) at run time, so each execution records the capabilities that were
true *when it ran*. This closes the loop between Surface A (used for planning)
and Surface B (persisted for audit), enabling "capabilities-at-execution-time"
drift detection. Until then, the snapshot is only available on demand via
`system.host.capability.list` / `GET /api/capabilities`.

## Consequences

- **Clean separation of concerns**: planning logic stays simple (booleans);
  observation stays rich (versions/details) without leaking into control-flow.
- **Audit integrity**: a captured snapshot is immutable evidence of host state;
  it does not silently change as the host evolves.
- **Security**: capability discovery remains a read-only, in-process kernel
  function — never a shell command, never a mutation path.
- **Testability**: `DetectCapability` is overridable via `WithCapability`;
  `Snapshot` is deterministic (sorted) and probe-swappable per platform.
- **Cost**: two representations of "what the host can do". Accepted — the
  decisioning one is tiny and the snapshot one is only computed on demand
  (today) or once per execution (2.1.5), not continuously.
- **Open item**: embedding the snapshot in `ExecutionRecord` (2.1.5) to make
  audits show capabilities-at-execution-time.
