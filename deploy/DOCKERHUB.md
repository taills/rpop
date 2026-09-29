# rpop — Reverse Proxy over Proxy

A multi-site reverse proxy written in Go, with an embedded web console. Every site gets its own listener, hostnames, TLS
settings, and upstream transport, and upstreams can be reached directly, through SOCKS5/HTTP(S) proxies, or along
multi-hop paths across rpop nodes. Configuration lives in SQLite and is managed entirely from the console or its API.

- Source, docs, and issues: <https://github.com/taills/rpop>
- Prebuilt binaries (linux, macOS, windows): <https://github.com/taills/rpop/releases>

## Features

- **Multi-site proxying** — HTTP and HTTPS sites; sites sharing an address route by `Host`/SNI. Reloads swap a site
  atomically: in-flight requests, SSE streams, and WebSockets finish on the previous version.
- **Flexible upstreams** — per-upstream HTTP(S), SOCKS5, and SOCKS5H proxies, custom CA roots, mutual-TLS client
  certificates, SNI override, and a direct dial-address override.
- **Caddy-style routing** — send requests to different upstreams by path and header, with a route simulator in the
  console that explains which rule matches and why.
- **Multi-node overlay** — run a controller and any number of data-plane nodes linked by HTTP/2 mutual TLS. An upstream
  can list candidate paths through nodes and proxies (`client > node1 > socks5-A > node2 > origin`); the first path that
  connects wins and failed paths cool down and are retried automatically.
- **Single-port deployment** — the console, the southbound listener, a node's relay port, and sites can share one port,
  dispatched by TLS SNI or the plaintext `Host` header.
- **Observability** — access logs to local files, S3, ClickHouse, or Elasticsearch, searchable in the console; per-site
  metrics; and per-request tracing across every hop.

## Tags

| Tag | Contents |
| --- | --- |
| `latest` | The newest stable release |
| `<major>.<minor>.<patch>` (e.g. `0.1.0`) | That exact release |
| `<major>.<minor>` (e.g. `0.1`) | The newest patch release of that minor version |
| `edge` | Built from every push to `main`; may be unstable |

Pre-release versions such as `1.2.0-rc.1` are published only under their full version tag. Every tag is a
multi-platform image for `linux/amd64` and `linux/arm64`.

## Quick start

```sh
docker run -d --name rpop \
  -p 127.0.0.1:8080:8080 \
  -v rpop-data:/app/data \
  -v rpop-logs:/app/logs \
  nil2026/rpop:latest
```

Open <http://127.0.0.1:8080> and set the administrator password (at least 12 characters) on the first visit. Every
management API requires that login.

### Exposing sites

Sites listen on ports you choose in the console, so the container has to expose them too:

- **Published ports** — add a `-p` for each site port and set the site's listen address to `0.0.0.0` (the container's
  own interfaces).
- **Host networking** — with `--network host` every site port is reachable without publishing it. Publishing no
  longer applies to the console either, so set `-e RPOP_ADDR=127.0.0.1:8080` to keep it off public interfaces.

### Docker Compose

```yaml
services:
  rpop:
    image: nil2026/rpop:latest
    restart: unless-stopped
    ports:
      - "127.0.0.1:8080:8080" # console and API
      - "80:80"               # site listeners configured in the console
      - "443:443"
    volumes:
      - rpop-data:/app/data
      - rpop-logs:/app/logs

volumes:
  rpop-data:
  rpop-logs:
```

[`deploy/docker-compose.example.yml`](https://github.com/taills/rpop/blob/main/deploy/docker-compose.example.yml)
extends this with optional RustFS (S3), ClickHouse, and Elasticsearch services for access-log storage.

## Controller and nodes

One image runs in three modes, selected with `RPOP_MODE`:

| Mode | Runs |
| --- | --- |
| `all-in-one` (default) | The console, the API, and an embedded node named `local` that serves sites not placed on other nodes |
| `controller` | The console and API only; nodes connect to its southbound listener on port `7443` |
| `node` | A data-plane node without a console, configured by the controller |

Start a controller and publish its southbound port to the nodes:

```sh
docker run -d --name rpop-controller \
  -e RPOP_MODE=controller \
  -p 127.0.0.1:8080:8080 \
  -p 7443:7443 \
  -v rpop-data:/app/data \
  -v rpop-logs:/app/logs \
  nil2026/rpop:latest
```

Create a node on the console's Nodes page. The dialog shows a single-use join token (valid for 24 hours) and generates
ready-to-copy `docker run`, Docker Compose, systemd, and plain CLI recipes with the controller address filled in. A
node container looks like this:

```sh
docker run -d --name rpop-node --network host \
  -e RPOP_MODE=node \
  -e RPOP_CONTROLLER=https://controller.example.com:7443 \
  -e RPOP_JOIN_TOKEN=<join-token> \
  -e RPOP_DATA_DIR=/app/data \
  -v rpop-node-data:/app/data \
  -v rpop-node-logs:/app/logs \
  nil2026/rpop:latest
```

The join token is needed only for the first registration. The node keeps its identity and the last applied
configuration in `/app/data`, and keeps serving that configuration while the controller is unreachable. To accept
remote nodes on an all-in-one controller as well, set `RPOP_SOUTHBOUND_ADDR=:7443`.

## Configuration

Command-line options have matching `RPOP_*` environment variables. The most common ones:

| Variable | Default in the image | Purpose |
| --- | --- | --- |
| `RPOP_MODE` | `all-in-one` | `all-in-one`, `controller`, or `node` |
| `RPOP_ADDR` | `0.0.0.0:8080` | Console and API listen address |
| `RPOP_DB` | `/app/data/rpop.db` | SQLite database path |
| `RPOP_LOG_DIR` | `/app/logs` | Application and local access-log directory |
| `RPOP_CONSOLE_HOSTNAMES` | — | Hostnames the console answers on when its port is shared with sites |
| `RPOP_SOUTHBOUND_ADDR` | `:7443` in `controller` mode, off otherwise | Address nodes connect to |
| `RPOP_CONTROLLER` | — | Node mode: the controller's southbound URL |
| `RPOP_JOIN_TOKEN` | — | Node mode: join token for the first registration |
| `RPOP_DATA_DIR` | `data/node` (under `/app`) | Node mode: node identity and configuration cache |
| `RPOP_RELAY_LISTEN` | — | Node mode: bind the relay port here instead of on the relay address's port |

List every option with `docker run --rm nil2026/rpop:latest -h`.

| Path or port | Purpose |
| --- | --- |
| `/app/data` | SQLite database (all configuration and secrets) and node identity — mount a volume |
| `/app/logs` | Application logs and local access logs — mount a volume |
| `8080` | Console and API |
| `7443` | Southbound listener for nodes (`controller` mode) |

The image's health check runs `rpop -health-check`. It probes the console at `RPOP_ADDR`, so change the console
address through `RPOP_ADDR` rather than a `-addr` argument. In `node` mode it reports healthy once the node has
registered with the controller. The container time zone is `Asia/Shanghai`.

## Security

- Keep the console on loopback or behind a trusted HTTPS reverse proxy; the login and session cookie are not encrypted
  on a plaintext port.
- Site secrets and log-adapter credentials are stored in SQLite without encryption at rest. Restrict access to the
  `/app/data` volume and its backups.
- Access-log bodies can contain credentials or personal data. Disable body capture where it is not needed.

## License

rpop is released under the [MIT License](https://github.com/taills/rpop/blob/main/LICENSE).
