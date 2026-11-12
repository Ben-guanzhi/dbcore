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

## 真实服务联调（2026-11-12）

`liveprobe/` 是一个手工联调夹具：用真实的 `consoleplugin.exe` 逐帧走
`open → tables → browse → connListActions → connRunAction → close`，对面是真实服务，
不是 httptest。它**不在** `go test` 里运行（需要活着的服务）。

```powershell
go build -o consoleplugin.exe .
$env:TABLEPRO_LIVE_BIN = (Resolve-Path .\consoleplugin.exe).Path

# Nacos（真实 v2.3.2，只读；另有 publish-config 写专用 dataId 后删除的用例）
go run ./liveprobe nacos http://localhost:8848

# Milvus（真实 v2.5.4；集合需按 create→insert→create index→load 的顺序准备好）
$env:TABLEPRO_LIVE_OBJECT = 'default/liveprobe'
$env:TABLEPRO_LIVE_ACTION = 'vector-search'
$env:TABLEPRO_LIVE_ARGS = '{"collection":"liveprobe","vector":"0.1,0.2,0.3,0.4","annsField":"vec","topK":3}'
go run ./liveprobe milvus http://localhost:19530

# Kafka（真实 3.9.0 KRaft；ConnectParams 的 Host/Port 分开传，夹具自动从 URL 拆）
$env:TABLEPRO_LIVE_OBJECT = 'liveprobe'
$env:TABLEPRO_LIVE_ACTION = 'consume-messages'
$env:TABLEPRO_LIVE_ARGS = '{"topic":"liveprobe","partition":0,"offset":-2,"max":10,"timeoutMs":5000}'
go run ./liveprobe kafka localhost:9092
```

`TABLEPRO_LIVE_HOST` / `TABLEPRO_LIVE_PORT` 可覆盖夹具推导出的 host/port；
`TABLEPRO_LIVE_OBJECT` / `TABLEPRO_LIVE_ACTION` / `TABLEPRO_LIVE_ARGS` 控制 browse 的
对象名与要运行的动作。夹具打印每一帧的 err/code/result（长结果截断），便于逐帧判断
真实服务的响应形状是否与插件假设一致。

### 已联调通过

| 引擎 | 真实实例 | 结果 |
|---|---|---|
| **Nacos** | `nacos/nacos-server:v2.3.2` standalone，5 个真实配置 | ✅ 全链路通过：`tables` 列出 5 个 `group/dataId`；`browse` 读回真实配置内容；`query-config` 动作成功；`publish-config` 写入专用 dataId、读回 `liveprobe: ok`、随即删除（未触碰任何已有配置） |
| **Milvus** | `milvusdb/milvus:v2.5.4` standalone（内嵌 etcd/local 存储），真实集合 `liveprobe`（4 维向量、3 条实体、L2 索引） | ✅ 全链路通过：`tables` 列出 `default/liveprobe`；`browse` 显示 `id/title/vec` 三列含 `FloatVector(4)` 维度；`vector-search` 返回按 L2 距离排序的 3 条命中（0 → 0.01 → 1.2），与直接调 REST 一致 |
| **Kafka** | `apache/kafka:3.9.0` KRaft 单节点，真实主题 `liveprobe`（1 分区、3 条消息） | ✅ 全链路通过：`tables` 列出 `liveprobe`；`browse` 与 `consume-messages` 都返回 3 条真实消息（offset 0/1/2 + 时间戳）；`produce-message` 写入 `plugin-produced` 并用官方 console-consumer 回读确认 |

### 联调抓到并修掉的真实缺陷（httptest 假服务全部掩盖）

1. **Nacos `query-config` 的 `type` 列永远为空**：值取自 `args["type"]`，而该动作的参数
   根本没有 `type`；且 v1 GET 只返回正文、不含元数据，真实列表里 `type` 多为 `null`。
   去掉该列，与 `browse` 一致（group/dataId/content）。
2. **Milvus `describe` 的真实形状与文档不同**：真实 v2.5.4 把 `fields` 放在 `data` 顶层，
   字段是 `name/type/primaryKey/nullable`，`params` 是 `[{key,value}]` 数组且 `dim` 是
   **字符串**；假服务用的 `data.schema.fields` + `is_primary_key` + `params:{dim:4}`
   真实服务器从不返回。后果是 `browse` 曾返回**空列 + 空行**。现在 `UnmarshalJSON`
   同时接受两种拼写。
3. **Milvus `search` 的 `data` 是扁平对象数组**：真实响应是 `[{distance,id,...}]`，不是
   假服务写的 `[[{...}]]`；旧代码直接 `cannot unmarshal object into .0`。现在先解码为
   `[]json.RawMessage` 再逐条判断，两种形状都吃。
4. **Milvus 集合状态机**：create → insert 之后集合**默认未 load**，query/search 报
   `collection not loaded`；load 又要求先建向量索引（否则 `index not found`）。这是集合
   生命周期约束，不是插件 bug —— 联调中按真实顺序准备数据后全部通过，README 记录于此，
   避免把"未 load 的集合"误判成插件故障。
5. **Kafka 的连接参数形状**：`ConnectParams` 把 `Host`/`Port` **分开**传（真实客户端
   就是这样），`baseUrl` 只是 HTTP provider 的覆盖项。第一版联调夹具把
   `localhost:9092` 塞进 `baseUrl`，结果 Kafka provider 读到空 Host、拼出 `live:9092`。
   夹具已改为按真实客户端形状传 `host`+`port`；插件代码本身无需改动。
6. **Pulsar 的 `/subscription/{sub}/backlog` 端点不存在**（源码核对发现，详见
   "仍未覆盖"）：backlog 来自 `/stats` 的 `subscriptions[sub].msgBacklog`。旧代码配合
   虚构该端点的假服务会把积压静默显示成 0。已改为一次 `/stats` 填满整表。

**部署坑（非插件问题，记录给复现者）**：`apache/kafka:3.9.0` 的 KRaft 单节点若把
`PLAINTEXT` 监听器绑到容器内 `localhost`，Windows→WSL2 的端口转发能连上 TCP 但 Kafka
协议层随后 `unexpected EOF`；必须用 `PLAINTEXT://0.0.0.0:9092` 绑定 + `advertised`
用 `localhost:9092`，controller 监听器单独留在 `localhost:9093`。

**假服务已改成真实响应形状**（Nacos 的 `type:null` 与额外字段、Milvus 的真实 describe /
search 形状），所以后续回归测试钉住的是**真实协议**，不是文档想象。Kafka 的映射逻辑
本就通过注入接口测试，真实 broker 另由上述联调覆盖。

### 仍未覆盖 / 部分覆盖

- **Pulsar 真实实例未起**：可达镜像源（`docker.1panel.live`）上
  `apachepulsar/pulsar:2.11.4` / `3.0.12` 的 manifest 均存在，但大层传输持续超时或
  连接重置（多次重试累计超过 1 小时、含 10 分钟上限的最终重试，均以
  `connection timed out` 结束）；`docker.m.daocloud.io` 返回 401，Docker Hub 直连
  不可达。**没有真实 Pulsar broker 的帧级联调**。

  **但做了源码级契约核对**：从 Apache Pulsar `branch-3.0` 官方源码逐一确认了插件用到的
  admin v2 路径——
  `GET /admin/v2/tenants`（`Tenants.java`）、
  `GET /admin/v2/namespaces/{tenant}`（`Namespaces.java`）、
  `GET /admin/v2/persistent/{tenant}/{namespace}` 列主题、
  `GET …/{topic}/subscriptions`、`GET …/{topic}/stats`（`PersistentTopics.java`）。
  核对抓到一个**真实契约缺陷**：插件原来调用的
  `GET …/subscription/{sub}/backlog` 端点**在官方源码中不存在**；backlog 实际来自
  `/stats` 响应的 `subscriptions[sub].msgBacklog`。旧代码（配合一个同样虚构该端点的
  httptest 假服务）会把每个订阅的积压静默显示成 0。已改为**一次 `/stats` 填满整表**，
  假服务改用官方真实结构，并加回归断言确保永不调用那个虚构路径。

  结论：Pulsar 的路径契约已由官方源码钉住，但**运行时行为（认证、重定向、stats 的
  实际字段细节）仍未经真实服务器验证**，这是四个引擎里唯一未做实例联调的一个。
- `distance=0` 的最近命中在协议里编码为 `{"k":"float"}`（`Flt` 的 `omitempty` 省略零值），
  内核解码为 0，语义正确但文本上不显式；这是内核既有编码约定，未改动。
- 控制台的 SQL 面是**如实拒绝**：`Execute` 返回"此引擎不支持 SQL"，而不是假装成功返回
  空结果——后者用户无法与"真的没有行"区分。
- server 模式（`--serve`）的只读白名单**只放行 `/api/plugin/actions`**，不运行动作：宿主
  无法判断一个动作是只读检索还是写配置，往宽松方向猜正是只读默认要防的事。

## 加一个新引擎

写一个文件实现 `Provider` + `Session`（见 `provider.go` 的两个接口），在 `init()` 里
`registerProvider(...)`，用 `;新名字` 注册即可。客户端不需要任何改动。