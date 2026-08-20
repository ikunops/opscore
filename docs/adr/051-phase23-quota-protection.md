# ADR-051 — Phase 23.2 Resource Quota Protection

- **Status**: Adopted (Architecture R110=A) · Erratum ratified (R111=B → R112 clarification)
- **Phase**: 23.2 (Resource Quota Protection)
- **Frozen principles**: R23-1, R23-2, R23-3, R23-4
- **Supersedes**: the "缺失/不完整 → Unknown → Reject" wording in the original R110
  signed text. That wording is **replaced** by the three-state model below.

## 1. Purpose

Allow an operator to configure per-(capability, principal) resource quotas
(RSS / CPU seconds) as an **optional** admission-protection dimension. Quota is
the final Gate step, inserted between `concurrency` and `rate` in the fixed
order:

```
kill → principalKill → breaker → concurrency → quota → rate → timeout
```

## 2. Ownership separation (R23-3)

| Component | Owns |
|---|---|
| `QuotaStore` | **quota definitions only** (the ceiling) |
| `QuotaEvidenceReader` | **consumption evidence only** (observed usage) |
| `Gate` | the admission decision |

`QuotaStore` has **no consumption field**. `GET /management/v1/protection/quotas`
projects definitions only and never leaks observed consumption.

## 3. Three-state admission (ADR-051 erratum — ratified R112)

The original R110 text wrote "定义缺失/不完整 → Unknown → Reject". That conflates
two distinct situations. The ratified model is **three-state**:

| State | Condition | Gate behaviour |
|---|---|---|
| **NotConfigured** | No quota definition exists for (capability, principal) | **No quota constraint — continue the Gate (admit)**. Quota is opt-in protection, not a global allowlist. |
| **Unknown** | A definition exists **but** its evidence is missing / incomplete (`!Complete`) / errored | **Fail-closed reject** → `protection.quota_evidence_unavailable` (503). Never substitute zero/default for unavailable evidence (R23-1/R23-4). |
| **Evaluated** | A definition exists **and** evidence is complete | Compare observed usage vs ceiling. Over ceiling → reject `protection.quota_exceeded` (503). |

> **Key invariant: `NotConfigured ≠ Unknown`.** Absence of configuration is not
> unavailability of evidence. Treating "no definition" as "Unknown → reject" would
> turn an opt-in protection dimension into a global allowlist and block every
> execution on first deploy.

Implementation (`internal/protection/gate.go`, step 4b):
```go
if def, ok := g.quotas.GetDefinition(capID, principal); ok {
    usage, err := g.evidence.CurrentUsage(capID, principal)
    if err != nil || !usage.Complete {        // Unknown
        return reject(ActionQuotaEvidenceUnavailable, 503)
    }
    if QuotaExceeded(def, usage) {            // Evaluated, over ceiling
        return reject(ActionQuotaExceeded, 503)
    }
}
// def not found ⇒ NotConfigured ⇒ fall through, admit.
```
A quota reject rolls back the concurrency slot acquired in step 4 (no leaked
semaphore).

## 4. Admission-only (R23-2)

Quota rejection affects **new admissions only**. It **never** terminates an
in-flight execution. A kill/cancel is a separate mechanism (`protection.killed` /
`protection.principal_killed`); quota reject is not a kill.

## 5. Evidence honesty (R23-1 / R23-4)

- Missing / incomplete evidence ⇒ `Unknown` ⇒ reject.
- Consumption is **never** read from `QuotaStore` and is **never** substituted
  with zero or a default when the evidence source is unavailable.
- Until live telemetry is wired into the evidence reader, every *defined*
  capability reads `Unknown` ⇒ fail-closed (conservative default posture).

## 6. Surface & security (unchanged invariants)

- Write seam: `OperatorQuotaMutation` (intent → mutation → outcome; outcome
  failure returns `ErrAuditOutcomeFailed`, no rollback) — P22-8 analog.
- Routes live on `:8082` management surface only; `:8080` serves **no** new
  quota route (R21-1).
- Admin-only + CSRF fail-closed (httpOnly cookie, `SameSite=Strict`,
  Origin/Referer same-host).
- Audit facts (`Principal`, `Action`, `Capability`) derived server-side.
- `external/v1` unchanged; frozen packages zero-diff.

## 7. Test contract (locks the three-state)

- `TestGateQuota_NotConfigured_Admits` — absent definition ⇒ admit (NotConfigured).
- `TestGateQuota_EvidenceUnknownFailsClosed` / `...EvidenceErrorFailsClosed` —
  present definition + unknown evidence ⇒ reject (Unknown).
- `TestGateQuota_ExceededRejects` — present definition + complete over-limit
  evidence ⇒ reject (Evaluated).
- `TestGateQuota_ConcurrencyRolledBackOnReject` — semaphore slot released.
- `TestQuotaStore_DefinitionOwnershipNoConsumption` — R23-3 (no consumption field).
