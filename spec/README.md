# 可用性指标与可及性基线（S-01 / S-04）

这两个文件把 SIMS 213 透镜里的两条处方变成了**可执行的契约**，随内核版本化：

| 文件 | 契约 | 消费者 |
|---|---|---|
| `usability-metrics.json` | 任务级可用性事件（成功 / 耗时 / 回退） | `dbcore/usability` 包 + 各客户端 |
| `a11y-baseline.json` | 可交互名字 / 对比度 / 键盘可达 / 不只靠颜色 | 各客户端的前端测试 |

## 为什么放在内核仓库

`D:\tools\db` 本身不是 git 仓库，契约放在那里无法版本化。内核是唯一的共性依赖，
`dbcore/spec/vectors/catalog-contract.json`（C3）已经确立了"契约随内核走"的先例。

## S-01：可用性指标

```go
um := usability.Open("dbstudio", filepath.Join(dir, "usability.ndjson"))
defer um.Close()
um.Record("query", "ok", time.Since(started), map[string]string{
    "statement_type": "SELECT", "rows": "42", "truncated": "false",
})
```

- **默认关闭**：只有 `POLYDB_USABILITY=1`（或 `true`）才落盘；关闭时 `Record` 是
  no-op，调用点不需要判断。
- **本地、有限**：NDJSON 追加写，1 MiB 封顶（`storage.max_bytes`），到顶即停并置
  `capped`；不上传、不联网。
- **三指标**：`outcome` 给成功/失败/取消/回退，`duration_ms` 给耗时，
  `task` 给任务类别。`Stats()` 聚合 N / 各 outcome 计数 / p50 / p95。
- **契约**：`usability_test.go` 读本目录的 `usability-metrics.json` 校验每条落盘
  事件（必填字段、outcome 枚举、已知 task）。

接入点（已落地）：

| 客户端 | 任务 | 位置 |
|---|---|---|
| dbstudio | connect / query / commit / rollback | `metrics.go` + `query.go` / `tx.go` |
| TableProGo | query / commit / rollback | `metrics.go` + `query.go` / `txn.go` |

其余客户端接入只需三行：`Open` 一次，在任务完成处 `Record` 一次，退出 `Close`。

## S-04：可及性基线

四条规则，`applies_to` 声明适用面（`web` / `gpu` / `fyne` / `tui`）：

1. `interactive-name` —— 每个可交互控件有可读名字；
2. `contrast` —— 正文对比度 ≥ 4.5:1（WCAG AA 小字号）；
3. `keyboard-reachable` —— 主要动作不依赖鼠标；
4. `no-color-only-signal` —— 状态不能只靠颜色（事务中 / 只读 / 健康灯）。

已落地的检查：

| 客户端 | 检查 | 结果 |
|---|---|---|
| dbstudio（GPU） | `a11y_test.go`：静态扫描 `ui.Button/Select/TextInput`，要求可见文本、`.Label(...)`、`.Placeholder(...)` 或外层 `ui.Field(...)` | 首跑抓出 4 处缺口（schema 选择器、隔离级别下拉、导入映射下拉、参数输入）——前两处已补 `.Label(...)`，后两处由表达式标签规则覆盖 |
| TableProGo（Web） | `a11yContract.test.ts`（契约覆盖）+ 既有的 `accessible-names.test.ts` / `contrast.test.ts`（阈值改为读契约 `min_ratio`） | 51 项全绿 |

## 验证

| 命令 | 结果 |
|---|---|
| `go test ./usability/`（dbcore） | ✅ 4 |
| `go test ./...`（dbcore 全仓） | ✅ |
| `go test .`（dbstudio） | ✅ |
| `go test .`（TableProGo） | ✅ |
| `tsc --noEmit` + `node --test`（TableProGo 前端） | ✅ |
