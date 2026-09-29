# rpop — 代理之上的反向代理

[English](README.md) | 简体中文

基于 Go 的多站点反向代理。每个站点都拥有独立的监听器和上游传输通道，SQLite 是配置与密钥数据的唯一可信来源。

## 开发

环境要求：Go 1.26+（SQLite 驱动使用 CGO）以及 Node.js/npm。

```sh
cd web && npm install && npm run build
cd ..
go run ./cmd/rpop -addr 127.0.0.1:8080 -db data/rpop.db -log-dir logs
```

`web/dist` 中的前端构建产物通过 `go:embed` 被嵌入二进制文件，因此单个 `rpop` 可执行文件同时提供 API 和控制台；执行 `npm run build` 后需要重新构建二进制文件。传入 `-web-dir web/dist` 可改为从磁盘提供控制台（便于前端开发迭代）。如果没有前端构建产物，嵌入的只是一个占位页面，控制台会返回 503。

React 开发服务器运行在 7106 端口，会将 `/api` 转发到 8080 端口上的 Go 服务。API 默认只监听 loopback（本地回环）地址。首次访问时，请在 Web 界面中设置管理员密码（至少 12 位）；所有管理类 API 都要求已认证的会话。请将管理 API 置于 loopback 或受信任的 HTTPS 反向代理之后。

## 容器镜像与 CI

`Dockerfile` 用 Node 构建控制台，将其嵌入一个静态链接的 CGO 二进制文件（musl），并基于 alpine 打包，容器时区为 `Asia/Shanghai`。镜像监听 `0.0.0.0:8080`，SQLite 存储在 `/app/data`，日志存储在 `/app/logs`（两者都应挂载为卷），健康检查使用无需认证的 `/api/health` 端点。`-addr`、`-db`、`-log-dir`、`-web-dir` 这几个参数默认分别读取环境变量 `RPOP_ADDR`、`RPOP_DB`、`RPOP_LOG_DIR`、`RPOP_WEB_DIR`；镜像已经设置了这些变量，其健康检查（`rpop -health-check`）探测的是 `RPOP_ADDR` 解析出的地址，因此请通过 `RPOP_ADDR` 而不是命令行的 `-addr` 来修改管理地址。

```sh
docker build --build-arg VERSION=dev -t rpop .
docker run -d --name rpop -p 127.0.0.1:8080:8080 -v rpop-data:/app/data -v rpop-logs:/app/logs rpop
```

管理端口只应发布在 loopback 上，或置于受信任的 HTTPS 反向代理之后。站点监听器使用的端口在控制台中配置，也需要一并发布（或者使用 `--network host` 运行），并把每个站点的监听地址设为容器内的 `0.0.0.0`。使用 `--network host` 时端口发布不再适用：请设置 `RPOP_ADDR=127.0.0.1:<port>` 来选择管理端口，并使其不暴露在公网接口上。

`deploy/docker-compose.example.yml`（配合 `deploy/.env.example`）会以可选的 RustFS（S3）、ClickHouse 和 Elasticsearch 服务运行该镜像，用于访问日志存储。通过 `COMPOSE_PROFILES`（`s3`、`clickhouse`、`elasticsearch`）按需启用，删除任何你不部署的服务；文件中的注释列出了每个服务对应的适配器设置。

预构建发行版：每个 `v*` 标签都会在 [GitHub Releases](https://github.com/taills/rpop/releases) 上发布 linux（amd64/arm64，静态链接）、macOS（amd64/arm64）和 windows（amd64）的归档文件，并附带一份 `SHA256SUMS` 文件，同时在 Docker Hub 上发布多平台镜像（linux/amd64、linux/arm64），标签为 `nil2026/rpop:<version>` 和 `nil2026/rpop:latest`；每次推送到 `main` 分支也会刷新 `nil2026/rpop:edge`。

```sh
docker run -d --name rpop -p 127.0.0.1:8080:8080 -v rpop-data:/app/data -v rpop-logs:/app/logs nil2026/rpop:latest
```

`.github/workflows/release.yml` 构建这些归档文件。go-sqlite3 需要 CGO，因此每个平台都在能为其编译 C 代码的 runner 上构建：linux 使用 `Dockerfile` 的 `binary` 目标，在原生的 amd64/arm64 runner 上构建（与镜像所用的同一份静态 musl 二进制文件），macOS 使用带 `-arch` 参数的 clang，windows 使用来自 MSYS2 的 MinGW-w64 gcc。Pull request 和推送到 `main` 分支会运行相同的构建，并将归档文件保留为工作流产物。`.github/workflows/docker.yml` 在原生 runner 上构建每个镜像平台，按 digest 推送，再合并成清单列表（manifest list）；它需要仓库变量 `DOCKERHUB_USERNAME` 和密钥 `DOCKERHUB_TOKEN`（一个具有读写权限的 Docker Hub access token）。`.github/workflows/dockerhub-description.yml` 会在 `deploy/DOCKERHUB.md` 于 `main` 分支上发生变化时，将其发布为 Docker Hub 的概览说明；更新仓库描述需要该 token 具备读/写/删除权限。这两个工作流都传入了 `--build-arg NPM_REGISTRY=https://registry.npmjs.org` 和 `--build-arg GOPROXY=https://proxy.golang.org,direct`，覆盖了 `Dockerfile` 默认使用的中国镜像源。

`.gitlab-ci.yml` 在 runner 自身的架构上原生构建，并在 `main`、`dev` 分支和标签上推送到私有 registry（标签只打版本号 tag，分支还会额外打 `latest`）；它不负责部署。所需的 CI/CD 变量：`DOCKER_REGISTRY_HOST`、`DOCKER_REGISTRY_USERNAME`、`DOCKER_REGISTRY_PASSWORD`（已脱敏）以及 `DOCKER_REGISTRY_MIRROR`（基础镜像的镜像源前缀，需要能提供 `library/node:22-alpine`、`library/golang:1.26-alpine` 和 `library/alpine:latest`）。

## 部署模式

一个二进制文件支持三种运行模式，通过 `-mode`（环境变量 `RPOP_MODE`）选择：

- `all-in-one`（默认）：控制器（控制台、API、SQLite）加上一个内置的、名为 `local` 的数据面节点。未设置 `config.nodes` 的站点会运行在这里，与单进程安装方式完全一致。
- `controller`：仅控制台和 API。节点通过其南向接口（southbound）监听地址 `-southbound-addr`（环境变量 `RPOP_SOUTHBOUND_ADDR`，默认 `:7443`）接入。当设置了 `-southbound-addr` 时，all-in-one 进程也可以接受远程节点接入。
- `node`：没有控制台的数据面节点。需要 `-controller https://controller:7443`（环境变量 `RPOP_CONTROLLER`）、`-data-dir`（环境变量 `RPOP_DATA_DIR`，默认 `data/node`），首次启动时还需要 `-join-token`（环境变量 `RPOP_JOIN_TOKEN`）。

用 `POST /api/nodes`（`{"id":"edge-1","name":"Edge 1"}`）创建节点；返回结果中包含一个一次性的 join token，有效期 24 小时。该 token 固定了控制器的内部 CA，因此节点首次接入时会以此验证控制器身份；随后节点会在本地生成密钥，并通过 CSR 获取证书。所有南向流量都通过双向 TLS 的 HTTP/2 传输。通过 `config.nodes: [edge-1, edge-2]` 可以把站点放到指定节点上运行；每当发布新版本时，控制器都会向每个节点推送该节点站点的完整快照，节点则会上报它应用的版本号、各站点的错误信息、正在运行的站点以及指标数据。如果某个站点在某个节点上应用失败（例如端口被占用），该节点会保留这个站点之前的配置。节点会缓存最近一次成功应用的快照（权限 `0600`，包括其对外提供的 TLS 私钥），即使控制器暂时不可达，重启后也能继续用这份快照提供服务。证书会在到期前 30 天自动续期。重新签发 token（`POST /api/nodes/{id}/token`）并重新注册会吊销该节点此前的证书；一个用 join token 启动的节点，如果控制器拒绝了它的证书，会自动重新注册。注册失败会按客户端地址限速。

节点的日志缓冲区（等待上传的访问日志和隧道事件）默认配额为 2 GiB，上传速率上限为 4 MiB/s，可通过 `-log-spool-quota-bytes`/`-log-upload-rate-bytes`（环境变量 `RPOP_LOG_SPOOL_QUOTA_BYTES`/`RPOP_LOG_UPLOAD_RATE_BYTES`）调整。覆盖网络（overlay）/南向接口的 HTTP/2 流控窗口与并发流数量限制（`-overlay-stream-window`、`-overlay-connection-window`、`-overlay-max-streams`、`-southbound-max-streams`）、控制器端日志接收的并发数与速率限制（`-log-ingest-max-concurrent`、`-log-ingest-rate-bytes`）、隧道事件存储的容量与保留期（`-tunnel-event-store-max-bytes`、`-tunnel-event-retention-days`）、控制台的时钟偏差告警阈值（`-clock-skew-warn-threshold`），以及 D19 主动路径探测（`-path-active-probe`，默认开启），都自带合理的默认值，并有对应的 `RPOP_*` 环境变量；完整的参数列表与默认值可运行 `rpop -h` 查看。

### 部署节点

在控制台中创建节点（“节点管理”页面，或点击 token 重置按钮）时，会弹出一个一次性的 join token 对话框，其中还包含节点接入向导：它会调用 `GET /api/nodes/bootstrap-info`（需要认证；返回控制器版本、进程模式、南向监听的开启状态/地址/端口，以及系统设置中的 `nodeControllerUrl`/`nodeImage`）来预填一个可用的 `-controller` 地址——如果运维人员配置了 `nodeControllerUrl`，就使用该设置，否则根据浏览器自身的主机名和南向端口猜测一个地址（IPv6 主机会加上方括号）——并且会随着该地址的编辑实时重新生成四份可直接复制的部署方案：纯命令行命令、systemd unit、`docker run` 命令，以及完整的 `docker-compose.yml`（token 保存在同目录的 `.env` 文件中，而不是写在 compose 文件里）。每份方案都对步骤编了号，每个命令/配置代码块都自带复制按钮，末尾还说明了注册成功后的样子，以及常见的失败原因（token 过期或已被使用、防火墙/端口不可达、CA 不匹配）。请在每个环境中设置一次系统设置里的“节点部署默认值”（`nodeControllerUrl`/`nodeImage`），这样向导就会默认使用你真实的控制器地址和镜像仓库，而不是靠同源猜测。如果控制器启动时没有开启南向监听，向导会显示警告，而不是给出一条实际无法工作的命令——此时应以 `controller` 模式运行，或者在 `all-in-one` 模式下设置 `-southbound-addr`。

最简 systemd unit 示例（向导自带的 systemd 标签页会生成更完整、加固过的版本——`ProtectSystem=strict`、`CAP_NET_BIND_SERVICE`、专用的 `rpop` 用户——以及围绕它的安装/环境变量文件/清理步骤）：

```ini
[Unit]
Description=rpop node
After=network-online.target

[Service]
User=rpop
EnvironmentFile=/etc/rpop/node.env
ExecStart=/usr/local/bin/rpop
Restart=always

[Install]
WantedBy=multi-user.target
```

最简 `docker-compose.yml` 示例（完整版本见 `deploy/docker-compose.node.example.yml`/`deploy/.env.node.example`，也可以让向导直接生成一份已经填好真实控制器地址和 token 的版本）：

```yaml
services:
  rpop-node:
    image: nil2026/rpop:latest
    restart: unless-stopped
    network_mode: host
    environment:
      RPOP_MODE: node
      RPOP_CONTROLLER: "https://controller.example.com:7443"
      RPOP_JOIN_TOKEN: "${RPOP_JOIN_TOKEN}"
      RPOP_DATA_DIR: /app/data
      RPOP_LOG_DIR: /app/logs
    volumes:
      - rpop-node-data:/app/data
      - rpop-node-logs:/app/logs
volumes:
  rpop-node-data:
  rpop-node-logs:
```

node 模式下的 `-health-check`（也就是上面提到的 Docker 镜像内置 `HEALTHCHECK` 所执行的检查）没有像 `controller`/`all-in-one` 那样的本地控制 API 监听可供探测；它检查的是 `-data-dir` 中是否保存了一份完整的节点身份（成功注册后得到的证书/私钥/CA）。一个从未注册成功的节点——join token 错误、控制器不可达、`-controller` 配置错误——会被上报为不健康，这正是运维最需要被暴露出来的故障；一旦注册成功，即使之后控制器出现短暂中断，节点也会持续上报健康，因为它会继续用缓存的快照提供服务，并自行重试注册，而不需要重启容器。

### 单端口部署

控制台（`-addr`）、南向接口（`-southbound-addr`）、节点的中继端口（由其 `relayAddress` 的端口推导出的通配地址，或者其自身的 `-relay-listen` 覆盖项），以及各站点监听器，都可以共用同一个物理端口。`internal/sharedport` 通过连接的第一个字节区分明文和 TLS 流量，再根据明文的 `Host` 头或 TLS SNI 分发给对应的占用者。这也解除了此前不允许在同一地址上混用明文和 TLS *站点*的限制——分发发生在第一个字节，与站点自身的模式无关。共享同一端口的两个占用者，其监听地址必须归一化后完全相同（`sharedport.NormalizeAddress`；通配地址——`""`/`0.0.0.0`/`::`/`*`——不能和具体地址共享端口），并且一旦某个地址上有一个以上的占用者，该地址上的每个站点都需要设置 `hostnames`（对 TLS 站点而言即 SNI）才能与其他占用者区分开。

保存或启动站点时，系统会预先校验这些规则，并在配置真正生效之前就返回中文错误提示，而不是等配置真的被使用时才失败。控制台、南向接口，以及该站点所放置的每个节点的中继端口，始终会被纳入比较，因为无论是否有站点在运行，它们都会占用自己的地址；而共享同一节点的其他站点，只有在其真正运行时（或者它自己正在被启动/重启）才会被纳入比较，因此把多个站点配置保存在同一地址下、再逐个启动而不发生冲突，依然是可行的。节点自身的 `-relay-listen` 覆盖项对控制器不可见（预校验只能看到它根据 `relayAddress` 猜测出的通配地址），因此这一类不匹配只能留给节点在真正尝试绑定时自行报告。控制器启动时自动启动的站点（`autoStart: true`，由 `StartAutoSites` 执行）会跳过这一预校验，转而走真正的准入检查；在这场竞争中落败的站点会独自失败并保留其之前的配置（D8），并记录日志，而不会阻塞其他自动启动的站点。完整的“共享端口”决策与三个阶段的实现记录见 `docs/architecture/control-data-plane.md` §2/§5。

下面三个示例可以按原样运行，只需把地址/域名换成真实值：

**(a) all-in-one：控制台与站点共用一个端口。** `-console-hostnames` 会把控制台限制在指定的 Host 上；如果某个 Host 既不属于任何站点，也不在控制台自己的列表中，会返回 404：

```sh
rpop -addr 0.0.0.0:8443 -console-hostnames console.example.com -db data/rpop.db -log-dir logs
```

创建一个 `listenAddress`/`listenPort` 归一化后与 `-addr` 相同（这里是 `"0.0.0.0"` + `8443`）、且 `hostnames` 与 `console.example.com` 不同（例如 `["app.example.com"]`）的站点。这样控制台会响应 `Host: console.example.com`，站点则响应自己的域名，二者在共享端口上互不干扰。

**(b) `controller` 模式：控制台与南向接口共用一个端口。** 控制台是明文的，南向接口是 TLS（节点通过 mTLS 接入），因此仅凭第一个字节就能区分二者，不需要额外配置：

```sh
rpop -mode controller -addr 0.0.0.0:8443 -southbound-addr 0.0.0.0:8443 -db data/rpop.db -log-dir logs
```

节点仍然把这同一个地址用作 `-controller`（例如 `https://controller.example.com:8443`）。

**(c) `node` 模式：节点的中继端口与放置在该节点上的站点共用一个端口。** 节点默认会在 `relayAddress` 端口对应的通配地址上绑定其中继端口（除非用 `-relay-listen` 覆盖）；把站点放在同一个端口上即可实现共享：

```sh
rpop -mode node -controller https://controller.example.com:7443 -join-token <join-token> -data-dir data/node -log-dir logs
```

与之匹配的站点：`config.nodes` 包含该节点的 ID，`listenPort` 等于该节点 `relayAddress` 的端口，`listenAddress` 留空以匹配节点默认的通配绑定，`tls: true`，且 `hostnames` 要同时避开节点自身的证书名（`<nodeID>.nodes.rpop`）和控制器的证书名（`controller.rpop`）。中继监听器本身只有在该节点确实在为另一个节点的路径做中继时（其快照中出现非空的 `Relay` 条目，参见 `internal/overlay.Overlay.applyRelayLocked`）才会绑定——一个从未被任何路径经由的节点永远不会打开这个端口，所以在那之前，该端口只由这个站点单独占用（不算冲突，只是还没真正共享）。

> **安全提示**：一旦控制台以明文端口（不启用 TLS）提供服务，登录密码和会话 Cookie 就会以明文传输——如果示例 (a)/(b) 需要接受来自不受信任网络的连接，请把控制台放在 TLS 反向代理之后，而不是直接把明文控制台暴露到公网，这与前文建议的“将控制台保持在 loopback 或受信任的 HTTPS 反向代理之后”是一致的。

### 跨节点的上游路径

上游可以通过其他节点和外部代理到达源站，而不必直接连接。`paths` 按优先级列出候选路径；每个 `via` 是节点和已注册代理的有序组合，空的 `via` 表示直连。直接在上游上设置 `via` 是只有单条路径时的简写形式。

```json
{
  "upstreams": [
    {
      "url": "https://example.com",
      "paths": [
        { "via": [{"proxy": "socks5-A"}, {"node": "node2"}, {"node": "node3"}, {"proxy": "socks5-B"}] },
        { "via": [{"proxy": "socks5-A"}, {"node": "node2"}, {"node": "node3"}] },
        { "via": [{"node": "node2"}, {"node": "node3"}] },
        { "via": [{"node": "node3"}] },
        { "via": [] }
      ]
    }
  ]
}
```

第一条路径是 `client > node1 > socks5-A > node2 > node3 > socks5-B > origin`；最后一条（`via: []`）为直连。

- 站点自身所在的节点（这里是 `node1`，来自 `config.nodes`）负责终结客户端连接，并保留全部 HTTP 逻辑：路由、访问日志、指标，以及上游的 TLS 设置。上游 TLS 是从该节点到源站端到端建立的，因此中继和代理始终只能看到密文。
- 中继节点不服务任何站点。每个中继节点开放一个中继端口，只接受持有控制器 CA 签发证书的节点连接，并且只会沿着控制器为来源节点计算好的路由转发隧道。给中继节点配置一个 `relayAddress`（其他节点拨号使用的 `host:port`）；它会在所有网卡上绑定该端口，如果端口转发把它映射到了别处，则使用 `-relay-listen`（环境变量 `RPOP_RELAY_LISTEN`）。正在为某个站点中继的节点不能被删除，也不能被清空其中继地址。
- 位于第一个节点之前的代理，承载的是入口到它之间的连接；两个节点之间的代理，承载的是这两个节点之间的连接；最后一个节点之后的代理，由出口节点直接拨号，因此与出口代理的 SOCKS 握手就发生在出口节点旁边。连续的代理会被串联起来。
- 节点之间的链路是持久化的双向 TLS（mTLS）HTTP/2 连接，会为每条候选路径保持活跃，每条隧道都是其上的一个 stream，因此节点间的请求不需要为 TCP、代理或 TLS 握手付出额外代价。中继使用带缓冲池的拷贝并逐块刷新，因此 SSE 和 WebSocket 流量不会在任何一跳被阻塞。
- 入口使用第一条能建立连接的路径。如果在发送任何请求字节之前建连就失败了（某个节点、链路或代理宕机，某个中继拒绝连接，出口无法到达源站，或超过 10 秒），会立即切换到下一条路径；失败的路径会以指数退避（1 秒到 1 分钟）进入冷却，之后再重试。已知处于宕机状态的链路会被直接跳过，无需等待。已经到达上游的请求永远不会被重新发送。每条路径都有自己独立的连接池，因此故障恢复后，新请求会使用首选路径，而走备用路径的请求会不受干扰地正常结束。
- all-in-one 控制器内置的节点可以作为这类路径的入口，但不能作为中继。

用 `POST /api/proxies`（`{"id":"socks5-A","name":"A","type":"socks5h","address":"10.0.0.1:1080","username":"u","password":"p"}`）注册代理；`type` 可以是 `socks5`（由节点解析域名）、`socks5h`（由代理解析域名）、`http` 或 `https`。密码只写不读：`PUT /api/proxies/{id}` 在省略 `password` 时保留已存储的密码，传空字符串则清空密码。被站点使用的代理不能删除。`paths` 与 `proxyUrl` 互斥。

## 本版本具备的能力

- 站点的 HTTPS 既可以使用系统设置中集中管理的服务器证书（`config.certificateId`），让多个站点共用一张泛域名证书，也可以使用站点专属的 `certificateSecret`/`privateKeySecret` 上传；二者互斥。
- 站点配置和二进制密钥（`site_secrets`）都存储在 SQLite 中；数据库和日志目录以受限的目录权限创建。
- 支持按站点启动、停止、重启和重新加载；标记为 `autoStart: true` 的站点会在进程启动时自动启动。监听地址、端口和 TLS 模式相同的站点会共用一个监听器，并按 `config.hostnames` 路由；对 HTTPS 而言，由 SNI 选择证书、由 HTTP Host 选择站点（Host 不匹配时回退到 SNI）。HTTPS 监听器支持 HTTP/2（WebSocket 仍使用 HTTP/1.1）。同一监听器上的重新加载会原子地替换站点：包括 SSE 流和 WebSocket 在内的进行中请求会在旧版本上完成，应用失败的配置不会影响正在运行的版本继续提供服务。
- 每个上游都有自己独立的传输通道，支持 HTTP/HTTPS、HTTP(S) 代理、SOCKS5/SOCKS5H、按站点选择的系统 CA 根证书、旧版自定义 CA Bundle、可选的双向 TLS（mTLS）Client 证书（可以是系统集中管理的 Client 证书，也可以是站点专属的证书/私钥引用）、跳过证书校验、自定义 server name/SNI，以及直连模式下的拨号地址覆盖。
- 类似 Caddy 的请求路由（`config.routes`）按路径和 Header 把请求分发给不同的上游。路径默认精确匹配，除非以 `*` 结尾，此时变为前缀匹配（`/api/*` 匹配 `/api/x` 但不匹配 `/api`；`/api*` 则同时匹配 `/api` 和 `/apix`）；匹配不区分大小写，且基于清理过的路径（已解析 `..` 和 `//`）。Header 条件支持精确匹配、`prefix*`、`*suffix` 或 `*contains*`（区分大小写；同一个 Header 的多个值之间是 OR，不同 Header 之间是 AND），`values` 为空时要求该 Header 存在，或用 `absent: true` 要求该 Header 不存在；`Host` 匹配的是请求的主机名。与 Caddy 的 `handle` 一样，最具体的规则获胜：路径更长的优先、精确匹配优先于前缀匹配、Header 条件更多的优先，最后按配置顺序。`stripPrefix` 会先去掉匹配到的前缀（对应 Caddy 的 `handle_path`），再拼接上上游 URL 自身的路径。未命中任何规则的请求会转发给默认上游 `upstreams[0]`。
- 站点编辑器可以管理任意数量的上游（可以指定默认上游，增删上游时会自动重新映射相关路由）和路由规则，并内置了路由模拟器：输入 URL、方法和 Header，即可看到命中了哪条规则、其他规则为何未命中，以及重写前后的路径。它通过 `POST /api/routes/simulate`，用处理实际流量的同一套服务端代码来评估尚未保存的配置。
- 多跳的 `paths`（见下文）有专门的编辑器，以有序的节点/代理跳数列表展示每条候选路由；路由模拟器的响应还会渲染出命中上游的完整候选路径、每一跳的实时健康状况，以及当前引擎会选择哪条路径。
- 只有当站点的 `config.accessLog.adapterId` 选择了一个已配置的适配器时，才会产生访问日志；ID 为空则关闭访问日志。请求/响应体采集和敏感 Header 选项都是按站点配置的。敏感 Header 默认会被脱敏，除非显式开启。日志写入使用有界的异步队列，因此存储 I/O 不会阻塞请求转发；队列打满时会丢弃记录，并增加该站点的日志丢弃指标。
- 按站点维护的内存指标：请求数/错误数、状态码分布、进行中请求数、进出字节数、平均 TTFB、平均和最大响应时间，以及近似的 P95 响应时间。
- 可以在系统设置页面完成管理员密码的初始设置、登录/登出和改密。密码使用 PBKDF2-HMAC-SHA256 哈希存储；界面使用 HttpOnly、SameSite 的会话 Cookie，登录失败会被限速。
- 可插拔的访问日志存储：可以配置多个具名的本地文件、ClickHouse HTTP、Elasticsearch 和 S3/兼容 S3 的适配器，同一类型也可以配置多个。每个站点选择自己的适配器；站点不能选择未配置的适配器。文件适配器可以按小时、按天或按大小（界面中例如 `1G`）轮转，支持 gzip 归档，并可配置保留的归档文件数量。
- 系统设置提供唯一的时区入口 `timeZone`（默认 `UTC`；支持 `UTC` 或 `Asia/Shanghai` 等 IANA 时区），UI 使用可搜索但不可自由输入的时区下拉框；系统设置内另有独立的“上游 CA 根证书”标签页，支持多个具名证书的增删改查。设置持久化到 SQLite `app_settings`，通过 `GET/PUT /api/settings` 管理。CA 根证书以 PEM 保存，在站点上游配置中按站点选择；所选自定义 CA 会加入操作系统默认信任池，站点已有 `caBundle` 也继续支持。系统设置另有“站点 HTTPS 证书”标签页，可预先添加站点 HTTPS 证书（如 `*.example.com` 泛域名证书，可附带中间证书链）供多个站点共用；证书须允许 serverAuth，界面会展示 SAN 域名并提示站点域名是否被覆盖。站点 HTTPS 证书使用随机的稳定 ID：续期时直接替换证书内容即可，ID 不变，所有引用它的运行中站点会立即对新 TLS 握手使用新证书，无需重启。还有“mTLS Client 证书”标签页，集中管理访问需要双向 HTTPS 认证的上游时使用的 Client 证书（可附带中间证书）与未加密私钥：保存时校验证书与私钥匹配、证书非 CA 且允许 TLS 客户端认证；私钥只写不读，接口仅返回证书、主题、签发者、有效期与 `hasPrivateKey`。更新时省略 `privateKeyPem` 会保留同 ID 条目的已存私钥，请求中省略整个 `clientCertificates` / `serverCertificates` 字段则保持现有列表不变。站点 HTTPS 证书与 mTLS Client 证书被站点引用时都不能删除。系统设置还统一控制本地文件的日/小时轮转，以及 S3、ClickHouse、Elasticsearch 的日/小时分区；日志时间戳和搜索时间范围仍表示绝对时间。远程适配器的 `splitMode` 为 `none|day|hour`：S3 在对象键中写入日期/小时目录，ClickHouse 使用 `_YYYYMMDD` 或 `_YYYYMMDDHH` 表后缀，Elasticsearch 使用 `-YYYYMMDD` 或 `-YYYYMMDDHH` 索引后缀。搜索兼容基础资源、历史分割模式和时区变更前写入的数据。旧配置缺少系统时区时默认 UTC；S3 继续默认 `hour`，ClickHouse/Elasticsearch 继续默认 `none`，以保持资源命名兼容。大小轮转仅适用于本地文件；S3 仍为每条日志写入一个 gzip 对象。
- Elasticsearch 写入使用 HTTP Bulk API，过滤/分页查询使用 Search API。需要配置索引和 HTTP(S) 集群地址，认证方式可以是无认证、Basic 认证或 API Key（粘贴 Base64 值）。Elasticsearch 账号需要具备对目标基础/分区索引的写入和搜索权限；如果索引尚不存在，还需要自动创建索引的权限。ClickHouse 的分割模式搜索会通过 `system.tables` 枚举匹配的表，因此其账号除了要能读取这些元数据，还要能创建、写入和查询日志表。
- 每条访问日志记录都包含标准 combined 日志格式的字段：客户端 IP 和端口（TCP 对端）、原始的 `X-Forwarded-For` 值（不会被信任用作 `clientIp`，因为客户端可以自行设置这个值）、scheme 和 TLS 版本、`Host`、方法、请求 URI、协议、状态码、字节数、`Referer`、`User-Agent`、上游 URL（凭证已脱敏），以及命中的路由规则（使用默认上游时为空），再加上 TTFB 和总响应时间。
- 已认证的 Web 日志搜索，支持按站点、状态码、文本和时间过滤，支持分页，并可查看请求/响应的 Header 和 Body 详情。
- 控制台的“节点管理”页面覆盖节点的完整生命周期（创建、重命名/修改中继地址、重新签发 join token、删除），以及每个节点的覆盖网络链路、上游路径和日志缓冲区健康状况；“拓扑”页面以图的形式展示每个节点被推断出的入口/中继/出口角色，以及节点之间的链路；“代理”页面管理具名的覆盖网络代理。
- “追踪”页面可以通过 `Rpop-Track-Id` 或隧道 ID 查找一个请求的完整链路，按入口/中继/出口逐跳展示耗时，并带有协议版本和时钟偏差标记，此外还有一个可选的客户端开关，可以按上报节点的时钟偏差校正显示的时间线（仅影响展示，从不改写存储的时间戳）。

## 站点配置示例

`POST /api/sites` 的请求体（`PUT /api/sites/{id}` 同样接受）：

```json
{
  "id": "example",
  "name": "Example site",
  "autoStart": true,
  "config": {
    "listenAddress": "127.0.0.1",
    "listenPort": 8443,
    "hostnames": ["app.example.test", "api.example.test"],
    "tls": true,
    "certificateSecret": "site-cert",
    "privateKeySecret": "site-key",
    "accessLog": {
      "adapterId": "default",
      "includeBodies": true,
      "maxBodyBytes": 1048576
    },
    "upstreams": [
      {
        "url": "https://origin.example.test",
        "proxyUrl": "socks5://127.0.0.1:1080",
        "proxyType": "proxy",
        "serverName": "origin.example.test",
        "dialAddress": "192.0.2.10:443",
        "insecureSkipVerify": false,
        "clientCertSecret": "upstream-client-cert",
        "clientKeySecret": "upstream-client-key"
      },
      { "url": "http://10.0.0.5:8080/v1" }
    ],
    "routes": [
      { "path": "/api/*", "stripPrefix": true, "upstream": 1 },
      {
        "path": "/api/*",
        "headers": [{ "name": "X-Canary", "values": ["1", "true"] }],
        "upstream": 0
      }
    ]
  }
}
```

第一条路由把 `/api/users` 发送到 `http://10.0.0.5:8080/v1/users`（前缀已被去除）。第二条规则更具体（同时匹配了 `X-Canary` Header），因此对于 `/api/*` 上的灰度（canary）流量会命中它，路径保持不变，转发给默认上游（`upstreams[0]`）。

`rootCertificateIds` 通过 SHA-256 DER 指纹 ID，从系统设置中选择一个或多个 CA 证书。选中的根证书会被加入该上游的操作系统信任池；同时也兼容旧版的上游 `caBundle`。如果没有选择任何系统根证书，显式设置的 `caBundle` 会保留原有的排他性信任池行为，而空 Bundle 则使用操作系统默认信任池。对于双向 TLS（mTLS），可以将 `clientCertificateId` 设为系统设置中管理的某个 Client 证书（其叶子证书的 SHA-256 指纹），或者配置 `clientCertSecret`/`clientKeySecret`；这两种方式互斥，且都要求上游为 HTTPS。被任何站点选用的系统 Client 证书不能删除。站点专属的证书和私钥字节保存在 SQLite 的 secrets 中，按名称引用，而不是直接内联传输。可以用 `PUT /api/sites/{siteId}/secrets/{name}`，以 `application/octet-stream` 请求体上传 PEM 字节。已认证的 `DELETE /api/sites/{siteId}/secrets/{name}` 接口可以删除未被使用的密钥。

## API

- `GET /api/health`（公开的存活探测端点）
- `GET /api/auth/status`, `POST /api/auth/setup`, `POST /api/auth/login`, `POST /api/auth/logout`, `PUT /api/auth/password`
- `GET /api/logging`（列出已配置的适配器）、`POST /api/logging/adapters`、`PUT/DELETE /api/logging/adapters/{id}`
- `GET /api/logs?adapterId=...&q=...&siteId=...&status=...&from=...&to=...&page=1&pageSize=25`（已认证的搜索；传入 `siteId` 时会自动选择其绑定的适配器）
- `GET /api/logging/trace/{trackId}` 返回该请求对应的单条访问日志记录（会搜索所有已配置的适配器）；`GET /api/logging/tunnels/{tunnelId}` 返回该隧道在各节点上上报的 `TunnelEvent`，按时间戳排序，每条都携带上报节点当前的时钟偏差。
- `GET /api/sites`, `POST /api/sites`
- `GET /api/sites/{id}`, `PUT /api/sites/{id}`, `DELETE /api/sites/{id}`
- `POST /api/sites/{id}/start|stop|restart|reload`
- `GET /api/sites/{id}/metrics` 返回当前进程的指标（进程重启会重置），是该站点所在各节点数据的汇总，`byNode` 中还有每个节点各自的数值。平均值按请求数加权；P95 取各节点 P95 中的最大值。
- `POST /api/routes/simulate` 针对一个示例请求评估尚未保存的站点路由规则；如果命中的上游配置了 `paths`，响应中还会包含完整的多跳候选路径、每一跳的健康状况，以及会选中哪条路径。
- `GET /api/nodes`、`POST /api/nodes`（返回一个 join token）、`GET/PUT/DELETE /api/nodes/{id}`、`POST /api/nodes/{id}/token`（重新签发）。被站点使用的节点不能删除。节点条目会上报 `registered`、`online`、`appliedRevision`、`publishedRevision`、`inSync`、按站点的 `errors`、正在 `running` 的站点、覆盖网络 `links`（对端、地址、连接数、打开的隧道数、失败次数）、上游 `paths` 健康状况、`certGeneration`、`protocolVersion`/`protocolStatus`、`clockSkewMillis`/`clockSkewStatus`，以及中继端口绑定失败时的 `relayError`。
- `GET /api/topology` 返回每个节点（附带其推断出的 `entry`/`relay`/`exit` 角色）、节点之间的有向链路（健康状况、代理链、连接数/隧道数计数器），以及已注册的具名代理，供控制台的拓扑图使用。
- `GET /api/proxies`、`POST /api/proxies`、`PUT/DELETE /api/proxies/{id}`。条目会上报 `hasPassword`，以及在 `usedBy` 中列出使用该代理的站点。
- `PUT/DELETE /api/sites/{id}/secrets/{name}`

## 安全性与当前限制

- TLS 的 `InsecureSkipVerify` 是刻意设计为需要主动开启的选项，开启后会关闭对上游证书的校验；不要把它当作日常的临时绕过手段来使用。
- 站点密钥和外部日志适配器的凭证都保存在 SQLite 中，**并未做静态加密**。管理员密码以 PBKDF2 哈希形式存储。请保护好数据库备份和文件系统访问权限；在用于生产环境之前，请补充密钥管理/加密层。
- 认证会话保存在进程内存中，12 小时后过期；进程重启会使所有会话失效。请将管理 API 置于 HTTPS 之后。当 TLS 在反向代理处终结时，请将该受信任代理配置为设置 `X-Forwarded-Proto: https`，并剥离客户端自行携带的转发类 Header。
- 访问日志的 Body 中可能包含凭证、个人信息或业务数据。Body 采集默认上限为 1 MiB；正数上限最高为 8 MiB，`maxBodyBytes: -1` 则关闭截断。不设上限的采集会占用较多内存，并且如果异步日志队列被打满，访问日志条目仍可能被丢弃；敏感 Header 默认会被脱敏。请限制日志文件的访问权限，并在不需要时关闭 Body 日志记录。
- 指标保存在内存中，进程重启会重置。访问日志通过配置的适配器异步写入；`droppedAccessLogCount` 上报的是有界队列打满时被丢弃的记录数。进程自身的 zap 诊断日志仍是独立的本地 `rpop.log` 文件。
- 每条路由只能指向一个上游；故障切换只发生在同一个上游的多条路径之间。跨上游/跨路径的健康检查与负载均衡、配置版本管理/回滚，以及优雅的零停机监听器替换，仍是后续工作。
- 节点只会收到它实际会经由的那些代理的凭证，这些凭证通过双向 TLS 内联在快照中下发，并随快照一起被缓存（权限 `0600`）。
- 自定义的 `dialAddress` 仅在直连模式下生效；通过上游代理进行自定义目标地址解析需要显式的策略，不会被默默实现。
- 文件适配器的搜索会扫描当前和已归档的本地日志文件。S3 搜索使用选定的日期前缀，并基于分隔符对直接对象做列举，以兼容早期未分割的目录结构；时间范围较宽或跨越不同分割模式时，仍可能需要枚举更多 key，因此对于需要大量可搜索日志的场景，建议优先使用 ClickHouse。ClickHouse 会同时搜索配置的基础表及其日/小时分表。
- 独立的 zap 诊断日志文件目前不会自动轮转；如有需要，请为 `rpop.log` 配置外部轮转方案。

## 许可证

rpop 基于 [MIT 许可证](LICENSE) 发布。
