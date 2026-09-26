# rpop — Reverse Proxy over Proxy

Go-based multi-site reverse proxy. Every site has an independent listener and upstream transport, with SQLite as the source of truth for configuration and secret material.

## Development

Requirements: Go 1.26+ (SQLite driver uses CGO) and Node.js/npm.

```sh
cd web && npm install && npm run build
cd ..
go run ./cmd/rpop -addr 127.0.0.1:8080 -db data/rpop.db -log-dir logs -web-dir web/dist
```

The React development server runs on port 7106 and forwards `/api` to the Go service on port 8080. The API defaults to loopback. On first visit, set the administrator password in the Web UI (at least 12 characters); all management APIs require an authenticated session. Keep the admin API behind loopback or a trusted HTTPS reverse proxy.

## Capabilities in this baseline

- SQLite storage for site configuration and binary secrets (`site_secrets`); DB and log directories are created with restricted directory permissions.
- Per-site start, stop, restart, and reload; sites marked `autoStart: true` start on process startup. Sites using the same listen address, port, and TLS mode share one listener and route by `config.hostnames`; for HTTPS, SNI chooses the certificate and HTTP Host chooses the site (SNI is the fallback when Host does not match). Reload uses a brief stop/start window.
- First upstream supports HTTP/HTTPS, HTTP(S) proxy, SOCKS5/SOCKS5H, custom CA bundle, client certificate/key secret references, skip-verification, custom server name/SNI, and a direct-mode dial-address override.
- YAML configuration import/export at `PUT/GET /api/config.yaml`. YAML contains configuration and secret *references*, not private key bytes; SQLite remains the canonical store.
- A site emits access logs only when its `config.accessLog.adapterId` selects a configured adapter; an empty ID disables access logging. Body capture and sensitive-header options remain site-specific. Sensitive headers are redacted unless explicitly enabled. Log writes use a bounded asynchronous queue so storage I/O does not block forwarding; queue saturation drops records and increments the site's dropped-log metric.
- Per-site in-memory metrics: request/error counts, status codes, in-flight requests, bytes in/out, average TTFB, average and maximum response time, and approximate P95 response time.
- Administrator password setup, login/logout, and password changes. Passwords are PBKDF2-HMAC-SHA256 hashed; the UI uses HttpOnly, SameSite session cookies, and failed logins are rate-limited.
- Pluggable access-log storage: configure multiple named local-file, ClickHouse HTTP, and S3/S3-compatible adapters, including multiple adapters of the same type. Each site selects its own adapter; the site cannot select an unconfigured adapter. File adapters can rotate hourly, daily, or by size (for example `1G` in the UI), gzip archives, and retain a configured number of archives.
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

`caBundle` accepts PEM text and is stored with the config in SQLite. Secret names are scoped to a site. Upload PEM bytes with `PUT /api/sites/{siteId}/secrets/{name}` using an `application/octet-stream` body. For example, configure the site with TLS and the `certificateSecret`/`privateKeySecret` names, then upload certificate and key under those names.

## API

- `GET /api/health` (public liveness endpoint)
- `GET /api/auth/status`, `POST /api/auth/setup`, `POST /api/auth/login`, `POST /api/auth/logout`, `PUT /api/auth/password`
- `GET /api/logging` (list configured adapters), `POST /api/logging/adapters`, `PUT/DELETE /api/logging/adapters/{id}`
- `GET /api/logs?adapterId=...&q=...&siteId=...&status=...&from=...&to=...&page=1&pageSize=25` (authenticated search; when `siteId` is supplied, its bound adapter is selected automatically)
- `GET /api/sites`, `POST /api/sites`
- `GET /api/sites/{id}`, `PUT /api/sites/{id}`, `DELETE /api/sites/{id}`
- `POST /api/sites/{id}/start|stop|restart|reload`
- `GET /api/sites/{id}/metrics` returns current process metrics (reset when the process restarts).
- `PUT /api/sites/{id}/secrets/{name}`
- `GET /api/config.yaml` (download), `PUT /api/config.yaml` (transactional upsert import)

## Security / current limitations

- TLS `InsecureSkipVerify` is intentionally opt-in and disables upstream certificate verification; do not use it as a routine workaround.
- Site secrets and external log-adapter credentials are stored in SQLite and are **not encrypted at rest**. The administrator password is stored as a PBKDF2 hash. Protect DB backups and filesystem access; add a key-management/encryption layer before production use.
- Authentication sessions are held in process memory and expire after 12 hours; a process restart invalidates all sessions. Put the management API behind HTTPS. When TLS terminates at a reverse proxy, configure that trusted proxy to set `X-Forwarded-Proto: https` and strip client-supplied forwarding headers.
- Access-log bodies may contain credentials, personal information, or business data. Body capture defaults to 1 MiB; positive limits are capped at 8 MiB, while `maxBodyBytes: -1` disables truncation. Unlimited capture can consume substantial memory, and access-log entries may still be dropped if the asynchronous log queue is saturated; sensitive headers are redacted by default. Restrict access to log files and disable body logging when it is not needed.
- Metrics are in-memory and reset on process restart. Access logs are asynchronously written through the configured adapter; `droppedAccessLogCount` reports records dropped when the bounded queue is saturated. The process's own zap diagnostic log remains a separate local `rpop.log` file.
- Each site currently forwards through its first configured upstream; health checks, failover/load balancing, config versioning/rollback, and graceful zero-downtime listener replacement remain follow-up work.
- The custom `dialAddress` is applied in direct mode; custom destination resolution through an upstream proxy requires an explicit policy and is not silently implemented.
- File-adapter search scans active and archived local log files; S3 search lists and reads matching objects, so provide a time range to constrain searches on large buckets. ClickHouse is the preferred adapter for large searchable log volumes.
- The separate zap diagnostic file currently does not rotate; configure external rotation for `rpop.log` if needed.
