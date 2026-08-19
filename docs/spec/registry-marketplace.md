# Phase 7.4 — Registry / Marketplace Specification

> **Status**: Implemented (spec only) — pending GPT Round 34 sign-off.
> **Author**: OpsCore Plugin Runtime Workstream
> **Companion**: ADR-012 §5.4 · Phase 7.2 (`ecosystem/packaging`) · Phase 6.2 (`internal/plugin/catalog`) · Phase 5.2 (OCI Provider) · Phase 5 Trust Pipeline.

This document is the **specification** for the third-party Executable Plugin
Registry / Marketplace. Per GPT Round 33 it is deliberately **specification,
not implementation**: it defines the metadata model, the Search API surface,
and the index format, but builds no transport, no download, no install.

---

## 1. Goal

Define the discovery path from a published Package to the single legal entry
`runtime` consumes:

```
Registry → Package Metadata (PackageRef) → Download Package → Unpack →
AddFromPackage() → Runtime
```

Phase 7.4 owns **only the first arrow** (`Registry → PackageRef`). Everything
after `PackageRef` is a later-phase / host-bootstrap concern:

| Arrow | Owner | Phase |
|---|---|---|
| Registry publishes metadata | this spec | 7.4 |
| Host discovers via Search API | this spec | 7.4 |
| Host downloads the archive | transport | 7.5 (OCI / File / Git) |
| Host unpacks to a directory | `packaging.Load` | 7.2 |
| Host bridges into Runtime | `isolation.AddFromPackage` | 7.2 MUST-3 |
| Host decides trust | Phase 5 Trust Pipeline | 5.2 |

The Marketplace's only job is **to help a host FIND a Package**. It never
enables, installs, upgrades, or trusts.

---

## 2. Frozen boundaries (Phase 7.4 MUSTs)

- **MUST-1** The `ecosystem/registry` package depends only on the standard
  library. It introduces **no new wire format**. It must not import any
  `internal/` package (enforced by `TestRegistryImportsOnlyStdlib`).
- **MUST-2** It **never touches the Runtime Core**: no Manifest, Provider,
  Loader, Manager, Compatibility, or Execution change. It never makes a trust
  decision.
- **MUST-3** It defines the discovery path. It does **not** implement HTTP, OCI
  pull, Git clone, download, install, enable, upgrade, or dependency resolution.

### Explicitly forbidden (out of scope)

Registry Server · HTTP transport · auto-install · auto-upgrade · dependency
resolution · package enable · trust decision (Phase 5 reused) · Provider or
Manifest modification.

---

## 3. Registry Metadata model (`PackageRef`)

A `PackageRef` is what a Marketplace advertises so a host can discover a
package. It is metadata only — not the package bytes, not the Runtime Manifest.

| Field | Meaning |
|---|---|
| `id` | Globally unique package id, e.g. `opscore/mysql-helper` |
| `name` | Display name |
| `description` | Free-text summary |
| `latestVersion` | Newest published version |
| `availableVersions` | All published versions |
| `sdkVersion` | Protocol the package targets (e.g. `opscore.isolation/v1`) |
| `supportedRuntime` | Host constraint, e.g. `opscore>=7.0` |
| `tags` | Discovery facets |
| `downloadURL` | Where the package archive lives (transport-agnostic URL) |
| `checksums` | Artifact path → hex sha256 (integrity, not trust) |
| `signatureRef` | Pointer into the Phase 5 Trust Pipeline (reference, not a decision) |

`PackageRef.Validate()` enforces the minimum structural contract (id / name /
latestVersion / sdkVersion / downloadURL present). It performs no network I/O
and makes no trust decision.

---

## 4. Search API (`Registry` interface)

The Search API is the discovery surface. The interface carries **no transport**
— a concrete Registry may be backed by HTTP, a local `catalog.json`, or a git
repo, but none of that is built in 7.4.

```go
type Registry interface {
    Search(ctx context.Context, q SearchQuery) (SearchResult, error)
    List(ctx context.Context) ([]PackageRef, error)
    Get(ctx context.Context, id string) (PackageRef, error)
    Versions(ctx context.Context, id string) ([]string, error)
}
```

- `SearchQuery`: `term` (matches name/description/id, case-insensitive), `tags`
  (AND filter), `sdk` (by sdkVersion), `offset`/`limit` (honest windowing).
- `SearchResult`: a windowed page; `total` is the pre-window match count
  (mirrors the Phase 6.2 catalog convention — results are windowed, not
  push-down, because the Contract is frozen).

`MemoryRegistry` is an in-memory **reference implementation** provided so the
Search API semantics are testable without a transport. It performs no network
fetch and is explicitly not a production Registry.

---

## 5. Index format (`index.json` / `catalog.json`)

A Registry's package list serializes as an `Index`:

```json
{
  "registry": { "id": "opscore", "name": "OpsCore Community Registry" },
  "packages": [
    {
      "id": "opscore/mysql-helper",
      "name": "MySQL Helper",
      "description": "Backup and query MySQL",
      "latestVersion": "1.4.0",
      "availableVersions": ["1.3.0", "1.4.0"],
      "sdkVersion": "opscore.isolation/v1",
      "tags": ["database", "backup"],
      "downloadURL": "https://registry.example.com/opscore/mysql-helper/1.4.0.tar.gz",
      "checksums": { "mysql-helper": "abc123" },
      "signatureRef": "sig:opscore/mysql-helper@1.4.0"
    }
  ]
}
```

`ParseIndex` decodes + validates every `PackageRef` (no network). `Index.Marshal`
serializes back. This is the on-disk contract a future HTTP/git Registry will
serve.

---

## 6. Relationship to existing layers

- **Phase 7.2 `ecosystem/packaging`** — a downloaded + unpacked archive becomes a
  `packaging.Package` via `Load(dir)`; `PackageRef` is the *pointer* to that
  archive, never the archive itself.
- **Phase 7.2 MUST-3 `isolation.AddFromPackage`** — the single legal entry from a
  discovered `PackageRef`'s resulting `Package` into the Runtime. The Marketplace
  never reaches the Runtime directly.
- **Phase 6.2 `internal/plugin/catalog`** — the *host-side* read-only index over
  already-loaded `manifest.Provider`s. The Registry is the *ecosystem-side*
  discovery index over *not-yet-downloaded* packages. They are complementary, not
  overlapping: Registry discovers; catalog reflects what is already loaded.
- **Phase 5.2 OCI Provider + Phase 5 Trust Pipeline** — `downloadURL` is resolved
  and the bytes are verified + trust-decided by those existing layers. The
  Registry only references (`signatureRef`); it does not reimplement trust.

---

## 7. Deferred to later phases (per GPT Round 33)

- **Version Compatibility Policy** → Phase 7.5. The Registry must first know what
  a Package looks like (this spec) before a version matrix is meaningful.
- **Transport (HTTP / OCI pull / Git clone)**, download, install, enable,
  upgrade, dependency resolution → later phases, consuming this specification.
