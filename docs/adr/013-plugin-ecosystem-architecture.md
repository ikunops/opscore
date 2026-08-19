# ADR-013 — Plugin Ecosystem Architecture

- **Status**: Accepted
- **Date**: 2026-08-03
- **Companion to**: ADR-010 (Plugin Runtime Contract — the freeze document), ADR-011 (Phase 5 Final Stability Report), ADR-012 (Runtime Core Stability Report)
- **Author**: OpsCore Plugin Runtime Workstream
- **Round**: 37 (authorized — Phase 7 closing report)

---

## 0. Abstract

This ADR records the **Plugin Ecosystem** architecture delivered in Phase 7 (Rounds 30–37).
It is the companion to ADR-012 (which declares the Runtime Core *Stable*) and describes the
**ecosystem ON TOP of that Stable Core** — the third-party executable-plugin supply chain
from authoring through distribution to runtime handoff.

ADR-012 and this ADR are deliberately separate: ADR-012 is the *Runtime Core Stability*
report; this ADR is the *Plugin Ecosystem* architecture. They share the same frozen
contracts but cover two distinct topics. Where ADR-012 says "the Core is done evolving,"
this ADR says "and here is the ecosystem that builds on it, with its own hard boundaries."

The headline invariant of Phase 7:

> **Every ecosystem package depends on, but never modifies, the frozen Runtime Contract.
> The only path from a Package into execution is `isolation.AddFromPackage`.**

Phase 7 turned a set of independent specifications (SDK, Packaging, Tooling, Registry,
Compatibility, OCI, Distribution) into one **verifiable complete Reference Flow**:

```
SDK  ───────►  Packaging  ─────►  Release Tooling  ─►  Registry Metadata
                                                        │
                                              Compatibility Policy
                                                        │
                                              OCI Distribution (Carrier)
                                                        │
                                              Reference Distribution (Transport)
                                                        │
                                              packaging.Load()
                                                        │
                                              registry.PackageRef
                                                        │
                                              Runtime  ─►  isolation.AddFromPackage()
```

This is an ecosystem, not a pile of discrete modules.

---

## 1. Ecosystem Scope — the Core / Ecosystem Boundary

The OpsCore plugin system has exactly two layers. The boundary between them is the single
most important architectural fact in Phase 7.

| Layer | What it is | May change? | Owned by |
|---|---|---|---|
| **Runtime Core** | `internal/plugin/*` — Manifest, Provider, Loader, Manager, `core.Handler`, Capability Negotiation, the Phase 5 Trust Pipeline, Phase 6 peripherals (catalog / isolation / sandbox envelope) | Frozen (ADR-010 / 011 / 012) | ADR-012 |
| **Plugin Ecosystem** | `ecosystem/*` — SDK, packaging, tooling, registry, compat, oci, dist | Permitted (with the rules in §5) | This ADR (ADR-013) |

The Ecosystem **consumes** the Core's stable surfaces (`core.Handler`, `ExecutionPlan`,
`ContextProjection`, `isolation.AddFromPackage`). It **never modifies** them. A third-party
plugin author imports `ecosystem/sdk` to speak `opscore.isolation/v1`; the Runtime never
learns that a *package* — as opposed to a *manifest* — exists.

**Hard rule:** Runtime Core is Stable. The Ecosystem is where evolution happens. Mixing the
two (e.g. promoting `plugin.json` fields into `manifest.json`, or giving the Ecosystem a
`Run()`) re-opens the freeze and is forbidden.

---

## 2. Frozen Contracts

The following are frozen by ADR-010 / 011 / 012 and are **not subject to evolution** from
the ecosystem layer. The ecosystem must work within them, never around them.

- **Runtime Contract** (ADR-010): Manifest schema (forward-compatible versioning only),
  Provider / Loader / Descriptor, Module / Manager lifecycle, Compatibility Gate, Capability
  Negotiation (host-observed), Reload / Watcher, `core.Context` interface surface.
- **Trust Boundary & Signature Policy** (ADR-011): Ed25519 detached signature, trust-root +
  required-signer + key-rotation (`SignatureVerifier`), `ErrSignatureInvalid` vs
  `ErrSignatureUntrusted`. Trust is **always** the Phase 5 Pipeline's decision — never the
  ecosystem's.
- **The single legal entry** (Phase 7.2 MUST-3, Round 32 design law):
  `isolation.AddFromPackage(op, pkg)` is the **only** path from a `packaging.Package` into
  execution. Every future Marketplace / Installer / Registry / OCI-Pull / Git-Pull must route
  a Package through it and may not bypass it.
- **Metadata separation** (Phase 7.2 MUST-5, Round 32 design law): `plugin.json`
  (distribution metadata) and `manifest.json` (Runtime identity) stay separate forever.
  `author` / `logo` / `license` / `homepage` belong in `plugin.json`, never in the frozen
  Manifest.
- **Wire protocol ownership** (Phase 7.1, Round 31): `opscore.isolation/v1` belongs to the
  SDK. Any future `/v2` must bump the SDK first; the Runtime adapter follows.
- **MediaType versioning** (Phase 7.6 SHOULD, confirmed Round 37): OCI MediaType versions
  may be `v1` / `v2` only; **never modify an existing string** — add a new one.

These invariants are enforced mechanically, not by memory: every `ecosystem/*` package carries
a `go/parser` AST guard that forbids any `internal/` import
(`TestSDKImportsOnlyStdlib`, `TestPackagingImportsOnlyAllowed`, `TestToolingImportsOnlyAllowed`,
`TestRegistryImportsOnlyStdlib`, `TestCompatImportsOnlyEcosystem`, `TestOCIImportsOnlyStdlib`,
`TestDistImportsOnlyEcosystem`). A dependency-direction regression is a **compile-time** failure.

---

## 3. Lifecycle — the complete Reference Flow

The ecosystem delivers a single, verifiable end-to-end lifecycle. Each stage is a separate
package; none duplicates another's responsibility.

```
Build ──► Package ──► Release Tooling ──► Registry ──► Compatibility ──► OCI ──► Transport ──► Runtime
```

| Stage | Package | Responsibility | Does NOT do |
|---|---|---|---|
| Build | `ecosystem/tooling` (+ `cmd/opscore-pkg`) | turn plugin Go source → verified `packaging.Package` directory + `release.json` | touch Runtime Core; sign; publish |
| Package | `ecosystem/packaging` | the **distribution unit** (`plugin.json` + binaries + resources) | become the Runtime Manifest; know its transport |
| Registry | `ecosystem/registry` | **discover** not-yet-downloaded packages (`PackageRef`) | implement transport; install; trust-decision |
| Compatibility | `ecosystem/compat` | judge *is this Package compatible with this Runtime?* | modify Contract; download; upgrade |
| OCI | `ecosystem/oci` | carry a Package as an OCI **artifact** (spec only) | implement push/pull; bind a vendor |
| Transport / Dist | `ecosystem/dist` | **move bytes** via a `Transport` interface; reference `LocalTransport` | bind Docker Hub/Harbor; install; resolve deps |
| Runtime | `internal/plugin/isolation` | `AddFromPackage` → `core.Handler` (frozen entry) | anything above |

Key properties (all confirmed across Rounds 31–37):

- **Source-agnostic**: a Package is identical whether it arrived via File / Git / OCI / HTTP.
  `Load(dir)` only reads the unpacked directory; transport is someone else's concern.
- **No Runtime reach**: every ecosystem package is compiled-testable against its own model
  with zero Core import; the Runtime only appears at the final `AddFromPackage` handoff.
- **Composed, not re-implemented**: Trust / Compatibility / Registry are reused wholesale by
  `dist`; the client adds zero new model, zero new metadata.

The reference `LocalTransport` proves the *OCI Distribution Spec* (not the *Docker Registry*):
`oci-layout` marker + `blobs/sha256/<hex>` + `index.json` mapping `name:version → manifest
digest`, with no network and no vendor client. Future HTTP / S3 / Filesystem transports are
all just `Transport` backends behind the same interface.

---

## 4. Trust Model — three independent layers

The ecosystem re-asserts the cardinal security rule from ADR-011 / Phase 7.6 (confirmed
Round 37): **Digest ≠ Signature ≠ Compatibility.** These are three separate concerns, and no
two may be conflated.

| Concern | Answers | Owning layer | Becomes "trusted"? |
|---|---|---|---|
| **Digest** (content addressing) | "Are these bytes intact / canonical?" | `oci.Digest`, `packaging` checksums, `registry.PackageRef.Checksums` | **NO** — integrity ≠ trust |
| **Signature** (trust) | "Was this signed by an allowed key?" | Phase 5 Trust Pipeline (`SignatureVerifier`, ADR-011) | **YES** — this is the trust decision |
| **Compatibility** (policy) | "Can this Package run on this Runtime?" | `ecosystem/compat` (`Policy.Check`) | **NO** — compatibility ≠ trust |

Design laws now in force:

- A valid digest **never** implies trust (`digest valid ⇒ trusted` must never hold).
- `signatureRef` / `checksums` are *references and records*, never trust decisions, wherever
  they appear (Registry, OCI, Release Tool). Verification stays the Phase 5 Pipeline.
- Compatibility is **metadata, not runtime behavior** — `PackageSpec` / `RuntimeSpec` /
  `Policy.Check()` are Value Objects with no Loader / Manager / Manifest / Provider reach.
- The ecosystem carries content digests; it never treats a valid digest as a trust signal.

---

## 5. Extension Points — what may evolve

The ecosystem is where future work lives. The **only** permitted extension axes (confirmed
Round 37) are:

- **Transport** backends — new `dist.Transport` implementations (HTTP OCI Distribution Spec
  client, S3, Filesystem) behind the existing interface. Interface-first; never bind a single
  vendor as the only implementation.
- **Packaging** — new `opscore.file` annotation kinds, new resource types, new `RunSpec`
  fields (forward-compatible, `plugin.json`-only). Never promote into `manifest.json`.
- **Tooling** — new build strategies, new resource bundling, new offline build flags. Never
  import `internal/`.
- **Registry** — new `Registry` implementations (HTTP, OCI, Git), new index formats. Never
  merge Registry with the Phase 6.2 `catalog` (Registry discovers *not-yet-downloaded*;
  Catalog reflects *already-loaded*).
- **Reference Backends** — new `dist` reference transports, new `MemoryRegistry`-style
  references. Never production-registry-shaped by default.

The **Runtime Core stays Stable** (ADR-012). Any of the following require a *new ADR* and a
major-version bump — they are **not** ecosystem extensions: new lifecycle stages, new
Contract fields / interfaces, `Manager` interface changes, new mandatory Provider / Loader
behaviors, or any re-opening of ADR-010.

---

## 6. Out of Scope — explicitly not the Plugin Ecosystem

The following are **not** part of the Plugin Ecosystem and must not be added to `ecosystem/*`
without re-opening the Runtime freeze (a separate, higher-bar decision):

- **`.so` / WASM / Native Backends** — these are *Execution Runtime* concerns, not *Plugin
  Ecosystem* concerns. They re-enter ADR-010 and are explicitly deferred (Round 32 / Round 37).
- **Container Runtime** — orchestration / sandboxing of plugin execution is a Runtime
  (Phase 6 / ADR-011) concern, not distribution.
- **Sandbox Rewrite** — the Phase 6.1 envelope and Phase 6.3 process isolation are frozen
  peripheral behavior; the ecosystem may not re-architect them.
- **Runtime Lifecycle Manager** — `Module` / `Manager` lifecycle is frozen Contract.
- **Manifest / Provider** — frozen (ADR-010). The ecosystem may reference them but never
  modify them.
- **Installer / Updater / Dependency Resolver / Marketplace transaction engine** — the
  `dist` client is pure artifact transport; these behaviors belong to a host operation that
  composes the ecosystem, not to the ecosystem itself.

---

## 7. Phase 7 Sign-off (companion to ADR-012 §6)

| Phase | Extension | Round | Verdict | Commit |
|---|---|---|---|---|
| 7.1 | Executable Plugin SDK (`ecosystem/sdk`) | 30/31 | PASS | `099c59d` |
| 7.2 | Packaging Specification (`ecosystem/packaging`) | 32 | PASS | `2d91484` |
| 7.3 | Packaging / Release Tooling (`ecosystem/tooling` + `cmd/opscore-pkg`) | 33 | PASS | `befaad3` |
| 7.4 | Registry / Marketplace Specification (`ecosystem/registry`) | 34 | PASS | `56f99b4` |
| 7.5 | Version Compatibility Policy (`ecosystem/compat`) | 35 | PASS | `441e9b9` (+ `1fb0000`) |
| 7.6 | OCI Distribution Specification (`ecosystem/oci`) | 36 | PASS | `cdce8da` (+ `a87af0f`) |
| 7.7 | Reference Distribution Implementation (`ecosystem/dist`) | 37 | PASS | `f71a0aa` (+ `faf91df`) |

**Phase 7 (Third-party Executable Plugin Ecosystem) is CLOSED at Round 37.** The ecosystem is
architecturally complete: a verified Reference Flow from Build → Package → OCI → Transport →
Runtime, with Trust / Compatibility / Registry as composed, independent layers, and a frozen
Runtime Core underneath. Further work is **implementation**, not system design — should a new
theme open (e.g. Phase 8: Operations / Observability / Enterprise Features), it begins a new
ADR chain rather than extending Phase 7.

---

## 8. Core Stability Statement (shared with ADR-012)

> **Core Stability Statement**
>
> Runtime Core is considered architecturally stable. Future features should preferentially be
> implemented as extensions built on top of the frozen Runtime Contract. Any change that
> modifies Runtime Contract semantics, lifecycle, or public invariants requires a new ADR and
> must not be introduced implicitly through ecosystem features.

The Plugin Ecosystem (Phase 7) is the proof that this principle scales to a full third-party
supply chain: seven ecosystem packages, zero Runtime Contract changes, one frozen entry point.

---

## 9. Design Principles — the laws that held across 7.1–7.7

These six principles are not lifecycle steps; they are the *rules of conduct* the entire
Plugin Ecosystem obeyed from SDK (7.1) through Reference Distribution (7.7). They are stated
explicitly here — once, in one place — so they are easier to maintain than if scattered across
every section above. Any future ecosystem extension (Phase 8 Platform Operations included) is
expected to honor all six.

1. **Runtime unaware of ecosystem** — the Core never learns that a *package* (as opposed to a
   *manifest*) exists. The only contact surface is `isolation.AddFromPackage`.
2. **Composition over modification** — every stage (Registry / Compatibility / OCI / Dist)
   composes the sibling ecosystem packages wholesale; none re-implements another's model.
3. **Metadata before behavior** — compatibility, trust reference, and registry metadata are
   *Value Objects* (no Loader / Manager / Manifest reach), never runtime behavior.
4. **Transport agnostic** — `dist.Transport` is an interface; `LocalTransport` proves the OCI
   Distribution Spec, not any vendor registry. HTTP / S3 / Filesystem are future backends only.
5. **Content addressing is not trust** — `Digest` (integrity) ≠ `Signature` (trust) ≠
   `Compatibility` (policy). `digest valid ⇒ trusted` must never hold.
6. **Single runtime entry** — `isolation.AddFromPackage` is the one legal path Package →
   Runtime. Every Marketplace / Installer / Registry / OCI-Pull / Git-Pull routes through it;
   bypass is forbidden.

---

## 10. ADR-013 Sign-off (Round 38)

| Item | Verdict | Round |
|---|---|---|
| ADR-013 as standalone ADR (not polluting ADR-012) | PASS | 38 |
| Six-section structure complete (Scope / Frozen / Lifecycle / Trust / Extension / Out-of-Scope) | PASS | 38 |
| Phase 7 签字表 7.1~7.7 全 PASS + Phase 7 CLOSED | PASS | 38 |
| Extension / Out-of-Scope boundary split | PASS | 38 |
| (SHOULD) add §9 Design Principles | applied | 38 |

**Round 38 Outcome: PASS.** GPT (Round 38) confirmed ADR-013 is the correct closing document
for Phase 7 — the four ADRs (010 Contract / 011 Trust / 012 Stability / 013 Ecosystem) form a
complementary set, not overlapping ones. Phase 7 is formally **CLOSED** — what closed is the
*Plugin Ecosystem Architecture*; Plugin Ecosystem *Development* (implementation of the
specified modules) may continue within the frozen boundaries. The §9 Design Principles were
added per GPT's SHOULD; §5 Extension / §6 Out-of-Scope were confirmed as the durable guardrail
("Runtime Core evolution requires a new ADR; ecosystem peripheral evolution stays within the
frozen boundary"). Phase 8 (Platform Operations) is authorized as a **new theme** — see ADR-014.

*Phase 7 Plugin Ecosystem Architecture — end of the Third-party Executable Plugin Ecosystem arc (Rounds 30–38).*
