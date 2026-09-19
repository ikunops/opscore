# ADR-063 · Phase 44 — Verifier Independence（验证者身份与信任锚独立）· Scope

- **Status**: PROPOSED（R219 投递；Phase 44, Scope stage）
- **Parent**: ADR-062（P43 Implementation, commit `ff9c4fc7`）
- **裁决前提**: R217 = A（ADR-061）；R218 = P43 实现交付并核验（`ff9c4fc`），P43 CLOSED。R218「下一步」指定 C1 为 Phase 44 第一候选——本条引自轮次简报（**未直读 r218 原文**），与 ADR-060 §3 的登记相互印证。
- **覆盖关系**: 本文为 Scope；后续 Architecture ADR（064，若开题）与之冲突处以 Architecture 为准（060/061 先例）
- **Author**: executor（方向自拍板，依据用户 2026-08-27 授权）

---

## 1. 分工句

| Phase | 回答的问题 |
|---|---|
| P37 Provenance | **who** — 谁签的（发布者） |
| P40 Anchoring | **where** — 域外何处见证 |
| P41 Key Lifecycle | **when** — 时间边界内吗 |
| P42 Attestation | **whether** — 验证发生过吗 |
| P43 Destruction | **why absent** — 不在的那段，证据去哪了 |
| **P44 Verifier Independence** | **who verified** — **「验证发生过」这句话是谁说的？说话的人能同时伪造证据吗** |

P42 把「从未验证」变成可断言，但 ADR-057 §8.1 同一句明文承认：「**不能把『验证者独立』变成可断言**」。P44 的唯一任务：把这句话中**密钥族可断言的部分**变成可断言；拓扑部分（独立主机）显式留在已知代价，不做代码断言。

---

## 2. 新问题（已核对代码）

### 2.1 验证报告由证据密钥自签自验

| # | 事实 | 位置 |
|---|---|---|
| 1 | 验证配置的 signer/trust 就是 manifest 平面的 `*exportSigner` / `*exportTrustStore` | `snapshot_verification.go:505-506`；接线 `history_export_scheduler.go:1000-1001`（`signer: s.signer` / `trust: s.trust`） |
| 2 | 验证条目由导出签名密钥签署 | `snapshot_verification.go:641-661`（`func (s *exportSigner) signVerificationEntry`）、`:950`（`KeyID: c.signer.keyID`） |
| 3 | 验签走 manifest 同一信任锚 | `snapshot_verification.go:663-690`（对 `*exportTrustStore`）；该 store 同时验 manifest（`snapshot_signature.go:232`；`history_export_scheduler.go:284-303`） |
| 4 | P42 构造守卫**强制**同源：`--export-verify-attest` 必须配 `--export-sign-key` + `--export-trust-keys` | `history_export_scheduler.go:367-374`；`main.go:488` |
| 5 | 报告/条目无任何独立验证者身份：唯一身份 = 导出钥 key_id | `snapshot_verification.go:194-207`、`:289-303`（`:299` `key_id`）、`:1219-1237`（视图无 verifier 字段） |
| 6 | 伪报告可被锚定——见证端收到的也是同一把钥签的记录 | `snapshot_verification.go:1150-1172`（`anchorVerificationReport` 用 `s.signer` 签 anchor 条目） |

（P39 ledger 同样用导出钥签——`snapshot_ledger.go:83`「same key as manifests」——但 ledger 记录的是**发布事实**，签发者=发布者是其语义本身（P37 的 who）；验证报告是对证据的**裁定**，签发者=证据持有者则是自证。二者不同类。）

### 2.2 核心悖论：裁定者与被裁定者同一把私钥

持导出私钥者（P40 主攻击对手模型）删除/改写证据后，补一份「全部 ok」的验证报告：验签通过（同钥在信任集内，`snapshot_verification.go:678-689`）、链自洽、可锚定（事实 6）。P42 的裁定词汇（ok / contradicted / unattested / unavailable，`snapshot_verification.go:87-90`）**没有任何一个能区分「独立验证过」与「攻击者自证过」**。

这不是本 Phase 的臆测，是 P42 自己写下的已知代价（`057-phase42-scope.md:281`，§8.1）：

> **验证者的独立性 = 本系统自己**。验证与发布同进程、同密钥、同主机；攻击者控制主机即可删除验证账本、或伪造一份「全部 attested」的报告。……**P42 把「从未验证」变成可断言，但不能把「验证者独立」变成可断言。**

对照：P43 已为**销毁裁定**完成密钥族分离（KAK 签发 + G3 + `destruction_unauthorized`，ADR-061 §1/§7；T217）。对手分层后（R216/R218 口径）：持导出私钥者伪造**销毁**记录已闭合（T217），伪造**验证**记录**原样可行**（事实 1~4）。**验证裁定是最后一个仍由在线证据密钥签署的裁定家族**——这就是 C1 的增量所在，也是它作为新判据的资格来源。

---

## 3. 候选比选（正面答复 judge）

| | 候选 | 新可断言判据 | 判据量 | 判定 |
|---|---|---|---|---|
| **C1（窄化）** | **验证者身份与信任锚独立** | 「验证报告的签署者无法签发证据」 | 小而完整 | **✅ 选为 Phase 44**（R218 第一候选） |
| C2 | 输入面完整性（durable 记录防篡改） | 「导出内容与库内记录一致」 | 很大 | 不选：需「记录级标识 + 摘要」基础设施且侵入 storage 平面，维持 ADR-060 §3「排序在后」，登记 Phase 45 首候选 |
| 其它 | 本轮自查 | — | — | 未发现比 §2.1 更尖锐且判据量相称的缺口 |

**ADR-060 当初不选 C1 的三条理由，今日为何不再成立（正面答复 judge）**：

1. **「不产生新维度，只是 who 的延伸」**——当时的对象是泛泛的「验证者独立性」。窄化后的判据不是 who 的重复：P37 的 who 断言**发布者**，P44 断言**裁定者与发布者的密钥族分离**——ADR-057 §8.1 明文承认这条**今天不可断言**。用 R210 击倒 HA 的尺子量：T227 场景（§10）今日本地全绿、P44 后必红 ⇒ 是此前给不出的判据，不是复述。
2. **「独立主机是拓扑」**——接受。**拓扑半边从头就不进判据**：P44 断言的只是信任锚互斥（构造期不变量 I1）；「验证进程与发布进程不同主机」进已知代价 1/2，与 P41 T177、P42 `verification_absent` 同域。
3. **「工程量不足以撑 Phase」**——判据量小是真，但单元完整：独立密钥面 + 四条守卫 + 一个新判据 + 两个红例 + 字节等价自证，恰好一个 Phase。再小就没有独立判据（R210 击倒）；并入 C2 则把密钥面改动与 storage 基础设施耦合，两边的冻结面互相拖累。R218 已裁定其为第一候选。

**与 P43 的对称性（为何不是「给验证报告换 KAK 签」）**：KAK 是**授权**密钥（什么可以存在/被信任，ADR-061 §1.4「生命周期 ∪ 销毁」），验证报告是**观测**结论；且验证周期自动运行（`verifyTick`，`snapshot_verification.go:1112`），让权限最高的离线密钥为每份报告在线签名 = 扩大 KAK 暴露面、制造「最高权限钥匙常在线」的新残差。观测身份单独成钥（VAK），三锚互斥（I1）。ADR-061 §1.3 否决 DAK 的逻辑在此**不**适用：VAK 不是「为同一判据再加一把钥匙」，它本身就是新判据的机制。

---

## 4. 机制（Scope 级；Architecture 轮定稿）

### 4.1 验证者密钥族（VAK）

- 新 flag：`--export-verifier-key`（Ed25519 私钥；**空 = 独立模式关闭**，P42 行为逐字节不变）/ `--export-verifier-trust`（独立公钥信任锚，复用 `newExportTrustStore`，`snapshot_signature.go:144`）。
- **零新建密码学/序列化**：复用 `exportSigner` / `exportTrustStore` / `signatureBlock` 与 P37 canonical 家族；**条目 schema 零改动**——`key_id` 字段已存在（`snapshot_verification.go:299`），独立模式下它的**值**变为 VAK key id，字段集、canonical 字节形状、链与窗口机制全部不变。

### 4.2 构造守卫（fail-fast，四条，不留「先启用后补钥匙」窗口）

| | 条件 | 结果 |
|---|---|---|
| **V1** | VAK 配置而 `--export-verifier-trust` 缺失/为空；或 VAK 不在其自身信任锚 | 构造失败（T79b / P41 I1 血统） |
| **V2 互斥可达** | verifier trust ∩ manifest trust ≠ ∅；或导出签名 key_id / KAK key_id ∈ verifier trust；或 VAK key_id ∈ manifest / KAK trust | 构造失败——**一把钥匙不得同时出现在两个信任锚**（ADR-060 §3「互斥可达」的落地） |
| **V3** | VAK key_id == 导出签名 key_id，或 == KAK key_id | 构造失败（G3 血统推广，`history_export_scheduler.go:352-354`） |
| **V4** | VAK 配置而 `--export-verify-attest` 未开 | 构造失败（对称 `:375-377` 的 interval 守卫） |

### 4.3 不变量

- **I1 三锚互斥**：verifier / manifest（`--export-trust-keys`）/ KAK（`--export-key-authority-trust`）三个信任锚两两不相交；三把私钥（导出 / VAK / KAK）两两不同。由 V2/V3 在构造期保证。
- **I2 拒绝追加（继承）**：独立模式下账本不可验（含 `verification_unauthorized`）⇒ 拒绝追加、不产任何断言（P42 fail-closed 通道原样：`snapshot_verification.go:554-561`、`:589-594`、`:927`）。
- **I3 判据分轨**：签署 key_id 属本部署已知（∈ manifest/KAK trust）但 ∉ verifier trust ⇒ **`verification_unauthorized`**（新）；key_id 在任何锚都不在 ⇒ 既有 `key_unknown`（`snapshot_signature.go:47`）。「异族已知的钥匙」不得误报成「未知钥匙」。
- **I4 尺子不变**：独立模式只换签署身份，不换判定逻辑——同输入下 `items[]` / `overall` 与关闭模式一致（T232）。
- **I5 出口不是输入（继承 P42 I7 / P43 I6）**：`verifier_independent` 等新字段绝不参与任何其它 verdict 的合流；VAK 不进 P41 生命周期账本（Q1）。
- **I6 冻结面**：独立模式关闭 ⇒ 新字段全部 `omitempty` 不出现，报告/条目/锚定/响应逐字节回到 P42 基线（T222b / T231）。

### 4.4 新可断言判据（结果模型）

| verdict / 断言 | 含义 | 触发 |
|---|---|---|
| **`verification_unauthorized`** | 条目签署身份属已知异族密钥（如导出签名钥）⇒ 不在 verifier 信任锚 ⇒ 账本不可信、拒绝追加、**无「已验证」断言** | **核心产出**：持导出私钥伪造报告（T227）；旧 P42 账本混入（T229） |
| **`verifier_independent`**（视图断言，omitempty） | 本报告签署身份 ∈ verifier 信任锚，且 I1 互斥在构造期成立 ⇒ 「签署者无法签发证据」可断言 | 独立模式合法报告 |
| `verifier_key_id`（omitempty） | 验证者身份明示（今日隐藏在 signature.key_id 里且恒等于 manifest 签名者） | 独立模式 |

既有 P42 词汇（ok / contradicted / unattested / unavailable；signature_ok / key_unknown / …）零改动。

---

## 5. 正交性

- **不新增文件、不新增 seq 家族、不新增 kind**：P44 复用 `verification-log.jsonl` 与 `report_seq`——独立账本会造成 whether 维度二源，违反单一裁定。改变的是**签署身份的信任锚来源**这一个自由度。
- **既有响应字节冻结**：`VerificationView` / status summary 新字段全 `omitempty`（P43 anchor omitempty 先例，ADR-062 §1）；既有字段与错误通道零改动。
- **两轴分离**：验证者身份不并入 coverage / chain / anchor / destruction 任何合流；三密钥面（证据 / 授权 / 观测）互斥、互不回写。

---

## 6. 非目标（重新冻结，14 条）

1. 不做部署拓扑断言（「验证者跑在独立主机」代码断言不了，ADR-057 §8.1 原话）；
2. 不改 P42 判定逻辑与尺子（I4：只换签名身份）；
3. 不改 P37 manifest / ledger 签名面（发布者自签发布事实是其语义）；
4. 不改 P40 锚定机制与既有 anchor 条目家族（验证锚定条目照旧由导出钥签——锚定是**存在见证**，不是作者见证）；
5. 不改 P41 KAK 面与生命周期账本（VAK 不入账，Q1）；
6. 不改 P43 销毁面任何文件；
7. 不迁移/转换既有 `verification-log.jsonl`（fail-closed，不洗白，Q2 / T229）；
8. 不做验证者的人类身份、授权策略或审批流判定（同 P43 Q2 边界，`policy` 是声明不是授权）；
9. 不提供 VAK 旋转/吊销机制（静态锚，同 manifest trust 先例；列 Phase 45+ 候选）；
10. 不回写既有只读面任何既有字段（新字段全 omitempty）；
11. 不新建第二套 append-only / canonical / 签名实现；
12. 不改 `go.mod` / `go.sum` 与四冻结包（platform / governance / plugin/{runtime,isolation} / controlplane/hostregistry）；
13. 不做时间权威（`verified_at` 自证，同 P41/P42/P43）；
14. 不让 C2 提前（storage 平面侵入另行立项，Phase 45 首候选）。

---

## 7. 逃逸分析（对手分层，诚实）

| 对手 | P44 前 | P44 后 |
|---|---|---|
| **持导出私钥**（文件泄露 / 备份 / RCE 读文件，P40 主对手） | 可伪造验证报告（同钥自验，§2 事实 1~4），本地全绿 | 伪报告 `verification_unauthorized` ⇒ 不可信 ⇒ 无断言（T227）。**本 Phase 的全部增量** |
| **控制进程/主机**（VAK 在线同进程） | 可伪造 + 可删账本 | **原样保留**：VAK 在线，主机级攻击者仍可伪造「已验证」或换 trust 配置重启。拓扑代价，代码断言不了（已知代价 1） |
| **持 KAK 者** | 已能旋转/吊销全部签名钥（P41） | 与验证面无新增交集（VAK ≠ KAK，I1）；若运维把 VAK 交 KAK 持有者保管 ⇒ 独立性形式化（已知代价 2） |
| **删账本** | `verification_absent`（P42） | 不变（同族逃逸，域外可检测） |

**结论（不美化）**：P44 把「伪造验证裁定」的门槛从「持有在线证据密钥」抬到「持有独立观测密钥或控制主机」，**没有消除**。它与 P41 T177 / P42 `verification_absent` / P43 §10-1 同族——每轮把残差收缩一层、如实登记一层。

---

## 8. 已知代价（如实）

1. **拓扑残差原样**：验证与发布仍同进程（`verifyTick`）。控制主机者可伪造 VAK 签名或直接更换 trust 配置重启。P44 断言的是**信任锚互斥**，不是**保管域分离**；
2. **VAK 无生命周期**：静态锚。VAK 泄露 = 持续伪造能力，直至人工换锚 + 换目录（Q1 未做的代价，如实登记，列 Phase 45+）；
3. **启用即毒化旧账本**：既有 P42 账本（导出钥签）在独立模式下 ⇒ unauthorized ⇒ 不产断言。fail-closed 的代价；迁移 = 新导出目录，不提供转换器（Q2）；
4. **`verifier_independent` 是构造期事实的投影**：互斥由守卫在构造期判定，运行期不持续重证（前提：trust 配置进程内不可变）；
5. **词汇双轨**：`verification_unauthorized`（异族已知钥）与 `key_unknown`（无锚钥）的区分要求读者理解 I3——若 Architecture 轮认为区分成本 > 收益，可收敛为单一词汇（Q3 备选）；
6. **继承自 P43 的两条程序债**（r218 裁定应补入 ADR-060/061 §8；本轮实测 grep 两 ADR 均无此二行，因单文件纪律未触碰已发布 ADR，登记如下保持账目可见）：① stuck-intended 组的 publications 可能同时出现在 `unaccounted` 与 `unconfirmed`；② destruction-anchor 流自身 compaction 不被观察（I5 自指，无外部损失）。

---

## 9. 提请裁决（Q1~Q3）

- **Q1 VAK 是否接入 P41 生命周期账本**（KAK 签 verifier 授权事件 ⇒ 可旋转/吊销/时间边界）？我：**否**——静态锚同 manifest trust 先例；接入 = KAK 授权域再扩张 + 判据膨胀，若做应连销毁授权域一起重审，列 Phase 45+。代价见已知代价 2。
- **Q2 旧 P42 账本在独立模式下的处置？** 我：**fail-closed 不迁移**（T229）——账本不可信 ⇒ 不产断言，绝不借双读/转换洗白。
- **Q3 `verification_unauthorized` 的暴露形态？** 我：独立模式新检查走结构化 problems（`{report_seq, verdict, detail}`，P43 `destructionProblem` 形态，`snapshot_destruction.go:976,1065`）；P42 既有失败路径保持现字符串通道（字节冻结）。若 judge 认为双轨词汇成本高（已知代价 5），备选 = 收敛单一词汇。

---

## 10. 测试契约（T222~T235，14 例；T 编号全局连续，实测既有最大 = T221）

- **T222（+ T222b 字节等价）**：VAK 未配置 ⇒ 全零回归：响应/报告/条目/锚定与 P42 基线逐字节一致。
- **T223 V1**：VAK 无 trust / 不在自身 trust ⇒ 构造失败。
- **T224 ★V2 互斥**：VAK ∈ manifest trust；导出钥 ∈ verifier trust；KAK ∈ verifier trust ⇒ 各自构造失败。
- **T225 V3**：VAK == 导出钥；VAK == KAK ⇒ 构造失败。
- **T226 合法流**：独立模式报告由 VAK 签；entry `key_id` = VAK；视图 `verifier_independent=true` + `verifier_key_id` 落盘。
- **T227 ★核心红例**：持**导出私钥**末端追加伪造「全部 ok」验证条目 ⇒ `verification_unauthorized` ⇒ 账本不可信 ⇒ **无「已验证」断言**；且逐一验证 P35~P43 各判据在该场景仍为「不断言」（non-vacuousness 自证，T213 式）。
- **T228 I2**：unauthorized 后追加被拒（fail-closed，绝不借 append 重建干净账本）。
- **T229（Q2 判别）**：P42 旧账本 + 独立模式 ⇒ 旧条目 unauthorized ⇒ 不产断言，绝不美化。
- **T230（I3 分轨）**：异族无锚钥 ⇒ 既有 `key_unknown`（与 unauthorized 区分）。
- **T231**：验证锚定条目照旧（导出钥签）；既有四族 anchor 条目零改动（T221 式字节等价）。
- **T232（I4）**：同输入，独立模式开/关 ⇒ `items[]` / `overall` 字节一致（只换签名身份，不换尺子）。
- **T233**：status 面新字段 omitempty；关闭时零变化。
- **T234 V4**：VAK 配置而 `--export-verify-attest` 未开 ⇒ 构造失败。
- **T235**：独立模式下窗口不连续 ⇒ 不断言（P42 I4 复用回归）。

**变异 M1~M5**（摘判据 ⇒ 对应 T 必红，按 sha256 字节还原）：M1 摘互斥守卫 ⇒ T224；M2 验签回退 manifest trust store ⇒ T227 / T229；M3 摘同钥守卫 ⇒ T225；M4 unauthorized 降级为逐条跳过（不毒化账本）⇒ T228；M5 关闭模式回写新字段（非 omitempty）⇒ T222b。

---

## 11. 文件与接线（Scope 预估，实现轮可微调）

- `snapshot_verification.go`：config 加 verifier signer/trust（`:502-519`）；`signVerificationEntry` 换源（`:641`）；`verifyVerificationEntrySignature` 按 I3 分轨（`:663-690`）；append 路径（`:900-979`）；视图新字段（`:1219-1237`）。**条目 schema 零改动**。
- `history_export_scheduler.go`：守卫 V1~V4（紧跟 `:367-401` 既有守卫块）；`verificationConfig` 接线（`:993-1006`）。
- `snapshot_verification_test.go`：T222~T235。
- `cmd/opscore/main.go`：2 个 flag（`--export-verifier-key` / `--export-verifier-trust`，紧邻 `:488-490`）。
- `server.go`：**零新路由**（复用 `:1660-1661`）；视图字段经既有 handler 暴露。
- `go.mod` / `go.sum` 零改动；四冻结包零 diff；P40 / P43 文件零改动。
