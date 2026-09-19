# ADR-068 — Phase 46: Witness Reconciliation（Architecture）· Revised

- **Status**: Proposed (Phase 46, Architecture stage) · Revised per review（对齐 ADR-067 修订版：投影对账、无宽免层、毒化语义、缺席矩阵）
- **Base**: ADR-067（Scope 修订版）。冲突以 Scope 为准。

---

## 1. 文件清单

| 文件 | 变更 | 内容 |
|---|---|---|
| `internal/controlplane/server/snapshot_witness_reconcile.go` | **新增** | 投影解析 / 身份派生比对 / 家族缺席矩阵 / 三段式窗口 / digest 对比 / 视图 |
| `internal/controlplane/server/snapshot_witness_reconcile_test.go` | **新增** | T258~T273 |
| `internal/controlplane/server/mgmt_obs.go` | **修改** | GET/POST 两条 handler（4MiB body 上限沿用既有 reconcile 面预算） |
| `internal/controlplane/server/server.go` | **修改** | 路由注册（:8082 admin-only + CSRF） |
| `internal/controlplane/server/history_export_scheduler.go` | **修改** | 家族注册表与本地探针（只读）+ 状态面字段 |
| `snapshot_anchor.go` / 五家族实现 / 冻结面 / go.mod / go.sum | **零 diff** | 评审 BLOCKER-1 闭合的关键承诺：**不扩展 witness 协议、不改派发路径**——对账消费 witness 已持有的投影字节 |

## 2. 家族注册表（只读探针）

```go
type witnessFamily struct {
    Name       string              // ledger / key_lifecycle / destruction / verification / acceptance
    LedgerPath func(dir) string    // 主账本（chain-ledger.jsonl / signing-key-log.jsonl / destruction-log.jsonl / verification-log.jsonl / record-acceptance.jsonl）
    AnchorPath func(dir) string    // 锚定日志（chain-anchor / key-lifecycle-anchor / verification-anchor / destruction-anchor / acceptance-anchor .jsonl）
    LocalDigest func(dir, seq) (string, bool)  // 本地锚定日志中 seq 条目的 anchor_digest（经该家族既有 load 函数）
}
```

- 五家族静态注册；全部只读（os.Stat + 既有 load 函数；无锁——与家族既有 GET 状态面并发语义一致，O_APPEND 单写者）。
- LocalDigest 对 seq ∈ 本地窗口的条目返回其 anchor_digest（各家族 anchor 流的既有 load 已产出条目集）。

## 3. 对账算法（per family）

```
reconcileFamily(f, items):            // items = witness 回显投影（anchorRequest JSON）
  if len(items) == 0:  return {verdict: not_provided}

  // ---- 身份（毒化语义，评审 MAJOR-1 修正）----
  local.keyID / local.streamID = 本地派生（R40-2）
  for it in items:
      if it.KeyID != local.keyID || it.StreamID != local.streamID:
          return {verdict: family_unverifiable, bad_identity: true}   // 整家族毒化，绝不跳过

  // ---- 缺席矩阵（评审 MAJOR-2 修正，A5 四象限）----
  ledgerAbsent = !exists(f.LedgerPath) || empty
  anchorAbsent = !exists(f.AnchorPath) || empty
  ledgerAbsent && anchorAbsent ⇒ {verdict: family_ledger_deleted, witness_entries: n}   ★
  ledgerAbsent && !anchorAbsent ⇒ {verdict: family_incomplete}
  !ledgerAbsent && anchorAbsent ⇒ {verdict: family_incomplete}          // 绝不 truncated 假警报（T266）
  // 都在 ⇒ 窗口对比

  // ---- 三段式窗口（对 f.AnchorPath，anchor_seq 轴；无宽免层——评审 BLOCKER-2）----
  local = loadAnchorState(f.AnchorPath)（窗口 [min,max]；不可解析 ⇒ family_unverifiable + error）
  seenLocal = map[seq]anchorDigest（本地条目）
  for it in items（去重 by anchor_seq；重复 ⇒ unverifiable）:
      seq < min  ⇒ outside（不断言，R40-1；无宽免）
      min ≤ seq ≤ max ⇒ seenLocal[seq] 存在？digest 不等 ⇒ divergent；本地无该 seq（窗口内空洞）⇒ unverifiable
      seq > max  ⇒ truncated_ranges 收录 ⇒ family_ledger_truncated（上界无合法逐出，P45 F2）
  本地条目 seq 无投影覆盖 ⇒ missing 计数 ⇒ family_incomplete
  verdict 优先级：unverifiable > truncated > divergent > incomplete > intact
```

**无宽免层的理由（评审 BLOCKER-2 闭合）**：R40-6 保证 pending 行先于 dispatch 持久 ⇒ 本地锚定 max ≥ witness max 恒成立；锚定日志 compaction 只裁前缀 ⇒ 诚实状态永不产生「witness seq > 本地 max」——该状态唯一来源是尾部删除 ⇒ 可断言，无宽免。下界外不可断言（R40-1）：合法裁剪与删除在下界不可区分。

## 4. 路由与状态

- `POST .../export/witness-reconcile`：零副作用（不落盘/不写 audit/不发网络，T268）；body 上限 4MiB（沿用既有 reconcile 面预算——五家族投影 ~200B/条 ⇒ 4MiB ≈ 每家族 ~4000 条，覆盖默认 capacity；超限 ⇒ 400）。
- `GET .../export/witness-reconcile`：本地五家族存在性/窗口/not_enabled 探针。
- 响应 aggregate：`{families: {...}, }`，每家族 `{verdict, witness_entries, identity_ok, outside_count, divergent_seqs[], truncated_ranges[], missing_count, error?}。
- **两面词汇统一**（评审 MAJOR-3）：deleted 判定内建「文件在 ⇒ 非 deleted」，GET/POST 对同一文件状态不可能给出分歧结论（T273）。

## 5. 不变量（I1~I10）

| # | 不变量 |
|---|---|
| I1 | P40 对账路由/响应/witness 协议/`snapshot_anchor.go` 零改动（T258/T271） |
| I2 | 身份毒化：任一投影身份不符 ⇒ 整家族 unverifiable（绝不跳过/部分断言） |
| I3 | 三段式窗口：下界外不断言；上界外无条件 truncated（无宽免层）；窗口内 digest 对比本地全量条目 |
| I4 | 缺席矩阵四象限完整（评审 MAJOR-2）：只在「双双缺席 + 身份匹配条目存在」时判 deleted |
| I5 | 对账零副作用 |
| I6 | 「禁用不删文件」承重前提冻结（A7-7 非目标）；GET/POST 词汇统一 |
| I7 | 家族间隔离 |
| I8 | 身份 = 本地派生 key_id/streamID（R40-2）；**零签名验证**（投影不可验签——P40 验签职责在本地 load，deleted 场景本地已无文件；§7.3 调用方责任；评审 BLOCKER-1 闭合的代价声明） |
| I9 | 冻结面零 diff；go.mod/go.sum 零改动；零回归 |
| I10 | 对账顺序：先本地探针、后窗口对比（与 P45 I10 同族的承重顺序；投影分组为调用方声明——§7.3） |

## 6. 实现步骤（每步跑门禁）

1. 家族注册表 + 本地探针 → 编译绿
2. 身份毒化（I2）+ T265
3. 缺席矩阵（I4）+ T260/T261/T266
4. 三段式窗口 + digest 对比（I3）+ T262/T263/T264/T270
5. 路由 + GET/POST 词汇统一 + T258/T268/T273
6. 跨维/冻结 T267/T271
7. 变异 M1~M4（红→绿，sha256 还原）+ 三道门禁 + mktree 提交

## 7. 测试映射

T258→I1；T259→§3 全对齐；T260/T261→I4；T262→I3 上界；T263→I3 digest；T264→§3 missing；T265→I2；T266→I4；T267→I7；T268→I5；T269→I6；T270→I3 下界；T271→I9；T272→M1~M4；T273→I6。

## 8. 容量预算（评审 NOTE-3）

投影 ~200B；4MiB body ⇒ 每家族 ~4000 条，覆盖默认 capacity=4096；更大部署需分批对账（多次 POST，幂等只读）——ADR 明示，不扩预算。
