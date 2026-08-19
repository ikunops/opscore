# ADR-012 — Runtime Core Stability Report

- **Status**: Accepted
- **Date**: 2026-08-02
- **Companion to**: ADR-010 (Plugin Runtime Contract — the freeze document), ADR-011 (Phase 5 Final Stability Report), ADR-009 (Capability Snapshot Separation), ADR-013 (Plugin Ecosystem Architecture — the Phase 7 companion)
- **Author**: OpsCore Plugin Runtime Workstream
- **Round**: 29 (authorized)

---

## 0. Abstract

This ADR declares the OpsCore Plugin **Runtime Core frozen and entering long-term
stable maintenance**. It records the Phase 6 work — the four peripheral extensions
that complete the runtime *without modifying the Contract* — re-affirms the
trust / signature posture established in Phase 5, states the forward compatibility
guarantee, and sets the Phase 7 direction.

The headline invariant of the entire Round 17 → 29 arc:

> **Every capability added after the Contract freeze (Phases 4–6) lives in a
> peripheral package that depends on, but never modifies, the Runtime Contract.**

Phase 6 is the proof that this discipline scales: Sandbox, Catalog, Process
Isolation, and Execution Projection were all delivered as *additions on top*,
each with zero Contract field / interface / lifecycle change.

---

## 1. Frozen Runtime Contract (reference ADR-010)

The Runtime Contract was frozen at Round 17 / Phase 3 (commit `00f79e9`) and has
**not changed since**. ADR-010 is the authoritative freeze document. The following
remain frozen and are NOT subject to further evolution:

- Manifest schema (forward-compatible versioning only; unknown schema rejected at parse)
- Provider / Loader / Descriptor
- Module / Manager lifecycle
- Compatibility Gate
- Capability Negotiation (ADR-009 semantics: Host Observation, not Plugin Local Observation)
- Reload / Watcher (edge-trigger, observe-only)
- `core.Context` interface surface

No Phase 6 commit touched any of the above. The gate (`go build ./... && go vet ./...
&& go test ./...`) was green at every step, confirming the freeze held mechanically
as well as by declaration.

---

## 2. Trust Boundary & Signature Policy (reference ADR-011 / Phase 5)

Phase 5 produced ADR-011 (Phase 5 Final Stability Report), which defines:

- **Trust Boundary**
- **Provider Matrix**
- **Signature Policy Matrix**
- **Out-of-scope**

These remain the security baseline. Phase 6 made **no changes** to them. The trust
model is unchanged:

- verify-after / parse-before, fail-closed
- Ed25519 detached signature
- Trust-root + required-signer + key-rotation (`SignatureVerifier`)
- `ErrSignatureInvalid` (crypto failure) separated from `ErrSignatureUntrusted`
  (trust failure) — so operators can tell "bytes corrupted" from "signer not trusted"

The credential firewall introduced in Phase 6.3 (SSH Password / KeyPath / KeyBytes
never cross the process boundary) is a *free* reinforcement of this same boundary,
not a new one.

---

## 3. Runtime Extensions delivered in Phase 6 (all built ON TOP of the Contract)

Each extension is in a peripheral package and changes **zero** Contract
fields / interfaces / lifecycle. Dependency direction is mechanically pinned by
`go/parser` AST tests:

- `catalog` never imports `runtime` / `core` / `builtin` (invariant: `Catalog → Provider → PluginMetadata`)
- `isolation` and `runtime` are **mutually non-importing** (MUST-2 from Phase 6.3)

So the freeze is enforced by the compiler, not by memory.

### 3.1 Phase 6.1 — Sandbox / Isolation Envelope (`c877213`, `Decision.Code` in `517900b`)

Peripheral `core.Handler` **decision decorator** — deliberately *not* a runtime
controller. The host still owns execution; the envelope only observes and fails closed.

- **MUST**: exec timeout; permission / risk escalation fail-closed.
- **SHOULD**: opt-in resource boundary; peripheral audit sink.
- `Decision.Code` enum (`allowed` / `timeout` / `permission-escalation` /
  `risk-escalation` / `step-limit` / `input-too-large` / `plan-too-large` /
  `plan-error` / `nil-plan`) follows the `CompatibilityResult` / `SignatureResult`
  audit-code convention.
- Applied on `Manager` (`runtime.Module.Operations`) and `builtin.Module`
  (`plugin.Module.Register`) paths; **idempotent**.
- Caller-only fail-closed timeout semantics: the goroutine is *not* killed; real
  cancellation is deferred to Phase 6.3 (where the process is killed instead).

**Verdict: PASS (Round 26), no MUST blocking.**

### 3.2 Phase 6.2 — Marketplace / Catalog (`517900b`)

Peripheral `internal/plugin/catalog`: a **read-only index** over `manifest.Provider`.

- `List` / `Get` / `Versions` / `Search` across multi-source (File + Git + OCI).
- **NO** Install / Enable / Trust / Download / Upgrade / DependencyResolution — the
  catalog is an index, never an installer or a marketplace transaction engine.
- Invariant `Catalog → Provider → PluginMetadata` enforced by `go/parser` AST test
  (catalog may not import `runtime` / `core` / `builtin`).
- Result-**window** pagination (`Query.Offset` / `Query.Limit`, `SearchPage` →
  `Page{Items, Total, Offset, Limit}`); `Total` is the pre-window count. This is
  **honest**: results are windowed, *not* pushed down, because `Provider` is frozen
  Contract and cannot grow a LIMIT/OFFSET push-down.
- Content-addressed `Digest` (sha256 over canonical manifest JSON) for cache / diff /
  sync only — **explicitly NOT a trust signal**.
- `Source.Priority` for presentation ranking of duplicate entries across sources.
- `Description` / `Author` / `Tags` live in **catalog metadata only**, deliberately
  *not* promoted into Manifest (keeps the Contract minimal).

**Verdict: PASS (Round 27), no MUST. fail-loud / fail-soft recognized as acceptable.**

### 3.3 Phase 6.3 — Process Isolation (`96fd28c`)

Peripheral `internal/plugin/isolation`: `Handler.Plan` executes in a **helper
process** over length-prefixed JSON frames on stdio (stdlib only — no new
dependency, preserving the offline `GOSUMDB=off` / `GOTOOLCHAIN=local` constraint).

- **MUST-1**: `Plan` signature unchanged.
- **MUST-2**: `runtime` ↔ `isolation` mutually non-importing (pinned by `go/parser` test).
- **MUST-3**: **REAL termination** — `exec.CommandContext` + `cmd.WaitDelay`; a 30s
  helper is killed in ~0.4s and reaped. This is the qualitative fix over the 6.1
  caller-view fail-closed (which could not actually stop runaway work).
- **MUST-4**: helper **panic is contained** — the helper deliberately does *not*
  recover the panic (crash and error are different things); stderr trace is captured;
  the host keeps serving other operations.
- **MUST-5**: fail-closed on every path (spawn-failed / timeout-killed / helper-crash /
  protocol-error / plugin-error / response-too-large / unserializable-plan).
- Wire: discriminated union `Step` (only Executor-known kinds cross the frame).
  Protocol `opscore.isolation/v1`.
- Context projection is a **credential firewall**: SSH `Password` / `KeyPath` /
  `KeyBytes` never reach the plugin.
- Helper context carries an **empty `CapabilityContext` by design** — capability is
  host-observed (ADR-009); auto-detect on the helper would report the helper's own
  machine, silently wrong for a remote target.

**Verdict: PASS (Round 28), no MUST. GPT: "the most valuable peripheral extension yet."**

### 3.4 Phase 6.4 — Execution Projection (`b76c4e4`)

Peripheral `internal/plugin/isolation` (continued): Host-observed data is projected
**read-only** to the helper — **never re-detected**.

- `ContextProjection` gains 3 optional `omitempty` fields (backward-compatible, **no
  wire-version bump**): `capabilitySnapshot`, `hostSnapshot`, `requestId`.
- `RebuildContext` consumes them read-only (`WithCapabilitySnapshot` /
  `WithHostSnapshot` / `WithExecutionID`); it **never calls `DetectCapability`**.
  Host projects nothing → helper stays Capability-blind (honest default).
- **Ruling (b) — DeploymentMap**: a host-side JSON mapping `op → helper path/args/env/
  timeout`. The Manager still never imports `isolation`. Whether a plugin runs
  in-process or out-of-process is a **DEPLOYMENT POLICY**, not plugin identity — so
  the Manifest is untouched. (GPT: "this is the better design; keep this principle
  forever.")
- **Ruling (c) — Executable Plugin**: recorded as the **Phase 7 direction**; `.so`
  stays an optional backend, not the main route.
- The wire stays `opscore.isolation/v1` because `omitempty` is ignored by old helpers;
  only field deletion / semantic change / frame-format change would require `/v2`.

**Verdict: PASS (Round 29). `omitempty` keeps wire v1 compatible; no protocol bump.**

---

## 4. Compatibility Guarantees (the freeze going forward)

Effective with this ADR, the Runtime Core is **Stable**. Acceptable future changes:

| Allowed | Not allowed (requires new ADR + major version) |
|---|---|
| Bug fixes | New lifecycle stages |
| Security fixes | New Contract fields / interfaces |
| Performance improvements | Changes to the `Manager` interface |
| Peripheral-package additions (catalog / sandbox / isolation / ecosystem) | New mandatory Provider / Loader behaviors |

All new capability is expected to land in **peripheral packages** that depend on,
but never modify, the Contract — exactly as Phases 5 and 6 demonstrated. The
`go/parser` AST guards in `catalog` and `isolation` make regressions in dependency
direction a compile-time failure, not a code-review hope.

---

## 5. Future Work — Phase 7: Third-party Executable Plugin Ecosystem

Phase 7 is defined as an **ecosystem ON TOP of the Stable Runtime Core**, not as
further Runtime evolution. The Contract, Trust Boundary, and Signature Policy from
Phases 3–6 are the stable foundation.

Proposed scope (from Round 28 / 29 rulings):

- **Executable Plugin SDK** — the recommended plugin form; `.so` becomes an optional
  backend, not the main route.
- **Plugin Packaging & Release Tooling**
- **Registry / Marketplace** — builds on the Phase 6.2 catalog index (read-only) and
  the Phase 5.2 OCI Provider (distribution).
- **Version compatibility policy**
- **Distribution & lifecycle**
- **OCI Distribution** (builds on Phase 5.2 OCI Provider)
- **WASM** (optional backend)
- **`.so`** (optional backend, not the sole direction)

None of the above modifies Runtime Core. They consume the stable `core.Handler` /
`ExecutionPlan` / `ContextProjection` surfaces that Phases 3–6 finalized.

### 5.1 Phase 7.1 — Executable Plugin SDK (`099c59d`, PASS Round 31)

Delivered `ecosystem/sdk` as the **single source of truth** for the
`opscore.isolation/v1` wire protocol. It is a public, core-free, stdlib-only
package that a third-party plugin imports to implement `sdk.HandlerFunc` and call
`sdk.PluginMain`. `internal/plugin/isolation` is reduced to a *bridge* that adapts
`core.Handler -> sdk.HandlerFunc` and vice versa; the wire types and framing now
live only in the SDK.

Ownership principle (Round 31): **v1 belongs to the SDK.** Any future v2 must bump
the SDK first; the Runtime/in-process adapter follows. The five MUSTs held: v1 wire
format unchanged / no `core` import / reference implementation / standalone (stdlib
only) / knows only stdin+stdout+protocol. Zero Runtime Contract change.

### 5.2 Phase 7.2 — Packaging Specification (`ecosystem/packaging`, PASS Round 32)

Delivered `ecosystem/packaging` as the **distribution unit** for third-party
executable plugins. It is a public package depending only on the standard library
and `ecosystem/sdk` (verified by an AST guard that forbids any `internal/` import).

Frozen boundaries:

- **MUST-1** A Package is a distribution unit, **not** the Runtime Manifest. Its
  metadata (`plugin.json`) never becomes part of a plugin's Runtime identity.
  `plugin.json` is kept strictly separate from `manifest.json` (MUST-5).
- **MUST-2** It reuses `ecosystem/sdk` and `opscore.isolation/v1`; it introduces no
  new wire format. `Validate` rejects any `sdkVersion` other than the protocol the
  SDK speaks.
- **MUST-3** The only path from a Package to execution is the host-side bridge
  `isolation.DeploymentMap.AddFromPackage(op, pkg)`. It resolves the package-relative
  executable/working-dir against the package directory and inserts a `Deployment`,
  whose `Handler()` yields a `core.Handler` indistinguishable from an in-process
  plugin's. **The Runtime never learns a package exists.** (The AST guard also
  forbids `internal/plugin/runtime` from importing `ecosystem/packaging`.)
- **MUST-4** Source-agnostic: a Package is identical however it arrived (File / Git /
  OCI). `Load(dir)` only reads the unpacked directory; transport is someone else's
  concern.
- **MUST-5** Package metadata (`plugin.json`) is kept separate from the Runtime
  Manifest (`manifest.json`).

`Package` carries: `name`, `version`, `sdkVersion`, `operations` (op → `RunSpec` with
`executable/args/env/dir/execTimeoutSeconds/maxResponseMB`, package-relative paths),
and optional `checksums` (relative path → hex sha256). `Validate` enforces
`sdkVersion`, executable existence, and optional checksum integrity. All file I/O is
stdlib-only to preserve the offline build.

Explicitly **out of scope** (forbidden): Registry Server / Marketplace / OCI
push-pull / auto-install-upgrade / dependency resolution / `.so` / WASM. The Package
describes *what to run*; how it was delivered, and whether/how it is enabled, remain
Provider / Bootstrap concerns outside this package.

**Round 32 Outcome: PASS.** GPT (Round 32) confirmed Phase 7.2 at the architectural
level — MUST-1~5 all held and the Stable Core principle is preserved (no Runtime Core
regression from the ecosystem layer). Two key judgments are now design law: (a)
`isolation.AddFromPackage` is the **single legal entry** from Package → Runtime — every
future Marketplace / Installer / Registry / OCI-Pull / Git-Pull must route a Package
through it and may not bypass it; (b) `plugin.json` (distribution metadata) and
`manifest.json` (Runtime identity) stay separate forever — author/logo/license/homepage
belong in `plugin.json`, never in the frozen Manifest.

### 5.3 Phase 7.3 — Packaging / Release Tooling (`ecosystem/tooling` + `cmd/opscore-pkg`, PASS Round 33)

Authorized by GPT (Round 32) as the next Phase 7 step — **before** Registry / Marketplace.
The Tool turns a plugin's Go source into a verified package directory; it does not ship
code into the Runtime, it emits a `packaging.Package` that the host later bridges via the
single legal entry `isolation.AddFromPackage` (Phase 7.2 MUST-3).

Two frozen boundaries (GPT Round 32 design law):

- **MUST-1** Reuses `ecosystem/sdk` and `ecosystem/packaging` — introduces **no new
  protocol or package format**; it only composes them.
- **MUST-2** Must **NEVER touch the Runtime Core** (Manifest / Provider / Loader / Manager
  / Compatibility / Execution stay frozen). It emits a `packaging.Package` that the host
  bridges later; it does not call into `internal/`. An AST guard (`TestToolingImportsOnlyAllowed`)
  forbids any `internal/` import, pinning the dependency direction at compile time.
- **MUST-3** The Builder **composes `plugin.json`**; the CLI must never hand-assemble that
  JSON. Build order is mechanically fixed: build → checksum → write `plugin.json` →
  copy resources → write `release.json` → verify (Load + Validate).

Delivered:

- `ecosystem/tooling` (`package tooling`): `BuildSpec` (author input — **not** the
  generated `plugin.json`), `OpSpec` (per-op build + runtime descriptor), `BuildFunc`
  (injected; `DefaultGoBuild` shells `go build` with `GOTOOLCHAIN=local` / `GOSUMDB=off`
  for offline builds), and `Build` which orchestrates the fixed pipeline and ends by
  `packaging.Load` + `Validate` on its own output. Output is a `dist/` directory (not a
  compressed archive — zip/tar.gz deferred per GPT) containing the built helper(s),
  `plugin.json`, and `release.json`.
- `cmd/opscore-pkg` (`package main`): a thin offline CLI — `--spec <build-spec.json>`
  `--out dist` — that reads the spec, sets `OutDir`, and calls `tooling.Build`. It never
  hand-assembles `plugin.json`; that is the Builder's job.
- `release.json` (`Release Tool` concern, NOT Runtime): `{version, builtAt, sdkVersion,
  checksum}` where `checksum` is the sha256 of the produced `plugin.json` manifest — a
  package-integrity record the Release Tool owns, deliberately outside the frozen Runtime.
- Tests (`build_test.go`): `TestToolingImportsOnlyAllowed` (AST layering guard),
  `TestBuildProducesVerifiedPackage` (full pipeline → load + validate → checksums real →
  `release.json` checksum equals `plugin.json` checksum), `TestBuildFailsWhenBuildErrors`
  (build failures propagate, never silently swallowed).

Explicitly **out of scope** (forbidden by GPT Round 32): Registry Server / OCI Push /
Marketplace / Installer / Auto-Update / Dependency Resolution / Signature (the existing
Phase 5 Trust Pipeline is reused, not reimplemented). The Tool produces a directory; how
it is signed, published, or enabled is a separate concern.

**Round 33 Outcome: PASS.** GPT (Round 33) confirmed Phase 7.3 at the architectural level — MUST-1~3 held, forbidden scope respected, gate green. Two notes: (a) the `release.json` `checksum = sha256(plugin.json)` design is **accepted as-is** — GPT explicitly said "本轮 PASS，不建议返工" and only *suggested* (not required) a future extension adding per-artifact checksums via an `artifacts` map, without replacing the existing meaning; (b) **Phase 7.4 is authorized** as *Registry / Marketplace Specification* (spec, not implementation).

### 5.4 Phase 7.4 — Registry / Marketplace Specification (`ecosystem/registry`, PASS Round 34)

Authorized by GPT (Round 33) as the next Phase 7 step — **Registry / Marketplace Specification**, explicitly *not* Registry Implementation.

Delivered `ecosystem/registry` (package `registry`) as the **specification** for third-party Executable Plugin discovery. Per the freeze it is spec-only: it defines the metadata model, the Search API surface, and the index format, and builds no transport, download, install, or trust decision.

Frozen boundaries:

- **MUST-1** Standard-library only; introduces no new wire format; never imports `internal/` (pinned by `TestRegistryImportsOnlyStdlib` AST guard).
- **MUST-2** Never touches Runtime Core / Contract / Manifest / Provider / Trust. A `PackageRef` is metadata only; resolving it to a running plugin is delegated (download → unpack → `isolation.AddFromPackage`).
- **MUST-3** Defines the discovery path; does NOT implement HTTP / OCI pull / Git clone / install / enable / upgrade / dependency resolution.

Delivered:

- `PackageRef` — the Registry Metadata model (id / name / latestVersion / availableVersions / sdkVersion / supportedRuntime / tags / downloadURL / checksums / signatureRef). `Validate()` is structural only.
- `Registry` interface — the Search API (`Search` / `List` / `Get` / `Versions`); transport-free.
- `Index` — the serializable catalog (`index.json` / `catalog.json`); `ParseIndex` decodes + validates (no network), `Marshal` serializes.
- `MemoryRegistry` — an in-memory **reference implementation** so the Search API semantics are testable without a transport (no network fetch).
- `docs/spec/registry-marketplace.md` — the specification document: goal, frozen boundaries, metadata model, Search API, index format, relationship to Phase 7.2 / 6.2 / 5.2 / 5 Trust, and deferred items.

Aligned with existing layers (not duplicating them): the Registry discovers *not-yet-downloaded* packages (`PackageRef` → `downloadURL`); Phase 6.2 `catalog` reflects *already-loaded* `manifest.Provider`s. A discovered `PackageRef` becomes a `packaging.Package` via `Load(dir)` and enters the Runtime only through `isolation.AddFromPackage` (Phase 7.2 MUST-3). Trust stays in the Phase 5 Pipeline (`signatureRef` is a pointer, never a decision here).

Explicitly **out of scope** (forbidden for 7.4): Registry Server / HTTP transport / auto-install / auto-upgrade / dependency resolution / package enable / trust decision / Provider or Manifest modification. **Version Compatibility Policy deferred to Phase 7.5** (Registry must first know what a Package looks like).

**Round 34 Outcome: PASS.** GPT (Round 34) confirmed Phase 7.4 at the architectural level — MUST-1~3 all held, forbidden scope respected, gate green. Four judgments are now design law: (a) `PackageRef.Checksums` stays an `artifact → digest` mapping (e.g. `helper`, `resources.zip`); the Registry must NOT bind to `release.json` (a Release-Tool concern) — no rework needed despite the Round 33 `release.json` note; (b) `signatureRef` is a *reference*, never a Trust decision — placement in the Registry implies nothing about verification, which remains Phase 5 Trust Pipeline; (c) `MemoryRegistry` is explicitly a **reference implementation**, not a production registry; (d) the Registry↔Catalog lifecycle stays distinct — Registry discovers *not-yet-downloaded* packages, Catalog reflects *already-loaded* providers — never merge them. **Phase 7.5 is authorized** as *Version Compatibility Policy* (spec, not implementation).

### 5.5 Phase 7.5 — Version Compatibility Policy (`ecosystem/compat`, PASS Round 35)

Authorized by GPT (Round 34) as the next Phase 7 step — **Version Compatibility Policy**, explicitly a *Specification* (defines the model + validation rules), not an implementation (no download / install / upgrade / migrate).

Frozen boundaries:

- **MUST-1** New ecosystem layer (e.g. `ecosystem/compat`), standard-library only, never imports `internal/` (AST guard pins this). It may depend on sibling ecosystem packages `ecosystem/packaging` and `ecosystem/registry` (public, not Runtime Core).
- **MUST-2** Defines the compatibility *model and validation rules* — it must NOT modify Runtime Contract / Manifest / Provider / Loader / Manager, and must NOT decide how a package is downloaded, installed, or upgraded.
- **MUST-3** Its sole responsibility is to **judge whether a given `packaging.Package` (or `registry.PackageRef`) is compatible with a given Runtime** — forward/backward rules over SDK protocol version, package version, and supported-runtime range (min/max). No transport, no OCI, no auto-migrate.

Content (spec): SDK protocol version (`opscore.isolation/v1`), package version, supported Runtime range (min/max), forward/backward compatibility rules, validation rules, version-matrix example, and tests. The policy answers exactly one question: *is this Package compatible with this Runtime?*

Explicitly **out of scope** (forbidden for 7.5): Runtime Contract / Manifest / Provider / Loader / Manager modification, auto-upgrade, auto-migration, Registry Transport, OCI Distribution implementation.

**Round 35 Outcome: PASS.** GPT (Round 35) confirmed Phase 7.5 at the architectural level — MUST-1~3 all held, forbidden scope respected, gate green. Judgments now design law: (a) the compatibility layer is *metadata, not runtime behavior* — `PackageSpec` / `RuntimeSpec` / `Policy.Check()` are all Value Objects with no Loader / Manager / Manifest / Provider reach; (b) adding `MinRuntime` / `MaxRuntime` to `registry.PackageRef` (not to the frozen `manifest.json`) is the **correct** location for ecosystem-layer info; (c) `SupportedRuntime` stays as a human-readable summary alongside the machine-checked `Min/Max` window — do not delete it; (d) the Runtime Compatibility Parser is **intentionally minimal** — it supports `vX.Y.Z`, `>=`, `<=`, `=`, `^` only; it must NOT grow to support `~`, `||`, `&&`, `*`, `x` (documented here to prevent ecosystem creep). **SHOULD (applied):** `Result` now carries a machine-readable `Code` (`ok` / `sdk-mismatch` / `runtime-too-old` / `runtime-too-new` / `invalid-version`), matching the `DecisionCode` style of Sandbox and Signature verdicts, so Registry / Marketplace / CLI / UI switch on `Code` instead of parsing strings. **Phase 7.6 is authorized** as *(A) OCI Distribution Specification* (spec, not implementation) — chosen over Alternative Runtime Backends because the Package Lifecycle still lacked its distribution/carrier layer while the Executable Runtime is already stable.

### 5.6 Phase 7.6 — OCI Distribution Specification (`ecosystem/oci`, PASS Round 36)

Authorized by GPT (Round 35) as the next Phase 7 step — **OCI Distribution Specification**, explicitly a *Specification* (defines the artifact shape), not an implementation (no push / pull / login / upload / download).

Frozen boundaries:

- **MUST-1** Defines the OCI Layout / Artifact / MediaType / Tag Convention / Digest / Manifest — spec only.
- **MUST-2** Reuses `plugin.json` / `release.json` / `PackageRef`; OCI is a **Carrier, not a new Package** — it must not redefine the Package format.
- **MUST-3** Runtime is fully unaware of OCI — an artifact is unpacked by an out-of-scope transport, then handed to `packaging.Load()` → `isolation.AddFromPackage()`.
- **MUST-4** Transport is abstract — no binding to Docker Hub / GHCR / Harbor / ECR / ACR; the spec defines the artifact shape, not a Registry implementation.
- **MUST-5** Trust Pipeline stays Phase 5 (ADR-011); an OCI digest is content addressing, not a signature — `Signature` is not redefined.

Delivered `ecosystem/oci` (package `oci`) as the **specification** for carrying a plugin Package as an OCI artifact. Per the freeze it is spec-only: it models the artifact, manifest, media types, digest, and tag conventions, and builds no client/transport.

- `Digest` — `algorithm:hex`; only `sha256` accepted (`ParseDigest` validates the 64-hex form). Content addressing, NOT trust.
- `Descriptor` / `Manifest` / `Layout` — OCI image manifest + `oci-layout` marker; `Validate` checks media types + digest well-formedness (no network); `MarshalManifest` / `ParseManifest` / `MarshalLayout` / `ParseLayout` are pure functions, so the spec is testable without a registry.
- `Tag` / `ParseRef` — reference parsing for `name:version`, `name@sha256:<hex>`, and bare `name`; `FormatTag` / `FormatDigestRef` render them.
- `Artifact.FromPackage(pkg, files)` — builds the carrier view from a loaded `packaging.Package` + caller-supplied `map[string]FileHash` (path → digest + size). `plugin.json` becomes the config blob (first), `release.json` + every other file become layers, in deterministic order. **No file I/O in `oci`** — the transport that already hashed the bytes supplies the digests.
- `Artifact.ToPackageRef(meta)` — bridges an OCI artifact into a `registry.PackageRef`, **reusing the existing Registry metadata model**; the artifact contributes only identity, the runtime window / trust / download URL come from the publishing host. Nothing about the Package format changes.
- `docs/spec/oci-distribution.md` — the specification document: purpose, design law, artifact model, carrier bridge, discovery flow, forbidden scope, MUST summary.
- AST guard `TestOCIImportsOnlyStdlib` forbids any `internal/` import.

Aligned with existing layers (not duplicating them): OCI carries the `packaging.Package` that `ecosystem/tooling` produced; it does not replace `plugin.json`. Discovery (Phase 7.4) → OCI Artifact (download, shape defined here) → Unpack → `packaging.Load()` → `isolation.AddFromPackage()` (Phase 7.2 single legal entry). Trust (`signatureRef`) still points at ADR-011.

Explicitly **out of scope** (forbidden for 7.6): Runtime Core / Contract / Provider / Loader / Manifest modification, OCI Client implementation, `push` / `pull` / `login` / `upload` / `download`, Transport implementation (Docker Hub / GHCR / Harbor / ECR / ACR), Registry Server, auto-install, auto-upgrade, any redefinition of `Signature` (ADR-011 owns it).

**Round 36 Outcome: PASS.** GPT (Round 36) confirmed Phase 7.6 at the architectural level — MUST-1~5 all held. Five judgments are now design law: (a) `Digest` / `Descriptor` / `Manifest` / `Layout` / `Ref` / `Artifact` are the *Representation* layer, never the *Transport* layer; (b) OCI is a **Carrier, not a new Package** — the chain `plugin.json` → `packaging.Package` → `OCI Artifact` → `registry.PackageRef` is preserved, and "OCI Package / Zip Package / Local Package" triple models are explicitly forbidden going forward; (c) the Runtime Core is fully unaware — `ecosystem/oci` never enters Runtime / Loader / Provider / Manager / Manifest; (d) Transport is abstract — no Docker Registry Client / ORAS / Harbor / GHCR / ECR / ACR; OCI is an *Artifact Schema*, not a *Registry Client*; (e) **Digest ≠ Trust** — Digest→Content Addressing, Signature→Trust, Compatibility→Policy stay three independent layers, and `digest valid ⇒ trusted` must never hold. **SHOULD (applied):** OCI MediaType constants are centralized in `oci` (`application/vnd.opscore.plugin.{manifest,config,release,binary,resource}.v1+json` and `.layer.v1.tar`), distinguishing binary vs resource vs metadata layers rather than scattering string literals. **Phase 7.7 is authorized** as *(A) Reference Distribution Implementation* — renamed from "OCI Distribution Implementation" to match the `MemoryRegistry` / Reference SDK naming; its job is to *prove the spec set is implementable* (Build→Package→Push→Pull→Unpack→Load→AddFromPackage), not to ship a production registry client.

### 5.7 Phase 7.7 — Reference Distribution Implementation (`ecosystem/dist`, PASS Round 37)

Authorized by GPT (Round 36) as the capstone of the Phase 7 specification set — the first phase that actually **moves bytes**, whose sole purpose is to demonstrate the entire lifecycle works end to end. Renamed from "OCI Distribution Implementation" to *Reference* to match the `MemoryRegistry` / Reference SDK / Reference Tooling naming convention.

Frozen boundaries (Round 36 MUST-1..5):

- **MUST-1** Push / Pull / Fetch Manifest / Fetch Blob / Resolve Digest are all expressed through a `Transport` interface. The reference transport is a **local OCI layout** — NO Docker Hub / Harbor / GHCR / ECR vendor is bound as the only implementation.
- **MUST-2** The flow reuses `packaging.Load` → `oci.Artifact` → `registry.PackageRef` → `isolation.AddFromPackage`, never bypassing the single legal entry. The final `AddFromPackage()` handoff is performed by the host (the Runtime), not this client.
- **MUST-3** Reference Implementation stays in the ecosystem layer — `dist` never imports the Runtime Core (AST guard forbids `internal/`).
- **MUST-4** Trust / Compatibility / Registry are **composed**, not re-implemented — the client calls `packaging`, `oci` and `registry` wholesale.
- **MUST-5** The client is pure artifact transport — NOT an Installer / Updater / Dependency Resolver / Marketplace.

Delivered `ecosystem/dist` (package `dist`) as the **reference implementation**:

- `Transport` interface — `Push` / `Pull` / `FetchManifest` / `FetchBlob` / `ResolveDigest` over `oci.Tag` + `oci.Manifest` + content-addressable `map[oci.Digest][]byte`.
- `LocalTransport` — stores artifacts in a local OCI image layout (`oci-layout` marker, `blobs/sha256/<hex>`, `index.json` mapping `name:version` → manifest digest). NO network, NO vendor client — it only proves the flow.
- `Client` — composes the existing ecosystem modules: `PushPackage` (reads files → `oci.FromPackage` → `Manifest` → `Transport.Push`); `PullPackage` (transport pull → materialize blobs by `opscore.file` annotation → `packaging.Load`); `ResolveRef` (`oci.ArtifactFromManifest` → `Artifact.ToPackageRef(meta)`). Stops at the Runtime boundary.
- `docs/spec/reference-distribution.md` — the reference design document.
- AST guard `TestDistImportsOnlyEcosystem` forbids any `internal/` import.

Aligned with existing layers: the reference client exercises the exact path every prior spec defined — it adds zero new model, zero new metadata, zero Runtime reach. The `Digest ≠ Trust` law (7.6 MUST-5) is preserved: `dist` only carries content digests and never treats a valid digest as a trust signal.

Explicitly **out of scope** (forbidden for 7.7): Runtime Core / Contract / Provider / Loader / Manifest modification, remote Registry clients (Docker Hub / Harbor / GHCR / ECR), Installer / Updater / Dependency Resolver / Marketplace behavior, re-implementation of Signature / Compatibility / Registry Metadata, Alternative Runtime Backends (`.so` / WASM) — those re-enter the Runtime Boundary and are explicitly deferred.

**Round 37 Outcome: PASS.** GPT (Round 37) confirmed Phase 7.7 at the architectural level — MUST-1~5 all held, forbidden scope respected, gate green. This is the capstone: Phase 7 has now evolved from "a set of independent specifications" into "one verifiable complete Reference Flow." Five judgments are now design law: (a) the `Client → Transport Interface → (LocalTransport | future HTTP Transport)` direction is correct and stays vendor-neutral — the reference impl binds to **no** Registry Vendor; (b) the lifecycle `Build → Package → OCI Artifact → Transport → Pull → packaging.Load() → registry.PackageRef → Runtime → isolation.AddFromPackage()` holds, and `Client` has no `Run()` / `Install()` / `Enable()` — it never crosses the Runtime Boundary; (c) the AST-guard style (not code review) is the consistent way the entire ecosystem layer enforces layering — keep it; (d) `LocalTransport` is the correct Reference Backend because it proves the *OCI Distribution Spec*, not the *Docker Registry* — future HTTP / S3 / Filesystem are all just Transport Backends; (e) MediaType constants are confirmed worth maintaining, and the rule is now: **MediaType version only allows `v1` / `v2`, never modify an existing string.** **Phase 7 is hereby formally closed**: GPT chose direction **(A) Phase 7 收尾** and recommended a **standalone ADR-013 — Plugin Ecosystem Architecture** (not appended to ADR-012, because ADR-012 is the *Runtime Core Stability* report while Phase 7 is the *Plugin Ecosystem* — two distinct topics). Future work (if any) should open a **new theme** (e.g. Phase 8: Operations / Observability / Enterprise Features), not a 7.8 — the ecosystem is architecturally complete; further work is implementation, not system design.

---

## 6. Sign-off

| Phase | Extension | Round | Verdict | Commit |
|---|---|---|---|---|
| 6.1 | Sandbox / Isolation Envelope | 26 | PASS | `c877213` (+ `517900b`) |
| 6.2 | Marketplace / Catalog | 27 | PASS | `517900b` |
| 6.3 | Process Isolation | 28 | PASS | `96fd28c` |
| 6.4 | Execution Projection | 29 | PASS | `b76c4e4` |
| 7.1 | Executable Plugin SDK (`ecosystem/sdk`) | 30/31 | PASS | `099c59d` |
| 7.2 | Packaging Specification (`ecosystem/packaging`) | 32 | PASS | `2d91484` |
| 7.3 | Packaging / Release Tooling (`ecosystem/tooling` + `cmd/opscore-pkg`) | 33 | PASS | `befaad3` |
| 7.4 | Registry / Marketplace Specification (`ecosystem/registry`) | 34 | PASS | `56f99b4` |
| 7.5 | Version Compatibility Policy (`ecosystem/compat`) | 35 | PASS | `441e9b9` (+ `1fb0000`) |
| 7.6 | OCI Distribution Specification (`ecosystem/oci`) | 36 | PASS | `cdce8da` (+ `a87af0f`) |
| 7.7 | Reference Distribution Implementation (`ecosystem/dist`) | 37 | PASS | `f71a0aa` (+ `faf91df`) |

**Runtime Core declared STABLE** (this ADR, authorized Round 29). Phase 6 is closed.
Phase 7 (Third-party Executable Plugin Ecosystem) is formally **CLOSED** at Round 37 —
a complete, verifiable Reference Flow from Build → Package → OCI → Transport → Runtime.
The ecosystem architecture is recorded in the companion **ADR-013 — Plugin Ecosystem Architecture**.

---

## 7. Core Stability Statement

The three stability ADRs form one chain:

- **ADR-010 (Contract)** — the frozen Runtime Contract: the set of types, interfaces
  and lifecycle stages that every plugin and provider binds against.
- **ADR-011 (Trust)** — the trust boundary, provider matrix and signature policy that
  govern what code is allowed to load and run.
- **ADR-012 (Stability)** — this report: the declaration that the Core is done
  evolving and the forward compatibility guarantee.

> **Core Stability Statement**
>
> Runtime Core is considered architecturally stable. Future features should
> preferentially be implemented as extensions built on top of the frozen Runtime
> Contract. Any change that modifies Runtime Contract semantics, lifecycle, or
> public invariants requires a new ADR and must not be introduced implicitly
> through ecosystem features.

This is the principle that makes Phases 4–6 coherent: every capability added after
the freeze landed in a *peripheral package* (sandbox envelope, catalog, isolation,
projection, and now the executable-plugin SDK) that depends on, but never modifies,
the Contract. The freeze is therefore not a pause — it is the permanent shape of the
Core. Ecosystem work (Phase 7) builds on this stable base; it does not reopen it.

*Phase 6 Stability Report — end of Runtime Core evolution arc (Rounds 17–29).*
