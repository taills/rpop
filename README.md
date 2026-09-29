# rpop — Reverse Proxy over Proxy

Go-based multi-site reverse proxy. Every site has an independent listener and upstream transport, with SQLite as the source of truth for configuration and secret material.

## Development

Requirements: Go 1.26+ (SQLite driver uses CGO) and Node.js/npm.

```sh
cd web && npm install && npm run build
cd ..
go run ./cmd/rpop -addr 127.0.0.1:8080 -db data/rpop.db -log-dir logs
```

The frontend build in `web/dist` is embedded into the binary with `go:embed`, so a single `rpop` executable serves both the API and the console; rebuild the binary after `npm run build`. Pass `-web-dir web/dist` to serve the console from disk instead (useful while iterating on the frontend). Without a frontend build only a placeholder is embedded and the console responds with 503.

The React development server runs on port 7106 and forwards `/api` to the Go service on port 8080. The API defaults to loopback. On first visit, set the administrator password in the Web UI (at least 12 characters); all management APIs require an authenticated session. Keep the admin API behind loopback or a trusted HTTPS reverse proxy.

## Container image and CI

`Dockerfile` builds the console with Node, embeds it into a statically linked CGO binary (musl), and ships it on alpine with `Asia/Shanghai` as the container time zone. The image listens on `0.0.0.0:8080`, stores SQLite in `/app/data` and logs in `/app/logs` (mount both as volumes), and uses the unauthenticated `/api/health` endpoint for its health check. The `-addr`, `-db`, `-log-dir`, and `-web-dir` flags default to the `RPOP_ADDR`, `RPOP_DB`, `RPOP_LOG_DIR`, and `RPOP_WEB_DIR` environment variables; the image sets them, and its health check (`rpop -health-check`) probes whatever `RPOP_ADDR` resolves to, so change the admin address through `RPOP_ADDR` rather than a command-line `-addr`.

```sh
docker build --build-arg VERSION=dev -t rpop .
docker run -d --name rpop -p 127.0.0.1:8080:8080 -v rpop-data:/app/data -v rpop-logs:/app/logs rpop
```

Publish the admin port only on loopback or behind a trusted HTTPS reverse proxy. Site listeners use ports configured in the console, so publish them as well (or run with `--network host`) and set each site's listen address to `0.0.0.0` inside the container. With `--network host`, port publishing does not apply: set `RPOP_ADDR=127.0.0.1:<port>` to choose the admin port and keep it off public interfaces.

`deploy/docker-compose.example.yml` (with `deploy/.env.example`) runs the image with optional RustFS (S3), ClickHouse, and Elasticsearch services for access-log storage. Enable them through `COMPOSE_PROFILES` (`s3`, `clickhouse`, `elasticsearch`) and delete any service you do not deploy; the file's comments list the adapter settings for each service.

Prebuilt releases: every `v*` tag publishes archives for linux (amd64/arm64, statically linked), macOS (amd64/arm64), and windows (amd64) on [GitHub Releases](https://github.com/taills/rpop/releases) with a `SHA256SUMS` file, and a multi-platform image (linux/amd64, linux/arm64) on Docker Hub as `nil2026/rpop:<version>` and `nil2026/rpop:latest`; every push to `main` also refreshes `nil2026/rpop:edge`.

```sh
docker run -d --name rpop -p 127.0.0.1:8080:8080 -v rpop-data:/app/data -v rpop-logs:/app/logs nil2026/rpop:latest
```

`.github/workflows/release.yml` builds those archives. go-sqlite3 needs CGO, so each platform compiles on a runner that can build C for it: linux uses the Dockerfile's `binary` target on native amd64/arm64 runners (the same static musl binary the image ships), macOS uses clang with `-arch`, and windows uses MinGW-w64 gcc from MSYS2. Pull requests and pushes to `main` run the same builds and keep the archives as workflow artifacts. `.github/workflows/docker.yml` builds each image platform on a native runner, pushes it by digest, and merges the manifest list; it needs the repository variable `DOCKERHUB_USERNAME` and the secret `DOCKERHUB_TOKEN` (a Docker Hub access token with read/write scope). `.github/workflows/dockerhub-description.yml` publishes `deploy/DOCKERHUB.md` as the Docker Hub overview whenever it changes on `main`; updating a repository description needs that token to carry read/write/delete scope. Both workflows pass `--build-arg NPM_REGISTRY=https://registry.npmjs.org` and `--build-arg GOPROXY=https://proxy.golang.org,direct`, overriding the Dockerfile's default China mirrors.

`.gitlab-ci.yml` builds natively on the runner's architecture and pushes to the private registry on `main`, `dev`, and tags (tags get only the version tag; branches also get `latest`); it does not deploy. Required CI/CD variables: `DOCKER_REGISTRY_HOST`, `DOCKER_REGISTRY_USERNAME`, `DOCKER_REGISTRY_PASSWORD` (masked), and `DOCKER_REGISTRY_MIRROR` (base-image mirror prefix, which must provide `library/node:22-alpine`, `library/golang:1.26-alpine`, and `library/alpine:latest`).

## Deployment modes

One binary runs in three modes, selected with `-mode` (env `RPOP_MODE`):

- `all-in-one` (default): the controller (console, API, SQLite) plus an embedded data-plane node named `local`. Sites without `config.nodes` run here, exactly as in a single-process install.
- `controller`: the console and API only. Nodes connect to its southbound listener, `-southbound-addr` (env `RPOP_SOUTHBOUND_ADDR`, default `:7443`). An all-in-one process also accepts remote nodes when `-southbound-addr` is set.
- `node`: a data-plane node without a console. It needs `-controller https://controller:7443` (env `RPOP_CONTROLLER`), `-data-dir` (env `RPOP_DATA_DIR`, default `data/node`), and on its first start `-join-token` (env `RPOP_JOIN_TOKEN`).

Create a node with `POST /api/nodes` (`{"id":"edge-1","name":"Edge 1"}`); the response contains a single-use join token valid for 24 hours. The token pins the controller's internal CA, so the node authenticates the controller on first contact; the node then generates its key locally and receives a certificate through a CSR. All southbound traffic is HTTP/2 with mutual TLS. Place a site on nodes with `config.nodes: [edge-1, edge-2]`; the controller streams each node a full snapshot of its sites whenever a new revision is published, and the node reports the revision it applied, per-site errors, running sites, and metrics. A site that fails to apply on a node (for example, a port in use) keeps its previous configuration there. The node caches the last applied snapshot (mode `0600`, including the TLS keys it serves) and serves it after a restart even while the controller is unreachable. Certificates renew automatically 30 days before expiry. Reissuing a token (`POST /api/nodes/{id}/token`) and registering again revokes the node's earlier certificates; a node started with a join token re-registers by itself when the controller rejects its certificate. Failed registrations are rate-limited per client address.

A node's log spool (access logs and tunnel events awaiting upload) defaults to a 2 GiB quota uploaded at up to 4 MiB/s, tunable with `-log-spool-quota-bytes`/`-log-upload-rate-bytes` (env `RPOP_LOG_SPOOL_QUOTA_BYTES`/`RPOP_LOG_UPLOAD_RATE_BYTES`). Overlay/southbound HTTP/2 flow-control windows and stream limits (`-overlay-stream-window`, `-overlay-connection-window`, `-overlay-max-streams`, `-southbound-max-streams`), controller-side log-ingest concurrency and rate limits (`-log-ingest-max-concurrent`, `-log-ingest-rate-bytes`), the tunnel-event store's capacity and retention (`-tunnel-event-store-max-bytes`, `-tunnel-event-retention-days`), the console's clock-skew warning threshold (`-clock-skew-warn-threshold`), and D19 active path probing (`-path-active-probe`, on by default) all ship with sane defaults and a matching `RPOP_*` environment variable; run `rpop -h` for the full flag list and defaults.

### Deploying a node

Creating a node in the console (Nodes page, or the token reset button) opens a one-time join-token dialog that also renders a node onboarding guide: it calls `GET /api/nodes/bootstrap-info` (authenticated; returns the controller's version, process mode, southbound listen state/address/port, and the `nodeControllerUrl`/`nodeImage` system settings) to prefill a working `-controller` address — the operator's configured `nodeControllerUrl` setting when set, otherwise a guess built from the browser's own hostname and the southbound port (IPv6 hosts get bracketed) — and regenerates four ready-to-copy recipes live as that address is edited: a plain CLI command, a systemd unit, a `docker run` invocation, and a full `docker-compose.yml` (token kept in a sibling `.env`, not in the compose file itself). Each recipe numbers its steps, gives every command/config block its own copy button, and ends with what a successful registration looks like plus common failure causes (expired/used token, firewall/port reachability, CA mismatch). Set System Settings' "节点部署默认值" (`nodeControllerUrl`/`nodeImage`) once per environment so the guide defaults to your real controller URL and image registry instead of a same-origin guess. If the controller was not started with a southbound listener, the guide shows a warning instead of a command that would not work — run it in `controller` mode, or set `-southbound-addr` on `all-in-one`.

Minimal systemd unit (the guide's own systemd tab produces a fuller, hardened version — `ProtectSystem=strict`, `CAP_NET_BIND_SERVICE`, a dedicated `rpop` user — plus the install/env-file/cleanup steps around it):

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

Minimal `docker-compose.yml` (see `deploy/docker-compose.node.example.yml`/`deploy/.env.node.example` for the full version, or let the guide generate one with the real controller address and token filled in):

```yaml
services:
  rpop-node:
    image: rpop:1.4.0
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

Node mode's `-health-check` (what the Docker image's built-in `HEALTHCHECK` runs, see above) has no local control API listener to probe the way `controller`/`all-in-one` do; instead it checks that `-data-dir` holds a complete node identity (cert/key/CA from a successful registration). A node that has never registered — bad join token, unreachable controller, wrong `-controller` — reports unhealthy, the failure an operator most wants surfaced; once registered it keeps reporting healthy through a later, transient controller outage, since the node keeps serving its cached snapshot and retries registration on its own rather than needing the container restarted.

### Single-port deployment

The console (`-addr`), southbound (`-southbound-addr`), a node's relay port (the wildcard address derived from its `relayAddress`'s port, or its own `-relay-listen` override), and site listeners can all share one physical port. `internal/sharedport` tells plaintext and TLS apart by a connection's first byte, then dispatches to the right occupant by the plaintext `Host` header or the TLS SNI. This also lifts the earlier restriction against mixing a plaintext and a TLS *site* on the same address — dispatch happens at the first byte, independently of a site's own mode. Two occupants sharing a port must have listen addresses that normalize identically (`sharedport.NormalizeAddress`; a wildcard address — `""`/`0.0.0.0`/`::`/`*` — cannot share a port with a specific one), and once more than one occupant is on an address, every site there needs `hostnames` (SNI for a TLS site) to tell it apart from the rest.

Saving or starting a site pre-validates these rules and returns a Chinese error before anything is actually applied, instead of only failing once the configuration is really used. The console, southbound, and the relay port of every node the site is placed on are always compared, since they occupy their address whether or not any site is running; another site sharing a node is only compared against once it is actually running (or is itself what is being started/restarted), so saving several site configurations at the same address and starting them one at a time, without them ever colliding, still works. A node's own `-relay-listen` override is invisible to the controller (pre-validation only ever sees the wildcard address it guesses from `relayAddress`), so that specific mismatch stays the node's own job to report once it actually tries to bind. Sites auto-started at controller startup (`autoStart: true`, applied by `StartAutoSites`) skip this pre-validation and go through the real admission checks instead; a site that loses that race fails on its own and keeps its previous configuration (D8), logged, rather than blocking every other auto-started site. See `docs/architecture/control-data-plane.md` §2/§5 for the full "共享端口" decision and implementation record across all three stages.

The three examples below can be run as shown, with the addresses/hostnames replaced by real ones:

**(a) all-in-one: the console shares a port with a site.** `-console-hostnames` restricts the console to the given Host; a Host claimed by neither a site nor the console's own list gets a 404:

```sh
rpop -addr 0.0.0.0:8443 -console-hostnames console.example.com -db data/rpop.db -log-dir logs
```

Create a site whose `listenAddress`/`listenPort` normalize the same as `-addr` (here `"0.0.0.0"` + `8443`) and whose `hostnames` differ from `console.example.com` (for example `["app.example.com"]`). The console then answers `Host: console.example.com`, the site answers its own hostnames, and neither interferes with the other on the shared port.

**(b) `controller` mode: the console shares a port with southbound.** The console is plaintext and southbound is TLS (nodes connect over mTLS), so the two are told apart by the first byte alone, with no extra configuration needed:

```sh
rpop -mode controller -addr 0.0.0.0:8443 -southbound-addr 0.0.0.0:8443 -db data/rpop.db -log-dir logs
```

Nodes still use that same address as `-controller` (for example `https://controller.example.com:8443`).

**(c) `node` mode: a node's relay port shares a port with a site placed on it.** A node binds its relay port on the wildcard address for `relayAddress`'s port by default (unless overridden with `-relay-listen`); placing a site on that same port shares it:

```sh
rpop -mode node -controller https://controller.example.com:7443 -join-token <join-token> -data-dir data/node -log-dir logs
```

The matching site: `config.nodes` includes this node's ID, `listenPort` equals the node's `relayAddress` port, `listenAddress` is left empty to match the node's default wildcard bind, `tls: true`, and `hostnames` avoids both the node's own certificate name (`<nodeID>.nodes.rpop`) and the controller's (`controller.rpop`). The relay listener itself only binds once this node is actually relaying for another node's path (a non-empty `Relay` entry in its snapshot, see `internal/overlay.Overlay.applyRelayLocked`) — a node nobody ever routes through never opens that port, so until then the site alone occupies it (not a conflict, just not actually shared yet).

> **Security note**: once the console is served on a plaintext port (no TLS), the login password and session cookie travel in plaintext — if examples (a)/(b) need to accept connections from an untrusted network, put the console behind a TLS reverse proxy rather than exposing a plaintext console directly to the internet, consistent with the earlier advice to keep the console on loopback or behind a trusted HTTPS reverse proxy.

### Upstream paths across nodes

An upstream can reach its origin through other nodes and external proxies instead of connecting directly. `paths` lists candidate routes in priority order; each `via` is an ordered mix of nodes and registered proxies, and an empty `via` connects directly. `via` on the upstream itself is shorthand for a single path.

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

The first path is `client > node1 > socks5-A > node2 > node3 > socks5-B > origin`; the last (`via: []`) connects directly.

- The site's own node (here `node1`, from `config.nodes`) terminates the client connection and keeps all HTTP logic: routing, access logs, metrics, and the upstream's TLS settings. Upstream TLS runs end to end from that node to the origin, so relays and proxies only ever see ciphertext.
- Relay nodes serve no sites. Each opens a relay port that only accepts nodes holding a certificate from the controller's CA and forwards a tunnel only along a route the controller rendered for the node it came from. Give a relay node a `relayAddress` (`host:port` other nodes dial); it binds that port on every interface, or `-relay-listen` (env `RPOP_RELAY_LISTEN`) when port forwarding maps it elsewhere. A node that relays for a site cannot be deleted or lose its relay address.
- A proxy before the first node carries the ingress's link to it, a proxy between two nodes carries their link, and proxies after the last node are dialed by the exit node, so SOCKS handshakes with the exit proxy happen next to it. Consecutive proxies are chained.
- Links between nodes are persistent HTTP/2 mutual-TLS connections kept warm for every candidate path, and each tunnel is one stream on them, so no request pays for a TCP, proxy, or TLS handshake between nodes. Relays copy with pooled buffers and flush every chunk, so SSE and WebSocket traffic is not held back on any hop.
- The ingress uses the first path that connects. When connecting fails before any request byte is sent (a node, link, or proxy is down, a relay refuses, the exit cannot reach the origin, or 10 s pass) it moves on to the next path at once; the failed path cools down with exponential backoff (1 s to 1 min) and is tried again after that. Links known to be down are skipped without waiting. A request that already reached an upstream is never sent again. Every path has its own connection pool, so after failing back, new requests use the preferred path while requests on the fallback finish undisturbed.
- The embedded node of an all-in-one controller can be the ingress of such paths, but it cannot relay.

Register proxies with `POST /api/proxies` (`{"id":"socks5-A","name":"A","type":"socks5h","address":"10.0.0.1:1080","username":"u","password":"p"}`); `type` is `socks5` (the node resolves names), `socks5h` (the proxy resolves names), `http`, or `https`. Passwords are write-only: `PUT /api/proxies/{id}` keeps the stored password when `password` is omitted and clears it when it is empty. A proxy used by a site cannot be deleted. `paths` and `proxyUrl` are mutually exclusive.

## Capabilities in this baseline

- Site HTTPS can use a centrally managed server certificate from System Settings (`config.certificateId`), so several sites can share one wildcard certificate, or site-scoped `certificateSecret`/`privateKeySecret` uploads; the two are mutually exclusive.
- SQLite storage for site configuration and binary secrets (`site_secrets`); DB and log directories are created with restricted directory permissions.
- Per-site start, stop, restart, and reload; sites marked `autoStart: true` start on process startup. Sites using the same listen address, port, and TLS mode share one listener and route by `config.hostnames`; for HTTPS, SNI chooses the certificate and HTTP Host chooses the site (SNI is the fallback when Host does not match). HTTPS listeners offer HTTP/2 (WebSockets keep using HTTP/1.1). A reload on the same listener swaps the site atomically: in-flight requests, including SSE streams and WebSockets, finish on the previous version, and a configuration that fails to apply leaves the running version serving.
- Every upstream has its own transport and supports HTTP/HTTPS, HTTP(S) proxy, SOCKS5/SOCKS5H, system CA roots selected per site, a legacy custom CA bundle, optional mutual-TLS Client certificates (either a centrally managed system Client certificate or site-scoped certificate/key secret references), skip-verification, custom server name/SNI, and a direct-mode dial-address override.
- Caddy-style request routing (`config.routes`) sends requests to different upstreams by path and header. A path matches exactly unless it ends in `*`, which makes it a prefix match (`/api/*` matches `/api/x` but not `/api`; `/api*` also matches `/api` and `/apix`); matching is case-insensitive and uses the cleaned path (`..` and `//` resolved). Header conditions match exact, `prefix*`, `*suffix`, or `*contains*` values (case-sensitive; values of one header are ORed, different headers are ANDed), require the header to exist when `values` is empty, or require it to be missing with `absent: true`; `Host` matches the request host. Like Caddy's `handle`, the most specific rule wins: longer path first, exact before prefix, more header conditions first, then configuration order. `stripPrefix` removes the matched prefix (Caddy `handle_path`) before the upstream URL's own path is prepended. Requests that match no rule go to the default upstream, `upstreams[0]`.
- The site editor manages any number of upstreams (make one the default, add or remove them with their routes remapped) and routing rules, and includes a route simulator: enter a URL, method, and headers to see which rule matches, why the other rules did not, and the path before and after rewriting. It evaluates the unsaved configuration with the same server code as live traffic via `POST /api/routes/simulate`.
- Multi-hop `paths` (see below) get a dedicated editor listing every candidate route as an ordered list of node/proxy hops; the route simulator's response also renders the full candidate path for the matched upstream, each hop's live health, and which path the engine would pick right now.
- A site emits access logs only when its `config.accessLog.adapterId` selects a configured adapter; an empty ID disables access logging. Body capture and sensitive-header options remain site-specific. Sensitive headers are redacted unless explicitly enabled. Log writes use a bounded asynchronous queue so storage I/O does not block forwarding; queue saturation drops records and increments the site's dropped-log metric.
- Per-site in-memory metrics: request/error counts, status codes, in-flight requests, bytes in/out, average TTFB, average and maximum response time, and approximate P95 response time.
- Administrator password setup, login/logout, and password changes from the System Settings page. Passwords are PBKDF2-HMAC-SHA256 hashed; the UI uses HttpOnly, SameSite session cookies, and failed logins are rate-limited.
- Pluggable access-log storage: configure multiple named local-file, ClickHouse HTTP, Elasticsearch, and S3/S3-compatible adapters, including multiple adapters of the same type. Each site selects its own adapter; the site cannot select an unconfigured adapter. File adapters can rotate hourly, daily, or by size (for example `1G` in the UI), gzip archives, and retain a configured number of archives.
- 系统设置提供唯一的时区入口 `timeZone`（默认 `UTC`；支持 `UTC` 或 `Asia/Shanghai` 等 IANA 时区），UI 使用可搜索但不可自由输入的时区下拉框；系统设置内另有独立的“上游 CA 根证书”标签页，支持多个具名证书的增删改查。设置持久化到 SQLite `app_settings`，通过 `GET/PUT /api/settings` 管理。CA 根证书以 PEM 保存，在站点上游配置中按站点选择；所选自定义 CA 会加入操作系统默认信任池，站点已有 `caBundle` 也继续支持。系统设置另有“站点 HTTPS 证书”标签页，可预先添加站点 HTTPS 证书（如 `*.example.com` 泛域名证书，可附带中间证书链）供多个站点共用；证书须允许 serverAuth，界面会展示 SAN 域名并提示站点域名是否被覆盖。站点 HTTPS 证书使用随机的稳定 ID：续期时直接替换证书内容即可，ID 不变，所有引用它的运行中站点会立即对新 TLS 握手使用新证书，无需重启。还有“mTLS Client 证书”标签页，集中管理访问需要双向 HTTPS 认证的上游时使用的 Client 证书（可附带中间证书）与未加密私钥：保存时校验证书与私钥匹配、证书非 CA 且允许 TLS 客户端认证；私钥只写不读，接口仅返回证书、主题、签发者、有效期与 `hasPrivateKey`。更新时省略 `privateKeyPem` 会保留同 ID 条目的已存私钥，请求中省略整个 `clientCertificates` / `serverCertificates` 字段则保持现有列表不变。站点 HTTPS 证书与 mTLS Client 证书被站点引用时都不能删除。系统设置还统一控制本地文件的日/小时轮转，以及 S3、ClickHouse、Elasticsearch 的日/小时分区；日志时间戳和搜索时间范围仍表示绝对时间。远程适配器的 `splitMode` 为 `none|day|hour`：S3 在对象键中写入日期/小时目录，ClickHouse 使用 `_YYYYMMDD` 或 `_YYYYMMDDHH` 表后缀，Elasticsearch 使用 `-YYYYMMDD` 或 `-YYYYMMDDHH` 索引后缀。搜索兼容基础资源、历史分割模式和时区变更前写入的数据。旧配置缺少系统时区时默认 UTC；S3 继续默认 `hour`，ClickHouse/Elasticsearch 继续默认 `none`，以保持资源命名兼容。大小轮转仅适用于本地文件；S3 仍为每条日志写入一个 gzip 对象。
- Elasticsearch uses the HTTP Bulk API for writes and Search API for filtering/pagination. Configure an index and an HTTP(S) cluster URL, with no authentication, Basic authentication, or an API Key (paste the Base64 value). The Elasticsearch account needs permissions to write and search the target base/partition indices; if an index does not exist, it also needs permission for automatic index creation. ClickHouse split-mode search enumerates matching tables through `system.tables`, so its account must be able to read that metadata as well as create, write, and query the log tables.
- Each access-log record carries the standard combined-log fields: client IP and port (the TCP peer), the raw `X-Forwarded-For` value (not trusted for `clientIp`, since clients can set it), scheme and TLS version, `Host`, method, request URI, protocol, status, byte counts, `Referer`, `User-Agent`, the upstream URL (credentials redacted), and the matched route rule (empty when the default upstream was used), plus TTFB and total response time.
- Authenticated Web log search with site, status, text, and time filters, pagination, and request/response header/body details.
- The console's Nodes page covers the full node lifecycle (create, rename/re-point relay address, reissue join token, delete) plus per-node overlay link, upstream path, and log-spool health; a Topology page graphs every node's inferred entry/relay/exit role and the links between them; a Proxies page manages named overlay proxies.
- The Trace page looks up a request's full journey by its `Rpop-Track-Id` or tunnel ID, laying out entry/relay/exit timing per hop with protocol-version and clock-skew badges, plus an optional client-side toggle that corrects the displayed timeline for a reporting node's clock skew (display-only; it never rewrites stored timestamps).

## Site configuration example

Request body for `POST /api/sites` (also accepted by `PUT /api/sites/{id}`):

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

The first route sends `/api/users` to `http://10.0.0.5:8080/v1/users` (path prefix stripped). The second is more specific (it also matches on the `X-Canary` header) and wins for canary traffic on `/api/*`, keeping the path and going to the default upstream (`upstreams[0]`).

`rootCertificateIds` selects one or more CA certificates from System Settings by their SHA-256 DER fingerprint IDs. The selected roots are added to the OS trust pool for that upstream; a legacy upstream `caBundle` is also accepted. With no selected system roots, an explicit `caBundle` retains its existing exclusive-pool behavior, while an empty bundle uses OS defaults. For mutual TLS, either set `clientCertificateId` to a Client certificate managed in System Settings (the SHA-256 fingerprint of its leaf certificate), or configure `clientCertSecret`/`clientKeySecret`; the two options are mutually exclusive and require an HTTPS upstream. A system Client certificate selected by any site cannot be removed. Site-scoped certificate and private-key bytes are stored in SQLite secrets, referenced by name rather than sent inline. Upload PEM bytes with `PUT /api/sites/{siteId}/secrets/{name}` using an `application/octet-stream` body. The authenticated `DELETE /api/sites/{siteId}/secrets/{name}` endpoint removes an unused secret.

## API

- `GET /api/health` (public liveness endpoint)
- `GET /api/auth/status`, `POST /api/auth/setup`, `POST /api/auth/login`, `POST /api/auth/logout`, `PUT /api/auth/password`
- `GET /api/logging` (list configured adapters), `POST /api/logging/adapters`, `PUT/DELETE /api/logging/adapters/{id}`
- `GET /api/logs?adapterId=...&q=...&siteId=...&status=...&from=...&to=...&page=1&pageSize=25` (authenticated search; when `siteId` is supplied, its bound adapter is selected automatically)
- `GET /api/logging/trace/{trackId}` returns the single access-log record for that request (searches every configured adapter); `GET /api/logging/tunnels/{tunnelId}` returns every node's `TunnelEvent` reports for that tunnel, timestamp-ordered, each carrying the reporting node's current clock skew.
- `GET /api/sites`, `POST /api/sites`
- `GET /api/sites/{id}`, `PUT /api/sites/{id}`, `DELETE /api/sites/{id}`
- `POST /api/sites/{id}/start|stop|restart|reload`
- `GET /api/sites/{id}/metrics` returns current process metrics (reset when the process restarts), summed over the nodes the site runs on, with per-node values in `byNode`. Averages are weighted by request count; P95 is the highest node P95.
- `POST /api/routes/simulate` evaluates an unsaved site's routing rules against a sample request; when the matched upstream has `paths`, the response also includes the full multi-hop candidate paths, each hop's health, and which path would be selected.
- `GET /api/nodes`, `POST /api/nodes` (returns a join token), `GET/PUT/DELETE /api/nodes/{id}`, `POST /api/nodes/{id}/token` (reissue). A node used by a site cannot be deleted. Node entries report `registered`, `online`, `appliedRevision`, `publishedRevision`, `inSync`, per-site `errors`, `running` sites, overlay `links` (peer, address, connections, open tunnels, failures), upstream `paths` health, `certGeneration`, `protocolVersion`/`protocolStatus`, `clockSkewMillis`/`clockSkewStatus`, and `relayError` when the relay port could not bind.
- `GET /api/topology` returns every node (with its inferred `entry`/`relay`/`exit` roles), the directed links between them (health, proxy chain, connection/tunnel counters), and the registered named proxies, for the console's topology graph.
- `GET /api/proxies`, `POST /api/proxies`, `PUT/DELETE /api/proxies/{id}`. Entries report `hasPassword` and the sites that use them in `usedBy`.
- `PUT/DELETE /api/sites/{id}/secrets/{name}`

## Security / current limitations

- TLS `InsecureSkipVerify` is intentionally opt-in and disables upstream certificate verification; do not use it as a routine workaround.
- Site secrets and external log-adapter credentials are stored in SQLite and are **not encrypted at rest**. The administrator password is stored as a PBKDF2 hash. Protect DB backups and filesystem access; add a key-management/encryption layer before production use.
- Authentication sessions are held in process memory and expire after 12 hours; a process restart invalidates all sessions. Put the management API behind HTTPS. When TLS terminates at a reverse proxy, configure that trusted proxy to set `X-Forwarded-Proto: https` and strip client-supplied forwarding headers.
- Access-log bodies may contain credentials, personal information, or business data. Body capture defaults to 1 MiB; positive limits are capped at 8 MiB, while `maxBodyBytes: -1` disables truncation. Unlimited capture can consume substantial memory, and access-log entries may still be dropped if the asynchronous log queue is saturated; sensitive headers are redacted by default. Restrict access to log files and disable body logging when it is not needed.
- Metrics are in-memory and reset on process restart. Access logs are asynchronously written through the configured adapter; `droppedAccessLogCount` reports records dropped when the bounded queue is saturated. The process's own zap diagnostic log remains a separate local `rpop.log` file.
- Each route targets exactly one upstream; failover happens only between the paths of one upstream. Health checks, load balancing across upstreams or paths, config versioning/rollback, and graceful zero-downtime listener replacement remain follow-up work.
- A node receives the credentials of exactly the proxies it connects through, inline in its snapshot over mutual TLS, and caches them with the snapshot (mode `0600`).
- The custom `dialAddress` is applied in direct mode; custom destination resolution through an upstream proxy requires an explicit policy and is not silently implemented.
- File-adapter search scans active and archived local log files. S3 search uses the selected date prefix and a delimiter-based listing of direct objects to retain compatibility with earlier unsplit layouts; broad or mode-mixed time ranges can still require more key enumeration, so prefer ClickHouse for large searchable log volumes. ClickHouse searches the configured base table and its date/hour tables.
- The separate zap diagnostic file currently does not rotate; configure external rotation for `rpop.log` if needed.

## License

rpop is released under the [MIT License](LICENSE).
