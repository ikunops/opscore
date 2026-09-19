# ADR-058 — Phase 42 Architecture: Verification Attestation

- **Status**: Proposed (Round 214, Architecture stage)
- **Phase**: 42（Verification Attestation）
- **Base**: `5820060c`（P42 Scope ADR-057，R213=B 修订版）；origin/main 权威 HEAD
- **配套**: Scope **`057-phase42-scope.md`**（Accepted, R214）
- **分工句（延续）**：P37 决定 **who**，P40 决定 **where**，P41 决定 **when**，**P42 决定 whether**。

> 本 ADR 只定**机制**；语义边界（什么算分歧、什么算尺子变了）在 ADR-057 §4-A4 / §4-A4.1，不可在此变更。

---

## 1. 范围

| 项 | 内容 |
|---|---|
| 交付 | 验证报告 + 验证账本 + 四判据派生 + 单一裁定 + 第三 anchor 家族 |
| 不交付 | witness 侧 verification 对账（ADR-057 A7-11 冻结）；告警/修复；时间权威 |
| 默认 | 关闭（`--export-verify-attest=false`）⇒ 既有面逐字节一致、零新增文件、新路由 503 |

---

## 2. 文件与接线

| 文件 | 类型 | 内容 |
|---|---|---|
| `internal/controlplane/server/snapshot_verification.go` | **新增** | 报告结构 / 合流 / 账本 / 窗口 / 四判据 / 比较引擎 / 锚定调用 |
| `internal/controlplane/server/snapshot_verification_test.go` | **新增** | T180~T197 |
| `internal/controlplane/server/snapshot_anchor.go` | **仅加法** | +3 个 `omitempty` 字段（`ReportSeq`/`ReportDigest`/`Overall`，进 `anchorEntry` 与 `anchorSigned`）+ `anchorKindVerification` 常量。**已有函数体零改动**（字节等价由 T196b 钉住） |
| `internal/controlplane/server/server.go` | 改 | 注册 2 条路由（紧邻 `server.go:1654` 既有 anchor 路由） |
| `internal/controlplane/server/history_export_scheduler.go` | 改 | 构造守卫 + `verifyTick` 调度；既有构造分支零改动 |
| `cmd/opscore/main.go` | 改 | 3 个新 flag（§7） |
| `internal/controlplane/server/appendonly_log.go` | **零改动**（复用） | `readLogLines` / `appendLogLine` / `compactLogPrefixGroups` |
| `history_export_manifest.go` / `snapshot_signature.go` / `snapshot_chain.go` / `snapshot_ledger.go` / `history_export_coverage.go` / `snapshot_key_lifecycle.go` | **零改动** | P42 只**读取**它们的输出 |

**关键约束**：`VerifySnapshotsDetailed`（`history_export_manifest.go:544`）与 `Coverage`（`history_export_coverage.go:99`）**函数体零改动**，P42 只在其**外层**组装报告。

---

## 3. 数据结构

```go
// 逐 publication 的七维取值 + 该条目的合流裁定
type VerificationItem struct {
    PublicationID int64  `json:"publication_id"`
    Status        string `json:"status"`                  // P35 六态
    Signature     string `json:"signature,omitempty"`     // P37 五态 + P41 三态
    Chain         string `json:"chain,omitempty"`         // P38
    ChainSource   string `json:"chain_source,omitempty"`  // P39
    Anchor        string `json:"anchor,omitempty"`        // P40 roll-up
    Lifecycle     string `json:"lifecycle,omitempty"`     // P41 尺子（bounded|unbounded）
    Coverage      string `json:"coverage,omitempty"`      // P36（报告级，见 §3.2）
    Overall       string `json:"overall"`                 // attested|unattested|contradicted
}

// DimensionAvailability 是本次报告的「尺子」——每维是否可评估。
// 它是 R42-2 口径归一的唯一输入，必须与 Items 同源、同一次求值产生。
type DimensionAvailability struct {
    Status      bool `json:"status"`
    Signature   bool `json:"signature"`
    Chain       bool `json:"chain"`
    ChainSource bool `json:"chain_source"`
    Anchor      bool `json:"anchor"`
    Lifecycle   bool `json:"lifecycle"`
    Coverage    bool `json:"coverage"`
}

type VerificationSubject struct {
    Publications []int64 `json:"publications"`
    MinID        int64   `json:"min_id"`
    MaxID        int64   `json:"max_id"`
    Limit        int     `json:"limit"`
    Truncated    bool    `json:"truncated"`
}

type VerificationReport struct {
    ReportSeq  int64                 `json:"report_seq"`
    Subject    VerificationSubject   `json:"subject"`
    Evaluable  DimensionAvailability `json:"evaluable"`
    Items      []VerificationItem    `json:"items"`
    Overall    string                `json:"overall"`
    Reasons    []VerificationReason  `json:"reasons"`
    VerifiedAt string                `json:"verified_at"`
    Anchor     *verificationAnchor   `json:"anchor,omitempty"`
    Signature  *signatureBlock       `json:"signature,omitempty"`
}

type VerificationReason struct {
    Dimension string `json:"dimension"`
    Value     string `json:"value"`
    Evaluable bool   `json:"evaluable"`
    Blocking  bool   `json:"blocking"`
}
```

### 3.1 账本行（复用 `appendonly_log.go`）

```go
type verificationLogEntry struct {
    ReportSeq  int64                `json:"report_seq"`
    Subject    VerificationSubject  `json:"subject"`
    Evaluable  DimensionAvailability `json:"evaluable"`
    Items      []VerificationItem   `json:"items"`
    Overall    string               `json:"overall"`
    Reasons    []VerificationReason `json:"reasons"`
    VerifiedAt string               `json:"verified_at"`
    PrevDigest string               `json:"prev_digest,omitempty"`
    Digest     string               `json:"digest"`
    StreamID   string               `json:"stream_id"`
    KeyID      string               `json:"key_id"`
    Sig        string               `json:"sig,omitempty"`
}
```

- **分组键** `verificationGroupOf(raw) → e.ReportSeq`（注入 `appendonly_log.go` 的 `groupClassifier`，原文件零改动）。
- **`report_seq` 来源**：`nextReportSeq()` 在**与账本读取同一临界区**内取 `maxSeq+1`（P41 R41-6）。**无水位文件。**
- **哈希链** `Digest = sha256(canonicalP37(entry minus Sig/Digest))`，`PrevDigest` 指向前一行；**首行豁免**（I3）。
- **签名**复用 P37 `exportSigner`（`--export-sign-key`），零新建密码学。

### 3.2 七维取值来源（逐维钉死，禁推断）

| 维度 | 来源 | evaluable 条件 |
|---|---|---|
| `status` | `VerifyResult.Status`（P35 六态） | 恒 true |
| `signature` | `VerifyResult.Signature.Verdict` | `Signature != nil` |
| `chain` | `VerifyResult.Chain` | `!= ""` |
| `chain_source` | `VerifyResult.ChainSource` | `!= ""` |
| `anchor` | `summarizeAnchor(loadAnchorState(...))` roll-up | `s.anchorEnabled()` |
| `lifecycle` | 生命周期账本可验性 ruler（`bounded` / `unbounded`） | 账本已加载且可验 |
| `coverage` | `Coverage(nil, nil, limit)` 的 `CoverageResult` | 调用成功（`nil, err` ⇒ 不可用） |

**两条防重复计数纪律（必须）：**

- **(D1) lifecycle 只记尺子，不记裁决。** P41 的四个 verdict 已在 `VerifySnapshotsDetailed` 内部经 `authorizeByLifecycle`（`history_export_manifest.go:597`）折进 `Signature`。P42 **不得**再把它们当独立维度计一次 ⇒ `lifecycle` 维度取值只能是 `bounded`（该 key 的全部事件在幸存窗口内 ⇒ 时间边界可断言）/ `unbounded`（否）。
- **(D2) coverage 是报告级维度。** 不为每份 publication 各跑一次 `Coverage`（I/O 放大 + 假装成逐条证据）。它求值一次，写入 `Evaluable.Coverage` 并**以同一取值落到每个 item**，在 reason 里显式标 `scope=report`。

### 3.3 合流：分类表（classOf）

| 维度 | `ok` | `contradicted`（已断言的坏） | `unattested`（不可断言） |
|---|---|---|---|
| status | `ok` | `mismatch` | `unknown` / 其余 |
| signature | `signature_ok` | `invalid` / `signature_before_activation` / `signature_after_rotation` / `signature_after_revocation` / `signature_time_unparseable` | `key_unknown` / `malformed` / `absent` / `foreign_stream` |
| chain | `predecessor_verified` / `retention_boundary` | `chain_broken` | `chain_absent` |
| chain_source | `disk` / `ledger` / `disk+ledger` | — | 空（且 chain 已 verified） |
| anchor | `anchored` | — | `pending` / `unanchored` / 空（ADR-052 A5：未确认不参与断言） |
| lifecycle | `bounded` | — | `unbounded` |
| coverage | `complete` | — | `gap` / `indeterminate` / `unknown`（**P36 永不指篡改**） |

合流算法（`mergeOverall(dims)`）：

```
若任一已断言维 ∈ contradicted  ⇒ contradicted
否则若存在维 ∈ unattested 或存在维不可评估 ⇒ unattested
否则 ⇒ attested
```

两级：先对每条 `item` 求 `item.overall`，再对全部 item 做**同一函数**的单调归并（任一 contradicted ⇒ contradicted；否则全 attested ⇒ attested；其余 unattested）得 `report.overall`。
⇒ 单调铁律（ADR-057 I2）在两级上用**同一个函数**成立，不存在第二套合流逻辑。

---

## 4. 流程

### 4.1 写路径（`attestVerification`，唯一入口）

```
1  results, chainVerdict := VerifySnapshotsDetailed(limit)   // 只读，零改动
2  cov := Coverage(nil, nil, limit)                          // 只读，零改动
3  anchorRollup := summarizeAnchor(loadAnchorState(...))     // 只读
4  lifecycleRuler := lifecycleRulerOf(loadKeyLifecycle(...)) // 只读
5  report := build(items, evaluable, reasons)                // 确定性；reasons 为空 ⇒ 返回错误（fail-closed）
6  seq := nextReportSeq()                                    // 与账本读取同临界区
7  entry := sign(report, seq, prevDigest)                    // P37 规范字节
8  appendLogLine(verification-log.jsonl, entry)              // 真 O_APPEND；账本不可验 ⇒ 拒绝（I2 推广）
9  anchorVerificationReport(&report)                         // R42-1；失败不回滚、不阻塞
```

- **顺序冻结**：先落账本（8）后派发锚定（9）—— 与 ADR-052 A2「禁 anchor-first」同构；锚定失败**绝不**回滚已落账的事实。
- **非重入**：`verifyTick` 沿用 P34 的 Tick 非重入纪律。

### 4.2 读路径（`handleVerification` GET）

- **严格只读**：只加载账本派生视图，**不触发验证、不写任何文件**（与 ADR-053 §6-13 reconcile 零副作用同构）。
- 派生：`window` → `absent` / `gap`（含 `gap_indeterminate`）/ `divergent[]` / `scope_changed[]` / `window_discontinuous`。
- 未启用 ⇒ **503**（不回退、不返回空成功）。

---

## 5. R42-2 比较引擎（机制）

### 5.1 输入与位置

比较发生在**读路径**（§4.2），输入是账本里**幸存窗口内**的全部报告，按 `report_seq` 升序。
同一 publication 出现的**全部**报告两两按**相邻序**（`r_k → r_{k+1}`）比较 —— 相邻比较可定位变化点，且窗口截断时仍保持「最后一次变化」可观测。

### 5.2 可用性向量（唯一定义）

```go
func availabilityOf(r *verificationLogEntry, p int64) map[string]bool {
    out := map[string]bool{}
    for _, d := range allDimensions {
        out[d] = r.Evaluable.get(d) && valueOf(r, p, d) != ""
    }
    return out
}
```

全局尺子（`Evaluable`）∧ 该条目确实取到值 ⇒ **两者缺一不可**（例如：签名已启用但某份 pre-v3 manifest 无 `Signature` ⇒ 该条目该维不可比）。

### 5.3 判定（严格按 ADR-057 §4-A4.1）

```go
func classifyChange(r1, r2 *verificationLogEntry, p int64) changeKind {
    a1, a2 := availabilityOf(r1, p), availabilityOf(r2, p)
    // 规则 1：可比维度集合上的取值分歧
    for _, d := range allDimensions {
        if a1[d] && a2[d] && valueOf(r1,p,d) != valueOf(r2,p,d) {
            return divergent            // 附 dimension=d
        }
    }
    // 规则 2：尺子完全相同而结论仍分歧 ⇒ fail-closed 判 divergent（S3）
    if sameAvailability(a1, a2) && overallOf(r1,p) != overallOf(r2,p) {
        return divergent
    }
    // 规则 3：仅评估范围不同
    if !sameAvailability(a1, a2) {
        return scopeChanged             // 附 changed_dimensions[]
    }
    return consistent
}
```

- 规则 1 优先于规则 3：**只要可比维上真有取值分歧，就必须是 divergent**，哪怕可用性也变了（保守方向，S3）。
- `divergent` / `scope_changed` 两类条目**都**携带 `from_overall` / `to_overall` / 后者 `reasons[]`（S1：scope_changed 不是静默）。
- 比较类别**绝不改写** `r2` 自己的 `overall`（S2）。

### 5.4 窗口约束

`divergent` / `scope_changed` **仅在两次报告的 `report_seq` 都落在幸存连续窗口 `[min,max]` 内**时产出；否则该 publication 的比较结果整体不产出（I5）。

---

## 6. R42-1 锚定（机制）

### 6.1 第三家族

- 文件 `verification-anchor.jsonl`（export dir 同域），路径 helper `verificationAnchorPath(dir)`。
- 常量 `anchorKindVerification = "verification"`。
- **独立 seq 家族**：`anchorGroupOf`（`snapshot_anchor.go:351`）取 `AnchorSeq` 为分组键、与 `Kind` 无关 ⇒ 第三家族自动独立，**该函数零改动**。
- 新增字段（**全部 `omitempty`**，P40/P41 家族零值 ⇒ 字节不变）：

```go
// anchorEntry / anchorSigned 内追加
Kind         string `json:"kind,omitempty"`          // 已有（P41）
ReportSeq    int64  `json:"report_seq,omitempty"`    // 新增
ReportDigest string `json:"report_digest,omitempty"` // 新增
Overall      string `json:"overall,omitempty"`       // 新增
```

### 6.2 派发

`anchorVerificationReport(r *VerificationReport) error` **逐行照抄** `anchorKeyLifecycleEvent`（`snapshot_anchor.go:940`，R212 已获批准的第二家族模板）：

```go
path := verificationAnchorPath(s.cfg.Dir)
seq, err := nextAnchorSeqPath(path)
ae := anchorEntry{ AnchorSeq: seq, Kind: anchorKindVerification,
                   ReportSeq: r.ReportSeq, ReportDigest: reportDigest, Overall: r.Overall,
                   RecordedAt: s.clock().UTC().Format(time.RFC3339Nano),
                   State: anchorStatePending }
s.signer.signAnchorEntry(&ae, s.cfg.Dir, s.clock())   // 签名
appendAnchorEntryPath(path, s.cfg.AnchorCapacity, ae) // pending 先落盘（R40-6）
return s.dispatchAnchorPath(context.Background(), path, ae)
```

- **`ReportDigest = sha256(canonicalP37(report minus Signature/Anchor))`** —— 复用 P37 序列化器，**禁第二套 canonical**。
- **只写不建对账面**：**不**为 verification 家族新增 reconcile 路由（ADR-057 A7-11）。
- **I7 锚定不回流**：`r.Anchor` 只作为**引用**写进报告展示，**不进 `Evaluable`、不进 `reasons[]`、不参与 `mergeOverall`**（防循环依赖 + 防外部系统成为判据来源）。

---

## 7. 路由、flag、构造守卫

### 7.1 路由（`server.go`，紧邻既有 anchor 路由）

```
GET  /management/v1/protection/alerts/history/export/verification   → 派生视图（只读）
POST /management/v1/protection/alerts/history/export/verification   → 触发一次 attest（同步）
```

路由前缀沿用 P35 系列（`/management/v1/protection/alerts/history/export/...`），与 P41 的 `/management/v1/protection/export/key-lifecycle` 不同命名空间 —— 以**被扩展的面**为准（P42 扩展的是 export 面）。:8082 admin-only，沿用既有鉴权。未启用 ⇒ **503**。

### 7.2 flag

| flag | 默认 | 说明 |
|---|---|---|
| `--export-verify-attest` | `false` | 总开关 |
| `--export-verify-interval` | `0` | 0 = 禁用定时验证（Q3 裁决） |
| `--export-verify-capacity` | `4096` | `verification-log.jsonl` 保留的报告组数（0 = 全留） |

**锚定零新增 flag**：复用 P40 的 `--export-anchor-endpoint` / `-timeout` / `-max-attempts` / `-capacity`，以及 P37 的 `--export-sign-key` / `--export-trust-keys`。

### 7.3 构造守卫（fail-fast，与 `history_export_scheduler.go:259/281/307` 三处既有风格一致）

1. `attest && signer == nil` ⇒ 构造失败：未签名的报告与事后伪造的报告不可区分 ⇒ **不是证据**（与 P37 T79b「能签不能验」/ P41 I1 同构）。
2. `attest && trust == nil` ⇒ 构造失败（同上）。
3. `interval > 0 && !attest` ⇒ 构造失败（配置自相矛盾）。
4. `anchorEnabled() && attest` ⇒ verification 家族启用；`!anchorEnabled()` ⇒ **不写** `verification-anchor.jsonl`（零副作用，T192）。

---

## 8. 窗口与 gap 派生

- `window = {min, max, entries, continuous}`，由幸存 `report_seq` 的连续段计算（与 `anchorWindow` 同构）。
- `gap` **仅在 `continuous && min == 1` 时可断言**（账本从未被裁过头 ⇒ 幸存报告 = 全部历史报告）；否则 `gap_indeterminate: true` 且 `gaps` 为空 —— **空列表必须带 `gap_indeterminate` 说明，绝不静默返回空**。
- 已知弱点：删账本后重启会重新得到 `min == 1`，从而对重启前已验证的 publication 误报 gap（ADR-057 §6-8，本地不可检测；域外可检测但本轮冻结对账）。

---

## 9. 零回归自证

| 面 | 自证方式 |
|---|---|
| 既有 4 个只读面响应 | T191 同源比对，逐字节 |
| `appendonly_log.go` / `snapshot_signature.go` / `snapshot_chain.go` / `snapshot_ledger.go` / `history_export_coverage.go` / `history_export_manifest.go` / `snapshot_key_lifecycle.go` | T193 blob 哈希逐条比对 |
| `snapshot_anchor.go` 仅加法 | T196b：P40 publication 条目 + P41 lifecycle 条目的序列化字节与 canonical 字节，**与基线 `5820060c` 逐字节一致** |
| `go.mod` / `go.sum` | T193 零改动 |
| 冻结包 | `platform` / `governance` / `plugin/{runtime,isolation}` / `controlplane/hostregistry` 零 diff |
| 未启用 | T192：不产生 `verification-log.jsonl` 与 `verification-anchor.jsonl`；新路由 503 |

---

## 10. 测试映射（T180~T197）

| 编号 | 落点 |
|---|---|
| T180 | 两次 `build()` 同输入 ⇒ canonical 字节相同（`verified_at` 除外） |
| T181 | 表驱动 `mergeOverall`（七维 × 取值）+ 两级合流 |
| T182 | 不可断言维 ⇒ `unattested` |
| T183 | `reasons[]` 非空 / `blocking` / 空理由链 ⇒ error |
| T184 | `appendLogLine` 后既有字节零变；同 seq 第二行 ⇒ conflict 且字节不变 |
| T185 | 账本出现不可解析行 ⇒ 拒绝追加 |
| T186 | compaction 后窗口收缩 / 窗口外不判 / `window_discontinuous` |
| T187 | 空账本 ⇒ `absent=true`（删账本红例） |
| T188 | 3 份发布只验 1 份 ⇒ gap 区间；`min != 1` ⇒ `gap_indeterminate` |
| T189 | 先 attested 后 contradicted ⇒ `divergent` |
| T190 | 首行 `prev_digest` 指向被裁条目不算篡改 |
| T191 | 未启用 ⇒ 既有 4 面逐字节一致 |
| T192 | 未启用 ⇒ 零新增文件 |
| T193 | 冻结面 blob 零 diff |
| T194 | `--export-verify-interval` 默认 0 ⇒ 无写入、无 goroutine |
| T195 | M1~M6 变异，含 **M5 摘 `anchorVerificationReport` ⇒ T196 红**、**M6 摘 §5.3 归一支（改成只看 overall）⇒ T197 红** |
| T196 | 锚定：pending 先落盘、`kind=verification`、独立 seq 家族 |
| T196b | P40/P41 条目字节等价（§9） |
| T197 | ①配置翻转 ⇒ `scope_changed`（非 divergent）；②真篡改 ⇒ `divergent`；③可用性一致而 overall 不同 ⇒ `divergent` |

**测试注入**：沿用 P40 T154 的函数字段钩子惯例 —— `beforeVerificationAppend` / `beforeAnchorDispatch`，失败仍真实清理（Windows 句柄）。时钟走既有 `s.clock()`。禁用 `t.Context()`（go.mod 1.22）。

---

## 11. 未决（提请 judge 在 R214 裁决）

1. **路由命名空间**：P42 用 `/management/v1/protection/alerts/history/export/verification`（随被扩展的 export 面），而 P41 的 key-lifecycle 在 `/management/v1/protection/export/...`。两条前缀并存是否接受？（我倾向随 export 面，理由：P42 的 subject 就是 export 产物。）
2. **D2 coverage 报告级**：为每份 publication 各跑一次 `Coverage` 是否值得？（我倾向否，理由见 §3.2-D2。）
3. **gap 的 `min == 1` 门槛**（§8）是否过严 —— 它让任何一次 compaction 之后 gap 永久不可断言。这是 R40-1 同构的保守选项，请确认。
