# rpop 控制面 / 数据面分离架构(SDN 模式)

本文记录已确认的架构决策。原则:**性能优先**——SSE(含 LLM 流式输出)与 WebSocket 的时延与吞吐不得因分离、多跳、追踪或日志回传而下降。

## 1. 角色与拓扑

同一个 `rpop` 二进制,三种运行模式:

| 模式 | 职责 |
|---|---|
| `all-in-one`(默认) | 控制器 + 内嵌节点 `local`,行为与单机版一致 |
| `controller` | 北向 API/控制台、SQLite 真源、快照渲染、节点注册、日志汇聚 |
| `node` | 转发引擎 + 南向 agent + 中继端口,无管理 API |

- **控制面**:唯一配置真源(SQLite)与唯一管理入口;按节点渲染快照(SotW 全量 + 单调 revision)下发。
- **数据面**:节点只接收快照并转发;控制器离线时使用磁盘缓存的 last-known-good 快照继续服务(fail-standalone)。配置单向流动,节点从不回写配置。

## 2. 决策清单

| # | 决策 |
|---|---|
| D1 | 单二进制三角色,转发/路由/校验代码共享(路由模拟器与线上同一份代码) |
| D2 | 南向协议:节点主动外连控制器的 HTTP/2 over mTLS。`watch` 为流式响应(每行一个 JSON 快照帧),另有 `status`(心跳 + 应用结果)与 `logs`(日志段上传)。D23 之后不再需要反向命令,因此不引入 WebSocket 依赖 |
| D3 | 全量快照 + 全局单调 revision,影响数据面的变更防抖(~200ms)后推送 |
| D4/D10 | 内部 CA(ECDSA P-256)。一次性 join token(含 CA 指纹,首次连接即可校验控制器证书)换取节点证书;南向与节点间链路均为 mTLS。节点证书 DNS SAN 为 `<nodeID>.nodes.rpop` |
| D26 | 节点证书吊销用"注册代数":证书 Subject.SerialNumber 记录签发时的代数,只有当前代数的证书可认证;重新注册代数 +1 并吊销旧证书,续期沿用当前代数,因此续期响应丢失也不会把节点锁在外面。精确到每次注册,不依赖证书时间的秒级精度;后续可随快照下发给中继对端做吊销 |
| D5 | 站点放置 `placement`:节点 ID 列表;为空表示内嵌节点 `local`(兼容既有配置) |
| D6 | 快照内联证书/私钥(仅经 mTLS 传输);节点缓存文件权限 0600 |
| D8 | 节点应用失败(如端口占用)时该站点保留旧配置继续服务,错误随状态回报(NACK) |
| D9 | 节点间链路:常驻 HTTP/2 over mTLS,每个隧道是一个 CONNECT 流(多路复用 + 流级流控),零新增依赖 |
| D11/D17 | 中继 ACL:控制器按全部候选路径渲染"精确路径后缀 + 目标"白名单;路径禁止重复节点;隧道携带递减 TTL |
| D12 | 中继节点不承载站点监听,但开放一个节点间 mTLS 端口 |
| D13/D18 | upstream `paths`:按优先级排列的候选路径,每条 `via` 为节点与具名代理的混合序列;`via` 是单路径简写 |
| D14 | 具名外部代理注册表(SOCKS5/SOCKS5H/HTTP(S) CONNECT),凭据只写不读,被引用不可删除 |
| D15 | 链路以(对端节点, 代理链指纹)为键,分别常驻、分别计量 |
| D16 | 链尾代理(出口代理)由出口节点握手;两节点之间的代理为链路级代理,只在建链时握手 |
| D18 | 降级粒度为连接建立:入口节点按优先级拨号,建连失败(尚未发送任何请求字节)时安全切换下一条路径;请求中途断开不透明重试 |
| D19 | 健康:被动熔断(指数冷却)+ 链路层信号 + 可选主动探测 |
| D20 | 每条候选路径独立的 `http.Transport` 连接池:回切后旧路径的连接不会被复用,在途请求在旧连接上自然完成 |
| D21 | 所有候选路径引用的链路都常驻预热;ACL 覆盖全部候选路径,控制器离线时降级/回切完全在入口节点自治 |
| D22 | 追踪:入口节点生成 `Rpop-Track-Id`(UUIDv7)注入上游请求;隧道层 `tunnelID` 由各跳记录到达/建立/结束事件;控制器按 trace → tunnel 关联出全路径时间线。中继只见密文,因此中继侧是连接粒度 |
| D23 | 所有节点日志先写本地 spool(`<log-dir>/spool/`),再异步回传控制器写入适配器;节点不持有日志适配器凭据 |
| D24 | 回传幂等:段号单调递增、按序上传;控制器先写适配器、后持久化节点高水位(HWM)再 ACK;≤HWM 的重传直接 ACK |
| D25 | spool 配额(默认 2GiB):控制器长期不可达时丢弃最旧段并计数告警;回传限速,转发业务永远优先 |
| D27 | 南向/overlay 协议版本治理:`Rpop-Protocol-Version` 请求/响应头双向携带;控制器按 `[MinSupportedProtocolVersion, ProtocolVersion]` 接受南向请求,超出上限拒绝并提示先升级控制器,低于下限拒绝并提示升级节点,区间内落后则放行 + 告警;overlay CONNECT 握手版本不匹配永不拒绝隧道,只记警告;只有破坏性变更才提升版本号 |
| D28 | 节点时钟偏差:南向 `status` 心跳按 NTP 公式(节点发送时间/控制器接收与响应时间/节点收到响应时间)由节点估算偏差并随下一次心跳上报,控制器只存最近值(不落库),超阈值告警;追踪时间线按需展示偏差徽标与可选的前端校正,不悄悄改写原始时间戳 |
| D29 | 日志/隧道记录去重:access log 复用既有的 `trackId`、隧道事件复用 `tunnelId+nodeId+stage` 作为去重键;ClickHouse 换 `ReplacingMergeTree`+`dedup_key` 列(仅新建分表生效),Elasticsearch 用同一去重键做 `_id`,文件/S3 在查询聚合层去重,隧道事件存储用内存 LRU 短路重复写入 |
| D30 | D19 可选主动探测收口:仅在路径处于冷却态且到期的瞬间探测一次,复用既有建连代码只探测到连接建立、不发请求;按上游可选覆盖降级参数(建连预算、冷却上下限、探测开关),留空沿用全局默认 |
| D31 | 阶段 5 遗留的窗口/并发/容量常量分类收口:南向与 overlay 的 HTTP/2 窗口与并发上限、日志 ingest 限流与隧道事件存储容量/保留期开放为 CLI flag/环境变量;与协议安全边界绑定的常量(段/行大小上限、跳号阈值)与纯查询性能优化常量(查询窗口天数)明确不开放 |

## 3. 性能设计(硬性约束)

**P1 流式透明。** 任何一跳都不缓冲响应:`text/event-stream` 与未知长度响应(NDJSON、chunked)立即刷新;所有 `ResponseWriter` 包装器必须实现 `Flush`/`Hijack`/`Unwrap`;站点服务器不设 `WriteTimeout`;隧道流没有空闲超时(长连接 SSE/WebSocket 由链路 PING 保活判活)。

**P2 WebSocket。** 经 `ReverseProxy` 的 HTTP/1.1 Upgrade(hijack)转发;在 overlay 中升级后的连接只是同一隧道里的字节流,无额外处理。服务端未启用 RFC 8441 extended CONNECT,浏览器对 WebSocket 始终使用 HTTP/1.1。

**P3 站点 HTTPS 监听启用 HTTP/2。** 解除浏览器对 HTTP/1.1 同源 6 连接的限制,多个并发 SSE(EventSource)不再互相阻塞。

**P4 逐跳低时延。** 客户端侧 HTTP/2 每个 DATA 帧即刷新;中继服务端每个数据块写出后立即 Flush;TCP_NODELAY;链路套接字设置 `TCP_NOTSENT_LOWAT`,使交互式小帧(SSE token)不排在大流量下载之后的内核发送缓冲里;每条逻辑链路可配多条并行连接,按在途流数最少分配。

**P5 流控窗口按 BDP 放大。** 中继服务端默认流窗口 16MiB、连接窗口 64MiB(可配),长 RTT 跨境链路不被默认 1MiB 窗口限速。

**P6 热路径零握手。** 节点间 mTLS 链路常驻预热;入口节点到源站的连接按路径池化复用。稳态下每请求新增成本 = 绕行 RTT 之和 + 每跳一次用户态拷贝。

**P7 中继最小化。** 池化 32KiB 缓冲双向拷贝;中继不解析 L7、不产生逐请求日志;仅当站点开启访问日志时才记录隧道事件。

**P8 日志与追踪不阻塞转发。** Trace ID 用非加密随机源生成(无系统调用);日志经有界队列异步写 spool,队列满即丢弃计数而非阻塞;回传限速。

## 4. 实施阶段

1. 引擎抽取:`internal/dataplane`(监听、路由、代理、观测),行为零变化。
2. 快照与修订:`internal/snapshot` 渲染器、revision、`nodes` 表、`placement`,引擎按站点指纹差量应用。
3. 南向通道与 node 模式:内部 CA/mTLS、join token、磁盘缓存、节点管理 API。
4. Overlay:具名代理、代理链拨号、链路管理、中继/出口、`paths` 降级与回切、ACL。
5. 日志与追踪:`Rpop-Track-Id`、spool、幂等回传、控制器 ingest、配额保护。
6. 控制台:节点/拓扑/链路健康、paths 编辑、模拟器全路径、追踪时间线、spool 健康。
7. 长尾:窗口调优项、协议版本治理、ClickHouse 去重、时钟偏差标注。

## 5. 实施记录

**阶段 4(Overlay)** 的落地要点:

- 路由表按"路径后缀"聚合:`key = hash(首个节点起的 hop 序列 + 目标)`,与入口无关;同一后缀被多个入口/站点共用时只渲染一条中继路由,`From` 为所有允许的上一跳。中继只按 key 查表转发,请求无法把中继引向控制器未配置的地方,也无需 TTL。
- 控制器从 `relayAddress` 推导 `relayListen`(`:<port>`),节点可用 `-relay-listen` 覆盖(端口映射场景)。
- 内嵌节点 `local` 可作为入口:控制器用内部 CA 为自己签发固定代数(1)的证书,中继把 `local` 视作对端;`local` 不能作为中继(校验拒绝)。
- 节点同时应用同一 revision 时,上游中继常在下一跳的中继端口就绪前拨号,因此链路首次退避仅 200ms(指数增长至 30s),配置下发后链路在数百毫秒内就绪。
- 降级参数暂为常量:单路径建连预算 10s、路径冷却 1s→1min 指数退避;已知 down 的链路立即失败(不等待后台重拨)。
- 中继转发下一跳的拨号与承载它的入站 CONNECT 流解耦(`context.WithoutCancel`):同一链路的拨号由持锁的 leader 串行执行,一条隧道结束不应该让其他正在等待同一条链路的隧道跟着失败;`link.dial` 同时不再把"调用方 ctx 被取消"计为链路失败,只有真正的拨号超时才计入退避。
- `Overlay` 关闭后置位内部 `closed` 标记:节点身份被吊销重新注册会整体替换 overlay,旧实例的 `Apply`/`DialPath` 之后一律返回 `ErrClosed`,不会再建立无人回收的链路或中继端口。节点不再承担中继时,`Apply` 会主动 drain 并释放已有的中继监听,而不是让端口和 accept 协程常驻。`Close` 会等到每条链路的后台重拨协程真正退出后才返回。
- 出站链路(`newLink`/`PeerClientConfig`)在每次握手时都会校验对端证书的注册代数是否等于当前快照里的 `Peer.Generation`,与中继入站方向的校验(`authorizedPeer`)对称;代数写入 `linkKey`,对端代数变化时旧链路被立即退休、下一次拨号强制走一次新的握手校验,而不是继续信任变更前就已建立的连接。
- 具名代理中 `"http"` 类型以明文发送 `Proxy-Authorization`,这是该代理类型本身的固有属性(CONNECT 请求在 TLS 建立前发出);跨公网使用时应选 `"https"` 类型,`"http"` 仅适合链路已受其他方式保护的场景(如同机房内网)。
- D19 的可选主动探测与按上游配置降级参数已在阶段 7 第 2 步实现(见 §5 末尾);链路/路径健康的控制台展示见阶段 6。

**阶段 5(日志与追踪)第 1 步** 定下以下契约,第 2 步(spool/回传)与第 3 步(控制器 ingest/配额/查询)据此实施:

- **日志模型**:访问日志沿用 `accesslog.Record`,新增 `trackId`、`tunnelId` 两个可选字段(空表示不适用)。隧道事件是新的独立记录 `overlay.TunnelEvent`:`timestamp`、`tunnelId`、`nodeId`、`role`(`entry`/`relay`/`exit`)、`stage`(`arrived`/`established`/`ended`)、`peer`、`bytesIn`/`bytesOut`(尽力而为,`ended` 事件采样时不等待另一方向收尾)、`duration`(到上一阶段的耗时)、`error`。两者不合并成一张表:访问日志按站点选择的适配器落盘;隧道事件是连接粒度、无站点概念(同一中继路由可能被多个站点共用),独立成事件流。第 2 步落盘 spool 时,用一个 `kind: "access"|"tunnel"` 的包装信封承载两者,写入同一 NDJSON 段文件,保持单一上传通道。
- **Track ID**:入口节点(站点监听所在节点)在 `dataplane.observeSite` 为每个请求生成 `Rpop-Track-Id`(UUIDv7,`internal/traceid.New()`,非加密随机源、无系统调用),无条件覆盖客户端携带的同名请求头再转发给上游——不信任、不透传客户端值,防止伪造轨迹或跨请求关联注入;只在 access log 中记下 `trackId`。
- **Tunnel ID**:仅当发起隧道的站点开启了访问日志(`AccessLog.AdapterID != ""`)才生成,由 `overlay.Overlay.tunnel()` 在打开 CONNECT 流时随 `Rpop-Tunnel-Id`/`Rpop-Tunnel-Log` 请求头下发。中继/出口(`serveRelay`,按 `route.Next==""` 区分二者)只信任这两个头,不查自己的静态路由表——同一路由 `Key` 可能被多个站点共用,能否记事件是逐次隧道决定的,不是路由的静态属性。请求头只在节点间 mTLS 链路上传递,不经过任何未认证的公网入口,不存在客户端伪造问题。数据面通过 `httptrace.ClientTrace.GotConn` 从 `net.Conn`(必要时先 `*tls.Conn.NetConn()` 解一层端到端 TLS)取回隧道实现的 `TunnelID() string`,写入 access log 的 `tunnelId`,从而把一次请求和它经过的隧道关联起来;一个隧道连接可承载多个请求(连接池化,P6),因此隧道事件本身不带 `trackId`。
- **有界异步队列**:`dataplane.logQueue`(已存在,访问日志)和新增的 `overlay` 隧道事件队列结构一致——channel 容量固定、`atomic` 计数已排队字节/丢弃数,满了直接丢弃并计数,从不阻塞转发(P8)。两者都以"写入接口"解耦落盘位置:`dataplane.AccessLogWriter` / `overlay.TunnelEventSink`,通过 `Engine.SetAccessLogWriter` / `Overlay.SetTunnelEventSink` 注入,不设置时退化为写本地 zap 日志。第 2 步新增 `internal/spool` 包,提供同时实现这两个接口的 writer,序列化为 spool 记录后落盘;不需要改动第 1 步的调用点。
- **spool 段文件**(第 2 步实施):目录 `<log-dir>/spool/`,文件名 `%020d.jsonl.gz`(段号,零填充,单调递增,一个 gzip NDJSON 文件一段);另有 `state.json` 记录 `nextSegment` 与本地已确认的 `ackedUpTo`,重启后据此续传。封段条件:当前段达到 8MiB 或已打开超过 30s(先到者),没有事件时不建段、不上传。每行是 `{"kind":"access"|"tunnel","record":...}` 信封。
- **南向 `logs` 协议**(第 2 步实施):节点按段号严格递增、逐段等待上一段 ACK 后再发下一段(简单的停等流控,足够,因为回传本就要限速,不追求管道化)。`POST /southbound/v1/logs`,请求头 `Rpop-Log-Segment: <uint64>`、`Content-Encoding: gzip`,body 为该段的 NDJSON;响应 `{"ack": <uint64>}`。控制器收到一段后:①若段号 ≤ 该节点已持久化的高水位(HWM),直接回 ACK(幂等重传,不重复写入);②否则按顺序把 `kind:"access"` 记录写入该站点选择的适配器、把 `kind:"tunnel"` 记录写入隧道事件存储;③写入成功后把 HWM 更新为该段号并持久化;④最后才 ACK。HWM 存在控制器 SQLite 的 `nodes` 表新增列 `log_hwm`(第 2 步加迁移),而不是内存,保证控制器重启后不重复写入也不遗漏。
- **配额与限速**(第 2 步实施,默认值先在此定下):spool 配额默认 2GiB(`RPOP_LOG_SPOOL_QUOTA_BYTES` 可配),超过时丢弃最旧的未上传段并计数告警(不是丢新段,新事件更有时效性);回传默认限速 4MiB/s(`RPOP_LOG_UPLOAD_RATE_BYTES` 可配),用令牌桶节流上传的读取,转发路径的带宽永远优先。
- **全路径时间线查询**(第 3 步实施,接口先在此定形):控制器新增 `GET /api/logging/trace/{trackId}` 返回该请求的 access log 记录(唯一一条)及其 `tunnelId`;再用 `GET /api/logging/tunnels/{tunnelId}` 返回该隧道 ID 在所有节点上报的全部 `TunnelEvent`,按 `timestamp` 排序即为入口→中继…→出口的完整时间线。查询直接扫已入库的隧道事件存储(第 2 步选型,复用 access log 适配器的按时间分区能力);控制台 UI 是阶段 6 的事。

**阶段 5(日志与追踪)第 2 步(spool/回传)** 的落地要点,补充或收窄第 1 步定下的契约:

- **协议里 `LogEnvelope` 多了一个字段**:`internal/southbound.LogEnvelope` 在 `kind`/`record` 之外加了 `adapterId`(`kind:"access"` 时才有值)。原因:`dataplane.AccessLogWriter.WriteAccessLog(ctx, adapterID, record)` 本身就带 adapterID(站点选择的适配器,控制器渲染快照时下发,节点不解析含义),spool 落盘时把它随记录一起保存,控制器 ingest(第 3 步)就能直接 `registry.Write(ctx, envelope.AdapterID, record)`,与内嵌节点 `local` 现有的 `registryWriter`(`internal/control/runtime.go`)写法一致;也避免控制器按 `record.SiteID` 反查"站点当前选择的适配器"与节点观测记录时的选择不一致(回传有延迟、期间适配器配置可能变更)。`kind:"tunnel"` 不带这个字段。
- **段大小按未压缩 NDJSON 字节计,写完一行才检查**:`internal/spool.Spool.writeRecord` 每写一行 gzip 数据就检查累计的未压缩字节是否达到 `MaxSegmentBytes`(默认 8MiB),达到才封段——先写后查,所以一条超大记录(例如某站点把 body 采集上限配到 8MiB,访问日志请求体+响应体都命中上限,base64 后单条记录可到 ~22MiB)仍会完整地独占一段,不会被从中间截断。`southbound.MaxLogSegmentBytes`(32MiB)据此定值,同时覆盖控制器要读取的压缩后字节数——gzip 对这类已经偏随机的 base64 内容基本不会显著膨胀。这个检查逐行进行,不受下面的批量持久化影响:一批里某一行刚好把某个段写满,该段立即封段(封段本身就会 `gzip.Writer.Close()` + `file.Sync()`,不依赖批次边界),批次里剩下的行接着写入新段。
- **持久化按批次做,不再是每行一次 `gzip.Writer.Flush()` + `file.Sync()`**:早期实现是每写一行就 Flush+fsync,换来"崩溃最多丢一行",但代价是吞吐量被 fsync 速率封顶——机械盘/云盘每秒只有几百到几千次 fsync,节点 QPS 一高,有界入队队列(256 长)很快写满并大量丢弃(违背 P8 让日志丢弃、不让转发变慢的初衷,但丢弃的规模不该只由磁盘的 fsync 上限决定),而且每行一次 Flush 会明显拉低压缩率(gzip 在小块边界上重复重置滑动窗口)。现在写协程(`Spool.drainBatch`)每次把队列里已经到达的记录连续写入 gzip 流(只调用 `gzip.Writer.Write`,不 Flush、不 fsync),累计到 `maxBatchRecords`(4096 条)或 `maxBatchBytes`(`DefaultMaxSegmentBytes` 的一半)才做一次 Flush+fsync(`segmentWriter.persist`);另设 `MaxPersistInterval`(默认 1s,可配置,判断用 spool 现有的可注入时钟)作为兜底——低流量时一批可能长期攒不满,靠这个定时器保证缓冲的记录也能按时落盘,不会无限期停留在 gzip 的内存缓冲里。取舍是:**崩溃最多丢失一个批次(高吞吐时)或一个持久化间隔内的记录(低吞吐时)**,不再是"最多丢一行"——spool 自己的入队队列已经在事件产生和落盘之间解耦,这部分损失不影响 P8 的转发路径,而换来的是吞吐量不再被 fsync 次数直接封顶,压缩率也因为更大的 Flush 粒度而提升。真到吞吐瓶颈时,表现仍然是 spool 自身入队队列被写满并丢弃计数(`LogStats.AccessLogQueueDropped`/`TunnelEventQueueDropped`),这仍是设计上允许的降级路径。崩溃恢复逻辑(`recoverSegment`/`completeLines`)完全不用改:它只按"能从磁盘上的 gzip 字节解出多少个完整的 NDJSON 行"来判断保留范围,不关心这些字节是每行一次 Flush 攒出来的还是按批次一次 Flush 攒出来的——一个从未被 Flush 过的批次根本不会出现在磁盘上(还在 gzip/flate 的内存缓冲里),所以批次边界处的截断表现为"整段只包含已持久化的完整批次,未持久化的那一批完全不存在",不是"半条记录"。
- **段号与状态持久化的顺序**:`state.json` 的 `nextSegment` 在"决定要用这个号"时立刻落盘,永远早于对应的段文件被创建;因此重启后只有唯一一个"存疑"的段号(`nextSegment-1`),其他号码或者已经完整封段、或者已经被确认可以删除。崩溃恢复只需要检查这一个号码:能完整解出 gzip 尾部就原样保留;尾部截断则保留能完整解出的整行,重新封装成一个有效 gzip 文件(丢弃最后半条不可信的行);一行都解不出来就整段删除。三种情况都不会让这个号码被复用给不同内容——即使文件本身被丢弃,下一次开新段也从更大的号码开始,用 warn 日志计数这次损失(没有在 `LogStats` 里单独计数,只在节点本地日志可见,因为协议已经提交锁定,不再加字段)。
- **配额按"段已经关闭、落在磁盘上的字节数"算**,在每次封段后检查:超出 `QuotaBytes`(默认 2GiB)就按段号从小到大(最旧的未确认段)依次删除,直到回到配额以内(极端情况下,单个新段本身就超过配额,也会被自己删掉);告警日志按 10s 节流,避免持续超额刷屏。当前正在写的段不计入配额判断(还没关闭,不在候选范围)。
- **段号与控制器 HWM 对齐**:`Spool.Ack(ack)` 是唯一改 `ackedUpTo`/`nextSegment` 的地方,规则很简单——`ackedUpTo`只增不减(取 `max`),`nextSegment` 只在 `ack+1` 更大时才跳到 `ack+1`(不会把号码往回调)。上传器(`internal/spool.Uploader`)每次成功拿到 2xx 响应后,不是直接用响应里的 `ack` 字段,而是 `max(本次发送的段号, 响应的 ack)` 去调用 `Ack`——因为"控制器对这次上传返回 2xx"这件事本身就确认了这个段号,不需要依赖控制器回显的数值是否≥这个段号(正常实现下应该总是满足,但没必要依赖这一点)。`nextSegment` 跳到 `ack+1` 之后,下一个新建的段自然不会再落在 HWM 之下,不会重复触发"节点重装、spool 从 1 起号,但控制器记得旧节点更高 HWM"这个场景——但那一刻"卡在旧号码里的内容"本身还是没写进控制器(协议只有数值 HWM,没有内容级去重,控制器按契约把 ≤HWM 的段当重传直接 ACK、不重复写入),这是数值型 HWM 方案本身的已知代价,只保证不会一直重复丢,不保证这一次不丢。
- **上传器**(`internal/spool.Uploader`):严格按段号升序停等,一段成功(或识别为"已经在 `AckedUpTo` 之下,本地直接跳过、不发网络请求")才发下一段;失败按 1s→30s 指数退避重试同一段(不跳段);限速用标准库时间自实现的令牌桶(`internal/spool/ratelimit.go`),没有引入 `golang.org/x/time`;`ctx` 取消后无论是在空闲等待、退避睡眠还是进行中的 HTTP 请求,都会很快返回。复用节点现有的南向 mTLS `*http.Client`(`agent.Config` 传入一个返回当前 client 的函数,因为证书续期/重新注册会换 client)。
- **新增节点配置**:`-log-spool-quota-bytes`(`RPOP_LOG_SPOOL_QUOTA_BYTES`,默认 `spool.DefaultQuotaBytes`=2GiB)、`-log-upload-rate-bytes`(`RPOP_LOG_UPLOAD_RATE_BYTES`,默认 `spool.DefaultUploadRateBytesPerSecond`=4MiB/s);`-log-dir`(已有)复用为 spool 的父目录,实际落盘在 `<log-dir>/spool/`。`agent.Config.LogDir` 为空时退化为 `<DataDir>/logs`,只是为了不要求既有测试/调用方都显式设置。
- **接线范围**:仅 `node` 模式的 `agent.Agent` 接入 `internal/spool`(`Engine.SetAccessLogWriter`、每次 `use()` 换新 `overlay` 时都重新 `SetTunnelEventSink`,覆盖重新注册场景);内嵌节点 `local`(all-in-one/controller 进程内)继续走第 1 步之前就有的 `registryWriter` 直接落盘,不经过 spool。

**阶段 5(日志与追踪)第 3 步(控制器 ingest/查询)** 的落地要点:

- **`nodes.log_hwm` 迁移**:沿用 `sites.auto_start` 那套"启动时探测列是否存在、缺了就 `ALTER TABLE ADD COLUMN`"的机制(提炼成公共的 `addColumnIfMissing`),旧库升级后该列默认为 0。`store.Node` 增加 `LogHWM uint64` 字段,由 `GetNode`/`ListNodes` 一并读出,但 `SaveNode` 的 `ON CONFLICT DO UPDATE` 显式不覆盖这一列——注册、续期、改名等经 `SaveNode` 的路径都是"读出当前 Node、改几个字段、存回去",若把 `log_hwm` 也纳入 upsert 的 SET 列表,只要有调用方基于稍旧的 Node 存回,就会把并发写入的 HWM 冲掉。写这一列只经过唯一一个函数——`Store.UpdateNodeLogHWM(ctx, id, hwm)`,只 `UPDATE ... SET log_hwm=? WHERE id=?`,不经过 `opMu`,不会与站点/节点配置的编辑互相阻塞或覆盖;下一条要点说明它现在有两个调用方,靠 `internal/control` 里同一把按节点 ID 取的锁而不是这个函数本身来互相串行化。
- **注册重置 `log_hwm`**:`southboundRegister` 签发新证书代数(`node.CertGeneration++`)并 `SaveNode` 落盘后,持有该节点在 `logIngestLocks`(`southboundLogs` 用来串行化同节点并发上传的同一把 `keyedMutex`)上的锁,调用 `UpdateNodeLogHWM(..., 0)` 把 HWM 归零,再释放锁;`southboundRenew` 保持代数不变,不动这一列。动机:节点重装(丢失数据目录、重新加入)后其 spool 从段号 1 重新计起,若控制器仍保留旧代数遗留的高 HWM,新 spool 的第一段会被"段号 ≤ HWM"直接判定为重传并 ACK 掉,内容悄悄丢失。取锁再重置是为了让重置与"正在进行的、已经通过旧代数鉴权的上传"对 HWM 的读改写互斥,不会出现二者交叉执行导致的错误覆盖(谁后写谁赢,而不是"重置"确定性地生效)。取舍:旧代数的证书作废后,新的上传请求不可能再用它通过 `authenticateNode`,不会与重置产生持续冲突;唯一代价是一个罕见的重复写——某段已经写入并持久化过 HWM,但其 ACK 在网络上丢失,节点又恰好在重试前被重新注册,该段会在新代数下再传一次、再写一次(记录级去重是阶段 7 的事,`southboundLogs` 的"段级幂等、行级尽力而为"限制里已经写明了同类取舍;**已在阶段 7 第 4 步解决**,见下文"阶段 7 第 4 步实施记录":access log 按 `trackId`、隧道事件按 `tunnelId+nodeId+stage` 去重,覆盖这里描述的重复写场景)。
- **`POST /southbound/v1/logs` 处理器**(`internal/control/logs_ingest.go`):鉴权与代数校验复用 `authenticateNode`(与 `status`/`watch` 相同);同一节点的并发上传经 `keyedMutex`(按节点 ID 取锁)串行化,避免两个并发请求交叉读改 HWM;锁内重新 `GetNode` 读一次 HWM(而不是复用 `authenticateNode` 返回的旧值),因为等锁期间另一个请求可能已经推进过。段号 ≤ HWM 直接 ACK 当前 HWM(即便这不是当年真正推进过 HWM 的那个段号——见 `southbound.LogAck` 的语义:节点重装 spool 后可能需要跳过一大段历史);段号 > HWM+1 视为合法跳号(D25 配额丢段),记一条 Info 日志,照常接收。写入顺序严格是"逐行写入目标 → 全部成功后持久化 HWM(`UpdateNodeLogHWM`)→ 最后 ACK";任何一行的**写入**失败(区别于"这一行本身有问题"的丢弃)都直接返回非 2xx、不推进 HWM,让节点整段重传——这意味着重传可能把这一段里已经成功写入的记录再写一次,是有意接受的"段级幂等、行级尽力而为"的限制,已在代码注释中记录。
- **安全校验与降级**:①body 压缩前用 `southbound.MaxLogSegmentBytes`(32MiB)经 `http.MaxBytesReader` 限制;②解压后再套一层 `io.LimitReader` 限到 `maxDecompressedLogSegmentBytes`(64MiB),防止一个体积不大但高度可压缩的段解压成解压炸弹——32MiB 的界之上留一倍余量,覆盖"单条记录本身就接近 32MiB"的极端场景(见 `MaxLogSegmentBytes` 的文档注释),超出直接判定整段失败;③单行也设上限 `maxLogRecordLineBytes`(=32MiB,与协议里"单条记录的理论上限"对齐),超长行按坏行丢弃计数而不是撑爆内存或拖垮整段;④`kind:"tunnel"` 的 `TunnelEvent.NodeID` 一律用 mTLS 认证得到的节点 ID **覆盖**,不采信 payload 里的值——节点不可能替别的跳(hop)代报事件,覆盖比校验更彻底也更简单;⑤`kind:"access"` 的 `AdapterID` 通过 `nodeAdapterSet(nodeID)` 校验:先取当前全部站点配置,凡是"放置在这个节点上、且开着访问日志"的站点,把它选中的适配器 ID 收进一个集合,记录的 `AdapterID` 必须在这个集合里(且适配器本身存在)才接受;不通过则丢弃计数、记一条汇总 Warn(整段结束后按坏行/坏适配器行的计数各出一条日志,而不是逐行打日志),不阻塞整段。这里刻意没有进一步校验记录里的 `siteId` 是否恰好等于当时选中该适配器的那个站点——`AdapterID` 本来就是"节点观测记录那一刻站点选的适配器"(见 `southbound.LogEnvelope` 文档),回传路上站点的适配器选择、甚至站点放置都可能已经变化,只要这个节点当下确实有站点在用这个适配器,就认为记录大概率仍然合法,优先不阻塞回传而不是精确到站点级别。
- **隧道事件存储选型**:没有复用 `accesslog.Registry` 的适配器体系,而是在 `internal/control/tunnel_events.go` 里实现了一个独立的小型 `tunnelEventStore`:按 UTC 天分区、每天一个未压缩的 NDJSON 文件(`tunnel-events/events-YYYYMMDD.jsonl`)。`Query` 默认按调用方给出的 `start` 收窄扫描到 `tunnelQueryWindowDays`(2)个相邻日分区,拿不到 `start`(零值)时才线性扫描全部分区文件,按 `tunnelId` 过滤、按 `timestamp` 排序(收窄逻辑见下方"查询范围收窄"要点)。不接入 `accesslog` 适配器体系的原因是隧道事件本来就没有"站点选适配器"这个概念(P7:一条中继路由可能被多个站点共用),没有什么可给用户配置的,硬套一个适配器接口反而要为 ClickHouse/S3/ES 各写一遍 `TunnelEvent` 的序列化,收益不成比例;真正复用的是 accesslog 文件适配器的"按时间分区、旧分区整体过期"这个技术模式,而不是它的接口/代码。保留期固定 14 天(`tunnelEventRetentionDays`,写入时机会性裁剪过期分区),目前是常量、未暴露配置项,记为遗留 TODO。
- **内嵌节点 `local` 直写**:`newLocalOverlay` 创建 overlay 后立刻 `SetTunnelEventSink(localTunnelEventWriter{...})`,把隧道事件接到控制器自己的 `tunnelEventStore`;`overlay.eventQueue` 已经是一条容量固定、满了丢弃计数的 channel(P8),`local` 节点复用这条既有队列即可满足"经有界队列、不阻塞转发"的要求,不需要再叠一层队列。`local` 的访问日志继续走既有的 `registryWriter` 直写,未改动。
- **查询接口能力边界**:`GET /api/logging/trace/{trackId}` 依次搜索每一个已配置的 access log 适配器(`Query.TrackID`,新增字段,file/S3 复用共享的 `matches()`、ClickHouse 用 `JSONExtractString`、Elasticsearch 用 `trackId.keyword` 的 `term` 过滤——四种适配器都已支持精确字段查询,不存在"某适配器不支持"需要单独声明的情况);由于一次请求只会落在一个适配器里,查到第一条即返回,某个适配器查询失败不当场判"未找到"而是继续查其他适配器,全部查完仍未命中且期间有过失败则报 502,彻底没有失败才报 404。`GET /api/logging/tunnels/{tunnelId}` 直接查 `tunnelEventStore`。两个路径参数都做了 UUID 形状校验(长度、分组、十六进制字符),不校验版本/变体位以免绑死 `traceid` 的具体实现。
- **查询范围收窄(UUIDv7 时间戳)**:`trackId`/`tunnelId` 都是 `internal/traceid.New()` 生成的 UUIDv7,前 48 位是生成时刻的毫秒时间戳。新增 `traceid.Time(id string) (time.Time, bool)` 解出这个时间戳,顺带校验版本位确实是 `7`——不是就返回 `false`,交由调用方决定怎么退化,而不是替一个这个生成器根本没产出过的 ID 编造一个时间戳。`GET /api/logging/tunnels/{tunnelId}` 据此把 `tunnelEventStore.Query` 的分区扫描从固定 14 天全量,收窄到 `tunnelId` 所在的 UTC 日分区再加一天(`tunnelQueryWindowDays=2`,覆盖跨午夜或稍微长寿的隧道;跑得更久的隧道仍能找到它前一两天内的事件,只有晚于这个窗口才上报的事件会漏查——这个上限比全量扫描 14 天划算得多,过窄了再调大常量即可)。`GET /api/logging/trace/{trackId}` 同理,给 `accesslog.Query` 设置 `From`/`To` 为 `trackId` 时间戳前后各 1 分钟(`traceQueryWindow`)——入口节点在给请求铸造 track ID 和给它的 access log 记录打时间戳这两步用的是同一次 `time.Now()` 读数(见 `dataplane.observeSite`/`requestRecord`),两者实际上是同一时刻,1 分钟窗口留了远超所需的余量;file/S3/ClickHouse/Elasticsearch 四种适配器的 `Query.From`/`To` 早已实现,不需要新增适配器能力。两个端点在解析失败时都退回不设窗口的全量查询,而不是 400:一个形状合法但并非这个生成器产出的 ID(理论上允许,毕竟路径参数只做 UUID 形状校验,见上一条)仍然值得尽力去查,只是查得慢,而不是直接拒绝。
- **节点日志健康**:`southbound.Status.Logs` 现经 `nodeRegistry.report` 存入内存(不落库),`nodeView` 新增 `logs` 字段暴露在既有的 `GET /api/nodes` 响应里。`report` 同时返回节点上一次上报的 `LogStats`,南向 `status` 处理器据此对比新旧配额丢段数/队列丢弃数之和,任一项比上次上报增加就各记一条 Warn——只在"数值变大"时触发,天然按状态上报周期限流,而不是每次心跳都重复告警。

**阶段 5 安全加固(控制器侧)**:第 3 步合入后,以"节点是半可信方——持有合法 mTLS 证书,但可能已被入侵;能控制自己的日志内容,但不应能让控制器进入永久故障、放大资源消耗、写入与自己无关的站点/适配器、冒充其他节点"为前提又补了一轮加固:

- **段号边界与跳号上限**:`southboundLogs` 在触碰任何存储前拒绝 `Rpop-Log-Segment` 越出 `[1, store.MaxLogHWM]`(`MaxLogHWM=math.MaxInt64`,SQLite `INTEGER` 有符号 64 位的上限)的请求,`UpdateNodeLogHWM` 同一范围再校验一次作为兜底——此前这类请求要整段解压写入之后才在持久化 HWM 时失败,该段已经落盘却永远不被 ACK,节点无限重传,是一个放大攻击面。单次请求把 HWM 推进超过 `maxLogSegmentJump`(=2^32)直接 400 拒绝且不写入;超过 `warnLogSegmentJumpThreshold`(=2^20)但仍在上限内的跳号照常接收(D25 配额丢段的正常路径),只是改记 Warn 而不是 Info,便于运维发现异常幅度的跳号。
- **站点放置校验与"上报节点"字段**:`nodePlacement` 每段解析一次节点当前的站点放置,access log 记录的 `SiteID` 必须在这个集合里才接受(容忍放置漂移,与既有的适配器校验同一取舍);`accesslog.Record.ReportedBy` 新增字段,在南向 ingest 与内嵌节点 `local` 的直写路径上都由控制器用 mTLS 认证到的节点 ID 覆盖写入,不采信记录自称的值——节点不再能把流量记到与自己无关的站点,或冒充别的节点上报。
- **TunnelID 校验(ingest 与 relay 两处)**:南向 ingest(`logs_ingest.go`,复用既有的 `traceIDPattern`)与中继(`overlay/relay.go` 新增 `traceid.Valid`,`tunnelOpenFromRequest`)都校验 `Rpop-Tunnel-Id`/上报的 `TunnelID` 是否是合法 UUID 形状;不合法的一律当作"未请求隧道事件记录"处理——中继照常按 `Rpop-Route` 转发,只是不记这条事件,ingest 按坏行丢弃计数——而不是写入 `tunnelEventStore` 任意 key 或让整段失败。伪造一个形状合法但并非真实存在的隧道 ID 无法被彻底杜绝,但每条事件仍带着控制器认证到的上报节点,伪造事件依然可追责。
- **日志 ingest 的并发与速率上限**:全局并发信号量 `logIngestSemaphore`(容量 `DefaultMaxConcurrentLogIngests`=8)限制同时处理中的段数,按节点 ID 分桶的令牌桶 `logIngestRate`(默认速率 `DefaultLogIngestRateBytesPerSecond`=16MiB/s)限制单节点的上传速度,分别防止"同时解压中的段过多"与"单节点上传过快"把 `maxDecompressedLogSegmentBytes`(64MiB)级别的内存开销放大成整机压力;超限均答 429/503 并带 `Retry-After: 1`。令牌桶的突发额度(burst,即单个节点桶的容量、也是它一开始被创建时的满桶值)不再等于稳态速率:`newNodeRateLimiter` 把它钳制到至少 `southbound.MaxLogSegmentBytes`(32MiB),与稳态速率各自独立——稳态速率仍然只约束长期平均吞吐,不影响单次请求能放行多大。这是一次修复:此前 burst 就是速率本身,默认速率 16MiB/s 小于 `MaxLogSegmentBytes`(32MiB),节点上传一个单条记录接近 32MiB 的合法超大段(见 `MaxLogSegmentBytes` 的文档注释)时,`allow()` 会永远返回 false——不是等一等就能通过,而是桶容量本身就小于请求体积,无论等多久 token 都补不到这个数——而节点的上传器严格按顺序发送、当前段不成功就不推进,诚实节点因此被永久卡死在 429 上。二者(并发数、速率)均可经 `Control.SetLogIngestLimits(maxConcurrent, rateBytesPerSecond)` 覆盖默认值,但目前只是方法,`cmd/rpop` 还未接成 CLI flag/环境变量;`SetLogIngestLimits` 传入的速率同样只影响稳态速率,burst 的下限钳制在其内部无条件生效,不会被一个过小的速率参数绕开。
- **隧道事件存储容量、句柄缓存与后台清理**:`tunnelEventStore` 新增总容量上限 `tunnelEventStoreDefaultMaxBytes`(默认 10GiB,跨全部天分区,叠加在原有 14 天保留期之上),经 `Control.SetTunnelEventStoreCapacity(maxBytes)` 可覆盖(同样未接 CLI/环境变量)。乱序或延迟事件跨天交替到达时,若每次 `Write` 都要 Close+Open+目录 `Glob`,代价会随交替频率放大;改为按天缓存最近 `maxOpenTunnelEventFiles`(=3)个文件句柄的小型 LRU,淘汰最久未用的一个,保留期/容量上限的清理(`pruneLocked`)也从"每次切换分区时同步执行"改为独立的后台循环,每 `tunnelEventPruneInterval`(=1 小时)跑一次,`Close` 会等它退出再关闭所有缓存句柄。清理时只保留"最近一次写入所在的分区"不被删除(呼应单文件版本"当前打开的文件永不被裁剪"的既有语义),其余分区即便句柄仍缓存着也照常按年龄/容量裁剪并顺带关闭其句柄——避免节点靠不断在几个日期间交替写入,把某个陈旧分区的句柄永远挂在缓存里从而绕开保留期。这也意味着:一个节点长期离线后一次性回补积压的多天数据时,同一轮清理只保护"这一轮最后写入"的那一个分区,更早日期的分区即便刚刚才写入,只要总容量超出上限就可能在同一轮里被立即裁掉——这是容量优先于"最近写入豁免"的有意取舍,不是缺陷。
- **keyedMutex 改为引用计数**:南向日志 ingest 用来串行化"同一节点并发上传"的 `keyedMutex`,原来靠节点删除时显式调用 `forget(key)` 回收条目,存在与并发 `lock(key)` 的竞争——同一个刚被删除又立刻用同一 ID 重新注册的节点,`forget` 与 `lock` 交错执行时可能各自创建一把不同的 `*sync.Mutex`,导致两个"应当互斥"的请求实际并发执行。改为每个条目自带引用计数,最后一个使用者释放锁时才把条目从 map 里摘掉,不再需要单独的 `forget` 调用,`DELETE /api/nodes/{id}` 处理器里对应的调用已删除。
- **南向 HTTP/2 服务器限制真正生效**:`SouthboundHTTP2Config`(`MaxConcurrentStreams`=`MaxConcurrentSouthboundStreamsPerConn`=100,镜像中继端口 `maxStreamsPerConn` 的做法)此前定义了却从未真正挂到 `cmd/rpop` 构造的南向 `*http.Server` 上,是个不生效的摆设;补上 `HTTP2: control.SouthboundHTTP2Config()` 之后,连同已有的 `ReadHeaderTimeout`(=`control.HeaderTimeout`=8s)、`IdleTimeout`(=2 分钟)一起生效。三者都不会影响 `southboundWatch` 的长连接——它是单个一直开着的响应,不存在"等下一个请求"的空档,`IdleTimeout`/`ReadHeaderTimeout` 天然不适用于它;测试把这两个超时注入成远小于生产值来验证这一点没有回归。

- **遗留 TODO**:隧道事件保留期(14 天)、解压/单行大小上限(64MiB/32MiB)、查询范围收窄的两个窗口常量(`tunnelQueryWindowDays`、`traceQueryWindow`)、跳号阈值(`maxLogSegmentJump`、`warnLogSegmentJumpThreshold`)、日志 ingest 并发/速率上限(`DefaultMaxConcurrentLogIngests`、`DefaultLogIngestRateBytesPerSecond`,虽有 `Control.SetLogIngestLimits` 方法)、隧道事件存储容量上限(`tunnelEventStoreDefaultMaxBytes`,虽有 `Control.SetTunnelEventStoreCapacity` 方法)、句柄缓存大小与后台清理周期(`maxOpenTunnelEventFiles`、`tunnelEventPruneInterval`)目前都还是代码常量,未像 spool 配额/限速那样开放 CLI flag/环境变量;`nodeAdapterSet` 每次 ingest 都会 `store.List` 全部站点,站点数量很大时可考虑缓存或增量维护。

**阶段 5(日志与追踪)端到端测试**:`internal/agent/e2e_*_test.go` 用真实 `*control.Control`(`NewWithLogDir`)+ 真实 `*agent.Agent`、真实 mTLS 与真实 spool 目录跑通节点到控制器的完整链路,覆盖:基本上传与查询(`e2e_basic_test.go`,含 track id 防伪造、trace 查询、节点 `LogStats` 上报);跨入口/中继/出口三节点的隧道全路径时间线(`e2e_overlay_trace_test.go`);控制器不可达时的 spool 积压与恢复、不丢不重(`e2e_backlog_test.go`);ACK 丢失后的幂等重传(`e2e_ack_idempotency_test.go`,用一个只包一层 `POST /southbound/v1/logs` 的测试网关模拟“已写入但响应丢失”);节点吊销重新注册后新 spool 从段号 1 起不被当重传丢弃(`e2e_reregister_test.go`);spool 配额丢段与控制器接受跳号(`e2e_quota_test.go`)。这批测试还发现并修复了一个真实缺陷:`agent.Run()` 原先的 `defer` 顺序让日志 spool 先于 `engine.StopAll()`/`closeOverlay()` 关闭,导致节点优雅关闭时刚产生的最后一批隧道事件(如入口自身的 `ended`)进了一个已经没有协程在读的 channel,静默丢失且从未落盘;修复后改为先停站点、关 overlay,最后才关 spool(`e2e_shutdown_test.go` 为回归测试,直接读 spool 磁盘文件断言)。

**阶段 5 审查修复(accesslog/overlay/agent)** 的落地要点:

- **file 适配器的写入与归档策略**(`internal/accesslog/file.go`):活跃文件(`access.jsonl`)的轮转只由真实时钟驱动(`writePeriodicLocked`/`rotateActiveLocked`),不再比较记录自身的时间戳——ingest 回放旧 spool 段时,一条延迟记录不会再把一份当前数据错误地归档到自己的旧日期下(这正是它替换掉的那个"最新数据被当最旧数据删除"的严重缺陷)。记录所属周期与活跃文件不一致时(几乎总是延迟记录),直接写入该周期自己的文件,该文件已被归档时则改开一个 `.lateN` 分片(`chooseSlotLocked`),永远不会重命名或误改活跃文件。压缩(gzip)与按配额清理都改为在后台 goroutine 执行(`launchArchive`,由 `sweepWG` 跟踪、`Close` 等待其退出),不再在持锁的写路径上同步进行,避免这类操作拖慢每一次 ingest ACK;压缩现在先写到一个不以 `access-` 开头的隐藏临时文件、成功后才原子改名为最终的 `.gz` 名(`gzipFileTo`),防止并发的 `Search`/清理扫到一份尚未写完、无法解压的压缩文件,进程若在压缩中途崩溃,临时文件在下次启动时会被清除而不是永久卡住该名字(`resumeInterruptedArchives`)。保留策略(`KeepFiles`)按文件名解出的周期分组裁剪(`prunePeriodicArchives`),一个周期的主文件、迟到分片和压缩产物作为整体一起保留或删除,不再按写入顺序裁剪;`parseFilePeriod` 同时认得新的按周期命名和审查修复之前的按时间戳命名,升级节点上的旧归档文件不受影响。
- **file 适配器 `Search` 对后台归档/清理的容错**(`internal/accesslog/file.go`):压缩(`gzipFileTo` 的“`.gz` 原子改名 + 删除 `.archiving` 源”)与保留策略的删除都刻意留在 `s.mu` 之外执行(见上一条),因此仍可能与持锁列出文件后逐个打开的 `Search` 交错。修复后 `Search` 对同一周期同时列出的 `.archiving`/`.gz` 只认后者(`skipShadowedArchiving`),避免压缩窗口期把同一批记录重复计数;打开 `.archiving` 遇到 ENOENT 时依次回退去读 `.gz`、再回退到原始文件名(`readRecordsTolerant`,分别对应压缩已完成和压缩失败被回滚两种情形),其余文件遇到 ENOENT 一律视为已被保留策略正常清理而跳过,不再让整次查询因为一个文件被后台协程改名或删除而整体失败。
- **overlay 关闭语义**(`internal/overlay`):中继端口 `drain()` 现在同步关闭底层监听器(`relayServer.listener.Close()`),端口地址立刻可被下一次 `startRelay` 复用,不再依赖后台 goroutine 某个不确定的时刻才真正调用 `server.Shutdown`;已经建立的隧道仍通过 `server.Shutdown` 在后台继续排空,不受影响。链路(`link`)的后台重拨(`maintain`)在被 `retire` 时会取消其正在进行中的拨号,而不是等一次完整的拨号+握手超时,`Overlay.Close` 因此能在近乎恒定的时间内返回。事件队列(`eventQueue`)的投递协程在 `Overlay.Close` 时被显式停止并等待退出,不再是常驻到进程结束的孤儿 goroutine。`agent.Agent.use()` 在节点重新注册、换上新 overlay 后,把旧 overlay 的 `Close()`(可能因等待链路重拨协程退出而耗时)放到持有 `a.mu` 之外执行,避免例如 `agentPaths.DialPath`、状态上报等其他需要这把锁的调用被一次重新注册顺带阻塞。

**阶段 6(控制台)设计**:以下是控制台各步骤的契约,后续步骤(6.2-6.5)据此并行实施。

- **现状**:生产控制台是单文件 `web/src/App.jsx`(纯 `useState` + 侧栏 `page` 字符串切换,不经路由);`web/src/views/ShellView.jsx`、`router/`、`stores/`(zustand)、`components/ui/*`(`AppShell`、`AppSideNav`、`UiRelGraph` 等)是尚未接入 `main.jsx` 的独立组件目录/主题演示(`/catalog`、`/demo`、`/tokens`)。阶段 6 的新页面不再往 `App.jsx` 的 `page` switch 加分支:6.2 的第一件事是把 `main.jsx` 换成既有的 `AppRouter`,把"站点管理/访问日志/日志适配器/系统设置"各自拆成一个路由页面文件(行为不变),之后每个新页面是独立文件 + 路由表追加一行 + 侧栏菜单追加一行,菜单/路由注册都集中在 `router/index.jsx` 和 `ShellView.jsx` 的 `menus` 数组这两处,减少并行步骤间的文件冲突。
- **信息架构**(新增导航,复用 `AppSideNav`/现有图标与高亮约定):节点 `/nodes` 列表 + `/nodes/:id` 详情(在线状态、revision/`inSync`、证书代数、join token 生成、`logs`/`links`/`paths` 健康,复用 `nodeView` 已有字段的展示位置);拓扑 `/topology`,entry→relay→exit 分层布局,建议直接扩展 `UiRelGraph`(节点/边坐标由页面按列计算好传入,组件本身不认识业务概念,零新增依赖)+ 具名代理作为图中的方形节点;具名代理 `/proxies` 列表 + `UiDrawer` 编辑抽屉,凭据只写不读(`hasPassword` 复选提示),`usedBy` 只读展示;`SiteEditor.jsx`/`UpstreamFields.jsx` 内的 upstream 增加"候选路径"分区(有序列表,每条路径的 `via` 是节点/代理混合序列,上移/下移代替拖拽即可,展示 `POST /api/sites` 400 响应的字段级错误);`RouteSimulator.jsx` 在现有单跳结果旁新增"全路径"区块(见下方 `/api/routes/simulate` 扩展);追踪时间线 `/trace`(可带 `?trackId=` 预填)+ `LogDetailDrawer.jsx` 给 `record.trackId`/`record.tunnelId` 加跳转链接,时间线用 `UiTimeline` 按 `stage`(`arrived`/`established`/`ended`)渲染各跳。
- **API 契约(本步已实现)**:`nodeView`(`GET /api/nodes`、`GET/PUT /api/nodes/{id}` 共用)新增 `paths: UpstreamPathHealth[]`,与既有 `links: LinkStatus[]` 并列。`LinkStatus` 新增 `proxies: string[]`(链路代理链,元素为 `"type://address"`,绝不含凭据)、`status: "up"|"dialing"|"down"`、`downUntil`(RFC3339 字符串,空表示未在退避)、`lastError`(拨号失败信息,成功后清空)、`lastSuccess`(RFC3339 字符串)。`UpstreamPathHealth` 形状:`{siteId, upstream, paths: [{index, label, status: "healthy"|"cooling", until, failures, lastError}]}`。新增 `GET /api/topology`:`{nodes: [{id, name, embedded, online, roles: ("entry"|"relay"|"exit")[]}], links: [{from, to, proxies: string[], status, connections, tunnels, failures, downUntil, lastError}], proxies: [{id, name, type}]}`;`roles` 由已发布快照推导(托管站点→entry;某路径的 `RelayRoute.Next!=""`→relay;直接拨号或路径终点→exit,一个节点可兼有多个角色),`links` 的健康状态取 `from` 一侧上报的出站链路(按 `peer`+`proxies` 精确匹配,匹配不到按 `peer` 回退,再不到则 `status:"unknown"`)。
- **API 契约(6.5 已实现,见下方阶段 6 第 5 步的落地要点)**:请求体新增可选 `siteId`(未保存的站点留空即可);`POST /api/routes/simulate` 响应在现有单跳结果基础上追加 `paths: [{index, label, hops, status: "healthy"|"cooling"|"unknown", until, selected: bool, reason: string}]` 与 `selectedPath: number`(索引)。`hops` 比最初定形时多了两个健康字段:`{kind: "node"|"proxy", id, online?: bool, link?: {status: "up"|"dialing"|"down"|"unknown", downUntil, lastError}}`——`online` 只出现在 `kind:"node"` 上;`link` 是这一跳从上一跳(入口节点或上一个 relay)过来的隧道健康,入口跳和 `proxy` 跳都没有。`reason` 形如"第一条未冷却的路径"/"全部路径冷却中,回退第一条冷却路径",与 `dataplane.failoverTransport.order` 的选择语义一致。未配置 `paths` 的上游不返回 `paths`/`selectedPath`,现有响应形状不变。
- **前端约定**:健康类数据统一轮询刷新,间隔 5s(与 `App.jsx` 现有 `refresh` 轮询一致,不额外引入 WebSocket/SSE);空状态用 `UiEmpty`,错误用现有 `error` 条 + `UiAlert`,加载态用 `UiSkeleton`;深浅主题沿用 `--accent`/`--text-primary` 等既有 token,不新增调色板;新增依赖为零——拓扑图用手写 SVG(`UiRelGraph` 的模式),健康着色用既有 `UiStatusDot`/`UiTag` 语义色(up/healthy=success,dialing/cooling=warn,down=danger,unknown=neutral)。
- **任务切分**:6.2(具名代理页 + paths 编辑器;依赖 `/api/proxies`、`/api/nodes`、`POST /api/sites` 400 错误形状;新文件 `views/ProxiesPage.jsx`、`components/PathsEditor.jsx`,改动 `router/index.jsx`+`ShellView.jsx` 各一行,以及把 `App.jsx` 迁到路由)。6.3(追踪时间线;依赖 `/api/logging/trace/{trackId}`、`/api/logging/tunnels/{tunnelId}`;新文件 `views/TracePage.jsx`,改动 `LogDetailDrawer.jsx` 加跳转)。6.4(节点/拓扑/健康/spool 页面;依赖 `/api/nodes` 的 `links`/`paths`/`logs`、`/api/topology`;新文件 `views/NodesPage.jsx`、`views/NodeDetailPage.jsx`、`views/TopologyPage.jsx`)。6.5(模拟器全路径;后端实现上面定形的 `/api/routes/simulate` 扩展,前端改 `RouteSimulator.jsx` 追加"全路径"区块,不改其单跳结果的既有渲染)。各步骤互不改动对方新增的文件,仅在 `router/index.jsx`(路由表)与 `ShellView.jsx`(`menus` 数组)各追加一行,合并冲突面很小。

**阶段 6 第 1 步(链路/路径健康上报 + 拓扑 API)** 的落地要点:

- **overlay 链路健康**(`internal/overlay/link.go`):`link` 新增 `proxies []string`(构造时用新导出的 `ProxyChainLabels` 从 `[]snapshot.Proxy` 渲染为 `"type://address"`,不含凭据,`newLink`/测试用的 `newLinkWithoutMaintain` 都要填)、`lastErr`、`lastSuccess`,均在既有的 `dial()` 持锁段内更新,不新增锁、不影响 `acquire` 热路径。`LinkStatus.Status` 由 `status()` 现有的 `l.pruneLocked()` 之后一次性判定:`len(conns)>0` → `up`;否则 `dialing!=nil` → `dialing`;否则 `down`。`DownUntil`/`LastSuccess` 用 RFC3339 字符串(零值输出为空,而不是 `"0001-01-01T00:00:00Z"`),与 `nodeView.LastSeen` 的既有约定一致。
- **dataplane 路径健康**(`internal/dataplane/paths.go`):`pathTransport` 新增 `lastErr`,`failed(now, err)` 签名新增 `err` 参数(调用点 `failoverTransport.RoundTrip` 传入 `dialErr.err`,即 `pathDialError` 解包后的原始错误,不重复 `"path %s: %v"` 前缀)。新增 `PathHealth`(`index`/`label`/`status: healthy|cooling`/`until`/`failures`/`lastError`)与 `UpstreamPathHealth`(`siteId`/`upstream`/`paths`)。`Engine.PathHealth()` 遍历 `e.runs`(与 `RunningSites`/`Metrics` 同源,只读快照,不影响转发),按 `siteId`+`upstream` 排序。为把 `*failoverTransport` 从 `newUpstreamTarget` 一路带到 `Engine`,`upstreamTarget`/`siteRuntime` 各新增一个字段(`failover`/`pathGroups`),`siteHandler`/`Handler`/`buildRuntime` 的返回值相应多了一个 `[]upstreamPathGroup`;只在构造/替换 runtime 时经过一次,不在请求路径上。
- **接线**:`southbound.Status` 新增 `Paths []dataplane.UpstreamPathHealth`;`agent.status()` 补一行 `status.Paths = a.engine.PathHealth()`(与既有的 `status.Links = a.overlay.Links()` 对称)。`nodeView` 新增 `Paths` 字段,`localNodeView` 直接读 `c.engine.PathHealth()`(嵌入节点在控制器进程内,不经过南向上报);其余节点的 `Paths`/`Links` 都来自 `nodeRegistry.report` 存的最近一次上报。
- **安全**(`internal/control/status_sanitize.go`):`nodeRegistry.report` 在加锁存储前统一调用新增的 `sanitizeStatus`,对 `Links`/`Paths` 做条数上限(`maxReportedLinks`=512、`maxReportedUpstreams`=512、`maxReportedPathsPerUp`=64、`maxReportedPathsTotal`=4096、`maxProxyChainEntries`=32)与字符串长度上限(`maxHealthStringBytes`=256,覆盖 `Peer`/`Address`/`LastError`/代理链每一项/`SiteID`/`Upstream`/路径 `Label`/`LastError`)——现有的 `maxStatusBody`(4MiB)只挡住整包大小,挡不住"合法大小的包里塞几千个极小条目"这类放大;这一步之后 `/api/nodes`、`/api/topology` 的响应体也间接被这组上限约束。凭据不泄露:`overlay.ProxyChainLabels` 只渲染 `type://address`;`DialChain`/`throughProxy`/`socksConnect` 的错误格式化(`chain.go`)与 SOCKS5 库(`golang.org/x/net/proxy`/`internal/socks`)的认证失败错误都已确认只提及地址与状态文本,不含用户名密码,新增的 `TestLinkStatusReportsDialFailureWithoutLeakingProxyCredentials`(用真实 SOCKS5 服务器 + 错误密码触发一次真实拨号失败)与拓扑测试里的字符串扫描都覆盖了这一点。
- **`GET /api/topology`**(`internal/control/topology.go`):节点列表(内嵌节点 + `store.ListNodes`)、有向边、具名代理三部分。角色由 `publication.Snapshots()`(新增的批量读取方法)推导:托管站点→`entry`;`RelayRoute.Next!=""` 的路由所在节点→`relay`;`RelayRoute.Next==""`(隧道终点)或某上游未配置 `paths`(直接拨号,不经过 Path/failover 概念)或某条 `path.FirstNode==""`(候选里的直连回退)→`exit`。边直接从已发布快照里“已经解析好”的 `Path.FirstNode`/`LinkProxies` 与 `RelayRoute.Next`/`LinkProxies` 读出(不重新做 `resolvePaths` 那套解析),按 `(from, to, 代理链)` 去重;健康状态取 `from` 一侧最近上报/实时的 `LinkStatus`(嵌入节点走 `c.overlay.Links()`,其余节点走 `nodeRegistry`),按 `peer`+`proxies` 精确匹配,退化到按 `peer` 匹配,再退化到 `"unknown"`。
- **测试**(均 `-race`):`internal/overlay/link_status_test.go`(链路状态机三态、代理链渲染与脱敏、失败/成功清空错误与置位时间);`internal/dataplane/paths_test.go` 新增两个用例(冷却/失败/错误正确记录、未配置 paths 的上游不出现在 `PathHealth` 里);`internal/control/status_sanitize_test.go`(小报告原样通过、nil 直通、条数与长度上限、`nodeRegistry.report` 确实先脱敏再存);`internal/control/nodes_test.go` 新增 `nodeView`/`localNodeView` 读取健康数据的用例;`internal/control/topology_test.go`(复用阶段 4 已有的三节点+双代理夹具验证角色/边/代理列表/健康匹配与回退/凭据不泄露,外加简单单节点场景与鉴权)。
- **未做(留给后续步骤)**:前端页面(6.2-6.4)。

**阶段 6 第 2 步(具名代理页 + paths 编辑器)实施记录**:

- `main.jsx` 已切换到 `AppRouter`;`ShellView`(`AppShell`)成为整个控制台唯一的外壳,`站点管理/访问日志/日志适配器/具名代理/系统设置`与原有的`组件目录/页面示例/Token`同列在 `ShellView.jsx` 的 `menus` 数组里,默认路由从 `/catalog` 改为 `/sites`。登录态拆到 `stores/auth.js` + `components/AuthGate.jsx`(整个路由树的公共前置),`api.js` 统一处理 401(自动回登录),行为(登录/建密码、5s 轮询、站点增删改查、YAML 导入导出)不变,原先近乎无样式的自定义 rail 侧栏被替换。
- `views/ProxiesPage.jsx` 按契约用 `UiTable`/`UiDrawer` 实现;`proxyForm.js` 的 `buildProxyMutation` 把编辑时留空的密码字段发送 `password: null`(保持不变),新建代理始终发送该字段;删除被引用时把 409 转成友好提示。
- `components/PathsEditor.jsx` 接入 `UpstreamFields.jsx` 的新增 `OptionToggle`;`pathsForm.js` 提供路径/跳的增删排序、`via` 单路径简写迁移(`normalizeUpstreamPaths`,在 `siteForm.js` 的 `prepareSiteForEditing` 时机做一次)、hop 下拉的候选节点过滤(排除内嵌节点与站点自身 placement)以及 `parsePathsError`——从 `upstreams[i].paths[j](.via[k])?` 前缀定位服务端 400 错误到具体路径/跳。
- 阶段 6.6b 已补齐:`SiteEditor` 新增"站点放置"字段(`PlacementFields.jsx`)编辑 `config.nodes`,从 `/api/nodes` 勾选(标出内嵌节点与离线节点),留空沿用服务端默认(仅内嵌节点 `local`);`isPlacementError` 把 `validatePlacement`/`validatePlacementReferences` 的 400 错误映射到该字段。
- `PathsEditor` 的 hop 下拉改用 `hopNodeOptions` 按当前编辑中的 placement 与节点是否已配置 `relayAddress` 逐个给出禁用原因(替换原先"只影响内嵌节点"的近似过滤),`hopNodeWarning` 标出已保存但因 placement/relayAddress 变化而失效的跳。

**阶段 6 第 3 步(追踪时间线)** 的落地要点:

- `views/TracePage.jsx`(路由 `trace/:trackId?`,`:trackId` 用 react-router 的可选段语法,同一个组件覆盖 `/trace` 与 `/trace/:trackId`;另支持 `/trace?tunnel=<id>` 直接按隧道查、`/trace?trackId=` 会被 302 到规范的 `/trace/:trackId`)先查 `GET /api/logging/trace/{trackId}` 拿到那一条 access log 记录做摘要(时间/站点/方法/host+path/状态码/完整耗时/`reportedBy` 作为入口节点),若其 `tunnelId` 非空再查 `GET /api/logging/tunnels/{tunnelId}`;两次请求都做了竞态保护(只应用最后一次发起的请求的结果)。
- 纯逻辑抽到 `web/src/trace.js`(`isValidTraceId`——与后端 `traceIDPattern` 同形状的 UUID 校验、`sortTunnelEvents`、`withRelativeTiming`、`groupHops`——按 `role` 排 entry→relay→exit,同角色多跳按各自最早事件时间破平、`hopDurationBars`——每跳耗时占比条,零时长/单跳/全部同时间戳都退化为可见的最小宽度或均分、`tunnelSectionState`——把加载/错误/空/无隧道折叠成一个状态机),`trace.test.js` 覆盖乱序、缺 `ended`、单跳、时间戳相同等边界,共 17 个用例。
- 时间线可视化是手写 CSS(每跳一行 · 按百分比定位的进度条 + `UiTimeline` 纵向列表),未引入图表依赖;页面提示"跨节点时间以各节点本地时钟为准"(时钟偏差标注留给阶段 7)。
- `LogDetailDrawer.jsx` 新增 Track ID / Tunnel ID 两行,渲染成可点击按钮(`useNavigate` 跳转到 `/trace/:trackId` 或 `/trace?tunnel=`),没有 tunnelId 时提示"直连出口,未经过隧道"。
- 未发现 API 缺口:`accesslog.Record` 的 `trackId`/`tunnelId`/`reportedBy`/`upstream` 与 `overlay.TunnelEvent` 的字段已经满足摘要与时间线所需的全部展示项。

**阶段 6 第 4 步(节点/拓扑/健康页面)** 的落地要点:

- 新增 `web/src/views/{NodesPage,NodeDetailPage,TopologyPage}.jsx`,路由/菜单只在 `router/index.jsx`、`ShellView.jsx` 追加一行;纯逻辑抽到 `web/src/nodeHealth.js`(字节/时间格式化、链路/路径/日志健康摘要与判定)与 `web/src/topologyLayout.js`(分层布局、多边偏移、颜色映射),均有 `node --test` 覆盖,零新增依赖。
- **拓扑分层规则**:一个节点可能同时具备多个 `roles`;契约未规定多角色如何选层,本步固定为"取 entry→relay→exit 中最靠前出现的角色"(等价于 `roles[0]`,因为 API 已按此顺序返回),无任何角色的节点单独归入末尾的"未分配角色"列,而不是被丢弃。布局只读节点自身的 `roles`,不遍历边,因此链路环路/自环不会影响分层或造成死循环。
- **多边区分**:边按 `(from, to, 代理链)` 三元组各自成行(与后端去重键一致);同一对节点(含反向 A↔B)的多条边按车道号 `0,+1,-1,+2,-2…` 沿垂直于连线的方向偏移,自环单独计数、向外画弧,车道计算与坐标布局一样是纯函数并有单测。
- **具名代理呈现**:`/api/topology` 的 `proxies` 是一份扁平列表,不携带到节点的连接信息,因此代理链只在边的 hover tooltip(SVG `<title>`)中按 `type://address` 展示,并在图下方列出全部具名代理供参照,没有按设计草案建议的"作为图中方形节点"渲染(缺少连接坐标,强行展示无意义)。
- **API 缺口(已解决,见文末《阶段 6 收尾修补》的落地要点)**:①`store.Node.CertGeneration` 打了 `json:"-"`,`/api/nodes` 从不下发证书代数,节点列表/详情页只能展示 `registered`(是否已完成过注册)与 `certNotAfter`,无法展示任务描述里的"证书代数";②没有 `GET /api/nodes/{id}`,`nodeAPI` 的 switch 只处理 `token`/`PUT`/`DELETE`,详情页改为拉取 `GET /api/nodes` 全量列表后按 id 过滤(与 SitesPage 编辑器同款模式),多一次全量请求但无需后端改动。
- 节点列表页承担生命周期管理:新建(`POST /api/nodes`)、编辑名称/中继地址(`PUT /api/nodes/{id}`)、生成或重置 join token(`POST /api/nodes/{id}/token`,token 只在一次性 `UiModal` 中出现,`persistent` 阻止误关)、删除(`DELETE /api/nodes/{id}`)。

**阶段 6 第 5 步(模拟器全路径)** 的落地要点:

- **后端**(`internal/control/routing_paths.go`):`addPathSimulation` 在既有 `simulateRoute` 之后按匹配到的 upstream 调用,直接复用 `upstreamPaths`/`pathLabel`(`paths.go`)与 `matchLinkStatus`/`linkStatuses`(`topology.go`),不重走 `resolvePaths`——hop 的 `id` 就是配置里的节点/代理 ID,不需要解析出带凭据的 `snapshot.Proxy`,代理凭据自然不会出现在响应里。入口节点取 `siteNodes(input.Config)` 的第一个(`config.nodes` 为空时即 `local`,与既有放置语义一致);多节点放置时只取第一个作为模拟视角,不逐节点展开。选择规则 `selectSimulatedPath` 与 `failoverTransport.order`/`RoundTrip` 完全对齐:第一条非冷却路径胜出,全部冷却则回退到优先级最靠前的一条;没有健康上报(`siteId` 留空,或该站点从未在其入口节点上运行过)时按"健康"处理,与刚启动、从未失败过的 `pathTransport` 行为一致,但 `status` 字段如实标注为 `"unknown"` 而不是编造 `"healthy"`。
- **安全**:响应里从不序列化 `namedProxy`/`snapshot.Proxy`,只用 `overlay.ProxyChainLabels` 派生的标签做链路匹配;代理跳只输出配置里的 ID。
- **前端**:`RouteSimulator.jsx` 请求体新增 `siteId`(`site.id`)、`config.nodes` 与每个 upstream 的 `via`/`paths`(之前只发 `url`);现有单跳渲染不变,新增 `<SimulatedPaths result={result}/>`。纯逻辑(响应 → 视图模型、健康色调映射)在 `routeSimulatorPaths.js`(`node --test` 覆盖),渲染在新文件 `components/SimulatedPaths.jsx` + `SimulatedPaths.css`,健康色调复用 `UiStatusDot`/`UiTag`。
- **测试**:`internal/control/routing_paths_test.go`(表驱动覆盖三种冷却场景选路、多跳含具名代理与链路 down/节点离线的健康渲染、未配置 paths 时字段不出现、无 `siteId` 时健康为 unknown、HTTP 层向后兼容与凭据不泄露的字符串扫描,均 `-race`);`web/src/routeSimulatorPaths.test.js`(`node --test`)。

**阶段 6 收尾修补** 的落地要点:

- 补齐第 4 步遗留的两个 API 缺口:`nodeView` 新增只读字段 `certGeneration`(`store.Node.CertGeneration` 的 `json:"-"` 保持不变,PUT 输入结构 `nodeMutation` 本就没有这个字段,不存在被覆写的风险);`nodeAPI` 补上 `GET /api/nodes/{id}`(含内嵌节点 `local`),形状与列表项一致,未找到 404,`internal/control/nodes_test.go` 表驱动 `-race` 覆盖三种场景。
- 前端:`NodeDetailPage` 改用 `GET /api/nodes/{id}` 而不再拉取全量列表过滤;节点列表/详情页展示证书代数替换原 `registered` 标签,证书到期时间保留。
- 导航:生产菜单重排为站点管理/节点/拓扑/具名代理/访问日志/请求追踪/日志适配器/系统设置并各配互不重复的语义图标;组件目录/页面示例/Token 演示页改为 `React.lazy` 懒加载且仅 `import.meta.env.DEV` 下注册路由/菜单,生产构建的主 chunk 不含其代码。

**阶段 6.6a(旧页面主题统一)** 的落地要点:

- `Admin.css`/`Rpop.css`/`Routing.css`/`Logs.css` 从压缩单行改为多行格式,并把全部硬编码颜色/阴影/背景换成 `styles/tokens.css` 既有语义 token(无需新增 token),对齐 `components/ui/*` 已有的 soft 色对、`--shadow-lg`、`--focus-ring` 等惯例;`Sidebar.css` 连同 `Rpop.css`/`Admin.css` 里 `.rail`/`.top`/`.crumb`/`.avatar`/`.main-nav`/`.main.subpage` 等因迁移到 `AppShell`/`AppSideNav` 而失效的选择器一并删除(grep 确认零引用)。`stores/theme.js` 在模块加载时同步调用 `applyTheme`,先于 `AuthGate` 渲染,登录页无需改动即已跟随主题。
- 残留两处硬编码 `#fff`(`.brand-mark`、`.primary` 按钮文字色),用于叠在 `--grad-primary` 渐变上的文字,与 `ui-btn--primary` 现有写法一致,不做 token 化。

**阶段 7(长尾)设计**:以下是 §4 第 7 阶段四项遗留工作(窗口调优、协议版本治理、ClickHouse 去重、时钟偏差标注)的契约,以及 §5 遗留 TODO(行 134)与 D19 的收口方案;7.1-7.6 据此并行实施,均为新增/追加式改动,不需要引入南向协议版本升级。

- **窗口/遗留常量配置化(D31)**:"窗口"覆盖三类。①overlay/南向 HTTP/2 流控与并发——`streamWindow`(16MiB)/`connectionWindow`(64MiB)/`maxStreamsPerConn`(1000,均在 `internal/overlay/link.go`,同时用于出站 `newLink` 与中继 `startRelay`)、南向 `MaxConcurrentSouthboundStreamsPerConn`(100,`internal/control/southbound.go`)——改成节点/控制器启动参数(如 `-overlay-stream-window`/`RPOP_OVERLAY_STREAM_WINDOW`、`-overlay-connection-window`、`-overlay-max-streams`、控制器侧 `-southbound-max-streams`),默认值不变,窗口校验范围 `[64KiB,256MiB]`、流数 `[1,10000]`;这是进程启动参数,不是配置真源要管的对象,不随快照下发,与既有的 `-log-spool-quota-bytes` 同类。②控制器侧日志/隧道事件遗留常量——`Control.SetLogIngestLimits`/`SetTunnelEventStoreCapacity` 已有方法但未接 CLI,补上 `-log-ingest-max-concurrent`/`-log-ingest-rate-bytes`/`-tunnel-event-store-max-bytes` 三个 flag/env;新增 `Control.SetTunnelEventRetention(days)` 方法 + `-tunnel-event-retention-days`(默认 14,替换常量 `tunnelEventRetentionDays`)。③明确不开放的常量及理由——`maxDecompressedLogSegmentBytes`(64MiB)/`maxLogRecordLineBytes`(32MiB)/`maxLogSegmentJump`/`warnLogSegmentJumpThreshold` 绑定 `southbound.MaxLogSegmentBytes` 这个协议常量,是安全边界而非调优项,开放成独立配置会让运维在不理解协议约束的情况下调宽攻击面,不做;`tunnelQueryWindowDays`/`traceQueryWindow` 是查询性能优化,配错会静默漏数据且无报错,不做;`maxOpenTunnelEventFiles`/`tunnelEventPruneInterval` 是纯内部实现细节,不做;`nodeAdapterSet` 每次 ingest 全量 `store.List` 站点的性能债务记为已知遗留,阶段 7 不处理。
- **协议版本治理(D27)**:新增 `southbound.ProtocolVersion`(当前值)与 `MinSupportedProtocolVersion`(控制器接受的最旧节点版本)两个常量,经 HTTP 头 `Rpop-Protocol-Version` 双向携带(不进入 JSON body):`register`/`renew`/`watch`/`status`/`logs` 与 overlay CONNECT 隧道握手统一带这一个头,节点请求带、控制器/中继响应也回同名头,一次往返即可发现任一方不匹配。判定逻辑收在 `authenticateNode` 旁新增的 `checkProtocolVersion`:节点版本 > 控制器 `ProtocolVersion` → 拒绝(`426 Upgrade Required`,提示先升级控制器,遵循控制面先行的滚动升级顺序,类似 Kubernetes 的版本偏差策略);节点版本 < `MinSupportedProtocolVersion` → 拒绝并提示升级节点;区间内但落后于当前值 → 放行 + 记一次 Warn + 置位 `nodeView.ProtocolVersion`/`ProtocolStatus`(`"current"`/`"outdated"`)供控制台展示。只有破坏性变更(字段语义变化、新增必需字段、既有枚举值含义调整)才提升 `ProtocolVersion`,纯新增可选字段按阶段 5/6 历次扩展的实际做法天然向后兼容,不提升。Overlay/中继 CONNECT 握手版本不匹配永不拒绝隧道(数据面可用性优先于版本一致性,呼应 fail-standalone 的既有取舍),只记一次 warn;只有南向控制通道可以硬拒绝,节点被拒后继续用缓存快照转发,不影响在途流量。
- **ClickHouse/ES/文件去重(D29)**:去重键复用已有字段,不新增协议字段——access log 用 `trackId`(D22 起每个请求必有且请求内唯一,重传段里的记录是同一 JSON 的原样重发,`trackId` 恒定不变);隧道事件用 `tunnelId+"|"+nodeId+"|"+stage`(同一事件在同一段内至多出现一次)。ClickHouse:`ensureTable` 的建表语句加一列 `dedup_key String`(写入时按上述规则取值,不解析 JSON),引擎从 `MergeTree` 换成 `ReplacingMergeTree`,`ORDER BY (timestamp, site_id, dedup_key)`;按天/按小时分表(`SplitMode`)下这只影响新建的分表,存量表继续用旧引擎(ClickHouse 不支持 `ALTER ... MODIFY ENGINE`),`SplitMode=none` 的单表需要运维手工 rename+回填迁移,记入升级说明而非代码。`Search` 生成的每个子查询加 `FINAL` 兜底(重复本是长尾事件,量小,可接受该开销);`/api/logging/trace/{trackId}` 取第一条命中即返回,不需要改。Elasticsearch:`Write` 的 `_bulk` `"index"` 动作补上显式 `"_id": dedupKey`(现状是自动生成 ID),同 `_id` 的写入天然幂等覆盖,零额外代码。文件/S3 适配器保持追加写、不改存储格式(重写历史段成本远高于收益),改为在共享的 `matches()` 聚合结果之后按 `dedup_key` 做一次内存态去重,覆盖用户可见的重复,不解决磁盘上无害的重复字节。`internal/control/tunnel_events.go` 的 `tunnelEventStore` 不走 accesslog 适配器体系,单独加一个有界 LRU(如最近 4096 个 `dedup_key`)在 `Write` 时短路重复事件,覆盖"重传紧跟上一次成功"的主要场景,重启后归零是已接受的残留风险。
- **时钟偏差标注(D28)**:复用南向 `status` 心跳的既有周期(15s),不新增端点。`southbound.Status` 新增 `SentAt time.Time`;响应从现在的 `204 No Content` 改成 `200` + 新类型 `southbound.StatusResponse{ReceivedAt, RespondedAt time.Time}`(纯新增字段/响应体,不触发协议版本提升)。节点侧记录每次心跳的 `t0`(发送前)/`t3`(收到响应后),下一次心跳按 NTP 公式 `offset=((ReceivedAt-t0)+(RespondedAt-t3))/2` 用上一轮的四个时间点算出偏差,写进新字段 `ClockOffsetMillis`(首次心跳没有上一轮数据,省略)。控制器 `nodeRegistry.report` 把偏差存入内存(不落库,与 `Links`/`Paths` 同类);`sanitizeStatus` 加一条范围校验(`|offset|` 超过 24h 直接丢弃,防止畸形值污染展示)。`nodeView`/`GET /api/topology` 新增 `clockSkewMillis`,超过 `clockSkewWarnThresholdMillis`(默认 2000ms,可配)标 `"warn"`。`GET /api/logging/tunnels/{tunnelId}` 响应给每条 `TunnelEvent` 附上查询时刻的上报节点 `clockSkewMillis`(尽力而为的当前值,不还原事件发生时刻的历史偏差);`TracePage.jsx` 展示每跳的偏差徽标,偏差超阈值的隧道顶部提示"跨节点时钟偏差较大,时间线可能不准确",并提供一个纯前端开关"按偏差校正显示"(新增 `trace.js` 纯函数 `applyClockSkew`,对时间戳做 `+skew` 校正后再算相对进度条;默认不开启,校正结果仍是估计值而非事实,不改后端)。
- **D19 主动探测与按上游降级参数(D30)**:主动探测只在路径处于冷却态时启用,单个冷却路径到期(`until`)那一刻触发一次(定时器,而非轮询),复用 `failoverTransport`/`pathTransport` 已有的建连代码路径(D1 同一份代码),只做到"连接建立成功"就立即关闭、不发请求字节,成功调用既有 `succeeded()` 清冷却,失败调用既有 `failed()` 按现有 `minPathCooldown`(1s)→`maxPathCooldown`(1min)指数序列重新排一次到期定时器——开销正比于"当前处于冷却态的路径数"这一天然很小的集合,健康路径零额外开销,符合 P1/P8。全局开关 `-path-active-probe`/`RPOP_PATH_ACTIVE_PROBE`(默认开)。降级参数(`pathEstablishTimeout` 建连预算、`minPathCooldown`/`maxPathCooldown`)从 `internal/dataplane/paths.go` 里的常量提升为每个 upstream 可选覆盖:`store.Upstream` 新增可选 `Failover *UpstreamFailover{DialTimeoutMs, MinCooldownMs, MaxCooldownMs, ActiveProbe *bool}` 字段(`paths` 的兄弟字段,不是逐条 path 的字段),留空沿用全局默认(纯新增可选字段,渲染进快照,节点侧无需协商);`internal/control/paths.go` 的 `validatePaths` 新增校验(`dialTimeout∈[1s,60s]`、`0<minCooldown≤maxCooldown≤10min`)。

**阶段 7 第 3 步(协议版本治理)实施记录**:

- 权威常量落在 `overlay.ProtocolVersionHeader`/`overlay.ProtocolVersion`(=1),`southbound` 包对二者取别名并新增 `MinSupportedProtocolVersion`(=1)——`southbound` 已依赖 `overlay`(`Status.Links`),反向依赖会成环,故不把常量放在 `southbound` 里。`checkProtocolVersion`(`internal/control/southbound.go`)把决策拆成纯函数 `classifyProtocolVersion(version, min, current)`,五个南向 handler 在 `authenticateNode`(或 register 的 token 校验)成功后各加一行调用;缺失头视为 `MinSupportedProtocolVersion`。拒绝均为 `426 Upgrade Required`,响应始终携带控制器自身版本,不影响业务错误码。
- `nodeRegistry`/`nodeView` 新增 `protocolVersion`/`protocolStatus`(`"current"`/`"outdated"`),仅在状态变化时记一次 Warn;内嵌节点固定为控制器自身版本、`"current"`。节点侧(`identity.go`/`agent.go`/`spool/upload.go`)在 register/renew/watch/status/logs 五处请求都带上头;收到 `426` 后 `registerUntilDone`/`watchLoop` 改用 `authRetryInterval`(1 分钟)退避而非指数重连退避。
- Overlay CONNECT 握手(`overlay.go`/`relay.go`/`tunnel.go`)双向携带同一个头,不匹配时仅计数(`protocolMismatches`)+ 按对端 ID 去重的一次 Warn,隧道始终正常建立。
- **升级顺序建议**:先升级控制器,再升级节点(控制面先行,呼应 Kubernetes 的版本偏差策略);节点落后于控制器但仍在窗口内时继续放行并告警,不阻塞滚动升级。

- **任务切分**(7.1-7.6,尽量不改同一文件,标注依赖):7.1(窗口/遗留常量配置化,D31):`internal/overlay/{link.go,relay.go}`、`internal/control/southbound.go`(仅 `SouthboundHTTP2Config`)、`internal/control/log_ingest_limits.go`、`internal/control/tunnel_events.go`(新增 `SetTunnelEventRetention`)、`cmd/rpop/main.go`(新增 flags)、`internal/agent/agent.go`(`Config` 新增窗口字段);无依赖,可立即开始。7.2(D19 主动探测 + 按上游降级参数,D30):`internal/dataplane/paths.go`、新文件 `internal/dataplane/paths_probe.go`、`internal/store/model.go`+`internal/snapshot`(`Failover` 字段)、`internal/control/paths.go`(`validatePaths` 校验);无依赖。7.3(协议版本治理,D27):`internal/southbound/southbound.go`(仅新增版本常量,追加)、`internal/control/southbound.go`(`authenticateNode` 旁新增 `checkProtocolVersion`,五个 handler 各加一行调用)、`internal/agent/identity.go`(`postJSON`/watch 请求构造加头)、`internal/overlay/{link.go,relay.go}`(CONNECT 头,宽松处理)、`internal/control/nodes.go`(`nodeView` 新增两个字段);无依赖,与 7.1 都会碰 `southbound.go`/`link.go`/`relay.go` 但改的是不同函数,冲突面小。7.4(ClickHouse/ES/文件/隧道事件去重,D29):`internal/accesslog/{clickhouse.go,elasticsearch.go,model.go,file.go,file_search.go,s3.go}`、`internal/control/tunnel_events.go`(LRU 去重,与 7.1 改的方法不同,注意合并顺序);无依赖。7.5(时钟偏差估计,D28):`internal/southbound/southbound.go`(新增 `SentAt`/`StatusResponse`,与 7.3 都追加内容,建议 7.3 先合并)、`internal/agent/agent.go`(`status()`/`statusLoop` 记录 t0/t3)、`internal/control/southbound.go`(`southboundStatus` 改响应体)、`internal/control/status_sanitize.go`、`internal/control/nodes.go`(`nodeView` 新增 `clockSkewMillis`)、`internal/control/tunnel_events.go`(查询响应附偏差);依赖 7.3 先落地协议版本头的改动模式(非强依赖,建议顺序合并减少 `southbound.go` 反复冲突)。7.6(控制台:协议版本/时钟偏差展示):`web/src/views/TracePage.jsx`(偏差徽标/告警/校正开关)、`web/src/trace.js`+`trace.test.js`(新增 `applyClockSkew`)、节点版本徽标补在阶段 6.4 产出的节点列表/详情页(该页尚未落地,若 7.6 启动时仍未合并,先只做 `TracePage.jsx` 部分,节点徽标随 6.4 落地后再补一个不冲突的小改动);依赖 7.3/7.5 定形的 API 字段(`protocolVersion`/`protocolStatus`/`clockSkewMillis`),字段形状已在上面给出,可先按契约写前端再接线合并。

**阶段 7 第 1 步(窗口/遗留常量配置化,D31)实施记录**:

- `internal/overlay/link.go` 新增 `Config{StreamWindowBytes,ConnectionWindowBytes,MaxStreamsPerConn}`/`DefaultConfig()`/`ValidateConfig`(范围 `[64KiB,256MiB]`/`[1,10000]`),`overlay.New(identity,log,cfg)` 构造时注入,`newLink`/`relay.go` 的 `startRelay` 都改读 `o.config`,不再有包级常量;`Overlay.config` 构造后只读,无需加锁。
- CLI(节点与控制器内嵌节点共用,默认值等于原常量,零配置行为不变):`-overlay-stream-window`/`RPOP_OVERLAY_STREAM_WINDOW`(16MiB)、`-overlay-connection-window`(64MiB)、`-overlay-max-streams`(1000);`agent.Config` 新增 `OverlayConfig` 字段供节点用,`Control` 新增 `SetOverlayConfig` 供内嵌节点用。
- 控制器专属:`-southbound-max-streams`(100,同一流数范围),`SouthboundHTTP2Config` 签名改为 `(maxStreamsPerConn int)`;`-log-ingest-max-concurrent`(8)/`-log-ingest-rate-bytes`(16MiB)接入既有 `Control.SetLogIngestLimits`;`-tunnel-event-store-max-bytes`(10GiB)接入既有 `Control.SetTunnelEventStoreCapacity`。
- 新增 `-tunnel-event-retention-days`(14)与 `Control.SetTunnelEventRetention`;`tunnel_events.go` 的常量 `tunnelEventRetentionDays` 改为 `tunnelEventStore.retentionDays` 字段,默认值来自新导出常量 `DefaultTunnelEventRetentionDays`。
- 所有取值在 `cmd/rpop`(新文件 `limits.go`)于 `flag.Parse()` 后立即校验,非法即 `log.Fatal` 并给出范围提示;窗口/流数复用 `overlay.ValidateConfig`/`control.ValidateSouthboundMaxStreamsPerConn`,其余四项(日志 ingest 并发/速率、隧道事件容量/保留期)要求为正数。
- 未开放项按契约不变:`maxDecompressedLogSegmentBytes`/`maxLogRecordLineBytes`/跳号阈值等协议安全边界常量,以及 `tunnelQueryWindowDays`/`maxOpenTunnelEventFiles`/`tunnelEventPruneInterval` 三个纯性能/实现细节常量均未改动。

**阶段 7 第 2 步实施记录**:`pathTransport` 的建连预算/冷却上下限从包级常量改为逐路径字段(`pathFailoverConfig`),由 `store.Upstream.Failover`(可选,`paths` 的兄弟字段)经 `resolveUpstream` 渲染进快照、`validatePaths` 新增的 `validateFailover` 校验范围(`upstreams[i].failover.xxx`);未配置时行为与常量时代完全一致。D19 主动探测落在新文件 `paths_probe.go`:`pathTransport.failed`/`succeeded` 驱动一个 `time.AfterFunc` 定时器,到期即复用 `dialer.DialPath` 探测一次(只建连不发字节),成功/失败分别调回 `succeeded`/`failed`;`siteRuntime.release` 统一收口定时器与在途拨号的 `context.CancelFunc`,`Engine.Handler` 的一次性预览 runtime 因为永不被 `release`,始终强制关闭探测。全局开关 `Engine.SetPathActiveProbe`(默认开)可被每上游 `Failover.ActiveProbe` 覆盖。前端在 `UpstreamFields` 新增折叠区块,纯逻辑落在 `failoverForm.js`。

**阶段 7 第 4 步(D29:access log 与隧道事件去重)** 的落地要点:

- 去重键:`accesslog.Record.DedupKey()` 返回 `trackId`,`overlay.TunnelEvent.DedupKey()` 返回 `tunnelId+"|"+nodeId+"|"+stage`;两者为空时都不去重,避免把互不相关的记录合并到一起。
- ClickHouse:`ensureTable` 新建分表已改用 `dedup_key String` 列 + `ReplacingMergeTree` + `ORDER BY (timestamp, site_id, dedup_key)`;探测到某表仍是旧 `MergeTree` 结构时记一条 Warn、不阻塞写入,手工迁移 SQL(rename 旧表→按新 DDL 建表→`INSERT ... SELECT timestamp, site_id, record, JSONExtractString(record,'trackId') FROM 旧表`→核对行数后 `DROP` 旧表)写在 `internal/accesslog/clickhouse.go` 的 `warnIfLegacyEngineLocked` 注释里;`Search` 对每个分表子查询加 `FINAL` 兜底。
- Elasticsearch:`_bulk` 的 `index` 动作补上 `"_id": dedupKey`,同 `_id` 写入天然幂等覆盖,零额外代码。
- 文件/S3:去重下沉到两者共用的 `paginate()`(`internal/accesslog/manager.go`),在分页切片前按 `dedup_key` 保留首条,`Total` 已是去重后的数量。
- 隧道事件:`tunnelEventStore.Write` 用有界(4096)、插入顺序淘汰的内存缓存短路重传事件,`Query` 再做一遍兜底去重;重启后缓存归零是已接受的残留风险,由 Query 的兜底覆盖。

## 6. 非目标

多路径负载均衡/加权分流;请求级透明重试;中继节点完全无入站(NAT 反向建链)。
