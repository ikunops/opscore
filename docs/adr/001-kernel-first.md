# ADR-001: Kernel First (Architecture First)

**Status**: Accepted
**Date**: 2026-07-21

## Context

The initial plan was to build OpsCore by evolving the demo codebase incrementally — starting with database + authentication, then adding execution engine, then plugins. ChatGPT architecture review identified this as "feature evolution, not architecture evolution."

The core problem: if you build DB + auth first, the execution layer becomes coupled to HTTP/JWT/DB concerns. By the time you reach the executor, everything above needs rewriting.

## Decision

Adopt a **four-phase Architecture First** approach:

| Phase | Content | Principle |
|-------|---------|-----------|
| Phase 0 | Core Runtime (Context/Operation/Executor/Audit) | No DB, no JWT, no users |
| Phase 1 | Control Plane (PostgreSQL + JWT + RBAC + API) | Governance layer |
| Phase 2 | Built-in Modules (Service/Firewall/Journal) | Only produce Operations |
| Phase 3 | Plugin Platform (Manager/Registry/SDK) | Same interface as Builtin |
| Phase 4 | Frontend (React + embed.FS) | Last |

**Phase 0 must be first.** The Executor must be independent of HTTP/JWT/DB. It only receives `Context + Operation`.

## Consequences

- Phase 0 has zero external dependencies (no DB driver, no HTTP framework)
- CLI, SDK, REST, WebSocket all reuse the same Core
- Migration is by Operation, not by Handler
- Demo code is reference only — new repo is a clean rewrite
