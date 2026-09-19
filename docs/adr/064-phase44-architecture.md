# ADR-064 · Phase 44 — Verifier Independence（验证者身份与信任锚独立）· Architecture

- **Status**: PROPOSED（R220 投递；与 ADR-063 Scope 修订版成对交付）
- **Parent**: ADR-063（P44 Scope, commit `652cacda`）；ADR-062（P43 Implementation, `ff9c4fc7`）
- **裁决前提**: R219 = Scope 评审 approved=false（R44-1 major / R44-2、R44-3 minor / note×6）；R44-1~R44-4 闭合于 Scope 修订版 §12，机制侧闭合于本文
- **Author**: executor
- **Scope 覆盖**: 本文与 ADR-063 冲突处以本文为准

---

## 0. 必修闭合表

| 编号 | judge 要求 | 闭合位置 | 结论 |
|---|---|---|---|
| **R44-1** | 逃逸分析补全：持导出私钥者伪造 `kind=verification` **锚定条目**的残差通道（签名区 `snapshot_anchor.go:109-111/:146-148`；导出钥签发 `snapshot_verification.go:1150-1184`；manifest 锚验签 `:1053`；发往见证端 `snapshot_anchor.go:919-936`）须在 §7 保留并给 §8 已知代价 | Scope §7 行1 重写 + §8-7 新增；本文 §7 逃逸 A-3（完整通道 + 处置论证） | 闭合 |
| **R44-2** | `verificationConfig` 接线行号更正（实为 `snapshot_verification.go:1000-1001` / `:993-1006`） | Scope §2.1 事实1 / §11 更正；本文 §5 接线表按更正后坐标书写 | 闭合 |
| **R44-3** | 「离线 KAK」前提与代码不符（KAK 构造期入进程 `history_export_scheduler.go:337`；验证 compaction 周期性产生 KAK 签名 `snapshot_verification.go:1004`） | Scope §3 末段重写；本文全文不依赖离线假设（VAK 为进程内观测密钥，同 verifyTick 运行模型） | 闭合 |
| **R44-4** | T226「落盘」措辞更正（落盘的是条目 `key_id` `:299`）+ POST 响应面字节契约钉死 | Scope §10 T226 更正 + T236 新增；本文 §7（POST 响应零改动） | 闭合 |

judge 确认项（note：V2/V3 密码学健全性——key_id 恒为公钥派生不可配置 `snapshot_signature.go:88-93/:111-113` ⇒ key_id 级比对等价公钥级；冻结面三重核实通过；T222~T235 无碰撞且既有最大 T221 属实；检查范围声明）**原样接受**，本文不再复述，T 编号沿用至 T236。

---

## 1. 数据结构定稿

**条目与报告 schema 零改动**——P44 改变的是签署身份的来源与验签的信任锚，`verificationLogEntry`（`snapshot_verification.go:289-303`）/ `verificationSignedEntry`（`:307-319`）/ canonical 形状一字不动；`key_id`（`:299`）的**值**在独立模式下为 VAK key id。

| 结构 | 改动 | 说明 |
|---|---|---|
| `verificationConfig` | **加 3 字段** | `verifierSigner *exportSigner` / `verifierTrust *exportTrustStore` / `foreignKeys map[string]bool`（manifest trust ∪ KAK trust 的 key_id 集，仅判「已知异族」用，不存公钥）。方法 `independent() bool` = `verifierSigner != nil && verifierTrust != nil && len(verifierTrust.keys) > 0`。不设新 `on` 开关：**VAK 配置即独立模式**，与 `--export-verify-attest` 的一致性由 V4 守卫保证 |
| `verificationState` | **加 1 字段** | `problems []verificationProblem`（仅独立模式填充；关闭模式恒 nil ⇒ 行为与 P42 逐字节一致） |
| `verificationProblem`（新） | — | `{ReportSeq int64, Verdict string, Detail string}`——`destructionProblem`（`snapshot_destruction.go:976`）同构；`Verdict ∈ {verification_unauthorized}` ∪ 既有 sigVerdict 串 |
| `VerificationView` | **加 3 字段（全 omitempty）** | `verifier_key_id` / `verifier_independent` / `problems`（`:1219-1237` 邻位追加） |
| `verificationStatusSummary` | **加 2 字段（全 omitempty）** | `verifier_key_id` / `verifier_independent`（`:1231-1237` 邻位追加） |
| `HistoryExportScheduler` | **加 2 私有字段** | `verifierSigner` / `verifierTrust`，构造期加载（见 §5）；无新 error 字段（复用 `verificationError` 通道 `:1008-1012`） |
| VAK 实例 | 复用 `exportSigner` | `newExportSigner(cfg.VerifierKeyPath, "")`——空路径返回 `(nil, nil)`（`snapshot_signature.go:100-102`），与 KAK 装载（`history_export_scheduler.go:337`）同一手法；**零新建密码学** |

**禁第二套 canonical 成立**：签名载荷仍是 `canonicalVerificationEntryPayload`（`:337-342`），`verifier_independent` 等**不进签名载荷**——它们是构造期守卫（V1~V4）的投影视图，与 P43 `state` 不进 payload（I7）同血统：**可变派生状态永不进签名域**。

---

## 2. 签名与验签定稿

- **签名**：`signVerificationEntry`（`:641-661`）保持 `*exportSigner` 方法不动；独立模式下调用者从 `c.signer` 换为 `c.verifierSigner`（`appendVerificationReport` `:958` 一处换源；报告级 `KeyID` `:950` 同步取 `verifierSigner.keyID`）。
- **验签**：`verifyVerificationEntrySignature`（`:663-690`）**薄包装化**（P41 `loadAnchorStatePath`/`dispatchAnchorPath` 同手法，ADR-061 §6.1）：

```
verifyVerificationEntrySignature(e, trust)                    // 原签名保留，= In(e, trust, nil)
verifyVerificationEntrySignatureIn(e, trust, foreign)         // 新实体：
    ... key 不在 trust：
        if foreign[e.KeyID] ⇒ verdict = "verification_unauthorized"   // 已知异族（I3）
        else                ⇒ verdict = sigVerdictKeyUnknown          // 无锚钥（原样）
    ... 其余分支（absent/malformed/invalid）逐字保留
```

  `foreign == nil` 时行为与 P42 **逐字节一致**（关闭模式零回归的构造性保证）。load（`:589`）与 append 自验（`:927`）按 `c.independent()` 二选一传 `verifierTrust`+`foreignKeys` 或 `trust`+nil。
- **I3 分轨的语义**：`verification_unauthorized` = 「签署者是这个部署认识的密钥（在证据信任锚或 KAK 信任锚里），但不被授权做验证」——比 `key_unknown` 强：它声明的是**家族越权**，不是身份未知。

---

## 3. 判据与状态机

**无新状态机**：验证条目仍是 FACT（ADR-057 §3）——同 `report_seq` 第二行即 conflict（`:577-586` 原样保留），P44 不引入任何 state 推进或组语义。P44 的全部判据变化是**验签信任锚的家族归属**：

| 场景（独立模式） | 判定 | 通道 |
|---|---|---|
| 条目由 VAK 签、全链可验 | 既有 P42 语义原样（窗口/gap/divergent/overall） | 既有字段 |
| 条目由**已知异族**钥签（如导出签名钥伪造） | `verification_unauthorized` ⇒ 整账本 `verifiable=false` ⇒ **无「已验证」断言** + 拒绝追加 | `problems[]` + 既有 `Error` |
| 条目由无锚钥签 | 既有 `key_unknown` ⇒ 同上 fail-closed | 既有 `Error`（分轨，I3） |
| 关闭模式 | P42 行为逐字节一致 | T222b |

**检查顺序（P43 I4 教训的对应物）**：窗口连续性在 load 末尾对**全部** seq 整体判定（`:618-635`），与逐条签名失败**并列输出、互不屏蔽**——`problems[]` 只增，绝不顶替既有 `window_discontinuous` / `Error` 通道；`verifier_independent` **绝不参与** `overall` 或任何其它 verdict 的合流（I5）。P43 实测踩过的「不连续被 unauthorized 掩盖」（ADR-062 §4）在此以结构预防：两通道物理分离。

---

## 4. 与既有流的隔离论证（P43 第 4 锚定流自指教训的对应物）

1. **不新增流**：P44 复用 `verification-log.jsonl` / `report_seq` / 既有 observed compaction 接线（`snapshot_verification.go:1004` `observe: s.destructionObserver(destructionKindVerificationCompaction)`）。签署身份更换**不触碰** observer 链：`appendonly_log.go` 零改动，四条 compaction 路径与 prune 挂钩（P43 §4）原样。
2. **无自指实例**：P43 的教训是「自记录流自身的 compaction 不得再被观察」（I5 专用路径，ADR-061 §6.2）。P44 **不新增任何自记录流**：VAK 不签任何关于 VAK 的记录（无生命周期账本，Scope 非目标 9），验证流记录的是对**证据**的观测，其签署者变更不产生新的递归面。
3. **三平面类型隔离**：evidence（`s.signer`+`s.trust`）/ authorization（`keyAuthority`+KAK trust）/ observation（`verifierSigner`+`verifierTrust`）各为独立实例；构造期互斥由 V2/V3 保证；运行期**无交叉传参**——独立模式验签只收 `verifierTrust`/`foreignKeys`，manifest 验签（`verifyManifestSignature`，`snapshot_signature.go:232`）与销毁验签（`snapshot_destruction.go:309-312`）各自的 trust 实例不互换。
4. **锚定出口不变**：`anchorVerificationReport`（`:1150-1184`）照旧由 `s.signer`（导出钥）签发与派发——锚定是传输层存在见证（Scope 非目标 4）；其代价即 §7 逃逸 A-3，如实登记不美化。

---

## 5. 文件与接线清单

| 位置 | 改动 |
|---|---|
| `snapshot_verification.go` | config 3 字段 + `independent()`（`:502-519` 邻位）；`verifyVerificationEntrySignatureIn` + 薄包装（`:663-690`）；load 分轨（`:589` 处按 `independent()` 选 store）；append 换源（`:900-979`）；`verificationConfig()` 装配（`:993-1006`，**R44-2 更正后的坐标**）；视图/summary 字段（`:1219-1237`）；POST 响应**零改动**（`:1599-1605`） |
| `history_export_scheduler.go` | 构造期加载 `verifierSigner`/`verifierTrust`（KAK 块 `:337-355` 同手法）+ 守卫 V1~V4（紧跟 `:367-401` 既有守卫块之后追加） |
| `snapshot_verification_test.go` | T222~T236 |
| `cmd/opscore/main.go` | 2 flag：`--export-verifier-key`("") / `--export-verifier-trust`("")，紧邻 `:488-490` |
| `server.go` | **零改动**（路由复用 `:1660-1661`；GET 经 `writeJSON` 自动携带新 omitempty 字段；POST 不动） |
| 零改动文件 | `appendonly_log.go`、`snapshot_anchor.go`、`snapshot_destruction.go`、`snapshot_signature.go`、`snapshot_chain.go`、`snapshot_ledger.go`、`history_export_coverage.go`、`history_export_manifest.go`、`snapshot_key_lifecycle.go`；四冻结包（platform / governance / plugin/{runtime,isolation} / controlplane/hostregistry）；`go.mod`/`go.sum` |

**守卫 V1~V4 定稿**（全部构造期 fail-fast，不留「先启用后补钥匙」窗口）：

| | 条件（`vakSigner`/`verifierTrust` 经 `newExportSigner`/`newExportTrustStore` 装载，空即 nil） | 结果 |
|---|---|---|
| **V1** | `vakSigner != nil` 而 `verifierTrust` 为 nil/空；或 `vakSigner.keyID ∉ verifierTrust.keys` | 构造失败（T79b / P41 I1 血统，同 `:345-351` 形状） |
| **V2 互斥可达** | ① `∀id ∈ verifierTrust.keys: id ∈ trust.keys`（与 manifest 锚相交）② `signer != nil && signer.keyID ∈ verifierTrust.keys`（发布者可自证）③ `kakSigner != nil && kakSigner.keyID ∈ verifierTrust.keys` ④ `vakSigner.keyID ∈ trust.keys` ⑤ `kakSigner != nil && vakSigner.keyID ∈ kakTrust.keys` | 任一命中 ⇒ 构造失败。**一把钥匙不得同时出现在两个信任锚**（key_id 派生不可配置 ⇒ id 级比对等价公钥级，judge 已确认） |
| **V3** | `vakSigner.keyID == signer.keyID`（signer 非 nil 时）；或 `kakSigner != nil && vakSigner.keyID == kakSigner.keyID` | 构造失败（G3 血统推广，`:352-354`） |
| **V4** | `vakSigner != nil && !cfg.VerifyAttest` | 构造失败（对称 `:375-377` interval 守卫） |

---

## 6. 只读面与 flag

| 面 | 决定 |
|---|---|
| `--export-verifier-key` / `--export-verifier-trust` | 默认空 ⇒ 独立模式关闭 ⇒ 全系统 P42 逐字节行为（T222/T222b） |
| GET /verification | `VerificationView` 加 3 个 omitempty 字段；关闭模式不出现（T222b）；未启用 attest 仍 503（`:1576` 语义不变） |
| **POST /verification** | **响应手工 map（`:1599-1605`）零改动（T236，R44-4）**——验证者身份只经 GET 视图与条目 `key_id`（`:299`，落盘）暴露 |
| scheduler status | verification summary 加 2 个 omitempty 字段（T233） |
| 锚定面 | 四族 anchor 条目 + verification anchor 条目字节零改动（T231） |

---

## 7. 逃逸分析（R44-1 重写：按通道分层，诚实）

| 编号 | 通道 | 对手 | P44 后 | 处置 |
|---|---|---|---|---|
| A-1 | 伪造验证**报告**（本地账本末端追加/整体重写） | 持导出私钥 | `verification_unauthorized` ⇒ 账本不可信 ⇒ 无断言（T227/T228） | **闭合**——本 Phase 增量 |
| **A-3** | 伪造验证**锚定条目**（`kind=verification`，`Overall=attested` + 任意 `ReportDigest`） | 持写权限 + 导出私钥 | **原样保留，未闭合**：条目字段在签名区（`snapshot_anchor.go:109-111`、`:146-148`），导出钥签发即合法（`anchorVerificationReport` 同款 `:1163-1172`），本地对 anchor log 的验签走 manifest 信任锚（`loadAnchorState(s.cfg.Dir, s.trust)`，`:1053`），`dispatchAnchorPath`（`:919-936`）照常发往见证端；锚定对账 A7-11 冻结（ADR-057 §8-8）⇒ **本地与域外均不可检测** | **不修（Scope §8-7 / 已知代价 7）**：修法一=锚定条目 VAK 会签，但见证端今日不验签（`anchorRequest` 携带 `Sig` 无验证方），且五族锚定共用导出钥传输层，单族改签撕裂传输模型；修法二=实现 A7-11 对账——两条都超本 Phase，**列 Phase 45 候选（与 C2 并案评估）** |
| A-2 | 连账本一起删 | 任意写权限 | `verification_absent`（P42 原样） | 不变 |
| — | 控制进程/主机（VAK 在线同进程） | 主机级攻击者 | 可伪造 VAK 签名 / 更换 trust 配置重启 | 拓扑代价（Scope 已知代价 1），代码断言不了 |

**结论（不美化）**：P44 闭合 A-1、不闭合 A-3——「报告不能伪造了，但『存在过一份报告』的域外痕迹仍可伪造」。这与 P43 逃逸 A-2（持 KAK 者末端追加）同构：每轮收缩一层、登记一层。

---

## 8. 不变量表

| | 内容 | 执行点 |
|---|---|---|
| **I1** | 三信任锚（verifier/manifest/KAK）两两不相交；三私钥两两不同 | V2/V3，构造期（§5） |
| **I2** | 账本不可验（含 unauthorized）⇒ 拒绝追加、无断言 | load `:589-594` / append `:927`，fail-closed 原样 |
| **I3** | 判据分轨：已知异族 ⇒ `verification_unauthorized`；无锚钥 ⇒ `key_unknown` | `verifyVerificationEntrySignatureIn`（§2） |
| **I4** | 尺子不变：同输入 `items[]`/`overall` 与关闭模式一致 | T232 |
| **I5** | `verifier_independent`/`problems` 不参与任何 verdict 合流 | §3 检查顺序；P42 I7 / P43 I6 血统 |
| **I6** | 关闭模式 = P42 逐字节（`foreign=nil` 薄包装退化 + omitempty） | T222b，构造性保证 |
| **I7** | POST 响应零改动；可变派生状态永不进签名载荷 | T236；§1 尾注 |

---

## 9. 实现步骤（每步含门禁；红例一律先证红再修绿，按 sha256 字节还原）

| 步 | 内容 | 门禁 |
|---|---|---|
| 1 | flags（main.go）+ scheduler 装载 + config 3 字段 | `bash _zcode_gate.sh all`；T222b 绿（关闭模式字节等价先钉住） |
| 2 | 守卫 V1~V4 | T223 / T224 / T225 / T234 红→绿 |
| 3 | 薄包装分轨验签 + 独立模式签名换源 | T226 / T230 红→绿；**T227 红→绿且 non-vacuousness 自证**（P35~P43 各判据在该场景仍「不断言」，T213 血统） |
| 4 | append 拒绝（I2） | T228 红→绿 |
| 5 | 视图/summary 字段 + `problems[]` | T226/T233 绿；T236 绿（POST 零改动）；T222b 复跑 |
| 6 | 旧账本行为（fail-closed 不迁移） | T229 红→绿 |
| 7 | 等价与继承回归 | T231 / T232 / T235 绿 |
| 8 | 全量自证 | `_zcode_gate.sh all`；变异 M1~M5 逐条红→sha256 还原；mktree 提交（冻结面包构造性继承），`git diff --stat <parent> HEAD` 仅列 §5 清单路径，`git ls-tree HEAD go.mod go.sum` = dfed3965 / 8d242344 |

---

## 10. 测试映射（T222~T236 ↔ 实现点 ↔ 变异）

| T | 钉什么 | 实现点 | 变异 |
|---|---|---|---|
| T222 / T222b | 关闭零回归 + 字节等价 | `foreign=nil` 退化路径 + omitempty | M5 |
| T223 | V1 | 守卫块 | — |
| T224 ★ | V2 互斥（3 组互相命中共 5 例断言） | 守卫块 | **M1**（摘互斥检查） |
| T225 | V3 | 守卫块 | **M3**（摘同钥检查） |
| T226 | 合法流：条目 `key_id`=VAK（落盘 `:299`）；视图字段派生 | 换源 + 视图 | — |
| T227 ★ | 核心红例：导出钥伪报告 ⇒ unauthorized ⇒ 无断言（+non-vacuousness） | `verifyVerificationEntrySignatureIn` | **M2**（验签回退 manifest trust） |
| T228 | I2 拒绝追加 | append `:927` 路径 | **M4**（unauthorized 降级为跳过） |
| T229 | 旧账本 + 独立模式 ⇒ fail-closed | load 分轨 | M2 |
| T230 | I3 分轨：无锚钥 ⇒ `key_unknown` | `In` 函数 else 分支 | — |
| T231 | 锚定面零改动（四族 + verification 条目） | `anchorVerificationReport` 不动 | — |
| T232 | I4 尺子不变 | 判定逻辑未触碰 | — |
| T233 | status 面 omitempty | summary | M5 |
| T234 | V4 | 守卫块 | — |
| T235 | 窗口不连续不被掩盖 | load `:618-635` + 视图 | — |
| T236 | POST 响应开启模式零改动 | `:1599-1605` 不动 | — |

**M1~M5**：M1 摘互斥守卫 ⇒ T224；M2 验签回退 manifest trust ⇒ T227/T229；M3 摘同钥守卫 ⇒ T225；M4 unauthorized 降级逐条跳过 ⇒ T228；M5 关闭模式回写新字段 ⇒ T222b/T233。

---

## 11. 冻结面自证

`foreign=nil` 时 `verifyVerificationEntrySignatureIn` 与 P42 原函数**逐分支一致**（薄包装，构造性）；条目/报告 schema 与 canonical 载荷零改动；POST 响应 map 字面量不动（T236）；`snapshot_anchor.go`/`appendonly_log.go`/五个冻结文件零改动；四冻结包与 `go.mod`/`go.sum` 零 diff 由 mktree 继承父 blob 构造性保证（ADR-062 §7 手法）。
