# consoleplugin — 向量库 / MQ 控制台（参考插件）

把 **Milvus / Kafka / Pulsar / Nacos** 的控制台做成**插件**，而不是编进客户端。这是
W17 对对比分析 #10 的落地：客户端保持对领域无知，所有向量/消息/配置知识都在插件侧。

## 为什么是插件

对比分析里 GoNavi 的 Milvus/Kafka、DBX 的 Pulsar/Nacos 是**内置特性**——每加一个系统，
客户端就要改一次、编一次、重新发布一次。插件化的代价是一次协议设计，收益是：

- 客户端**零领域知识**：它只会"列出插件声明的动作"和"按 id 运行一个动作"。
- 新系统 = 一个插件二进制，不改客户端、不重新发布主程序。
- 控制台的写操作（发布配置、生产消息）仍由插件的 `danger` 标记与宿主确认约束。

## 协议（内核新增，加法式）

内核 `plugindriver` 加了两个方法，旧插件不受影响（未知方法答 `ERR_NOT_SUPPORTED`，宿主
降级为"没有控制台"，而不是报错）：

| 方法 | 作用 |
|---|---|
| `connListActions` | 插件声明它在这个连接上提供哪些控制台动作 |
| `connRunAction` | 运行一个动作，返回**查询形状**的结果（列 + 行），复用网格渲染 |

动作描述（`ConnAction`）只有：`id`、`title`、`group`、`danger`、`params[]`。
`params[]` 的 `kind` 只是**画控件的提示**（text/number/textarea），宿主不解释它的含义。

结果复用 `ConnQueryResult`，所以控制台的输出和一条 SQL 的结果走**同一套渲染**——不需要
第二套网格、第二套导出、第二套复制逻辑。

## 一个二进制，多个引擎

引擎由**第一个命令行参数**选择，所以同一个可执行文件注册成多种类型：

```powershell
$env:TABLEPRO_PLUGIN_MILVUS  = "C:\tools\consoleplugin.exe;milvus"
$env:TABLEPRO_PLUGIN_KAFKA   = "C:\tools\consoleplugin.exe;kafka"
$env:TABLEPRO_PLUGIN_PULSAR  = "C:\tools\consoleplugin.exe;pulsar"
$env:TABLEPRO_PLUGIN_NACOS   = "C:\tools\consoleplugin.exe;nacos"
```

banner 的 `Kind` 必须与注册的类型一致，所以 provider 在打印 banner **之前**就按 argv 解析。

> 顺带修了客户端一个真实缺口：`TABLEPRO_PLUGIN_<TYPE>=path[;arg;arg]` 这个形式一直被
> 文档承诺，但 `Load()` 只 `stat` 整串、`pluginSpec` 也没有 Args 字段——参数从未被解析、
> 也从未传给子进程。本波补上了：`hostKey` 把 args 纳入身份（同一二进制以 `;milvus` 和
> `;kafka` 启动是两个引擎，不能共享进程）。

## 构建

```powershell
cd dbcore\plugindriver\consoleplugin
go build -o consoleplugin.exe .
```

这是**独立 Go 模块**（有自己的 `go.mod`），因为它依赖 `segmentio/kafka-go`，而这个依赖
不能泄漏进内核——fynequery、sqltui、polydb 都会编译内核。`replace` 指向本地内核，所以
插件始终跟随 checkout 的协议。

## 各引擎的映射

| 引擎 | 对象树 | Browse | 动作 |
|---|---|---|---|
| **Milvus** | 集合（`db/collection`） | 按字段顺序读实体 | `vector-search` 向量检索（rank + distance + 命中字段） |
| **Kafka** | 主题（`__` 内部主题标为 view） | 从最早消费一批 | `consume-messages` 消费、`produce-message` 生产（danger） |
| **Pulsar** | 主题（tenant→namespace→topic） | 订阅及其积压 | `topic-stats` 主题统计、`list-namespaces` |
| **Nacos** | 配置（`group/dataId`） | 读一份配置内容 | `query-config` 查询、`publish-config` 发布（danger） |

向量库是最能说明"为什么需要动作而不只是 Browse"的例子：**相似度检索不是一次浏览**。
列集合、看字段、读实体都在对象树里，但"给我离这个向量最近的 top-K"是一个动作——它接收
用户粘贴的向量、anns 字段和 topK，返回带距离的排序命中。

## 诚实的边界

- **HTTP provider（Nacos/Milvus/Pulsar）用 `httptest` 假服务测试**：证明的是映射逻辑
  与非 2xx 会作为错误上浮（而不是伪装成"0 行"）。**没有**对真实服务做过联调；各产品的
  REST 路径按官方 admin/Open API 文档编写，**版本差异可能需要调整**。
- **Kafka** 的 broker 代码是薄封装（`segmentio/kafka-go`），消费/生产的映射逻辑通过注入
  接口测试（无需 broker），但**真实 broker 行为未在本波验证**。
- 控制台的 SQL 面是**如实拒绝**：`Execute` 返回"此引擎不支持 SQL"，而不是假装成功返回
  空结果——后者用户无法与"真的没有行"区分。
- server 模式（`--serve`）的只读白名单**只放行 `/api/plugin/actions`**，不运行动作：宿主
  无法判断一个动作是只读检索还是写配置，往宽松方向猜正是只读默认要防的事。

## 加一个新引擎

写一个文件实现 `Provider` + `Session`（见 `provider.go` 的两个接口），在 `init()` 里
`registerProvider(...)`，用 `;新名字` 注册即可。客户端不需要任何改动。