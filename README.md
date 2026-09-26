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

## Capabilities in this baseline

- Site HTTPS can use a centrally managed server certificate from System Settings (`config.certificateId`), so several sites can share one wildcard certificate, or site-scoped `certificateSecret`/`privateKeySecret` uploads; the two are mutually exclusive.
- SQLite storage for site configuration and binary secrets (`site_secrets`); DB and log directories are created with restricted directory permissions.
- Per-site start, stop, restart, and reload; sites marked `autoStart: true` start on process startup. Sites using the same listen address, port, and TLS mode share one listener and route by `config.hostnames`; for HTTPS, SNI chooses the certificate and HTTP Host chooses the site (SNI is the fallback when Host does not match). Reload uses a brief stop/start window.
- First upstream supports HTTP/HTTPS, HTTP(S) proxy, SOCKS5/SOCKS5H, system CA roots selected per site, a legacy custom CA bundle, optional mutual-TLS Client certificates (either a centrally managed system Client certificate or site-scoped certificate/key secret references), skip-verification, custom server name/SNI, and a direct-mode dial-address override.
- YAML configuration import/export at `PUT/GET /api/config.yaml`. YAML contains configuration and secret *references*, not private key bytes; SQLite remains the canonical store.
- A site emits access logs only when its `config.accessLog.adapterId` selects a configured adapter; an empty ID disables access logging. Body capture and sensitive-header options remain site-specific. Sensitive headers are redacted unless explicitly enabled. Log writes use a bounded asynchronous queue so storage I/O does not block forwarding; queue saturation drops records and increments the site's dropped-log metric.
- Per-site in-memory metrics: request/error counts, status codes, in-flight requests, bytes in/out, average TTFB, average and maximum response time, and approximate P95 response time.
- Administrator password setup, login/logout, and password changes from the System Settings page. Passwords are PBKDF2-HMAC-SHA256 hashed; the UI uses HttpOnly, SameSite session cookies, and failed logins are rate-limited.
- Pluggable access-log storage: configure multiple named local-file, ClickHouse HTTP, Elasticsearch, and S3/S3-compatible adapters, including multiple adapters of the same type. Each site selects its own adapter; the site cannot select an unconfigured adapter. File adapters can rotate hourly, daily, or by size (for example `1G` in the UI), gzip archives, and retain a configured number of archives.
- 系统设置提供唯一的时区入口 `timeZone`（默认 `UTC`；支持 `UTC` 或 `Asia/Shanghai` 等 IANA 时区），UI 使用可搜索但不可自由输入的时区下拉框；系统设置内另有独立的“上游 CA 根证书”标签页，支持多个具名证书的增删改查。设置持久化到 SQLite `app_settings`，通过 `GET/PUT /api/settings` 管理。CA 根证书以 PEM 保存，在站点上游配置中按站点选择；所选自定义 CA 会加入操作系统默认信任池，站点已有 `caBundle` 也继续支持。系统设置另有“站点 HTTPS 证书”标签页，可预先添加站点 HTTPS 证书（如 `*.example.com` 泛域名证书，可附带中间证书链）供多个站点共用；证书须允许 serverAuth，界面会展示 SAN 域名并提示站点域名是否被覆盖。站点 HTTPS 证书使用随机的稳定 ID：续期时直接替换证书内容即可，ID 不变，所有引用它的运行中站点会立即对新 TLS 握手使用新证书，无需重启。还有“mTLS Client 证书”标签页，集中管理访问需要双向 HTTPS 认证的上游时使用的 Client 证书（可附带中间证书）与未加密私钥：保存时校验证书与私钥匹配、证书非 CA 且允许 TLS 客户端认证；私钥只写不读，接口仅返回证书、主题、签发者、有效期与 `hasPrivateKey`。更新时省略 `privateKeyPem` 会保留同 ID 条目的已存私钥，请求中省略整个 `clientCertificates` / `serverCertificates` 字段则保持现有列表不变。站点 HTTPS 证书与 mTLS Client 证书被站点引用时都不能删除。系统设置还统一控制本地文件的日/小时轮转，以及 S3、ClickHouse、Elasticsearch 的日/小时分区；日志时间戳和搜索时间范围仍表示绝对时间。远程适配器的 `splitMode` 为 `none|day|hour`：S3 在对象键中写入日期/小时目录，ClickHouse 使用 `_YYYYMMDD` 或 `_YYYYMMDDHH` 表后缀，Elasticsearch 使用 `-YYYYMMDD` 或 `-YYYYMMDDHH` 索引后缀。搜索兼容基础资源、历史分割模式和时区变更前写入的数据。旧配置缺少系统时区时默认 UTC；S3 继续默认 `hour`，ClickHouse/Elasticsearch 继续默认 `none`，以保持资源命名兼容。大小轮转仅适用于本地文件；S3 仍为每条日志写入一个 gzip 对象。
- Elasticsearch uses the HTTP Bulk API for writes and Search API for filtering/pagination. Configure an index and an HTTP(S) cluster URL, with no authentication, Basic authentication, or an API Key (paste the Base64 value). The Elasticsearch account needs permissions to write and search the target base/partition indices; if an index does not exist, it also needs permission for automatic index creation. ClickHouse split-mode search enumerates matching tables through `system.tables`, so its account must be able to read that metadata as well as create, write, and query the log tables.
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
- `GET /api/sites/{id}/metrics` returns current process metrics (reset when the process restarts).
- `PUT/DELETE /api/sites/{id}/secrets/{name}`
- `GET /api/config.yaml` (download), `PUT /api/config.yaml` (transactional upsert import)

## Security / current limitations

- TLS `InsecureSkipVerify` is intentionally opt-in and disables upstream certificate verification; do not use it as a routine workaround.
- Site secrets and external log-adapter credentials are stored in SQLite and are **not encrypted at rest**. The administrator password is stored as a PBKDF2 hash. Protect DB backups and filesystem access; add a key-management/encryption layer before production use.
- Authentication sessions are held in process memory and expire after 12 hours; a process restart invalidates all sessions. Put the management API behind HTTPS. When TLS terminates at a reverse proxy, configure that trusted proxy to set `X-Forwarded-Proto: https` and strip client-supplied forwarding headers.
- Access-log bodies may contain credentials, personal information, or business data. Body capture defaults to 1 MiB; positive limits are capped at 8 MiB, while `maxBodyBytes: -1` disables truncation. Unlimited capture can consume substantial memory, and access-log entries may still be dropped if the asynchronous log queue is saturated; sensitive headers are redacted by default. Restrict access to log files and disable body logging when it is not needed.
- Metrics are in-memory and reset on process restart. Access logs are asynchronously written through the configured adapter; `droppedAccessLogCount` reports records dropped when the bounded queue is saturated. The process's own zap diagnostic log remains a separate local `rpop.log` file.
- Each site currently forwards through its first configured upstream; health checks, failover/load balancing, config versioning/rollback, and graceful zero-downtime listener replacement remain follow-up work.
- The custom `dialAddress` is applied in direct mode; custom destination resolution through an upstream proxy requires an explicit policy and is not silently implemented.
- File-adapter search scans active and archived local log files. S3 search uses the selected date prefix and a delimiter-based listing of direct objects to retain compatibility with earlier unsplit layouts; broad or mode-mixed time ranges can still require more key enumeration, so prefer ClickHouse for large searchable log volumes. ClickHouse searches the configured base table and its date/hour tables.
- The separate zap diagnostic file currently does not rotate; configure external rotation for `rpop.log` if needed.
