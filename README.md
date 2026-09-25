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
- Per-site start, stop, restart, and reload; sites marked `autoStart: true` are started on process startup, with failures logged without preventing other sites from starting. Reload is currently a brief stop/start, so existing requests may be interrupted.
- Independent listener address/port and optional HTTPS listener certificate.
- First upstream supports HTTP/HTTPS, HTTP(S) proxy, SOCKS5/SOCKS5H, custom CA bundle, client certificate/key secret references, skip-verification, custom server name/SNI, and a direct-mode dial-address override.
- YAML configuration import/export at `PUT/GET /api/config.yaml`. YAML contains configuration and secret *references*, not private key bytes; SQLite remains the canonical store.
- JSON API and React management UI; zap writes application logs to the selected log directory.

## YAML example

```yaml
sites:
  - id: example
    name: Example site
    autoStart: true
    config:
      listenAddress: 127.0.0.1
      listenPort: 8443
      tls: true
      certificateSecret: site-cert
      privateKeySecret: site-key
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
- `PUT /api/sites/{id}/secrets/{name}`
- `GET /api/config.yaml` (download), `PUT /api/config.yaml` (transactional upsert import)

## Security / current limitations

- TLS `InsecureSkipVerify` is intentionally opt-in and disables upstream certificate verification; do not use it as a routine workaround.
- Secrets are stored as SQLite BLOBs but are **not encrypted at rest** yet. Protect DB backups and filesystem access; add a key-management/encryption layer before production use.
- The control API has no authentication yet and is intentionally bound to `127.0.0.1` by default. Add authentication, CSRF protections for browser use, and audit logging before binding to a non-loopback address.
- Each site currently forwards through its first configured upstream; health checks, failover/load balancing, config versioning/rollback, and graceful zero-downtime listener replacement remain follow-up work.
- The custom `dialAddress` is applied in direct mode; custom destination resolution through an upstream proxy requires an explicit policy and is not silently implemented.
- zap currently writes to a single file without rotation. Configure external log rotation until an in-process rotation policy is added.
