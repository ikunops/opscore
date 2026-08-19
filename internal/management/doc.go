// Package management implements the Phase 17 Management API — the project's
// first deliberate WRITE boundary over governance policy (ADR-035 scope,
// ADR-036 architecture, signed at R77 verdict A).
//
// # What it is
//
// A mutation CLIENT of governancepolicy.Repository and an audit WRITER through
// storage.AuditStore. It owns no persistence (P17-3), no decision logic
// (P17-4/P17-10) and no lifecycle state: the Repository remains the single
// owner of policy state, and every write goes through exactly two
// revision-aware primitives — CompareAndSave and CompareAndTransition.
//
// It is write-only on purpose. Reads live on the frozen external/v1 surface
// (:8080); adding a read here would duplicate a contract this phase is
// explicitly forbidden to touch (ADR-036 §7.5).
//
// # Closed dependency graph (ADR-036 §3.6)
//
//	management
//	   ├── governancepolicy   (Repository, PolicyRecord, Rule value types)
//	   └── storage            (AuditStore, AuditEvent)
//
// Nothing else. internal/governance is NOT imported: rule value types arrive
// through the governancepolicy aliases (see governancepolicy/rules.go), so
// governance.Engine cannot be named here at all. "Management must not call
// Engine.Evaluate to validate a policy" is therefore not a rule anyone has to
// remember — it is a compile error (§4.5.2).
//
// # The pipeline is structural, not conventional (ADR-036 §2)
//
//	AuthN (X-Management-Token)      missing/invalid ⇒ 401, fail-closed
//	  → AuthZ (policy:manage)       absent          ⇒ 403, fail-closed
//	  → Validation                  bad input       ⇒ 422
//	  → audit INTENT   (durable, error-checked)     ⇒ 503 and NO mutation on failure
//	  → CompareAndSave / CompareAndTransition       ⇒ 409 on revision conflict
//	  → audit OUTCOME  (durable, error-checked)     ⇒ 500 degraded on failure
//	  → 2xx
//
// No handler implements those steps. serveMutation does, once; the handlers
// only supply the single CAS call as a closure. A handler therefore cannot skip
// the intent write or reorder the audit pair, because it does not own them.
// AuthN/AuthZ wrap the whole mux rather than individual routes, so an unknown
// path on this surface answers 401 before it answers 404 — an unauthenticated
// caller learns nothing about which routes exist.
//
// # What this package deliberately does not guarantee
//
// The Policy Store and the Audit Store are independent persistence domains.
// Phase 17 claims NO cross-store atomicity (MUST-P17-13). The state
// "intent durable, mutation applied, outcome write failed" is a permitted,
// identifiable, reconciliation-needed degraded state — it is reported to the
// caller as 500 with the correlation ID, never silently swallowed and never
// described as a rollback.
package management
