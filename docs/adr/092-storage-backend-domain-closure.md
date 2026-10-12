# ADR-092 — 维护轮：storage 后端域闭合（未知值拒绝启动，不得静默回退内存）

- **Status**: Accepted（维护轮，2026-10-12；无新判据、无新机制）
- **Base**: HEAD = `aabc1d9`（ADR-091 后）
- **Author**: judge（ZCode 代行），用户授权自主推进（2026-10-12）

---

## 1. 缺陷 D4（对抗巡航第二笔，false-clean 类）

`cmd/opscore serve` 的 storage 选择：

```go
switch *storageKind {
case "sqlite":
    s, err := sqlite.NewSQLiteStorage(*dbPath); ... ; stor = s
default:
    stor = storage.NewMemoryStorage()      // ← 任何未知值静默落内存
}
```

`--storage` 的域是 `{memory, sqlite}`（flag help 原文），但**域外的任何值静默回退到
内存存储**——包括运维的拼写错误（`sqllite` / `sqlte` / 配置文件里 `storage=sqllte`）。

**两项经验证据（2026-10-12 实测，修复前）**：

1. `opscore serve --storage sqllite …` **照常启动**（`timeout` 杀死时 exit 124，即服务
   在跑），日志原文：`OpsCore Control Plane listening addr=127.0.0.1:0 storage=sqllite`
   —— **把错误值当已接受回显**；`--db` 指向的库文件从未被打开。
2. 同一进程里 **两个组件对「这是什么部署」的认定互相矛盾**：
   - 口令门槛（Phase 27）与 jwt-secret 门槛按 `*storageKind != "memory"` 判定 ⇒ 把
     `sqllite` 当**持久化部署**严格对待（要求强口令、强 secret）；
   - storage 开关却把它当**内存部署**执行。
   ⇒ 「严格检查通过、数据每次重启清零」——正是本项目明文禁止的 false-clean 类
   （P30-I11：「不可把『持久化历史读不到』变成『没有历史』」；P34-I5：「绝不静默降级」；
   同族：`--export-interval/--export-dir` 不成对 fail-fast）。

## 2. 判定与修法

`--storage` 的域**闭合**为 `{memory, sqlite}`；域外值 = 操作者错误，**拒绝启动**并指名
错误值。修法：把内联 switch 提为 `openStorage(kind, dbPath) (storage.Storage, error)`：

```go
func openStorage(kind, dbPath string) (storage.Storage, error) {
    switch kind {
    case "memory": return storage.NewMemoryStorage(), nil
    case "sqlite":
        s, err := sqlite.NewSQLiteStorage(dbPath)
        if err != nil { return nil, fmt.Errorf("open sqlite %q: %w", dbPath, err) }
        return s, nil
    default:
        return nil, fmt.Errorf("unknown backend %q (want \"memory\" or \"sqlite\")", kind)
    }
}
// 调用点：错误 ⇒ logger.Error + os.Exit(1)。
```

同时消除了 §1.2 的组件矛盾：storage 解析完成后 `*storageKind` **必然** ∈ {memory,
sqlite}，下游所有 `!= "memory"` 判定（transitionPath、执行存储、口令/secret 门槛）此后
与真实后端**同源**。

## 3. 声明的行为变更（穷举）

1. `--storage` 域外值（含空串的显式传入）：**拒绝启动**（此前：静默内存）。
   受影响面 = 本来就配错了的部署——新行为是它们**应该**得到的响亮失败。
2. `--storage memory` / `--storage sqlite`：行为逐字节不变（含 `sqlite` 打开失败的
   既有 exit(1) 路径）。
3. 默认值不变（`memory`；经 cfgVal 播种）。

## 4. 测试契约（T404）与变异

| T | 覆盖 | 红证据 |
|---|---|---|
| T404 | `openStorage`：memory ⇒ 内存实现；sqlite（临时路径）⇒ sqlite 实现且可 Close；`sqllite`/`""` ⇒ **错误** | 编译级（接缝缺席）+ **经验红**（§1.1 的实测启动回显）+ MU 变异红 |

| MU | 变异 | 预期红 |
|---|---|---|
| MU3 | `default:` 恢复 `return storage.NewMemoryStorage(), nil` | T404（`sqllite` 得到可用 store） |

## 5. 非目标

- 不新增后端（域仍是 {memory, sqlite}）；
- 不改任何其它 flag 的默认值与语义；
- 不改 `sqlite` 打开失败时的既有 exit(1) 文案。

## 6. 自对抗（预判评审会打的点，先自答）

| 预判质疑 | 自答 |
|---|---|
| **Q1「宽容回退对开发便利难道不是好事？」** | 代价与便利不对等：一个字母的成本换「库文件没被碰过 + 重启清零 + 日志把错值当已接受回显」。开发要用内存，写 `--storage memory`（六键）。且本项目在 P30-I11/P34-I5 已两次明文拒绝 silent degradation——同一条尺子。 |
| **Q2「会不会有人在生产依赖这个回退？」** | 依赖回退 = 依赖「配错了也照跑」；这类部署的真实状态是「每次重启都丢数据而没人知道」。响亮的拒绝是修复，不是破坏。 |
| **Q3「这算新判据吗？」** | 不算：部署配置层的 fail-closed 收口；与证据链十二问面无关。配 ADR 是因为**改变既有行为**且带经验红证据（同 ADR-091）。 |
