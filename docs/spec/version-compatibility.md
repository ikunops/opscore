# Version Compatibility Policy — Specification (Phase 7.5)

**Status:** Specification (GPT Round 34 authorized). This phase defines the
compatibility *model and validation rules*. It does **not** download, install,
upgrade, or migrate anything, and it does **not** modify the frozen Runtime Core
(Contract / Manifest / Provider / Loader / Manager).

**Authorized by:** GPT Round 34 — *Phase 7.5 — Version Compatibility Policy*,
spec-only.

---

## 1. Goal

Answer exactly one question, mechanically:

> **Is this Package compatible with this Runtime?**

A host (the Phase 7.4 Registry consumer, or the bootstrap that calls
`isolation.AddFromPackage`) asks the Policy *before* it commits to loading a
Package. The Policy is pure: same inputs ⇒ same answer, no I/O, no side effects.

---

## 2. Frozen boundaries (MUST)

| # | Rule |
|---|------|
| MUST-1 | New ecosystem layer (`ecosystem/compat`), standard-library only, never imports `internal/`. May import sibling `ecosystem/packaging` and `ecosystem/registry`. |
| MUST-2 | Defines the compatibility *model and validation rules*. Must NOT modify Runtime Contract / Manifest / Provider / Loader / Manager, and must NOT decide how a package is downloaded, installed, or upgraded. |
| MUST-3 | Sole responsibility: judge whether a `packaging.Package` (or `registry.PackageRef`) is compatible with a given Runtime — forward/backward rules over SDK protocol version, package version, and supported-runtime range (min/max). No transport, no OCI, no auto-migrate. |

**Forbidden (explicitly out of scope):** Runtime Contract / Manifest / Provider /
Loader / Manager modification, auto-upgrade, auto-migration, Registry Transport,
OCI Distribution implementation.

---

## 3. Model

### 3.1 RuntimeSpec
Describes the host Runtime a Package would load into.

| Field | Meaning |
|-------|---------|
| `SupportedSDK` | Isolation protocols the Runtime speaks (e.g. `opscore.isolation/v1`). Empty ⇒ `DefaultSDK`. |
| `Version` | Runtime's own semantic version (`MAJOR.MINOR.PATCH`). |

### 3.2 PackageSpec
The compatibility declaration a Package makes. Derived — never re-declared — by:
- `compat.FromPackage(*packaging.Package)` → SDK + version (loaded package has no runtime window yet).
- `compat.FromRef(registry.PackageRef)` → SDK + version + inclusive `MinRuntime`/`MaxRuntime` window.

| Field | Meaning |
|-------|---------|
| `SDKVersion` | Isolation protocol the Package was built against. |
| `Version` | Package's own semantic version. |
| `MinRuntime` | Inclusive lower bound on host Runtime version. Empty ⇒ unbounded below. |
| `MaxRuntime` | Inclusive upper bound on host Runtime version. Empty ⇒ unbounded above. |

The `MinRuntime` / `MaxRuntime` window is carried by `registry.PackageRef`
(added in Phase 7.5) so the Registry can advertise exactly the constraint the
Policy evaluates. `SupportedRuntime` remains a human-readable summary.

---

## 4. Validation rules

1. **SDK protocol match** — `PackageSpec.SDKVersion` ∈ `RuntimeSpec.SupportedSDK`.
   Forward/backward compatibility across SDK protocols is exercised by *widening*
   `SupportedSDK`, never by silently accepting a mismatch.
2. **Runtime version window** — `RuntimeSpec.Version` must lie within
   `[MinRuntime, MaxRuntime]` when those bounds are declared. Each bound is
   inclusive; a missing bound means no constraint on that side.

A `Result` carries `Compatible` plus a `Reasons` slice: every blocking rule
emits a reason when incompatible; a single confirmation when compatible.

---

## 5. Version matrix (example)

| Package SDK | Package window | Runtime | Verdict |
|-------------|----------------|---------|---------|
| `v1` | — | `v1`, `1.4.0` | ✅ compatible |
| `v1` | `min 1.5.0` | `1.4.0` | ❌ below min |
| `v1` | `max 1.3.0` | `1.4.0` | ❌ above max |
| `v1` | `min 1.0.0`, `max 2.0.0` | `1.4.0` | ✅ in window |
| `v1` | `min 1.0.0` (no max) | `2.3.0` | ✅ forward-compatible |
| `v2` | — | `v1` runtime | ❌ SDK mismatch |

---

## 6. Relationship to other phases

- **Phase 7.4 (Registry, spec):** owns *discovery*; a `PackageRef` now carries the
  compatibility window (`MinRuntime`/`MaxRuntime`) that this Policy consumes.
- **Phase 7.2 (Packaging):** `packaging.Package` is the loaded form this Policy
  can judge via `FromPackage`.
- **Phase 6.2 (Catalog):** reflects *already-loaded* providers — by the time a
  Package is in the Catalog it has already passed this Policy.
- **Phase 5 (Trust):** this Policy decides *compatibility*, never *trust*. A
  compatible Package still requires the Phase 5 Trust Pipeline before load.

The Policy is the gate a host calls on the path:
`Registry → PackageRef → (Compatibility Policy) → Download → Unpack → AddFromPackage → Runtime → Catalog`.

---

## 7. Deferred

- OCI Distribution (Phase 7.5+ / 7.6) — transport, not policy.
- Auto-migration / auto-upgrade — explicitly forbidden here.
- Multi-SDK negotiation beyond the `SupportedSDK` set.
