# OCI Distribution Specification — Phase 7.6

**Status:** Specification (no implementation). Authorized by GPT Round 35.
**Owner package:** `ecosystem/oci` (package `oci`).
**Depends on:** `ecosystem/packaging`, `ecosystem/registry` (both reused, not modified).

---

## 1. Purpose

This spec defines how a **built Plugin Package** — described by
`ecosystem/packaging` (`plugin.json`) and emitted by `ecosystem/tooling`
(`dist/` + `release.json`) — is **carried** as an OCI artifact. It answers one
question: *how is a Package published and addressed as an OCI image?*

It does **not** answer: *how does a Package run?* — that is the stable Executable
Runtime (Phases 1–6). OCI is the **last missing link in the Package Lifecycle**,
sitting after SDK → Packaging → Release Tooling → Registry Metadata →
Compatibility Policy.

## 2. Design law (frozen boundaries)

- **OCI is a Carrier, not a new Package.** `plugin.json` / `release.json` /
  `PackageRef` are reused wholesale. OCI never redefines the Package format.
- **Runtime is unaware of OCI.** An OCI artifact is unpacked by a transport out
  of scope here, then handed to `packaging.Load()` → `isolation.AddFromPackage()`
  — exactly like any other distribution source. Nothing in `internal/plugin` is
  touched.
- **Trust stays in ADR-011.** An OCI digest is *content addressing*, not a
  signature. The Phase 5 Trust Pipeline remains the only authority on signatures;
  `PackageRef.SignatureRef` points at it. OCI must not redefine `Signature`.
- **Transport is abstract.** This spec defines the *artifact shape*, not a
  concrete Docker Hub / GHCR / Harbor / ECR / ACR client. No `push` / `pull` /
  `login` / `upload` / `download` is implemented here.

## 3. Artifact model (`ecosystem/oci`)

### 3.1 Media types

| Media type | Meaning |
| --- | --- |
| `application/vnd.opscore.plugin.manifest.v1+json` | The OCI image manifest |
| `application/vnd.opscore.plugin.config.v1+json` | Config blob (references `plugin.json`) |
| `application/vnd.opscore.plugin.layer.v1.tar` | A layer carrying the `dist/` tree |

Namespaced under `opscore.plugin.*` so they cannot collide with generic OCI
image media types.

### 3.2 Digest

`Digest` is `algorithm:hex`. Only **`sha256`** is accepted (intentionally
minimal, matching the ecosystem's zero-dependency stance). `ParseDigest` validates
the algorithm and the 64-hex form.

### 3.3 Descriptor / Manifest / Layout

- `Descriptor` = `{ mediaType, digest, size, annotations? }`.
- `Manifest` = OCI image manifest (`schemaVersion: 2`, `mediaType` =
  plugin manifest) with a `config` descriptor and a `layers[]` list. `Validate`
  checks media types and digest well-formedness — no network.
- `Layout` = the `oci-layout` marker file (`{ "imageLayoutVersion": "1.0.0" }`).

`MarshalManifest` / `ParseManifest` / `MarshalLayout` / `ParseLayout` are pure
functions (no I/O), so the spec is testable without a registry.

### 3.4 Tag convention (`Tag` / `ParseRef`)

| Reference | Form | Meaning |
| --- | --- | --- |
| `name:version` | `demo:1.2.3` | pinned by version |
| `name@sha256:<hex>` | `demo@sha256:…` | pinned by digest |
| `name` | `demo` | bare, unpinned |

`ParseRef` parses all three; `FormatTag` / `FormatDigestRef` render them.

## 4. Carrier bridge (reuses the Package format)

`FromPackage(pkg, files)` builds an `Artifact` from a loaded `packaging.Package`
plus a caller-supplied `map[string]FileHash` (path → digest+size). It emits the
`plugin.json` config blob **first**, then `release.json` and every other file as
layers, in deterministic order. **No file I/O happens in `oci`** — the transport
that already hashed the bytes supplies the digests.

`Artifact.ToPackageRef(meta)` bridges an OCI artifact into a `registry.PackageRef`,
**reusing the existing Registry metadata model**. The artifact contributes only
identity (`name`, `version`); the runtime window / trust / download URL come from
the publishing host (`PackageMeta`). Nothing about the Package format changes.

## 5. Discovery flow (unchanged shape)

```
Registry → PackageRef → OCI Artifact (download) → Unpack → packaging.Load()
         → isolation.AddFromPackage() → Runtime
```

7.6 owns the *OCI Artifact (download)* arrow's **shape**; the actual download and
unpack are future implementation phases (MUST-4 keeps them transport-free here).

## 6. Explicitly out of scope (forbidden for 7.6)

Runtime Core / Contract / Provider / Loader / Manifest modification · OCI Client
implementation · `push` / `pull` / `login` / `upload` / `download` · Transport
implementation (Docker Hub / GHCR / Harbor / ECR / ACR) · Registry Server ·
auto-install · auto-upgrade · any redefinition of `Signature` (ADR-011 owns it).

## 7. MUST summary (GPT Round 35)

- **MUST-1** — Define OCI Layout / Artifact / MediaType / Tag Convention /
  Digest / Manifest. Spec only.
- **MUST-2** — Reuse `plugin.json` / `release.json` / `PackageRef`. OCI is a
  Carrier, not a new Package.
- **MUST-3** — Runtime is fully unaware of OCI.
- **MUST-4** — Transport is abstract; do not bind to any concrete registry.
- **MUST-5** — Trust Pipeline stays Phase 5 (ADR-011); OCI digest ≠ trust.
