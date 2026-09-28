// nodeBootstrap.js turns "a node was just created/re-tokened" into the concrete commands and config files an
// operator needs to bring that node online: a plain CLI invocation, a systemd service, a `docker run`, and a
// docker-compose stack. It is pure and framework-free on purpose (see nodeBootstrap.test.js) — the wizard UI
// (NodeBootstrapGuide.jsx) is just presentation over these functions, numbering the steps and giving each code
// block its own copy button.

// Default host paths used by the command-line and systemd recipes (outside a container, so FHS-style paths).
export const DEFAULT_HOST_DATA_DIR = '/var/lib/rpop-node'
export const DEFAULT_HOST_LOG_DIR = '/var/log/rpop-node'
// Default paths inside the container, matching the Dockerfile's declared volumes (/app/data, /app/logs).
export const DEFAULT_CONTAINER_DATA_DIR = '/app/data'
export const DEFAULT_CONTAINER_LOG_DIR = '/app/logs'
export const DEFAULT_BIN_PATH = '/usr/local/bin/rpop'
export const DEFAULT_SYSTEMD_ENV_PATH = '/etc/rpop/node.env'
export const DEFAULT_SYSTEMD_SERVICE_NAME = 'rpop-node'

// shellQuote wraps value in single quotes for a POSIX shell, the only quoting style that needs no exceptions
// for a join token or URL: nothing inside single quotes is special except a single quote itself, which is
// closed, escaped as a literal ('\''), and reopened. Used for every value in a generated command that did not
// come from one of this module's own fixed path constants (join tokens, the controller URL, the image name).
export function shellQuote(value) {
  return `'${String(value ?? '').replace(/'/g, `'\\''`)}'`
}

// formatHost brackets an IPv6 literal for use in a URL's authority component, e.g. "[::1]:7443" — a defensive
// normalization for callers that might hand this an unbracketed IPv6 address; window.location.hostname already
// comes back bracketed for IPv6 (the WHATWG URL host serializer always brackets one), so this is a no-op for the
// browser-derived case deriveControllerURL actually uses it for. An already-bracketed or non-IPv6 host is
// returned unchanged.
export function formatHost(hostname) {
  const host = hostname || ''
  if (!host || host.startsWith('[')) return host
  return host.includes(':') ? `[${host}]` : host
}

// hostFromURL returns the host (no port; IPv6 stays bracketed, per the URL host serializer) of a
// "https://host[:port]" value, for the systemd guide's scp hint; '' for anything that does not parse as a URL.
export function hostFromURL(value) {
  try {
    return new URL(value).hostname
  } catch {
    return ''
  }
}

// deriveControllerURL picks the node onboarding guide's default "-controller" value: the operator's own
// nodeControllerUrl system setting when set, otherwise a best-effort guess built from the browser's own
// hostname and the southbound port the controller reported (see nodeBootstrapInfoAPI); '' when neither is
// available (southbound disabled and no setting configured), which the guide shows as a warning instead of a
// guess that would not work.
export function deriveControllerURL({ nodeControllerUrl = '', hostname = '', southboundPort } = {}) {
  const configured = String(nodeControllerUrl || '').trim()
  if (configured) return configured
  if (hostname && southboundPort) return `https://${formatHost(hostname)}:${southboundPort}`
  return ''
}

// relayPortFromAddress extracts the numeric port from a node's relayAddress ("host:port", including a
// bracketed IPv6 host); null when relayAddress is empty or has no valid trailing ":port".
export function relayPortFromAddress(relayAddress) {
  if (!relayAddress) return null
  const match = String(relayAddress).trim().match(/:(\d+)$/)
  if (!match) return null
  const port = Number(match[1])
  return port >= 1 && port <= 65535 ? port : null
}

// defaultNodeImage is the fallback image name (system setting nodeImage empty) — see system_settings.go's
// NodeImage doc comment, which this mirrors on the frontend.
export function defaultNodeImage(version) {
  return `rpop:${version || 'dev'}`
}

// nodeCommandLine is the plain "run it directly" recipe (guide tab 1): a single, readable multi-line command.
export function nodeCommandLine({
  controllerURL, joinToken, dataDir = DEFAULT_HOST_DATA_DIR, logDir = DEFAULT_HOST_LOG_DIR, relayListen = '',
  binPath = 'rpop',
} = {}) {
  const lines = [
    binPath, '-mode node',
    `-controller ${shellQuote(controllerURL)}`,
    `-join-token ${shellQuote(joinToken)}`,
    `-data-dir ${shellQuote(dataDir)}`,
    `-log-dir ${shellQuote(logDir)}`,
  ]
  if (relayListen) lines.push(`-relay-listen ${shellQuote(relayListen)}`)
  return lines.join(' \\\n  ')
}

// systemdInstallFromImageCommands is systemd guide step 1, option a: pull the node binary out of the node image
// without ever running it, so the host only gets the single static binary rather than the whole container.
export function systemdInstallFromImageCommands({ image, binPath = DEFAULT_BIN_PATH } = {}) {
  return [
    `docker create --name rpop-tmp ${shellQuote(image)}`,
    `docker cp rpop-tmp:/app/rpop ${binPath}`,
    'docker rm rpop-tmp',
    `chmod +x ${binPath}`,
  ].join('\n')
}

// systemdInstallFromScpCommand is systemd guide step 1, option b: copy the same-version binary straight from
// the controller host (only meaningful when rpop already runs natively there, e.g. controller/all-in-one mode).
export function systemdInstallFromScpCommand({ host = '', binPath = DEFAULT_BIN_PATH } = {}) {
  const from = host ? `<user>@${host}` : '<user>@<controller-host>'
  return [`scp ${from}:${binPath} ${binPath}`, `chmod +x ${binPath}`].join('\n')
}

// systemdSetupCommands is step 2: a dedicated, unprivileged system user and its data/log directories.
export function systemdSetupCommands({ dataDir = DEFAULT_HOST_DATA_DIR, logDir = DEFAULT_HOST_LOG_DIR } = {}) {
  return [
    'useradd --system --no-create-home --shell /usr/sbin/nologin rpop',
    `mkdir -p ${dataDir} ${logDir}`,
    `chown -R rpop:rpop ${dataDir} ${logDir}`,
  ].join('\n')
}

// systemdEnvFile is the content of step 3's EnvironmentFile; the join token is only needed for the first start
// (see systemdRemoveTokenCommands, step 7).
export function systemdEnvFile({
  controllerURL, joinToken, dataDir = DEFAULT_HOST_DATA_DIR, logDir = DEFAULT_HOST_LOG_DIR, relayListen = '',
} = {}) {
  const lines = [
    'RPOP_MODE=node',
    `RPOP_CONTROLLER=${controllerURL}`,
    `RPOP_JOIN_TOKEN=${joinToken}`,
    `RPOP_DATA_DIR=${dataDir}`,
    `RPOP_LOG_DIR=${logDir}`,
  ]
  if (relayListen) lines.push(`RPOP_RELAY_LISTEN=${relayListen}`)
  return lines.join('\n') + '\n'
}

// systemdEnvFileSetupCommands is the rest of step 3: create the directory the env file lives in and lock its
// permissions down, since it briefly holds a live join token.
export function systemdEnvFileSetupCommands({ envPath = DEFAULT_SYSTEMD_ENV_PATH } = {}) {
  return [`mkdir -p ${envPath.slice(0, envPath.lastIndexOf('/')) || '/'}`, `chmod 600 ${envPath}`].join('\n')
}

// systemdUnitFile is step 4's unit file: hardened, but ReadWritePaths keeps it able to write its own data and
// log directories under ProtectSystem=strict, and AmbientCapabilities lets the unprivileged "rpop" user still
// bind a site to a privileged port (80/443) without running as root.
export function systemdUnitFile({
  dataDir = DEFAULT_HOST_DATA_DIR, logDir = DEFAULT_HOST_LOG_DIR, binPath = DEFAULT_BIN_PATH,
  envPath = DEFAULT_SYSTEMD_ENV_PATH,
} = {}) {
  return `[Unit]
Description=rpop node
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=rpop
Group=rpop
EnvironmentFile=${envPath}
ExecStart=${binPath}
Restart=always
RestartSec=2
AmbientCapabilities=CAP_NET_BIND_SERVICE
LimitNOFILE=65536
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=${dataDir} ${logDir}

[Install]
WantedBy=multi-user.target
`
}

// systemdEnableCommands is step 5.
export function systemdEnableCommands({ serviceName = DEFAULT_SYSTEMD_SERVICE_NAME } = {}) {
  return ['systemctl daemon-reload', `systemctl enable --now ${serviceName}`].join('\n')
}

// systemdStatusCommand and systemdLogsCommand are step 6's two independent checks.
export function systemdStatusCommand({ serviceName = DEFAULT_SYSTEMD_SERVICE_NAME } = {}) {
  return `systemctl status ${serviceName}`
}
export function systemdLogsCommand({ serviceName = DEFAULT_SYSTEMD_SERVICE_NAME } = {}) {
  return `journalctl -u ${serviceName} -f`
}

// systemdRemoveTokenCommands is step 7: the join token in the env file is only needed once, so drop it and
// restart once the node shows up online in the console.
export function systemdRemoveTokenCommands({
  envPath = DEFAULT_SYSTEMD_ENV_PATH, serviceName = DEFAULT_SYSTEMD_SERVICE_NAME,
} = {}) {
  return [`sed -i '/^RPOP_JOIN_TOKEN=/d' ${envPath}`, `systemctl restart ${serviceName}`].join('\n')
}

// dockerRunCommandHostNetwork is the "docker run" tab's recommended recipe: --network host needs no port
// mapping at all (every port the node ever binds, relay or site, is already reachable), which is the whole
// reason it is recommended over the bridge-network alternative below.
export function dockerRunCommandHostNetwork({
  image, controllerURL, joinToken, dataDir = DEFAULT_CONTAINER_DATA_DIR, logDir = DEFAULT_CONTAINER_LOG_DIR,
} = {}) {
  return [
    'docker run -d',
    `--name ${DEFAULT_SYSTEMD_SERVICE_NAME}`,
    '--network host',
    '--restart unless-stopped',
    '-e RPOP_MODE=node',
    `-e RPOP_CONTROLLER=${shellQuote(controllerURL)}`,
    `-e RPOP_JOIN_TOKEN=${shellQuote(joinToken)}`,
    `-e RPOP_DATA_DIR=${dataDir}`,
    `-e RPOP_LOG_DIR=${logDir}`,
    `-v rpop-node-data:${dataDir}`,
    `-v rpop-node-logs:${logDir}`,
    shellQuote(image),
  ].join(' \\\n  ')
}

// dockerRunCommandBridge is the alternative without host networking: only the relay port is known ahead of
// time (from the node's relayAddress), so it is the only one mapped here — the guide's prose explains that each
// site's own listen port needs the same "-p" treatment once the node runs one. RPOP_RELAY_LISTEN binds the
// container's own interface (0.0.0.0) while relayAddress keeps advertising the host's published port, which is
// what makes a bridge-network relay port reachable at all.
export function dockerRunCommandBridge({
  image, controllerURL, joinToken, dataDir = DEFAULT_CONTAINER_DATA_DIR, logDir = DEFAULT_CONTAINER_LOG_DIR,
  relayPort,
} = {}) {
  const lines = ['docker run -d', `--name ${DEFAULT_SYSTEMD_SERVICE_NAME}`, '--restart unless-stopped']
  if (relayPort) lines.push(`-p ${relayPort}:${relayPort}`)
  lines.push(
    '-e RPOP_MODE=node',
    `-e RPOP_CONTROLLER=${shellQuote(controllerURL)}`,
    `-e RPOP_JOIN_TOKEN=${shellQuote(joinToken)}`,
    `-e RPOP_DATA_DIR=${dataDir}`,
    `-e RPOP_LOG_DIR=${logDir}`,
  )
  if (relayPort) lines.push(`-e RPOP_RELAY_LISTEN=0.0.0.0:${relayPort}`)
  lines.push(`-v rpop-node-data:${dataDir}`, `-v rpop-node-logs:${logDir}`, shellQuote(image))
  return lines.join(' \\\n  ')
}

// yamlDoubleQuote is a minimal YAML double-quoted scalar: enough for the plain strings this module ever embeds
// (a URL, an image reference), not a general YAML encoder.
function yamlDoubleQuote(value) {
  return `"${String(value ?? '').replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`
}

// dockerComposeFile is the compose tab's docker-compose.yml; the join token is read from a sibling .env file
// (see dockerComposeEnvFile) instead of being written into this file directly, so the compose file itself is
// safe to commit if the operator wants to.
export function dockerComposeFile({
  image, controllerURL, dataDir = DEFAULT_CONTAINER_DATA_DIR, logDir = DEFAULT_CONTAINER_LOG_DIR,
} = {}) {
  return `services:
  rpop-node:
    image: ${yamlDoubleQuote(image)}
    container_name: rpop-node
    restart: unless-stopped
    network_mode: host
    environment:
      RPOP_MODE: node
      RPOP_CONTROLLER: ${yamlDoubleQuote(controllerURL)}
      RPOP_JOIN_TOKEN: "\${RPOP_JOIN_TOKEN}"
      RPOP_DATA_DIR: ${dataDir}
      RPOP_LOG_DIR: ${logDir}
    volumes:
      - rpop-node-data:${dataDir}
      - rpop-node-logs:${logDir}
    # 不使用 host 网络时，删除上面的 network_mode: host，改为映射中继端口与每个站点的端口，例如：
    # ports:
    #   - "<中继端口>:<中继端口>"
    #   - "<站点端口>:<站点端口>"

volumes:
  rpop-node-data:
  rpop-node-logs:
`
}

// dockerComposeEnvFile is the .env file docker-compose.yml above reads RPOP_JOIN_TOKEN from.
export function dockerComposeEnvFile({ joinToken } = {}) {
  return `RPOP_JOIN_TOKEN=${joinToken}\n`
}

// dockerComposeUpCommand and dockerComposeLogsCommand are the compose tab's last step.
export function dockerComposeUpCommand() {
  return 'docker compose up -d'
}
export function dockerComposeLogsCommand() {
  return 'docker compose logs -f'
}
