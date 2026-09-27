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

`.gitlab-ci.yml` builds natively on the runner's architecture and pushes to the private registry on `main`, `dev`, and tags (tags get only the version tag; branches also get `latest`); it does not deploy. Required CI/CD variables: `DOCKER_REGISTRY_HOST`, `DOCKER_REGISTRY_USERNAME`, `DOCKER_REGISTRY_PASSWORD` (masked), and `DOCKER_REGISTRY_MIRROR` (base-image mirror prefix, which must provide `library/node:22-alpine`, `library/golang:1.26-alpine`, and `library/alpine:latest`).

## Deployment modes

One binary runs in three modes, selected with `-mode` (env `RPOP_MODE`):

- `all-in-one` (default): the controller (console, API, SQLite) plus an embedded data-plane node named `local`. Sites without `config.nodes` run here, exactly as in a single-process install.
- `controller`: the console and API only. Nodes connect to its southbound listener, `-southbound-addr` (env `RPOP_SOUTHBOUND_ADDR`, default `:7443`). An all-in-one process also accepts remote nodes when `-southbound-addr` is set.
- `node`: a data-plane node without a console. It needs `-controller https://controller:7443` (env `RPOP_CONTROLLER`), `-data-dir` (env `RPOP_DATA_DIR`, default `data/node`), and on its first start `-join-token` (env `RPOP_JOIN_TOKEN`).

Create a node with `POST /api/nodes` (`{"id":"edge-1","name":"Edge 1"}`); the response contains a single-use join token valid for 24 hours. The token pins the controller's internal CA, so the node authenticates the controller on first contact; the node then generates its key locally and receives a certificate through a CSR. All southbound traffic is HTTP/2 with mutual TLS. Place a site on nodes with `config.nodes: [edge-1, edge-2]`; the controller streams each node a full snapshot of its sites whenever a new revision is published, and the node reports the revision it applied, per-site errors, running sites, and metrics. A site that fails to apply on a node (for example, a port in use) keeps its previous configuration there. The node caches the last applied snapshot (mode `0600`, including the TLS keys it serves) and serves it after a restart even while the controller is unreachable. Certificates renew automatically 30 days before expiry. Reissuing a token (`POST /api/nodes/{id}/token`) and registering again revokes the node's earlier certificates; a node started with a join token re-registers by itself when the controller rejects its certificate. Failed registrations are rate-limited per client address.

### Upstream paths across nodes

An upstream can reach its origin through other nodes and external proxies instead of connecting directly. `paths` lists candidate routes in priority order; each `via` is an ordered mix of nodes and registered proxies, and an empty `via` connects directly. `via` on the upstream itself is shorthand for a single path.

```yaml
upstreams:
  - url: https://example.com
    paths:
      - via: [{proxy: socks5-A}, {node: node2}, {node: node3}, {proxy: socks5-B}]  # client > node1 > socks5-A > node2 > node3 > socks5-B > origin
      - via: [{proxy: socks5-A}, {node: node2}, {node: node3}]
      - via: [{node: node2}, {node: node3}]
      - via: [{node: node3}]
      - via: []                                                                   # direct
```

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
- YAML configuration import/export at `PUT/GET /api/config.yaml`. YAML contains configuration and secret *references*, not private key bytes; SQLite remains the canonical store.
- A site emits access logs only when its `config.accessLog.adapterId` selects a configured adapter; an empty ID disables access logging. Body capture and sensitive-header options remain site-specific. Sensitive headers are redacted unless explicitly enabled. Log writes use a bounded asynchronous queue so storage I/O does not block forwarding; queue saturation drops records and increments the site's dropped-log metric.
- Per-site in-memory metrics: request/error counts, status codes, in-flight requests, bytes in/out, average TTFB, average and maximum response time, and approximate P95 response time.
- Administrator password setup, login/logout, and password changes from the System Settings page. Passwords are PBKDF2-HMAC-SHA256 hashed; the UI uses HttpOnly, SameSite session cookies, and failed logins are rate-limited.
- Pluggable access-log storage: configure multiple named local-file, ClickHouse HTTP, Elasticsearch, and S3/S3-compatible adapters, including multiple adapters of the same type. Each site selects its own adapter; the site cannot select an unconfigured adapter. File adapters can rotate hourly, daily, or by size (for example `1G` in the UI), gzip archives, and retain a configured number of archives.
- 系统设置提供唯一的时区入口 `timeZone`（默认 `UTC`；支持 `UTC` 或 `Asia/Shanghai` 等 IANA 时区），UI 使用可搜索但不可自由输入的时区下拉框；系统设置内另有独立的“上游 CA 根证书”标签页，支持多个具名证书的增删改查。设置持久化到 SQLite `app_settings`，通过 `GET/PUT /api/settings` 管理。CA 根证书以 PEM 保存，在站点上游配置中按站点选择；所选自定义 CA 会加入操作系统默认信任池，站点已有 `caBundle` 也继续支持。系统设置另有“站点 HTTPS 证书”标签页，可预先添加站点 HTTPS 证书（如 `*.example.com` 泛域名证书，可附带中间证书链）供多个站点共用；证书须允许 serverAuth，界面会展示 SAN 域名并提示站点域名是否被覆盖。站点 HTTPS 证书使用随机的稳定 ID：续期时直接替换证书内容即可，ID 不变，所有引用它的运行中站点会立即对新 TLS 握手使用新证书，无需重启。还有“mTLS Client 证书”标签页，集中管理访问需要双向 HTTPS 认证的上游时使用的 Client 证书（可附带中间证书）与未加密私钥：保存时校验证书与私钥匹配、证书非 CA 且允许 TLS 客户端认证；私钥只写不读，接口仅返回证书、主题、签发者、有效期与 `hasPrivateKey`。更新时省略 `privateKeyPem` 会保留同 ID 条目的已存私钥，请求中省略整个 `clientCertificates` / `serverCertificates` 字段则保持现有列表不变。站点 HTTPS 证书与 mTLS Client 证书被站点引用时都不能删除。系统设置还统一控制本地文件的日/小时轮转，以及 S3、ClickHouse、Elasticsearch 的日/小时分区；日志时间戳和搜索时间范围仍表示绝对时间。远程适配器的 `splitMode` 为 `none|day|hour`：S3 在对象键中写入日期/小时目录，ClickHouse 使用 `_YYYYMMDD` 或 `_YYYYMMDDHH` 表后缀，Elasticsearch 使用 `-YYYYMMDD` 或 `-YYYYMMDDHH` 索引后缀。搜索兼容基础资源、历史分割模式和时区变更前写入的数据。旧配置缺少系统时区时默认 UTC；S3 继续默认 `hour`，ClickHouse/Elasticsearch 继续默认 `none`，以保持资源命名兼容。大小轮转仅适用于本地文件；S3 仍为每条日志写入一个 gzip 对象。
- Elasticsearch uses the HTTP Bulk API for writes and Search API for filtering/pagination. Configure an index and an HTTP(S) cluster URL, with no authentication, Basic authentication, or an API Key (paste the Base64 value). The Elasticsearch account needs permissions to write and search the target base/partition indices; if an index does not exist, it also needs permission for automatic index creation. ClickHouse split-mode search enumerates matching tables through `system.tables`, so its account must be able to read that metadata as well as create, write, and query the log tables.
- Each access-log record carries the standard combined-log fields: client IP and port (the TCP peer), the raw `X-Forwarded-For` value (not trusted for `clientIp`, since clients can set it), scheme and TLS version, `Host`, method, request URI, protocol, status, byte counts, `Referer`, `User-Agent`, the upstream URL (credentials redacted), and the matched route rule (empty when the default upstream was used), plus TTFB and total response time.
- Authenticated Web log search with site, status, text, and time filters, pagination, and request/response header/body details.

## YAML example

```yaml
sites:
  - id: example
    name: Example site
    autoStart: true
    config:
      listenAddress: 127.0.0.1
      listenPort: 8443
      hostnames: [app.example.test, api.example.test]
      tls: true
      certificateSecret: site-cert
      privateKeySecret: site-key
      accessLog:
        adapterId: default
        includeBodies: true
        maxBodyBytes: 1048576
      upstreams:
        - url: https://origin.example.test
          proxyUrl: socks5://127.0.0.1:1080
          proxyType: proxy
          serverName: origin.example.test
          dialAddress: 192.0.2.10:443
          insecureSkipVerify: false
          clientCertSecret: upstream-client-cert
          clientKeySecret: upstream-client-key
        - url: http://10.0.0.5:8080/v1
      routes:
        # /api/users -> http://10.0.0.5:8080/v1/users
        - path: /api/*
          stripPrefix: true
          upstream: 1
        # Canary traffic for /api/* keeps its path and goes to the default upstream
        - path: /api/*
          headers:
            - name: X-Canary
              values: ["1", "true"]
          upstream: 0
```

`rootCertificateIds` selects one or more CA certificates from System Settings by their SHA-256 DER fingerprint IDs. The selected roots are added to the OS trust pool for that upstream; a legacy upstream `caBundle` is also accepted. With no selected system roots, an explicit `caBundle` retains its existing exclusive-pool behavior, while an empty bundle uses OS defaults. For mutual TLS, either set `clientCertificateId` to a Client certificate managed in System Settings (the SHA-256 fingerprint of its leaf certificate), or configure `clientCertSecret`/`clientKeySecret`; the two options are mutually exclusive and require an HTTPS upstream. A system Client certificate selected by any site cannot be removed. Site-scoped certificate and private-key bytes are stored in SQLite secrets, not YAML. Upload PEM bytes with `PUT /api/sites/{siteId}/secrets/{name}` using an `application/octet-stream` body. The authenticated `DELETE /api/sites/{siteId}/secrets/{name}` endpoint removes an unused secret.

## API

- `GET /api/health` (public liveness endpoint)
- `GET /api/auth/status`, `POST /api/auth/setup`, `POST /api/auth/login`, `POST /api/auth/logout`, `PUT /api/auth/password`
- `GET /api/logging` (list configured adapters), `POST /api/logging/adapters`, `PUT/DELETE /api/logging/adapters/{id}`
- `GET /api/logs?adapterId=...&q=...&siteId=...&status=...&from=...&to=...&page=1&pageSize=25` (authenticated search; when `siteId` is supplied, its bound adapter is selected automatically)
- `GET /api/sites`, `POST /api/sites`
- `GET /api/sites/{id}`, `PUT /api/sites/{id}`, `DELETE /api/sites/{id}`
- `POST /api/sites/{id}/start|stop|restart|reload`
- `GET /api/sites/{id}/metrics` returns current process metrics (reset when the process restarts), summed over the nodes the site runs on, with per-node values in `byNode`. Averages are weighted by request count; P95 is the highest node P95.
- `GET /api/nodes`, `POST /api/nodes` (returns a join token), `PUT/DELETE /api/nodes/{id}`, `POST /api/nodes/{id}/token` (reissue). A node used by a site cannot be deleted. Node entries report `registered`, `online`, `appliedRevision`, `publishedRevision`, `inSync`, per-site `errors`, `running` sites, overlay `links` (peer, address, connections, open tunnels, failures), and `relayError` when the relay port could not bind.
- `GET /api/proxies`, `POST /api/proxies`, `PUT/DELETE /api/proxies/{id}`. Entries report `hasPassword` and the sites that use them in `usedBy`.
- `PUT/DELETE /api/sites/{id}/secrets/{name}`
- `GET /api/config.yaml` (download), `PUT /api/config.yaml` (transactional upsert import)

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
