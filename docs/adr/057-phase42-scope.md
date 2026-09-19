# ADR-057 — Phase 42: Verification Attestation（验证留痕 / 判据覆盖与合流裁定）

- **Status**: Proposed (Round 213, Scope stage)
- **Phase**: 42（Verification Attestation）
- **Base**: Phase 41 CLOSED（R212=A，origin/main = `6b4f239e`）
- **Author**: executor（WorkBuddy），方向自拍板授权（用户 2026-08-27）
- **Supersedes**: 无。本 ADR **不修改** P35/P36/P37/P38/P39/P40/P41 的任何冻结判据。
- **配套**: Architecture ADR 待 Scope 裁决 A 后交付（预定 `058-phase42-architecture.md`）。
- **分工句（延续）**：P37 决定 **who**，P40 决定 **where**，P41 决定 **when**，**P42 决定 whether**。

---

## 1. 方向选择

### 1.1 候选淘汰表

| 候选 | 判定 | 理由 |
|---|---|---|
| **时间权威（外部 TSA / RFC3161 时间戳）** | ❌ 淘汰（记录为后续候选） | P41 已把「`signed_at` 自证」显式列为已知代价，缺的是**绝对时间**。但把 digest 推给外部可信方换回一个时间凭证，与 P40 Publication Anchoring **结构同构** —— 判据类别仍是「该 digest 是否被域外记录」，不产生**新类别**的可断言判据，只是 P40 的第二实例；同时引入第二个外部在线依赖，违背 8-bit 的 Single Binary / Embedded Default。**它被 P41 的「时间边界」部分替代**：账本给的是相对边界（which key, when），绝对时间只在跨域对账时有意义，而那条路 P40 已用更低成本走通。 |
| **HA 多副本** | 🔒 仍冻结 | R213 明确：HA 仍冻结，解冻需新的主线接口论证。本轮不开放（与 R210 论证一致：一致性不是证据性，不产生新判据）。 |
| **呈现/运维面「单一视图」**（judge R213 提示 1） | 🟡 **部分采纳，但不独立成 Phase** | 纯粹的合流函数 `f(六维) → 单一结论` **不产生新信息**：它是已有判据的函数。按 R210 击倒 HA 的同一把尺子（「不产生新可断言判据」），单独成 Phase 会被判为「呈现层改动」。**但它是 P42 的天然副产物**（§4-A2 / §4-A8 交付），因为一旦验证要落账，就必须先有确定性的裁定对象。 |
| **Verification Attestation（验证本身的证据化）** | ✅ **选定** | 见 §2。它是唯一一个「现有判据集合在**求值时机**上缺一维」的候选：P35~P41 全部是**惰性纯函数**，由此产生一个在运营上必然发生、而在模型里无解的不可区分性（§2.2）。 |

### 1.2 P22 时代遗留候选自查

| 遗留候选 | 现状 | 结论 |
|---|---|---|
| 每能力资源配额 | `mgmt_quota.go` + ADR-051 | 已闭合，不开放 |
| Dashboard-UI kill 翻转面 | `mgmt_kill.go` + P22-9 | 已闭合，不开放 |

⇒ R213 无需解冻任何遗留候选。

---

## 2. 新问题（Problem Statement）

### 2.1 现状事实（已核对代码，非推断）

**(a) 验证是一次性的、无状态的、蒸发式的只读查询。**

`handleHistoryExportVerify`（`internal/controlplane/server/mgmt_obs.go:862`）转调
`HistoryExportScheduler.VerifySnapshotsDetailed`（`history_export_manifest.go:544`）。两者的契约注释都明写：

> `// Strictly read-only: nothing here writes, deletes, or repairs (M3).`

全仓检索 `verified_at` / `last_verified` / `verification-log` ⇒ **零命中**。验证结论只存在于那一次 HTTP 响应的字节里，响应返回即蒸发。

**(b) 六维判据分散在 4 个互不相干的只读面，没有任何一处合流。**

| 面 | 承载维度 |
|---|---|
| `GET /management/v1/protection/alerts/history/export/verify` | status(P35) + signature(P37) + chain(P38) + chain_source(P39) |
| `GET .../export/coverage` | coverage(P36) |
| `GET|POST .../export/anchor[/reconcile]` | anchor(P40)，且 reconcile 契约**零副作用**（ADR-053 §6-13） |
| `GET|POST .../export/key-lifecycle` | lifecycle(P41) |

⇒ 合流发生在**读者的头脑里**，而人是会犯错的：两个运营者看同一份产物可以得出不同结论，且没有任何一方能拿出「结论 + 理由链」的凭证。

### 2.2 核心悖论：惰性判据（lazy verdict）

P35~P41 的每一个判据都是**纯函数**：`f(产物) → 判据`。它们**只在被调用的那一刻存在**。

由此推出一个尖锐的后果：**攻击者只要不触发验证，就永远不产生不利结论。**

设攻击者持有签名私钥并控制文件系统，删掉 publication #100、重签 #101 令 `prev=99`：

- P38 会报 `chain_broken`；
- P40 会报 `orphan_witness`；
- P41（若已吊销 K1）会报 `signature_after_revocation`；

——**但前提是有人去问**。无人问 ⇒ 系统在所有接口上呈现的是**一片沉默**；而这片沉默与「一切正常」在现有任何接口上**字节不可区分**。

运营侧的同形不可区分性：

| 实际状态 | 现有系统呈现 |
|---|---|
| 从未有人验证过 | 沉默 |
| 验证过，全部 ok | 沉默 |
| 验证过，报了 broken，无人处理 | 沉默 |
| 验证过，报了 broken，已修复 | 沉默 |

⇒ 这与 P36 面对的不可区分性**完全同构**，只是覆盖对象从**记录（seq）区间**变成了**判定（publication）区间**：
P36 问「这段数据有没有被导出覆盖」，P42 问「**这批判据有没有被求值覆盖**」。

**本地密码学消除不了**：不调用纯函数就不会有输出；而在没有留痕的前提下，「事后补写一条验证记录」与「当时确实验证过」不可区分。

### 2.3 两个派生的同形性

1. **同 R40-1（窗口丢失）**：验证账本必然有界 + 前缀 compaction ⇒ 早期验证记录被**合法**裁掉后，「从未验证」与「验证过但记录已裁」不可区分 ⇒ 一律不做断言（§4-A4 直接继承 P40 窗口纪律）。
2. **同 R41-1（时间边界）**：验证也有 when —— `verified_at`。同一 publication 在不同时刻被验证可能给出**不同**结论；这个差异**不是噪声而是情报**（证据状态发生了变化），必须可被断言（§4-A4 `verification_divergent`）。

### 2.4 问题的精确表述

> 现有体系证明了「产物**可被**验证」，但无法证明「**验证发生过**」，也无法断言「**哪些产物从未被任何验证覆盖**」。
> 判据是惰性的 ⇒ 不验证的代价为零 ⇒ **沉默与正常不可区分**。

### 2.5 为什么必须由 P42 解决、不能并入既有 Phase

- **不并入 P36**：P36 是严格只读查询，覆盖对象是 **seq 记录区间**；P42 覆盖的是 **publication 判定区间**，且需要**新的持久化**（P36 铁律是零副作用，与之直接冲突）。两轴严格分离（同 P40「`anchor_seq` 永不参与 predecessor 裁决」）。
- **不并入 P40**：P40 锚定的是**发布事实**（publication 发生了），P42 记录的是**判定事实**（我认为结论是什么）。**发布不可重放，验证可重放**；两者混流会破坏 ADR-053「单一 `anchor_seq` 序列」的连续性不变量 —— 与 P41 的 I5 同构，judge 在 R212 已批准该论证。
- **不并入 P41**：P41 决定 when（密钥何时有效），P42 决定 whether（判据是否曾被求值）。

---

## 3. 决策（Scope）

**Phase 42 = Verification Attestation**：把「验证」从一次性只读查询升级为**一次有记录的判定事件**。四项产出：

1. **验证报告 `VerificationReport`** —— 一次完整验证的确定性快照：六维取值 + 合流裁定 `overall` + 强制理由链 `reasons[]`。
2. **验证账本 `verification-log.jsonl`** —— append-only，复用 P40 `appendonly_log.go` 共享原语（**持久化纪律零新建**）。
3. **新判据（核心产出，P35~P41 给不出）** —— `verification_absent` / `verification_gap` / `verification_divergent`。
4. **单一裁定（回应 judge R213 提示 1）** —— `overall ∈ {attested, unattested, contradicted}` + `reasons[]`。

---

## 4. 能力定义（A1~A8）

### A1 — 验证报告（确定性对象）

```go
type VerificationReport struct {
    ReportSeq  int64                 `json:"report_seq"`
    Subject    VerificationSubject   `json:"subject"`     // 本次验证覆盖到的 publication 集合与上下界
    Dimensions VerificationDims      `json:"dimensions"`  // 六维原始取值（原样搬运，不重算、不改写）
    Overall    string                `json:"overall"`     // attested | unattested | contradicted
    Reasons    []VerificationReason  `json:"reasons"`     // 强制非空
    VerifiedAt string                `json:"verified_at"` // 自证时间（§6-3）
    Signature  *signatureBlock       `json:"signature,omitempty"` // 复用 P37 规范，零新建
}
```

- **确定性**：同一输入 ⇒ 同一报告字节。canonical 序列化**复用 P37 `canonicalSignaturePayload` 的序列化器，禁第二套**（ADR-052 A1 纪律的第二次实例化）。`verified_at` 是唯一非确定输入，必须显式标注为自证。
- **`report_seq` 从账本导出**：在与读取**同一临界区**内取 `maxSeq+1`（P41 R41-6 纪律），**禁独立水位文件**（照搬 P35 `publication-seq.state` 会造 seq 空洞 ⇒ `window_discontinuous` 自伤）。

### A2 — 合流规则（核心：单调性 + 理由链）

输入七维：`status`(P35 六态) / `signature`(P37) / `chain`(P38) / `chain_source`(P39) / `anchor`(P40) / `lifecycle`(P41) / `coverage`(P36)。

| `overall` | 条件 |
|---|---|
| `attested` | **每一个**可评估维度都给出 ok 类取值，**且不存在**任何不可断言维度 |
| `contradicted` | **至少一个**维度给出已断言的 broken/mismatch 类取值 |
| `unattested` | 既非 attested 也非 contradicted —— 存在不可断言维度（unknown / indeterminate / unavailable / 未启用 / 覆盖不足） |

**合流单调铁律（三条，缺一不可）：**

1. 合流**不得**比最坏的**已断言**维度更乐观；
2. **不得**因某维度不可断言就判 `contradicted` —— **不可证明 ≠ 已证伪**；
3. **不得**让可断言维度去补偿不可断言维度（证据不可相加）。

`reasons[]` **强制非空**，每维一条 `{dimension, value, evaluable, blocking}`，`blocking=true` 表示它使 `overall ≠ attested`。
**没有理由链的裁定不产出**（fail-closed）。

### A3 — 验证账本（持久化纪律零新建）

- 文件 `verification-log.jsonl`，**与 export dir 同域**（P41 R41-3 同构，异域 ⇒ 整本不可验）。
- 复用 `appendonly_log.go`：`readLogLines` / `appendLogLine` / `compactLogPrefixGroups`，注入 classifier。**该文件零改动**。
- **分组键 = `report_seq`**；**同 seq 出现任何第二行（payload 相同也算）⇒ conflict ⇒ fail-closed**。
  ⇒ 验证结论是**事实不是状态**（P41 已获 judge 批准的同一纪律，与 ADR-053 anchor 的「状态推进」**刻意相反**）：重复写入就是伪造，不存在「状态推进」这种合法解释。
- 哈希链 + 签名复用 P37 规范字节（同一把 `--export-sign-key`）；**首行豁免**（P41 I3 同构：合法 prefix compaction 后首行 prev 指向已被裁掉的条目，判篡改会自伤）。
- **账本不可验 ⇒ 拒绝追加**（P41 I2 推广）：不可解析行 / 签名不验 / 哈希链断裂 / 异域 stream，**一概不写**。

### A4 — 新判据（核心产出）

| 判据 | 含义 | 为什么 P35~P41 给不出 |
|---|---|---|
| **`verification_absent`** | 账本为空（零报告）⇒ 可断言「**从未验证过**」 | 此前沉默只是**无声**；现在沉默是一个**可断言的事实** |
| **`verification_gap`** | 存在已发布的 publication **从未被任何报告的 subject 覆盖** ⇒ 可断言并给出区间 | P36 覆盖的是记录，不覆盖判定 |
| **`verification_divergent`** | 同一 publication 被两次报告覆盖且 `overall` **不同** ⇒ 可断言「**证据状态发生了变化**」 | 这是「删档重签」攻击在**验证维度**的投影：攻击前 `attested`、攻击后 `contradicted`，两次都在账本里 ⇒ **变化本身留痕** |

**窗口纪律（R40-1 同构，强制）**：账本有界 ⇒ 早期记录被合法裁掉后与「从未记录」不可区分 ⇒ **绝不断言 gap**。结果**强制携带** `verification_window{min,max,entries,continuous}`；窗口外不判；段不连续 ⇒ `window_discontinuous` ⇒ 不做正面断言。

### A5 — 与既有 Phase 的关系（正交，不互推）

- P42 **不改变**任何既有判据：`VerifySnapshotsDetailed` 函数体**零改动**；既有 4 个只读路由**零 diff**（P42 走新增独立路由，见 §8-Q2）。
- **`attested` 不等于「历史无篡改」**（与 P38「`chain_ok` 不等于整个历史无删除」严格同构）。
- **验证报告不锚定 P40**（非目标，理由见 §8-Q1）。

### A6 — 默认零回归（硬约束）

- 未启用（`--export-verify-attest` 未开或 scheduler 未配置）⇒ 既有**全部**只读面响应**逐字节一致**，**不产生任何新文件**，新路由 503（与 P34/P41「未启用 ⇒ 503 不回退」纪律一致）。
- **冻结包零 diff**：`platform` / `governance` / `plugin/{runtime,isolation}` / `controlplane/hostregistry`；`go.mod` / `go.sum` 零改动；`appendonly_log.go` 零改动（仅复用）；`snapshot_signature.go` / `snapshot_chain.go` / `snapshot_ledger.go` / `snapshot_anchor.go` / `snapshot_key_lifecycle.go` / `history_export_coverage.go` 零改动。
- **定时验证默认关闭**（`--export-verify-interval` 默认 0），仅显式 POST 触发 ⇒ 默认零新增磁盘写入。

### A7 — 非目标（重新冻结清单）

1. 不做告警 / 通知 / 升级（运营动作，非证据）。
2. 不自动修复、不重发布、不回滚。
3. **不提供时间权威**（`verified_at` 自证，与 P41 同）。
4. 不做验证报告的域外锚定（§8-Q1）。
5. 不改变 P36 coverage 的任何取值（记录维度 × 判定维度，两轴分离）。
6. 不引入外部依赖、不做 RFC3161/TSA。
7. 不把 `overall` **回写**进任何既有响应字段（只新增独立字段/路由）。
8. **不做「验证失败 ⇒ 停止导出」** —— 那会制造反向 fail-open：攻击者只要让验证失败就能掐断发布。
9. 不做验证者身份判定（验证由同一进程执行，见 §6-1）。

### A8 — 结果模型

```
VerificationAttestation {
    overall: attested | unattested | contradicted
    reasons[]: {dimension, value, evaluable, blocking}
    window:   {min, max, entries, continuous}
    coverage: {gaps[]: [from_id, to_id], absent: bool, divergent[]: {publication_id, from, to}}
}
```

---

## 5. 不变量（I1~I6）

- **I1 报告确定性**：同输入 ⇒ 同字节（`verified_at` 除外，且它必须显式标注自证）。
- **I2 单调合流**：不得比最坏已断言维更乐观；不可断言 ≠ 已证伪；证据不可相加。
- **I3 真追加**：既有字节永不重写；唯一重写 = 显式前缀 compaction（逐字复制、整组裁）；不可解析行 ⇒ fail-closed。
- **I4 事实性**：同 `report_seq` 的第二行（含 payload 相同）⇒ conflict ⇒ fail-closed。
- **I5 窗口内才断言**：窗口外 / 段不连续 ⇒ 不做正面断言。
- **I6 未启用即透明**：既有面逐字节一致 + 零新增文件 + 新路由 503。

---

## 6. 已知代价（诚实声明）

1. **验证者的独立性 = 本系统自己**。验证与发布同进程、同密钥、同主机；攻击者控制主机即可删除验证账本、或伪造一份「全部 attested」的报告。P42 **不检测**这种删除（与 P39 ledger「不提供自身历史完整性证明」同构，与 P37「签名不是 authorship 证明」同源）。**P42 把「从未验证」变成可断言，但不能把「验证者独立」变成可断言。**
2. 只覆盖**启用之后**的验证行为 —— 历史沉默**仍是**沉默。
3. `verified_at` 是自证时间：**P42 不是时间戳证明**。
4. 账本有界 ⇒ 下界丢失（R40-1 同构），已由窗口纪律处理。
5. 一次完整验证要遍历全部 retained manifest 并重算 digest，定时验证是**新增的周期性 I/O** ⇒ 默认关闭。
6. **`unattested` 大概率是常态**（coverage 常为 `indeterminate`、anchor 常未启用）。这是诚实的代价而非缺陷：**宁可说「我无法断言」，也不说「一切正常」**。

---

## 7. 测试契约（T180~T195，16 例）

| 编号 | 内容 |
|---|---|
| T180 | 报告确定性：同输入两次 ⇒ 除 `verified_at` 外字节相同（canonical 复用 P37 序列化器） |
| T181 | **合流单调性**（表驱动，七维 × 取值组合）：`overall` 从不比最坏已断言维更乐观（判别用例） |
| T182 | 不可断言维 ⇒ `unattested`，**不是** `contradicted`（反「不可证明即证伪」） |
| T183 | `reasons[]` 强制非空；`blocking` 标记正确；缺失理由链 ⇒ 不产出裁定 |
| T184 | 账本真追加（既有字节零变）+ 同 seq 第二行 ⇒ conflict 且文件字节不变（红例） |
| T185 | 不可解析行 ⇒ 拒绝追加（I2 推广） |
| T186 | compaction 后窗口收缩；窗口外不判 gap；段不连续 ⇒ `window_discontinuous` |
| T187 | `verification_absent`：零报告 ⇒ 可断言从未验证（**non-vacuousness 红例**：删账本 ⇒ absent，不是「正常」） |
| T188 | `verification_gap`：发布 3 份只验证 1 份 ⇒ gap 区间正确 |
| T189 | `verification_divergent`：同一 publication 先 `attested` 后 `contradicted` ⇒ 可断言变化（**核心红例**：删档重签在验证维的投影） |
| T190 | 首行豁免（合法 compaction 后首行 prev 指向被裁条目不算篡改） |
| T191 | 未启用 ⇒ 既有 4 个只读面响应与基线**逐字节一致**（零回归，同源比对） |
| T192 | 未启用 ⇒ 不产生 `verification-log.jsonl` |
| T193 | 冻结面零 diff（`snapshot_*.go` / `appendonly_log.go` / `history_export_coverage.go` / 四个包 / `go.mod` / `go.sum`） |
| T194 | 定时验证默认关闭（无 flag ⇒ 无写入、无 goroutine 副作用） |
| T195 | 变异 M1~M4：摘掉 gap 判据 / 摘掉 divergent 判据 / 允许同 seq 第二行 / 允许窗口外断言 ⇒ 对应用例**必红**，按 sha256 还原 |

---

## 8. 三处取舍（待 judge 裁决）

### Q1 — 验证报告是否同轮锚定到 P40（我倾向 **否**）

理由：①**发布不可重放，验证可重放** —— 锚定一个可重算的派生事实，收益远低于发布事实；②混流会破坏 ADR-053「单一 `anchor_seq` 序列」的连续性不变量（与 P41 I5 同构，R212 已批准该论证）；③anchor 见证的是「**发生了什么**」，验证报告是「**我认为发生了什么**」，让 witness 序列混入可重算内容会稀释其核心语义。

### Q2 — `overall` 是否并入 `/export/verify` 响应（我倾向 **独立路由 + 报告内字段**）

理由：既有 `/export/verify` 响应字节属于**冻结面**（P35 A6 结果模型 + P37/P38/P39 三个正交字段已定）。新增字段会破坏 T191 的逐字节零回归证明。⇒ 新增 `GET|POST /management/v1/protection/alerts/history/export/verification`。

### Q3 — 定时验证是否默认启用（我倾向 **默认关闭**）

理由：与 P34「scheduler 未启用 ⇒ 503 不回退」的既有纪律一致；定时验证是新增周期性 I/O（§6-5）。flag `--export-verify-interval`（默认 0 = 禁用）。若 judge 认为应默认跟随 `--export-interval` 一起跑，请明示。

---

## 9. R213 judge 提示闭合表

| 提示 | 处理 |
|---|---|
| **提示 1**：下一个真实缺口可能在呈现/运维面（把 P35~P41 证据栈变成运营者可消费的单一视图） | **部分采纳** —— 单一裁定 `overall` + 强制理由链 `reasons[]` 作为 P42 副产物交付（§4-A2 / §4-A8）；**但不独立成 Phase**，因为纯合流不产生新可断言判据（会被 R210 击倒 HA 的同一把尺子击倒）。它依附于「验证要落账」这一真实缺口而获得合法性。 |
| **提示 2**：HA 仍冻结，解冻需新主线接口论证 | **确认冻结**，本轮不开放（§1.1）。 |
