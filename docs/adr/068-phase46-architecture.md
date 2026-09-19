# ADR-068 — Phase 46: Witness Reconciliation（Architecture）

- **Status**: Proposed (Phase 46, Architecture stage) · drafted by judge-as-executor（同 ADR-067 程序说明）
- **Base**: ADR-067（Scope）。冲突以 Scope 为准并回改本 ADR。

---

## 1. 文件清单

| 文件 | 变更 | 内容 |
|---|---|---|
| `internal/controlplane/server/snapshot_witness_reconcile.go` | **新增** | 对账体解析 / 逐条验签与身份 / 家族路由 / 三段式窗口 / 宽免层 / 视图 |
| `internal/controlplane/server/snapshot_witness_reconcile_test.go` | **新增** | T258~T272 |
| `internal/controlplane/server/mgmt_obs.go` | **修改** | GET/POST 两条路由 handler |
| `internal/controlplane/server/server.go` | **修改** | 路由注册（:8082 admin-only） |
| `internal/controlplane/server/history_export_scheduler.go` | **修改** | 家族本地状态探针（各账本存在性/窗口的只读聚合）+ 状态面字段 |
| 其余（含 P40 对账、五家族实现、appendonly_log.go、冻结面、go.mod/go.sum） | **零 diff** | — |

## 2. 家族注册表（唯一新增的映射面）

```go
type witnessFamily struct {
    Kind        string            // anchor kind（P40 既有常量）
    LedgerPath  func(dir) string  // 家族主账本（chain-ledger / signing-key-log / destruction-log / verification-log / record-acceptance）
    AnchorPath  func(dir) string  // 家族锚定日志（chain-anchor / key-lifecycle-anchor / verification-anchor / destruction-anchor / acceptance-anchor）
    KindOfLog   func(entry) kind  // 锚定条目 kind 过滤（chain-anchor 流只认 publication…）
}
```

五家族静态注册；**不新增任何写路径**。本地探针全部只读（os.Stat + 既有 load 函数）。

## 3. 对账算法（per family）

```
reconcileFamily(f, items):                       // items = witness 完整签名条目
  if len(items) == 0:            return {verdict: not_provided}
  // ---- 逐条验证（A2）----
  for e in items:
      sig  = verifyAnchorEntrySignature(e, trust)         // P37 五态
      ident = e.KeyID == local.keyID && e.StreamID == local.streamID
      if sig != ok || !ident:     bad++; continue         // 计数，绝不静默
      if e.Kind != f.Kind:        bad++（路由错误条目）
  if bad > 0 && bad == len(items): return {verdict: family_unverifiable, bad}
  // ---- 本地状态（A3）----
  ledgerAbsent  = !exists(f.LedgerPath) || empty
  anchorAbsent  = !exists(f.AnchorPath) || empty
  if ledgerAbsent && anchorAbsent:
      // witness 持有本部署 stream 的有效条目 ⇒ 本地双双缺席不是「从未启用」
      return {verdict: family_ledger_deleted, witness_entries: n}   // ★核心产出
  if ledgerAbsent && !anchorAbsent: return {verdict: family_incomplete}  // A5 诚实边界
  // ---- 三段式窗口（A4）----
  local = loadAnchorState(f.AnchorPath)（窗口 [min,max]；不可解析 ⇒ family_unverifiable + input_error）
  for e in validItems（按 seq 排序）:
      seq < min  ⇒ outside：宽免层查询（见 §4）⇒ 覆盖 ⇒ excused；否则 outside（不判 deleted）
      min ≤ seq ≤ max ⇒ 与本地同 seq 条目 digest 对比 ⇒ 不等 ⇒ divergent
      seq > max  ⇒ 宽免层覆盖 ⇒ excused；否则 ⇒ truncated（上界无合法逐出，P45 F2 同款）
  local 有而 witness 无 ⇒ missing（incomplete 计数，never broken）
  verdict = divergent|truncated ⇒ family_divergent|family_ledger_truncated
            missing ⇒ family_incomplete；否则 family_intact
```

**宽免层（§4）**：`excused(range) = ∃ 已完成销毁记录 d：d.kind == 该家族 compaction kind ∧ d.targets 覆盖 range`，来源 = 本地 destruction-log（loadDestructionState 的 usable+completed 组）；本地缺失时 = 对账体 destruction 家族的 witness 条目（同样验签后按 entry 结构解析 targets）。

## 4. 路由与状态

- `POST /management/v1/protection/alerts/history/export/witness-reconcile`（body 见 ADR-067 A1；零副作用：不落盘/不写 audit/不发网络，T269）。
- `GET .../export/witness-reconcile`：本地各家族存在性/窗口探针 + not_enabled 诚实输出（A6）。
- 响应 aggregate：`{families: {ledger: {...}, key_lifecycle: {...}, ...}, bad_entries_total}`；每家族 `{verdict, witness_entries, bad_entries, excused_ranges[], divergent_ids[], truncated_ranges[], missing_count, error?}`。
- admin-only + CSRF fail-closed；未启用各家族区块 `not_enabled`。

## 5. 不变量（I1~I9）

| # | 不变量 |
|---|---|
| I1 | P40 publication 对账路由/响应零改动（T270） |
| I2 | 逐条验签+身份；坏条目毒化该家族断言（unverifiable + 计数），绝不静默跳过 |
| I3 | 三段式窗口：下界外不判 deleted（宽免层只影响解释，不影响下界不可断言性）；上界未宽免 ⇒ truncated |
| I4 | 宽免层只认「已完成 + kind 匹配 + targets 覆盖」的销毁记录；宽免是解释不是断言 |
| I5 | 对账零副作用：不落盘、不写 audit、不发网络 |
| I6 | 双双缺席（账本+锚定日志）∧ witness 有效条目 ⇒ deleted；仅账本缺席 ⇒ incomplete（A5 不夸大） |
| I7 | 家族间隔离：单家族坏数据不污染他族断言 |
| I8 | 验签信任锚 = 既有 P37 export trust（零新增信任源）；身份 = P40 R40-2 派生 key_id/stream_id |
| I9 | 冻结面零 diff；go.mod/go.sum 零改动；不新增依赖；零回归（T258） |

## 6. 实现步骤（每步跑门禁）

1. 家族注册表 + 本地探针 → 包编译绿
2. 逐条验证（I2）+ T266
3. 双双缺席判定（I6）+ T260/T261
4. 三段式窗口 + 宽免层（I3/I4）+ T262/T263/T264/T265/T267
5. 路由 + 状态面 + T258/T269
6. 跨维/冻结/零回归 T268/T270/T271
7. 变异 M1~M4（红→绿，sha256 还原）+ 三道门禁 + mktree 提交

## 7. 测试映射

T258→I9/I1；T259→§3 全对齐；T260/T261→I6；T262/T263→I3/I4；T264→§3 divergent；T265→§3 missing；T266→I2；T267→A5/I6；T268→I7；T269→I5；T270→I1；T271→I9；T272→M1~M4。
