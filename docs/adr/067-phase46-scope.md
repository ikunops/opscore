# ADR-067 — Phase 46: Witness Reconciliation（对账面全家族化 / 域外回照）

- **Status**: Proposed (Phase 46, Scope stage) · Revised per review（BLOCKER-1/2、MAJOR-1/2/3 全部闭合，见 §9）
- **Phase**: 46（Witness Reconciliation）
- **Base**: Phase 45 CLOSED（origin/main = `53df740`）
- **Supersedes**: 无。不修改 P35~P45 的任何冻结判据；P40 既有 publication 对账语义、witness 协议（anchorRequest 投影）、响应字节零改动；`snapshot_anchor.go` 零 diff。

---

## 1. 分工句（第七问）

P37 who / P40 where / P41 when / P42 whether / P43 why-absent / P45 what-accepted ⇒
**P46 = whether witnessed（域外回照）**：P39/P41/P42/P43/P45 五个 Phase 的已知代价里五次重复同一句——「账本被删 ⇒ 本地与『从未启用』不可区分，**域外可检测**」。P46 把「域外可检测」升级为**域内可断言**。

## 2. 新问题（已核对代码，含评审修正）

**事实**：

1. 五个家族全部向 witness 推送锚定条目——但推送的是 **P40 anchorRequest 投影**（`dispatchAnchorPath`，snapshot_anchor.go:948 → anchorRequest :648-661：`v/key_id/stream_id/publication_id/anchor_seq/anchor_digest/manifest_digest/prev_*/recorded_at/sig`），**不是完整 anchorEntry**（无 Kind、无家族字段）。dev witness 逐字追加投影（:714-724）。
2. 投影中的 `anchor_digest` = sha256(完整签名 payload)——**与本地锚定日志中同 seq 条目的 digest 可比**；`anchor_seq` 即该家族锚定流自身的 seq（轴一致）；`key_id/stream_id` 可与本地派生值**精确比对**（R40-2 机制）。
3. 投影中的 `sig` 覆盖完整 payload 但 payload 不在投影内 ⇒ **投影不可验签**（评审 BLOCKER-1）。因此对账不验签名——P40 的验签职责从来在**本地 load**（snapshot_anchor.go:511+），dev witness 逐字保管不验签；deleted 场景本地已无文件，投影验签既不可能也从来不是 witness 的义务。本地对账信任「witness 逐字回显 + 调用方传输」，调用方责任沿袭 P40 §7.3
4. 对账面只有 publication 家族（ReconcileAnchor :1281）；P39/P41/P42/P43/P45 五处「账本被删本地不可区分」无一实现域内断言。
5. 五家族的启用开关是静态配置门；**禁用不删文件**（P45 T237：未启用不建文件；全包无删除主账本的流程）——这是 deleted 判据健全性的承重前提（评审 MAJOR-3，本 ADR 首次显式声明并冻结）。

**悖论**：攻击者删除本地家族账本 F 与其锚定日志 ⇒ 本地全部检查沉默；而 witness 持有 F 家族的锚定投影（stream_id 与本部署精确匹配——本地可派生验证）。**「本部署曾锚定过 F」在 witness 侧存在，本地无面能引用。**

## 3. 决策（Scope）

新增**多家族 witness 对账面**（独立路由，P42 Q2 先例）：调用方按家族提交 witness 回显的**投影条目**（就是 witness 持有的字节），本地做三件事：身份比对（可派生验证）、窗口三段式（对家族锚定日志）、digest 对比（对本地全量条目）。

**核心产出**：

| 新判据 | 条件 | 语义 |
|---|---|---|
| **`family_ledger_deleted`** | 家族 F 的 witness 投影存在（stream_id/key_id 与本地派生一致）∧ 本地 F **主账本**与**锚定日志**双双缺失/空 | 账本被删——合法禁用保留文件 ⇒ 不误报（§2 事实 5 承重前提，冻结为 I-级不变量）；伪造投影不需要私钥 ⇒ 信任层级 = **P40 §7.3 调用方责任**（非 §2.2，评审修正） |
| **`family_ledger_truncated`** | witness 投影 anchor_seq **> 本地锚定日志窗口上界** | 锚定日志尾部被裁——**无宽免层**（评审 BLOCKER-2：上界宽免机制性不可达——R40-6 保证 pending 先于 dispatch ⇒ 本地 max ≥ witness max 恒成立；compaction 只裁前缀 ⇒ 诚实状态永不产生覆盖上界外区间的记录；照抄 P45 F2：无合法上界逐出 ⇒ 可断言） |
| `family_divergent` | 窗口内同 seq：投影 anchor_digest ≠ 本地条目 digest | 内容分歧（与本地全量条目比对——本地持有全文，无需投影可验签） |
| `family_incomplete` / `family_intact` / `family_unverifiable` | witness 缺失部分本地条目（never broken）/ 全对齐 / 身份不符（异 stream/key） | |

**对五个已知代价的兑现**：P39（ledger）/P41（lifecycle）/P42（verification）/P43（destruction）/P45（acceptance）的「域外可检测」各获得一个可断言 verdict。publication 家族**的对账 FACE 不变**（P40 语义/响应零改动），但 `ledger` 家族**保留在本 Phase**——它的锚定流就是 chain-anchor.jsonl（同一链流）：P40 在其上表达「对齐性」断言（对齐门/ahead-of-window ⇒ broken），P46 在同一流上表达 P40 **无法表达**的「存在性/上界」断言（identity 门/deleted+truncated）。两面回答不同问题、信任门不同（P40 §2.2 私钥层级 vs P46 §7.3 调用方层级），分歧边界枚举：零共享 id 态下 P40=unverifiable 而 P46 可判 truncated/deleted——**不矛盾**（可断言域不同），两 face 并存各自成立（评审 MINOR-3 诚实闭合）。

## 4. 能力定义（A1~A8）

- **A1 对账体格式**：`POST .../export/witness-reconcile`，body = `{families: {ledger: [...], key_lifecycle: [...], destruction: [...], verification: [...], acceptance: [...]}}`，每项 = **witness 回显的 anchorRequest 投影 JSON**（witness 持有的原样字节）。既有 P40 路由零改动。
- **A2 身份与分组**：投影按调用方分组归家族；每条本地比对 `key_id`/`stream_id` 与派生值——不符 ⇒ 该家族 `family_unverifiable`（**毒化语义**：任一条目身份不符 ⇒ 整家族不可断言，P40 既有「单条坏 ⇒ 整批 unverifiable」同款，评审 MAJOR-1 修正——原稿的「跳过计数」废除）。**分组本身是调用方声明**（§7.3 责任），但 deleted 判定额外要求 stream_id 精确匹配 ⇒ 异部署投影不能栽赃（其 stream_id 对不上）。
- **A3 本地对照**：家族 F 主账本 = chain-ledger / signing-key-log / destruction-log / verification-log / record-acceptance；锚定日志 = 各自 anchor 文件。**「禁用不删文件」为承重前提**（冻结：任何家族未来若在禁用时清理主账本文件，必须先废除本 Phase 的 deleted 判据——写入非目标）。
- **A4 三段式窗口（对家族锚定日志，anchor_seq 轴）**：投影 seq < 本地窗口下界 ⇒ outside（合法裁剪与删除不可区分，**不断言、无宽免层**——评审 BLOCKER-2：原宽免设计三重不可行，废除）；∈ 窗口 ⇒ digest 对比本地条目；**> 上界 ⇒ `family_ledger_truncated`**。
- **A5 缺席矩阵（评审 MAJOR-2 修正）**：主账本缺席 ∧ 锚定缺席 ⇒ deleted；主账本缺席 ∧ 锚定在 ⇒ incomplete；主账本在 ∧ 锚定缺席 ⇒ incomplete（A5 原则：锚定日志被删与从未锚定不可区分）；都在 ⇒ 窗口对比。
- **A6 正交与零回归**：P40 路由/响应零改动；五家族判据取值零变化；`snapshot_anchor.go` 零 diff（投影解析是新面自己的事）；GET 本地面输出各家族存在性/窗口 + not_enabled；POST 无启用门但 deleted 判定内建「文件在 ⇒ 非 deleted」⇒ 两面词汇统一（评审 MAJOR-3 修正：同一家族两面结论不可能分歧——deleted 需要双双缺席，而「文件在」时 POST 也判不出 deleted）。
- **A7 非目标**：①不做 witness 拉取；②不厂商化；③**不对投影做签名验证**（不可实现——评审 BLOCKER-1；P40 验签职责在本地 load，deleted 场景本地已无文件；对账信任回显 + §7.3 调用方责任）；④不动 P40 publication 对账；⑤不做跨部署对账；⑥对账零副作用；⑦**禁止各家族禁用时清理主账本文件**（承重前提冻结）；⑧不新增常驻组件；⑨不做「原地清空重建」的合法性区分（同目录同钥重建 ⇒ stream_id 不变 ⇒ 会判 deleted——机器无法区分制裁性重置与删除，A8 声明）。
- **A8 已知代价**：①**伪造层级 = P40 §7.3 调用方责任**：对账体可被任何能拿到 admin 面的人伪造（投影无需私钥即可构造）——栽赃 deleted/truncated 与掩盖分歧都可行；witness 收货时的验签（P40 A1）+ 调用方传输责任是仅有的防线。②合法「清空重建」（同目录同钥）⇒ 误报 deleted（A7-9）。③witness 保留策略不可知 ⇒ 缺失恒 incomplete。④下界外（seq < 窗口下界）的删除永不可断言（R40-1）。⑤信任锚不可用 ⇒ 身份比对失败 ⇒ 全家族 unverifiable（响亮）。

## 5. 测试契约（T258~T273）

| # | 断言 |
|---|---|
| T258 | P40 既有对账路由/响应逐字节不变 + snapshot_anchor.go 零 diff（冻结） |
| T259 | witness 投影（真 fixture 产出的 dispatch 字节）× 本地全在 ⇒ 各家族 family_intact |
| T260 | **核心红例**：删除 acceptance 主账本+锚定日志 ⇒ P35~P45 全部沉默 ⇒ 仅 P46 断言 family_ledger_deleted |
| T261 | 删除 lifecycle 账本+锚定日志 ⇒ family_ledger_deleted（第二家族） |
| T262 | witness seq > 本地锚定窗口上界 ⇒ family_ledger_truncated（无宽免层——上界机械不可达性由本用例的构造方式自证：直接裁剪锚定日志尾部模拟） |
| T263 | 窗口内 digest 不等（改本地锚定日志同 seq 条目 digest）⇒ family_divergent，与 truncated 可判别 |
| T264 | witness 缺失部分本地条目 ⇒ family_incomplete，绝不 broken |
| T265 | 异 stream_id 投影 ⇒ family_unverifiable + 毒化（同家族零断言） |
| T266 | 主账本在 ∧ 锚定日志缺席 ⇒ family_incomplete（A5，绝不 truncated 假警报——评审 MAJOR-2 判别） |
| T267 | 跨家族隔离：acceptance 投影损坏不影响 lifecycle 断言 |
| T268 | 对账零副作用（不落盘/不写 audit/不发网络） |
| T269 | 「禁用不删文件」承重前提钉死：acceptance 启用→追加→flag 关闭→文件仍在 ⇒ 视图非 deleted（合法禁用不误报） |
| T270 | 下界外投影 seq ⇒ outside，不断言（R40-1） |
| T271 | 冻结面零 diff + go.mod/go.sum 零改动 + snapshot_anchor.go 零 diff |
| T272 | 变异 M1~M4（摘 deleted 判定 / 摘 truncated 上界 / 放行异 stream 投影 / 摘 digest 对比 ⇒ 对应用例必红，sha256 还原） |
| T273 | GET 面与 POST 面词汇统一（同一状态两面结论不分歧——评审 MAJOR-3） |

## 6. 与既有 Phase 的关系

不降级任何判据；P40 面零改动；五家族「域外可检测」的兑现是其各自已知代价条款的升级（后续 ADR 触碰时把「域外可检测」改写为「P46 已域内断言」）。

## 7. §9 Scope 评审闭合表（本轮）

| 发现 | 闭合 |
|---|---|
| BLOCKER-1 投影不可验签 | A2/A7-3 重造：不验签；验签归 witness 收货职责；对账 = 身份派生比对 + digest 对比 + 窗口；zero-diff 桶恢复（snapshot_anchor.go 零改动） |
| BLOCKER-2 宽免层三重不可行 | A4 整体废除宽免层；上界 = 无条件 truncated（P45 F2）；下界 = 不断言；witness 副本路径删除 |
| MAJOR-1 坏条目跳过 | A2 毒化语义（整家族 unverifiable） |
| MAJOR-2 缺席矩阵漏支 | A5 四象限完整（T266 判别） |
| MAJOR-3 禁用/被删前提 | §2 事实 5 + A3 承重前提冻结 + A7-7 非目标 + T269 判别；GET/POST 词汇统一（T273）；A8-2 清空重建代价 |
| MINOR-1~3 / NOTE | §6-13 错引删除（T268/I5 无引用，效果达成）；验链承诺撤回（投影无链字段——链验证职责在本地 load，本就不可能由对账体承载）；ledger 家族双 face 分歧显式枚举（非排除式消除，见 §3 修正）；注册表按投影现实重写（ADR-068 §2）；T258/T270 分工明确（T258=字节冻结，T270=下界纪律）；4MiB 体量预算写入 ADR-068 §8 |
