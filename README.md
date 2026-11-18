# dbcore

共享数据库内核：方言、驱动、连接池、分页、元数据、错误分类、schema 比对、
插件宿主。七个客户端（TableProGo、dbstudio-go、fynequery、sqltui、db-dev、
polydb、sqlgpui 的契约基线）都用它，所以「同一个 SQL 在不同引擎上怎么写」
「一次浏览怎么翻页」「一个驱动错误是什么」只在这里定义一次。

```go
require github.com/Ben-guanzhi/dbcore v0.11.0
```

```sh
go get github.com/Ben-guanzhi/dbcore@v0.11.0
```

没有外部服务、没有 cgo：`go build ./...` 在干净 clone 上就能过。

## 包地图

| 包 | 职责 |
|---|---|
| `dbcore`（根） | 核心接口与工厂表：`Driver` / `SQLDriver` / `KVDriver`、`ConnSpec`、`Open`、`RegisterFactory` / `UnregisterFactory` / `FactoryReliance`、`Value` 标签联合、`QueryResult`、`TableRowsRequest` |
| `sqltext` | Catalog SQL 生成（列库/表/列/索引/外键、LIMIT/OFFSET、标识符引用、字面量转义）。引擎是类型标签，每个函数是一条 switch 臂 |
| `ddl` | DDL 文本生成：`CREATE TABLE` 渲染、`ALTER` 动词、视图/存储例程的取定义语句 |
| `mysql` `postgres` `mssql` `oracle` `sqlite` `dm` | 各引擎驱动（native pool + 方言 + 元数据），`init()` 注册进工厂表 |
| `oracledsn` | 只拼 Oracle DSN，**不引 go-ora**——客户端可以在不链接 13MB 驱动的情况下渲染连接串 |
| `redis` | KV 驱动（键浏览、类型、TTL） |
| `keyset` | keyset（seek）分页；游标走文本，避免 64 位键过 JSON 数字被舍入 |
| `dberror` | 把驱动错误分类成稳定码（重复键/连接断开/死锁/超时），零依赖、不打驱动包 |
| `schemadiff` | 两份 catalog 快照求差 → 有序可选的计划 → DDL（纯函数，不连库） |
| `masking` | 出机前脱敏（MCP / LLM 上下文）：先按列名整体遮蔽，再按值形状兜底 |
| `fuzzy` | 模糊筛选（子串/分词），供表格过滤 |
| `drivertest` | 引擎契约套件：`Harness` 给内核驱动用，`Spec` 让客户端把手里的 `Open` 交出来跑同一套断言 |
| `contract` | 契约测试的共享夹具 |
| `sqlformat` | SQL 美化器（分词 + 关键字大写 + 子句独占缩行；正确的 SQL 原样往返）——dbstudio / sqlgpui / db-dev 共用一份实现 |
| `sqltext` 的查询构建器 | `SelectSpec` + `BuildSelect`：JOIN/聚合/嵌套条件/GROUP/ORDER/LIMIT 的纯函数生成（可视化查询构建器的数据模型） |
| `processes` | `ProcessLister` 能力：列出/终止服务器会话（MySQL PROCESSLIST+KILL、PG pg_stat_activity、MSSQL dm_exec_requests）；ID 校验内核收口 |
| `servervars` | `ServerVariablesReader` 能力：只读列出系统变量（三方言；**不含改值**——SET GLOBAL 的副作用边界写进接口文档） |
| `users` | `UserAdmin` 能力：列出可登录用户 + Create/Drop/Grant 的 DDL 生成（**执行归客户端闸门**） |
| `datagen` | `GenerateRows` / `GenerateInserts`：类型驱动的确定性测试数据 + 方言 INSERT（tinyint(1) 布尔优先、列名猜语义） |
| `transfer` | `TransferTable`：跨连接整表搬运（分页拉取 → 按列名对齐 → 逐批 ApplyBatch + 进度回调） |
| `model` | 模型设计正向工程：ER 模型（可序列化 JSON）→ 方言 DDL（依赖拓扑序、N:M 关联表、类型映射） |
| `value` 的单元格上限 | `MaxCellRunes` / `ClipCell`：超长 TEXT/BLOB 单值按 rune 截断并标明原长（valueForColumn 收口，五客户端共用） |
| `sqlformat` 的工具链 | `toolchain go1.27.3`（TableProGo 显式声明）——stdlib 条目随编译工具链走 |
| `plugindriver` | 插件宿主：newline-JSON 上的「一进程一连接」(`Driver`) 与「一进程多连接」(`ConnDriver`)，含 banner 握手、取消、流式。`consoleplugin/` 是独立子模块的参考插件（非 SQL 控制台），它带 Kafka 依赖，刻意隔离 |
| `usability` | 跨客户端可用性指标（S-01）：任务/结果/耗时/回退。默认关闭，只在 `POLYDB_USABILITY=1` 时落本地 NDJSON，上限 1 MiB，不上传 |
| `spec/` | 机器可读契约：`a11y-baseline.json`（可及性基线）、`interaction-baseline.json`（交互基线）、`usability-metrics.json`（可用性事件形状）、`vectors/`（方言向量语料）。每个客户端用自己语言的原生测试读同一份 |

## 三条设计约束

1. **方言是一等公民**。所有 per-engine 字符串差异（引用符、占位符、分页语法、
   类型名、字面量转义）都在 `sqltext` / `ddl` 的 switch 臂里，调用方只传引擎
   标签。加一个引擎 = 加若干臂，不改调用点。
2. **零可选依赖倒灌**。内核不引 go-ora（→ `oracledsn`）、不引 Kafka（→ 独立
   子模块）、不引 UI。客户端的体积预算由它自己的驱动选择决定。
3. **契约可以执行**。`drivertest` 让客户端拿自己的连接胶水跑内核的断言；
   `consumer_claims_test.go` 校验各消费仓「文档声称的接入程度」是否仍是代码的
   事实，还校验它们依赖的是**已发布的版本化模块**而不是本地 `replace`。

## 版本与升级

- 当前 `v0.2.0` 是第一个**真实模块路径**版本：`v0.2.0` 之前 tag 指向占位路径
  `github.com/dbcore/dbcore`，与托管地不符，下游不可用。
- 破坏性变更升 minor（v0 阶段允许），稳定后升 major。
- 消费者**不要**加 `replace`：它会让 CI 编译的不是外人 clone 得到的东西。
  本地改内核请用工作区根的 `go.work`（不提交，CI 看不到）。
- 升级步骤：改 `go.mod` 的版本号 → `go mod tidy` → `go build ./...` →
  `go test ./...`。`drivertest` 的失败通常指向真实的方言或粘合回归。

## 消费方

TableProGo · dbstudio-go · fynequery · sqltui · db-dev · polydb（Go 侧）·
sqlgpui（Rust，读 `spec/` 里的契约 JSON 而非 Go 模块）

## 测试

```sh
go test ./...              # 离线套件：20 个包，无需任何服务器
go test -race ./...        # 并发纪律
```

需要真实服务器的契约测试由环境变量开启（`DBCORE_TEST_PG_DSN` /
`DBCORE_TEST_MYSQL_DSN` / `DBCORE_TEST_MSSQL_DSN` / `DBCORE_TEST_ORACLE_DSN`），
未设置时 skip；Redis 的 KV 契约跑在 miniredis 上，离线可用。
