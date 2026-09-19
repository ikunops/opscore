# ADR-067 — Phase 46: Witness Reconciliation（对账面全家族化 / 域外回照）

- **Status**: Proposed (Phase 46, Scope stage) · drafted by judge-as-executor（代行模式第三轮；独立评审换人负责）
- **Phase**: 46（Witness Reconciliation）
- **Base**: Phase 45 CLOSED（origin/main = `53df740`）
- **Supersedes**: 无。不修改 P35~P45 的任何冻结判据；P40 既有 publication 家族对账语义与响应字节零改动。

---

## 1. 分工句（第七问）

P37 who / P40 where / P41 when / P42 whether / P43 why-absent / P45 what-accepted ⇒
**P46 = whether witnessed（域外回照）**：P40~P45 五个 Phase 的已知代价里五次重复同一句——「账本被删 ⇒ 本地与『从未启用』不可区分，**域外可检测**」。P46 把「域外可检测」升级为**域内可断言**：witness 手里握着全部家族的完整签名条目，系统却只对 publication 家族建了对账面。

## 2. 新问题（已核对代码）

**事实**：

1. 五个家族全部向 witness 推送**完整签名条目**（P40 §5.1 请求体即整个 anchorEntry；dev witness 逐字追加）——`dispatchAnchorPath`（snapshot_anchor.go:948，publication 面）、`anchorDestructionEntry`（:1043）、`anchorAcceptanceEntry`（:1087）、`anchorKeyLifecycleEvent`（:1121）、`anchorVerificationReport`（snapshot_verification.go:1342）。
2. 对账面只有 publication 家族：`ReconcileAnchor`（:1281）消费 `witnessItem{publication_id, anchor_seq, anchor_digest, key_id, stream_id, sig}`——三元组比对，Sig「存而不验」（注释明言签名在本地侧独立验证）。
3. `anchorKind` 五族常量俱在（:66-78：key_lifecycle / verification / destruction / acceptance + publication），anchorEntry 含 KeyID/StreamID（P40 R40-2）。
4. P39（ledger_error）/P41（key_lifecycle_error）/P42（T177 腐账红例）/P43（destruction_absent）/P45（input_absent）各自声明「删除与从未启用本地不可区分，域外可检测」——**五处同构缺口，无一实现域内断言**。

**悖论（精确表述）**：攻击者删除本地账本 F 及其锚定日志 ⇒ 本地全部检查沉默（家族语义「absent ≠ 从未启用本地不可区分」）；而 witness 持有 F 家族的完整签名条目。**「这份部署曾经锚定过 F 家族」这一事实在 witness 侧完整存在，本地却没有任何面能引用它下断言。** 五个 Phase 各自把删除检测推向域外，然后停在那里。

### 为什么独立成 Phase

并入 P40？P40 的对账语义（A6~A8）与响应字节已冻结，且家族对账需要 P43 的销毁记账作「宽免层」、P45 的 stream 身份作归属——P40 时代两者都不存在。并入 P43？P43 是销毁的记账面，不是销毁的**利用面**。它是五处已知代价的**汇合点**，独立成 Phase 是它们各自声明的后续（「域外可检测」的兑现）。

## 3. 决策（Scope）

新增**多家族 witness 对账面**：调用方提交 witness 侧持有的**完整签名条目**（按家族分组），本地逐条验签（P37 信任锚）、验身份（key_id/stream_id）、验链，然后对照本地各家族账本/锚定日志下断言。

**核心产出（P35~P45 给不出）**：

| 新判据 | 条件 | 语义 |
|---|---|---|
| **`family_ledger_deleted`** | witness 持有家族 F 的签名有效条目 ∧ 本地 F 账本缺失/空 ∧ 本地 F 锚定日志缺失/空 | **账本被删**——「从未启用」部署的 witness 不会有其 stream_id 的条目（伪造需要导出私钥，P40 §2.2 同层级已知代价） |
| **`family_ledger_truncated`** | witness 条目 seq 连续延伸**超出本地窗口上界** ∧ 本地销毁记账（或其 witness 副本）**未覆盖**该区间 | **账本被裁而未记账**——P43 销毁记账成为合法 compaction 的**宽免层** |
| `family_divergent` | 同 seq 条目本地/witness digest 不等 | 内容替换（P40 digest_mismatch 家族语义延伸） |
| `family_intact` / `family_incomplete` / `family_unverifiable` | 全对齐 / witness 缺失部分（never broken，witness 裁剪不可区分）/ 验签或身份失败 | |

**宽免层（本 Phase 的组合价值）**：witness 条目 seq 超出本地窗口时，查本地销毁账本中「已完成、kind 匹配该家族 compaction、targets 覆盖该 seq 区间」的销毁记录——覆盖 ⇒ 合法 compaction（窗口收缩，非删除）；销毁账本本地缺失时，用对账体中的 destruction 家族 witness 条目代替（其本身经验签）。P43 的记账第一次成为**被机器消费的宽免证据**。

## 4. 能力定义（A1~A7）

- **A1 对账体格式**：`POST .../export/witness-reconcile`，body = `{families: {ledger: [...], key_lifecycle: [...], destruction: [...], verification: [...], acceptance: [...]}}`，每项 = **完整 anchorEntry JSON（witness 原样回显）**。既有 `POST .../anchor/reconcile`（publication 面）零改动（响应字节冻结）。
- **A2 逐条验证**：签名（export trust，P37 五态）⇒ 身份（key_id/stream_id 与本地派生一致，R40-2 机制）⇒ per-family 锚定链连续（同 P40 锚定 seq 轴）。失败条目 ⇒ 该家族 `family_unverifiable` + 计数，**绝不静默跳过**（家族纪律：坏条目毒化该家族断言）。
- **A3 本地对照**：家族 F 的本地账本 = 各自主文件（chain-ledger / signing-key-log / destruction-log / verification-log / record-acceptance）；本地锚定日志 = 各自 anchor 文件。缺失/空 ⇒ `absent`。
- **A4 三段式窗口（R40-1 纪律实例化）**：witness seq < 本地窗口下界 ⇒ outside（合法裁剪与删除不可区分，**宽免层尝试**：本地或 witness 副本的销毁记录覆盖 ⇒ 合法收缩；不覆盖 ⇒ 仍不判 deleted——下界不可断言，诚实）；∈ 窗口 ⇒ 逐条 digest 对比；**> 上界 ⇒ 无合法上界逐出 ⇒ 未被宽免即 `family_ledger_truncated`**（P45 F2 同款上界论证）。
- **A5 锚定日志缺失的宽免对称**：本地锚定日志缺失但账本在 ⇒ 家族条目对账退化为「仅账本主文件存在性」⇒ `family_incomplete`（不断言 deleted——锚定日志被删与从未锚定不可区分，除非 witness 侧有更多证据；诚实边界）。
- **A6 正交与零回归**：不修改 P40 既有对账路由/响应；不改五家族任何判据取值；新增 `GET .../export/witness-reconcile`（本地状态面）与上述 POST；状态面 omitempty；未启用各家族时其区块输出 `not_enabled`（诚实，非错误）。
- **A7 非目标**：①不做 witness 拉取（验证路径零网络，A6 沿袭）；②不厂商化 witness；③不解决导出私钥持有者伪造对账体（P40 §2.2 同层级，头条已知代价）；④不修改 P40 publication 对账语义；⑤不做跨部署对账；⑥不回填/重建本地账本（对账严格只读 + 零副作用）；⑦不新增常驻组件。
- **A8 已知代价**：①伪造对账体需导出私钥——持有者可伪造 `family_ledger_deleted` 栽赃（响度与 P40 orphan_witness 同层）；②witness 保留策略不可知 ⇒ 缺失恒 incomplete；③签名验证依赖本地信任锚可用；④对账体的完整性/来源由调用方负责（§7.3 沿袭）；⑤宽免层只认「已完成 + kind 匹配 + 区间覆盖」的销毁记录——记账写晚则宽免缺席 ⇒ truncated 误报（响亮方向的误报，运营要求先记账后裁剪的时序纪律，与 P43 顺序铁律一致）。

## 5. 测试契约（T258~T272）

| # | 断言 |
|---|---|
| T258 | 默认零回归：P40 既有对账路由/响应逐字节不变；新 GET 面未启用家族输出 not_enabled |
| T259 | witness 全家族条目验签+身份+链通过 ∧ 本地全在 ⇒ 各家族 family_intact |
| T260 | **核心红例（non-vacuousness）**：删除 acceptance 账本+其锚定日志 ⇒ P35~P45 全部沉默（各自 absent 语义）⇒ 仅 P46 断言 `family_ledger_deleted` |
| T261 | 删除 lifecycle 账本 ⇒ family_ledger_deleted（第二家族，证明非 acceptance 专属） |
| T262 | witness seq 超出本地窗口 + 销毁记录覆盖 ⇒ 合法收缩，不判 truncated |
| T263 | witness seq 超出本地窗口 + **无**销毁记录 ⇒ `family_ledger_truncated`（核心产出之二） |
| T264 | digest 不等 ⇒ family_divergent；与 truncated 可判别 |
| T265 | witness 缺失部分本地条目 ⇒ family_incomplete，绝不 broken |
| T266 | 坏签名/异身份条目 ⇒ family_unverifiable + 计数，绝不静默跳过 |
| T267 | 锚定日志缺失但账本在 ⇒ family_incomplete（A5 诚实边界） |
| T268 | 跨家族隔离：acceptance 家族损坏不影响 lifecycle 家族断言 |
| T269 | 对账零副作用（不落盘/不写 audit/不发网络——P42 §6-13 沿袭） |
| T270 | P40 publication 面不受影响（既有测试全绿 + 响应字节） |
| T271 | 冻结面零 diff + go.mod/go.sum 零改动 |
| T272 | 变异 M1~M4（摘 deleted 判定 / 摘宽免层 / 摘上界 truncated / 放行验签失败条目 ⇒ 对应用例必红，sha256 还原） |

## 6. 已知代价见 A8；提请评审确认的取舍（已裁决，可反驳）

- **Q1** 新对账面为独立路由（POST witness-reconcile），不动 P40 冻结面 —— 采纳（P42 Q2 先例）。
- **Q2** 对账体携带完整签名条目而非摘要三元组 —— 采纳（摘要无法本地验签；witness 本就持有全文，回显零成本）。
- **Q3** 宽免层消费销毁记账（含其 witness 副本）—— 采纳（P43 记账的第一次机器消费；组合即价值）。
- **Q4** `family_ledger_deleted` 的伪造面（导出私钥持有者可栽赃）—— 接受为 P40 §2.2 同层级代价，响亮度优先。
