# ADR-090 — Phase 55 Architecture: P2 打磨包

- **Status**: Accepted（配套 ADR-089）
- **Base**: HEAD = `a457c9a`
- **Author**: judge（ZCode 代行）

---

## 1. 文件清单

| 文件 | 动作 | 说明 |
|---|---|---|
| `internal/controlplane/server/server.go` | 修改 | `decodeJSON` 缺席体显式错误；`handleRun` 结果映射一律 200 |
| `internal/controlplane/server/server_test.go` | 修改 | 既有 fake-SSH 集成测试断言更新为 T400 契约（200 + `success:false` + 非本机门） |
| `internal/controlplane/server/decode_json_test.go` | 新增 | T399（缝级） |
| `docs/operations.md` | 修改 | §3 增注释语言约定（P2-4） |
| `internal/demo/demo.go` | 修改 | 字段级禁用声明（P2-5） |

**冻结面零 diff**；无新路由、无新依赖、无新包。

## 2. 实现细节

### 2.1 `decodeJSON`（P2-2）

```go
func decodeJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return errors.New("request body required") // 缺席体是请求错误，不是可继续的默认值
	}
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}
```

调用点审计（10 处，全部体必需）：`server.go:686/791/809/1018/1434/1697`、
`mgmt_kill.go:74/118`、`mgmt_quota.go:73/138`。均把非 nil 错误映射为 400 ✓。

### 2.2 `handleRun` 结果映射（P2-3）

```go
	// The operation's OUTCOME is business data, not a server fault: a request
	// that ran the operation and got a negative result COMPLETED. Mapping it to
	// 5xx would let proxies/alerting/retries read a business "no" as an
	// infrastructure failure (ADR-089 §2.2). Gate rejections keep their own
	// statuses; plan failures (404/500) are unchanged — a failed plan is not an
	// operation result.
	status := http.StatusOK
	...
	writeJSON(w, status, body)   // success/cancelled 字段承载结果
```

（`adm.RecordOutcome(!result.Success)` 不变——保护面反馈与 HTTP 语义正交。）

### 2.3 语言约定（P2-4，operations.md §3 追加）

> **注释语言约定（P55/ADR-089）**：代码注释与错误消息**英文**（可 grep、日志/审计单一
> 语言、错误串进 API 响应）；ADR、运维文档、UI 文案**中文**。既有注释不追溯重写。

### 2.4 demo 禁用声明（P2-5，demo.go 字段处）

```go
		// DEMO ONLY — NEVER FOR REAL MACHINES: this fake host listens on
		// 127.0.0.1 only (net.Listen above) and its identity is a throwaway
		// in-process key, so skipping host-key verification is harmless HERE
		// and fatal anywhere else. A real target MUST verify the host key.
		InsecureIgnoreHostKey: true,
```

## 3. 不变量（I1~I5）

| # | 不变量 |
|---|---|
| I1 | `decodeJSON` 对正常体/空体（EOF）行为不变；仅缺席体从「静默 nil」变「显式错误」（T399） |
| I2 | run 的成功/取消语义不变（200 ± 体）；仅「失败」从 500 变 200（T400） |
| I3 | 认证/授权/gate 拒绝的状态码（401/403/429 等）零改动 |
| I4 | plan 失败路径零改动 |
| I5 | 冻结面零 diff；行为变更全部登记（ADR-089 §4） |

## 4. 测试映射与变异

| T | 形态 | 判别点 |
|---|---|---|
| T399 | 单元（缝） | nil 体 ⇒ 非 nil 错误；正常体 ⇒ 解码成功；空体 ⇒ `io.EOF` |
| T400 | 集成（fake SSH） | 远端失败 ⇒ 200 ∧ `success:false` ∧ 非 401/403/404 ∧ 不含本机门文案 |

| MU | 变异 | 预期红 |
|---|---|---|
| MU1 | 恢复 `r.Body == nil ⇒ return nil` | T399 |
| MU2 | 恢复失败 ⇒ 500 | T400 |

## 5. 实现顺序（每步跑门禁）

1. 写 T399 + 更新集成测试为 T400 契约 ⇒ **先红**（T399：nil 体当前返回 nil；T400：当前 500）
   ——红证据留行。
2. 落 §2.1/§2.2 ⇒ T399/T400 绿。
3. 变异 MU1/MU2（现取 sha256、还原核验）。
4. §2.3/§2.4 文档与注释。
5. 快速档门禁全绿 → 提交（docs + impl 两笔，mktree）→ R248 归档 → **队列收尾**：
   `test-heavy` + `race-heavy` 全量档（P52~P55 全部落定后的最终验收）。

## 6. 成本与诚实声明

- 零运行时成本：两处判定级改动；零新状态。
- **不声称**：不改任何既有错误文案；不追溯重写既有注释（§2.3）；P2-3 只动同步 run
  （异步 `/api/executions` 的失败本就经执行记录承载，不在本条）。
