# rpop — Reverse Proxy over Proxy

Go-based multi-site reverse proxy. Every site has an independent listener and upstream transport, with SQLite as the source of truth for configuration and secret material.

## Development

Requirements: Go 1.26+ (SQLite driver uses CGO) and Node.js/npm.

```sh
cd web && npm install && npm run build
cd ..
go run ./cmd/rpop -addr 127.0.0.1:8080 -db data/rpop.db -log-dir logs -web-dir web/dist
```

The React development server runs on port 7106 and forwards `/api` to the Go service on port 8080. The API defaults to loopback; do not expose it publicly without adding authentication and authorization.

## Capabilities in this baseline

- SQLite storage for site configuration and binary secrets (`site_secrets`); DB and log directories are created with restricted directory permissions.
- Per-site start, stop, restart, and reload; sites marked `autoStart: true` start on process startup. Sites using the same listen address, port, and TLS mode share one listener and route by `config.hostnames`; for HTTPS, SNI chooses the certificate and HTTP Host chooses the site (SNI is the fallback when Host does not match). Reload uses a brief stop/start window.
- First upstream supports HTTP/HTTPS, HTTP(S) proxy, SOCKS5/SOCKS5H, custom CA bundle, client certificate/key secret references, skip-verification, custom server name/SNI, and a direct-mode dial-address override.
- YAML configuration import/export at `PUT/GET /api/config.yaml`. YAML contains configuration and secret *references*, not private key bytes; SQLite remains the canonical store.
- Detailed zap access logs carry `site_id`, method, URI, request/response headers, status, bytes, TTFB, and total response time. Per-site `accessLog.includeBodies` captures request/response bodies up to a configurable limit; sensitive headers are redacted unless explicitly enabled. Log writes run on a bounded asynchronous queue so disk I/O does not block forwarding; when the queue is full, log records are dropped and counted rather than slowing requests.
- Per-site in-memory metrics: request/error counts, status codes, in-flight requests, bytes in/out, average TTFB, average and maximum response time, and approximate P95 response time.

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

- `GET /api/health`
- `GET /api/sites`, `POST /api/sites`
- `GET /api/sites/{id}`, `PUT /api/sites/{id}`, `DELETE /api/sites/{id}`
- `POST /api/sites/{id}/start|stop|restart|reload`
- `GET /api/sites/{id}/metrics` returns current process metrics (reset when the process restarts).
- `PUT /api/sites/{id}/secrets/{name}`
- `GET /api/config.yaml` (download), `PUT /api/config.yaml` (transactional upsert import)

## Security / current limitations

- TLS `InsecureSkipVerify` is intentionally opt-in and disables upstream certificate verification; do not use it as a routine workaround.
- Secrets are stored as SQLite BLOBs but are **not encrypted at rest** yet. Protect DB backups and filesystem access; add a key-management/encryption layer before production use.
- Access-log bodies may contain credentials, personal information, or business data. Body capture defaults to 1 MiB; positive limits are capped at 8 MiB, while `maxBodyBytes: -1` disables truncation. Unlimited capture can consume substantial memory, and access-log entries may still be dropped if the asynchronous log queue is saturated; sensitive headers are redacted by default. Restrict access to log files and disable body logging when it is not needed.
- Metrics are currently in-memory and reset on process restart; request logs are asynchronously written to the zap log file with a `site_id` field for filtering. `droppedAccessLogCount` reports log events dropped when the bounded queue is saturated.
- Each site currently forwards through its first configured upstream; health checks, failover/load balancing, config versioning/rollback, and graceful zero-downtime listener replacement remain follow-up work.
- The custom `dialAddress` is applied in direct mode; custom destination resolution through an upstream proxy requires an explicit policy and is not silently implemented.
- zap currently writes to a single file without rotation. Configure external log rotation until an in-process rotation policy is added.
