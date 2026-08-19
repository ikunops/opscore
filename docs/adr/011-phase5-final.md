# ADR-011 — Phase 5 Final: Supply-Chain Trust Pipeline (Stability Report)

**Status:** Final (Phase 5 CLOSED) · **Date:** 2026-07-31
**Supersedes / extends:** ADR-010 (Runtime Contract freeze)
**Sign-off:** Round 21 (5.1), Round 22 (5.2), Round 23 (5.3), Round 24 (overall PASS)

---

## 1. Summary

Phase 5 在**冻结的 Runtime Contract** 之上构建了完整的供应链信任通道（Supply-Chain
Trust Pipeline）。三层验证链全部为**纯外围**（peripheral），零修改 Runtime Contract
的字段 / 接口 / 生命周期状态。

| Sub-phase | Capability | Commits | Sign-off |
|-----------|------------|---------|----------|
| 5.1 | Signature Verification (Ed25519 detached sig) | `15520b6` | Round 21 ✅ |
| 5.2 | OCI / Git Provider (source-agnostic) | `b8d4224` | Round 22 ✅ |
| 5.3 | Signature Policy (trust root + required signer + rotation) | `2321c7c` + `035bc63` | Round 23 ✅ |
| 5.3 SHOULD | `ErrSignatureInvalid` / `ErrSignatureUntrusted` 分离 | (this round) | Round 24 ✅ |

**Phase 5 Overall:** Trust Pipeline CLOSED · Runtime Contract UNCHANGED.

---

## 2. Trust Boundary (single trust boundary, unchanged)

```
          Artifact Source (File / Git / OCI)
                      |
                      v
              raw manifest bytes
                      |
                      v
        Signature Verification   (Ed25519, crypto/ed25519)
                      |
                      v
        Signature Policy          (trust root + required signer + key rotation)
                      |
                      v
              Manifest Parse
                      |
                      v
           Manifest Validate (Phase 3 limits)
                      |
                      v
                Descriptor / Runtime Load
```

The verify-after / parse-before gate is the **single trust boundary** praised across
Rounds 21–24. Fail-closed: any unmet condition returns an error before `Parse` / `Load`.

---

## 3. Provider Matrix

All three providers implement the **same frozen `Provider` interface**
(`List` + `Read`) and route through the **same `VerifyManifest` gate** — one trust
boundary covers every source.

| Provider | Transport | Deps (offline-safe) | Signature gate |
|----------|-----------|---------------------|----------------|
| `FileProvider` / `SignedFileProvider` | local filesystem | stdlib `os` | `VerifyManifest(key, data, sig, v)` |
| `GitProvider` / `SignedGitProvider` | `git` CLI (`os/exec`) | system `git` | same |
| `OCIProvider` / `SignedOCIProvider` | OCI Distribution v2 | stdlib `net/http` (no oras-go) | same |

- **Git**: lazy `git clone --no-checkout` + `git show <ref>:<path>` (no working tree).
- **OCI**: minimal Distribution v2 client; auto Bearer auth (401 → token → retry, token
  cached); OCI layer located by `org.opencontainers.image.title` annotation.
- **Zero extra Go modules**: `go.mod` unchanged (still only `golang.org/x/crypto` +
  `modernc.org/sqlite`). No `oras-go` / `go-git` (would break offline `GOSUMDB=off`).

---

## 4. Signature Policy Matrix (error taxonomy — Round 24 refined)

| Situation | Error | Code | Fail-closed? |
|-----------|-------|------|--------------|
| `.sig` artifact absent but verifier configured | `ErrSignatureMissing` | `MISSING_SIGNATURE` | yes |
| manifest bytes changed / sig bytes changed / wrong-or-external key | `ErrSignatureInvalid` | `NO_MATCHING_KEY` | yes |
| signature valid but trusted key **expired / rotated out** | `ErrSignatureUntrusted` | `KEY_EXPIRED` | yes |
| empty / fully-expired trust root (no trusted identity) | `ErrSignatureUntrusted` | `NO_TRUST_ROOT` | yes |
| trusted key valid but namespace not permitted | `ErrSignaturePolicy` | `POLICY_REQUIRES_SIGNER` | yes |
| verifier nil (legacy unsigned mode) | — (skipped) | `NO_VERIFIER` | n/a |

> **Note on `ErrSignatureInvalid` vs `ErrSignatureUntrusted` (Round 24 SHOULD):**
> In a *closed* trust-root model an "externally-valid-but-untrusted" signature is
> undecidable — the runtime does not possess the external public key needed to prove
> validity. Such cases are therefore reported as `ErrSignatureInvalid` (cryptographic
> failure / cannot establish authenticity), which is the correct fail-closed posture.
> `ErrSignatureUntrusted` is reserved for trust-**root** problems (empty / expired root,
> or a signature that *is* cryptographically valid but under an expired trusted key).
> This keeps "密码学失败" and "信任策略失败" cleanly separated for audit.

`Verifier` interface itself is a Phase 5.1 **peripheral type** (may evolve) — NOT the
frozen Runtime Contract. `SignatureResult` (audit analogue of Phase 3.5
`CompatibilityResult`) and `AuditSink` are also peripheral; they never touch the
Runtime Audit Contract.

---

## 5. Out-of-scope (explicitly deferred — per Round 23 bounds)

- HSM / KMS
- CA hierarchy / certificate chains / trust delegation
- Sigstore · Fulcio / cosign
- Transparency logs (Rekor)
- Supply-chain attestation

These belong to Phase 6+ and would, if ever needed, attach as **peripheral verifiers**
— not Contract changes.

---

## 6. Phase 6 Candidate Order (recommended by GPT, Round 24)

Risk-ordered; **none requires a Runtime Contract change**:

1. **6.1 Sandbox / Isolation** (recommended next) — limits *post-load* blast radius:
   resource boundary, execution timeout, syscall/process isolation, permission envelope.
   Does NOT alter the load contract.
2. **6.2 Marketplace / Catalog** — plugin index, metadata discovery, version listing.
   NO auto-install, NO auto-trust.
3. **6.3 `.so` Dynamic Loading** — highest risk (ABI, memory safety, crash isolation,
   unload safety); defer until the Provider peripheral layer is stable.

---

## 7. Stability verdict

Phase 5 delivers a stable baseline: **Runtime Contract** (frozen) + **Provider
abstraction** (source-agnostic) + **Supply-chain trust boundary** (verify + policy).
All three layers share one trust boundary and one error taxonomy. Safe to freeze and
proceed to Phase 6.1.
