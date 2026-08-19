# Phase 7.7 — Reference Distribution Implementation

**Status:** Implemented (specification + reference client). Signed off Round 36.
**Package:** `ecosystem/dist` (Reference Implementation)
**Reuses:** `ecosystem/packaging`, `ecosystem/oci`, `ecosystem/registry`

## Purpose

Phases 7.1–7.6 produced a *specification set*. 7.7 is the first phase that
**moves bytes** — its only job is to prove the whole lifecycle is implementable:

```
Build → Package → OCI Push → OCI Pull → Unpack → Load → (AddFromPackage @ Runtime)
```

It is the **Reference** Implementation, deliberately parallel in naming to
`MemoryRegistry` (7.4) / Reference SDK (7.1) / Reference Tooling (7.3).

## Frozen boundaries (Round 36 MUST-1..5)

| MUST | Rule | How it is enforced |
|------|------|--------------------|
| 1 | Push / Pull / Fetch Manifest / Fetch Blob / Resolve Digest all go through a `Transport` interface; no vendor (Docker Hub / Harbor / GHCR / ECR) is the only impl. | `dist.Transport` interface; `LocalTransport` is the reference (local OCI layout, no network). |
| 2 | Reuse `packaging.Load → oci.Artifact → registry.PackageRef → isolation.AddFromPackage`; never bypass the single legal entry. | `Client.PullPackage` ends at `packaging.Load`; `Client.ResolveRef` uses `Artifact.ToPackageRef`. The final `AddFromPackage` is the host's (Runtime) job, not the client's. |
| 3 | Reference Implementation stays in the ecosystem layer; never enters Runtime. | AST guard in `dist_test.go` forbids any `internal/` import. |
| 4 | Trust / Compatibility / Registry are composed, not re-implemented. | `Client` calls `packaging`, `oci`, `registry` wholesale; it adds no signature/compat/metadata logic. |
| 5 | The client is pure artifact transport — NOT Installer / Updater / Dependency Resolver / Marketplace. | `Transport` exposes only push/pull/fetch/resolve; no enable/upgrade/resolve-deps surface. |

## Components

```go
type Transport interface {
    Push(ctx, ref oci.Tag, m *oci.Manifest, blobs map[oci.Digest][]byte) error
    Pull(ctx, ref oci.Tag) (*oci.Manifest, map[oci.Digest][]byte, error)
    FetchManifest(ctx, ref oci.Tag) (*oci.Manifest, error)
    FetchBlob(ctx, ref oci.Tag, digest oci.Digest) ([]byte, error)
    ResolveDigest(ctx, ref oci.Tag) (oci.Digest, error)
}

type Client struct { t Transport }
func NewClient(t Transport) *Client
func (c *Client) PushPackage(ctx, ref, files map[string]string) error
func (c *Client) PullPackage(ctx, ref, outDir string) (*packaging.Package, error)
func (c *Client) ResolveRef(ctx, ref, meta oci.PackageMeta) (registry.PackageRef, error)
```

`LocalTransport` stores artifacts in a local OCI image layout:

```
<root>/oci-layout            # oci.Layout marker
<root>/blobs/sha256/<hex>    # content-addressable blob bodies
<root>/index.json            # tag (name:version) -> manifest digest
```

## Digest ≠ Trust (carried from 7.6 MUST-5)

`dist` only carries **content digests**. A valid digest is content addressing,
never a trust signal. Signature verification remains the sole authority of the
Phase 5 Trust Pipeline (ADR-011); `dist` does not assert or imply trust.

## Media types (Round 36 SHOULD, centralized in `oci`)

- `application/vnd.opscore.plugin.manifest.v1+json`
- `application/vnd.opscore.plugin.config.v1+json`      (plugin.json)
- `application/vnd.opscore.plugin.release.v1+json`     (release.json)
- `application/vnd.opscore.plugin.binary`              (bin/*)
- `application/vnd.opscore.plugin.resource`            (resources/*)
- `application/vnd.opscore.plugin.layer.v1.tar`        (other files)

## Test coverage

- `TestDistImportsOnlyEcosystem` — AST guard: no `internal/` import.
- `TestLocalTransportRoundTrip` — Build→Push→Pull→Load, `pkg.Validate()` passes,
  file contents (binary + resource) survive the round trip.
- `TestResolveDigestAndFetch` — tag & digest-pinned resolution agree; fetched
  manifest validates; config blob equals the original plugin.json bytes.
