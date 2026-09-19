# ADR-062 — Phase 43 Implementation: Evidence Destruction Accountability

- **Status**: Implemented (Round 218 提交)
- **Phase**: 43（Evidence Destruction Accountability / 销毁留痕）
- **Base**: `b1e5668`（ADR-061 Architecture）
- **配套**: Scope `060-phase43-scope.md`／Architecture `061-phase43-architecture.md`（R217 = A，Q4/Q5/Q6 全部支持）
- **分工句**: P37 决定 **who**，P40 决定 **where**，P41 决定 **when**，P42 决定 **whether**，**P43 决定「消失本身是否在案」**。

> 本 ADR 只记录实现决策。语义边界（什么算留痕、什么算越权、什么算断裂）仍在 ADR-061，不可在此变更。

---

## 1. 交付面

| 文件 | 类型 | 内容 |
|---|---|---|
| `internal/controlplane/server/snapshot_destruction.go` | **新增** | 条目模型 / 载荷-状态分离 / 组级链 / KAK 签发与验证 / 追加与推进 / 自裁剪 / 观察者 / 视图 / HTTP 面 |
| `internal/controlplane/server/snapshot_destruction_test.go` | **新增** | T200~T221（18 例） |
| `internal/controlplane/server/appendonly_log.go` | **加** | `compactionObserver` 类型 + `compactLogPrefixGroupsObserved` + `planLogPrefixDrop`；原 `compactLogPrefixGroups` 保留为薄包装，**语义零变更** |
| `internal/controlplane/server/snapshot_anchor.go` | **仅加法** | `destructionAnchorFile` / `anchorKindDestruction` / `anchorEntry`+`anchorSigned` 三个 `omitempty` 字段 / `identityID()` 新族 / `appendAnchorEntryPathObserved` / `compactAnchorPrefixPathObserved` / `destructionAnchorPath` / `anchorDestructionEntry` |
| `internal/controlplane/server/snapshot_verification.go` | 改 | `verificationConfig` 加 `observe` 字段；`compactVerificationPrefix` 走 observed 变体 |
| `internal/controlplane/server/snapshot_key_lifecycle.go` | 改 | `keyLifecycleConfig` 加 `observe` 字段；`compactKeyLifecyclePrefix` 走 observed 变体 |
| `internal/controlplane/server/snapshot_ledger.go` | **加** | `appendLedgerEntryObserved`（只新增函数，既有路径零改动） |
| `internal/controlplane/server/history_export_scheduler.go` | 改 | `DestructionLog`/`DestructionCapacity` 配置 + `Destruction` 状态汇总 + `destructionError` + G1/G2 构造守卫 + `destructionObserver()` + prune 挂钩 |
| `internal/controlplane/server/server.go` | 改 | 1 条路由 `GET /management/v1/protection/alerts/history/export/destruction`（紧邻既有 verification 路由） |
| `cmd/opscore/main.go` | 改 | 2 个 flag：`--export-destruction-log`(false) / `--export-destruction-capacity`(4096) |
| `go.mod` / `go.sum` | **零改动** | 无新依赖 |
| 冻结包 platform / governance / plugin/{runtime,isolation} / controlplane/hostregistry | **零改动** | 由 mktree 继承父 blob 保证（见 §7） |

默认 **关闭**（同 P34/P42 纪律）；锚定**零新增 flag**（复用 `--export-anchor-*`）。

---

## 2. R43-1 / R43-2 落地

| 条款 | 落地 |
|---|---|
| R43-1（KAK 而非 DAK，也**不是**导出签名密钥） | `signDestructionEntry` 只在 `destructionConfig.ka.signer` 上签；导出签名密钥签出的留痕记录 ⇒ `destruction_unauthorized`（T217）。P41「KAK 签授权事件类」扩展为「生命周期 ∪ 销毁」，`keyAuthority` 零改动复用 |
| G1/G2（构造期 fail-fast） | 无 KAK 私钥 / 无 KAK trust ⇒ 拒绝可写；KAK == 导出签名密钥 ⇒ 构造失败（G3 继承 `scheduler` 既有守卫） |
| R43-2（载荷/状态分离 + I9） | `entry_digest = sha256(canonical payload)`（**内容承诺**，组内两行同值）；`line_digest = sha256(prev_digest ":" entry_digest ":" state)`（**链值**，含 state，从不落盘） |
| I7 | `state` **永不**进 `destructionSigned`，因此永不进签名域；改状态不需要重签 |
| I8 | 推进（intended→completed）复用 intended 行的 `entry_digest` 与 `signature` **字节**，KAK 无需在线 |

---

## 3. 关键实现决策：链是**组级**的（本轮唯一咬人的地方）

Architecture 只说「一行 = 一个销毁组的两行之一」，没说链指针挂在哪。第一版把 `prev_digest` 当链值、每行独立计算，立刻崩两处：

1. **I1' 自伤**：`prev_digest` 属载荷 ⇒ 第二行（completed）的 `prev_digest` 指向第一行 ⇒ 载荷与第一行**不同** ⇒ 被 `destructionPayloadEqual` 判为 conflict，合法推进被当成篡改。
2. **链自指**：第二行的链指针指向同组第一行，而「下一组链谁」变成歧义。

**最终形态（本轮冻结）**：

- 分组键 = `destruction_seq`（`destructionGroupOf` 注入共享原语）。
- **入边指针属于组**：`appendDestructionEntry` 扫描时按 `groupPrev` / `groupExempt` 逐组记录入边；同组两行共享同一个 `prev_digest`（⇒ 载荷相同 ⇒ 签名可复用，I1'/I8 成立）。
- **出边取组内最后一行**：`prevLine` 逐行更新为当前行的 `line_digest`；下一组的 `prev_digest` = 上一组**最后一行的** `line_digest`。
- **I3 豁免下沉为组级**：首个幸存**组**的入边指针豁免（不是「首行」）—— 合法 prefix 裁剪后这才成立（T203）。
- 组内两行 `line_digest` 因 `state` 不同而不同 ⇒ 删掉 completed 行、或重复追加，都能被链检出（I9 的价值正在于此）。

**为什么这没有削弱判据**：内容承诺保证「说了什么」，链值保证「说的顺序与次数」。二者分工与 P39 ledger（`prev_publication_id` 同构）一致，未引入新信任源。

---

## 4. 接线：一次挂钩，五条路径

`compactLogPrefixGroupsObserved` 是 `appendonly_log.go` 里**唯一的**挂钩点；ledger / anchor / key-lifecycle / verification 四条 compaction 路径各自把自己的 `observe` 传进去即可（T207~T210）。retention 的 `prune()` 不共用该原语，单独挂钩（T206/T211）。

**I5（自裁剪必须记账，且不许递归）**：`compactDestructionSelf` 走**专用路径**，且只在**一组闭合后**（`e.State != intended`）才触发 —— 在 intent 行之后立刻自裁剪会让「自裁剪的 intent」再触发一次自裁剪。终止性因此是**结构性**的，不是「实测很浅」。

**I4 检查顺序**：视图里「窗口不连续」的检查**必须先于**「可验证」检查。否则一个既不连续又不可验证的账本会被报成 `destruction_unauthorized`，掩盖真正该说的 `destruction_window_discontinuous`（本轮实测踩到，已修，T212）。

---

## 5. 测试契约 T200~T221

| 用例 | 钉什么 |
|---|---|
| T200 | 关闭 ⇒ 零回归：无文件、无记录、无断言 |
| T201 / T218 | 合法推进：载荷同、签名字节同、`line_digest` 必不同；下一组链到上一组最后一行 |
| T219 | 同 seq **不同载荷** ⇒ conflict（I1' 判别） |
| T220 | 终态是终态：第三行被拒（即便载荷合法） |
| T202 | I2：账本不可评估 ⇒ 拒绝追加（fail-closed，绝不静默放行） |
| T203 | I3：合法 prefix 裁剪后，首个幸存**组**入边豁免 |
| T204 | I4：幸存 seq 有洞 ⇒ 禁断言 |
| T212 | I4 在视图层：不连续窗口优先报 `destruction_window_discontinuous` |
| T205 | I5：账本为自己**自己的**裁剪记账，递归终止 |
| T207~T210 | 四条 compaction 路径全部被观察、全部记账 |
| T206 / T211 | retention 记账；合法 prune ⇒ `accounted`（**不**误报 unaccounted） |
| T213 ★ | **核心红例**：证据被删且**无留痕** ⇒ `unaccounted_disappearance`，受害者被点名 |
| T215 | 往链中插入记录 ⇒ `destruction_chain_broken` |
| T216 | 删掉账本 ⇒ `destruction_absent`，**绝不**美化成 `accounted` |
| T217 ★ | R43-1 判别：导出签名密钥伪造的留痕 ⇒ `destruction_unauthorized` |
| T221 | 第四锚定族不得改动前三族**一个字节**（P40/P41/P42 条目逐字节比对） |
| — | 状态汇总 + G1/G2 构造守卫（无编号） |

**T214 为空号**：该处无独立可改代码路径（不伪造恒过用例凑号，接龙铁律）。

**T213 的非空转性**：逐一核对 P35~P42 各判据在此场景下仍为「不断言」，证明 `unaccounted_disappearance` 是新判据而非复述。

---

## 6. 变异测试（现取，非书面声明）

`M1`~`M5b`/`M6`/`M7a`+`M7b`（后两组需成对施加）：分别摘掉「观察者挂钩」「I1' 载荷比对」「I8 签名复用」「I2 拒绝追加」「I4 窗口」「I5 自裁剪」「R43-1 密钥族判据」。**每一次都让它对应用例转红**，随后按 sha256 字节还原。

---

## 7. 冻结自证

- 提交用 **mktree**（Windows 索引不可用）：除本轮改动文件外，全部条目**继承父提交 blob** ⇒ 冻结包与 `go.mod`/`go.sum` 零改动是**构造性**的，不依赖 diff 观察。
- `git ls-tree -r -z` 全量比对父/新树，变更路径集合 = §1 表格中标记为「新增/改/加」的文件。

---

## 8. 已知弱点（如实）

1. **留痕的可信根 = KAK**。持 KAK 者可签出「合法」留痕；这与 P41 genesis 的处境同构，P43 不解决、不掩盖。
2. **未挂钩的路径不留痕**：只有 §4 五条路径被观察。未来新增的删除路径若忘记传 `observe`，该删除将**静默无记**——这正是 T207~T210 存在的理由，但判别能力止于「已挂钩的路径」。
3. **T213 依赖 `knownPublications` 的完备性**：若一次删除同时抹掉了「它曾经存在」的所有本地痕迹（publication 记录 + 账本 + 锚定），本地退化为 `destruction_absent`；与 P41 T177 / P42 的域外可检测性处境一致，本轮**不**做域外对账。
4. **时钟**：`recorded_at` 自证。P43 不是时间权威（同 P41/P42）。
5. **容量下界**：`--export-destruction-capacity` 有界 ⇒ 旧留痕会被裁掉 ⇒ 对窗口之外的销毁**不得断言**（I4），与 P39/P40 的窗口纪律同构。
