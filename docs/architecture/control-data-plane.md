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
- 尚未实现:D19 的可选主动探测(回切依赖冷却到期后的真实请求)、按上游配置降级参数、链路/路径健康的控制台展示(阶段 6)。

**阶段 5(日志与追踪)第 1 步** 定下以下契约,第 2 步(spool/回传)与第 3 步(控制器 ingest/配额/查询)据此实施:

- **日志模型**:访问日志沿用 `accesslog.Record`,新增 `trackId`、`tunnelId` 两个可选字段(空表示不适用)。隧道事件是新的独立记录 `overlay.TunnelEvent`:`timestamp`、`tunnelId`、`nodeId`、`role`(`entry`/`relay`/`exit`)、`stage`(`arrived`/`established`/`ended`)、`peer`、`bytesIn`/`bytesOut`(尽力而为,`ended` 事件采样时不等待另一方向收尾)、`duration`(到上一阶段的耗时)、`error`。两者不合并成一张表:访问日志按站点选择的适配器落盘;隧道事件是连接粒度、无站点概念(同一中继路由可能被多个站点共用),独立成事件流。第 2 步落盘 spool 时,用一个 `kind: "access"|"tunnel"` 的包装信封承载两者,写入同一 NDJSON 段文件,保持单一上传通道。
- **Track ID**:入口节点(站点监听所在节点)在 `dataplane.observeSite` 为每个请求生成 `Rpop-Track-Id`(UUIDv7,`internal/traceid.New()`,非加密随机源、无系统调用),无条件覆盖客户端携带的同名请求头再转发给上游——不信任、不透传客户端值,防止伪造轨迹或跨请求关联注入;只在 access log 中记下 `trackId`。
- **Tunnel ID**:仅当发起隧道的站点开启了访问日志(`AccessLog.AdapterID != ""`)才生成,由 `overlay.Overlay.tunnel()` 在打开 CONNECT 流时随 `Rpop-Tunnel-Id`/`Rpop-Tunnel-Log` 请求头下发。中继/出口(`serveRelay`,按 `route.Next==""` 区分二者)只信任这两个头,不查自己的静态路由表——同一路由 `Key` 可能被多个站点共用,能否记事件是逐次隧道决定的,不是路由的静态属性。请求头只在节点间 mTLS 链路上传递,不经过任何未认证的公网入口,不存在客户端伪造问题。数据面通过 `httptrace.ClientTrace.GotConn` 从 `net.Conn`(必要时先 `*tls.Conn.NetConn()` 解一层端到端 TLS)取回隧道实现的 `TunnelID() string`,写入 access log 的 `tunnelId`,从而把一次请求和它经过的隧道关联起来;一个隧道连接可承载多个请求(连接池化,P6),因此隧道事件本身不带 `trackId`。
- **有界异步队列**:`dataplane.logQueue`(已存在,访问日志)和新增的 `overlay` 隧道事件队列结构一致——channel 容量固定、`atomic` 计数已排队字节/丢弃数,满了直接丢弃并计数,从不阻塞转发(P8)。两者都以"写入接口"解耦落盘位置:`dataplane.AccessLogWriter` / `overlay.TunnelEventSink`,通过 `Engine.SetAccessLogWriter` / `Overlay.SetTunnelEventSink` 注入,不设置时退化为写本地 zap 日志。第 2 步新增 `internal/spool` 包,提供同时实现这两个接口的 writer,序列化为 spool 记录后落盘;不需要改动第 1 步的调用点。
- **spool 段文件**(第 2 步实施):目录 `<log-dir>/spool/`,文件名 `%020d.jsonl.gz`(段号,零填充,单调递增,一个 gzip NDJSON 文件一段);另有 `state.json` 记录 `nextSegment` 与本地已确认的 `ackedUpTo`,重启后据此续传。封段条件:当前段达到 8MiB 或已打开超过 30s(先到者),没有事件时不建段、不上传。每行是 `{"kind":"access"|"tunnel","record":...}` 信封。
- **南向 `logs` 协议**(第 2 步实施):节点按段号严格递增、逐段等待上一段 ACK 后再发下一段(简单的停等流控,足够,因为回传本就要限速,不追求管道化)。`POST /southbound/v1/logs`,请求头 `Rpop-Log-Segment: <uint64>`、`Content-Encoding: gzip`,body 为该段的 NDJSON;响应 `{"ack": <uint64>}`。控制器收到一段后:①若段号 ≤ 该节点已持久化的高水位(HWM),直接回 ACK(幂等重传,不重复写入);②否则按顺序把 `kind:"access"` 记录写入该站点选择的适配器、把 `kind:"tunnel"` 记录写入隧道事件存储;③写入成功后把 HWM 更新为该段号并持久化;④最后才 ACK。HWM 存在控制器 SQLite 的 `nodes` 表新增列 `log_hwm`(第 2 步加迁移),而不是内存,保证控制器重启后不重复写入也不遗漏。
- **配额与限速**(第 2 步实施,默认值先在此定下):spool 配额默认 2GiB(`RPOP_LOG_SPOOL_QUOTA_BYTES` 可配),超过时丢弃最旧的未上传段并计数告警(不是丢新段,新事件更有时效性);回传默认限速 4MiB/s(`RPOP_LOG_UPLOAD_RATE_BYTES` 可配),用令牌桶节流上传的读取,转发路径的带宽永远优先。
- **全路径时间线查询**(第 3 步实施,接口先在此定形):控制器新增 `GET /api/logging/trace/{trackId}` 返回该请求的 access log 记录(唯一一条)及其 `tunnelId`;再用 `GET /api/logging/tunnels/{tunnelId}` 返回该隧道 ID 在所有节点上报的全部 `TunnelEvent`,按 `timestamp` 排序即为入口→中继…→出口的完整时间线。查询直接扫已入库的隧道事件存储(第 2 步选型,复用 access log 适配器的按时间分区能力);控制台 UI 是阶段 6 的事。

## 6. 非目标

多路径负载均衡/加权分流;请求级透明重试;中继节点完全无入站(NAT 反向建链)。
