import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  DEFAULT_CONTAINER_DATA_DIR, DEFAULT_CONTAINER_LOG_DIR, DEFAULT_HOST_DATA_DIR, DEFAULT_HOST_LOG_DIR,
  defaultNodeImage, deriveControllerURL, dockerComposeEnvFile, dockerComposeFile, dockerRunCommandBridge,
  dockerRunCommandHostNetwork, formatHost, hostFromURL, nodeCommandLine, relayPortFromAddress, shellQuote,
  systemdEnvFile, systemdInstallFromImageCommands, systemdInstallFromScpCommand, systemdRemoveTokenCommands,
  systemdUnitFile,
} from './nodeBootstrap.js'

test('shellQuote wraps plain values and escapes embedded single quotes', () => {
  assert.equal(shellQuote('rpop:1.2.3'), "'rpop:1.2.3'")
  assert.equal(shellQuote(''), "''")
  assert.equal(shellQuote("it's a token"), "'it'\\''s a token'")
  // A value with several quotes escapes every one of them independently.
  assert.equal(shellQuote("a'b'c"), "'a'\\''b'\\''c'")
  assert.equal(shellQuote(undefined), "''")
})

test('formatHost brackets an IPv6 literal but leaves everything else alone', () => {
  assert.equal(formatHost('controller.example.com'), 'controller.example.com')
  assert.equal(formatHost('10.0.0.5'), '10.0.0.5')
  assert.equal(formatHost('::1'), '[::1]')
  assert.equal(formatHost('2001:db8::1'), '[2001:db8::1]')
  assert.equal(formatHost('[::1]'), '[::1]')
  assert.equal(formatHost(''), '')
})

test('hostFromURL extracts the host (port stripped, IPv6 still bracketed) from a controller URL', () => {
  assert.equal(hostFromURL('https://controller.example.com:7443'), 'controller.example.com')
  // The WHATWG URL parser keeps IPv6 brackets in .hostname (unlike a bare host component elsewhere), so this
  // stays bracketed too - callers that need it unbracketed (there are none today) would strip it themselves.
  assert.equal(hostFromURL('https://[::1]:7443'), '[::1]')
  assert.equal(hostFromURL('not a url'), '')
})

test('deriveControllerURL prefers the configured setting over any derivation', () => {
  const got = deriveControllerURL({
    nodeControllerUrl: 'https://controller.example.com:7443',
    hostname: 'console.example.com',
    southboundPort: 9443,
  })
  assert.equal(got, 'https://controller.example.com:7443')
})

test('deriveControllerURL falls back to https://{hostname}:{southboundPort}', () => {
  assert.equal(
    deriveControllerURL({ nodeControllerUrl: '', hostname: 'console.example.com', southboundPort: 7443 }),
    'https://console.example.com:7443',
  )
})

test('deriveControllerURL brackets an IPv6 browser hostname when deriving', () => {
  assert.equal(
    deriveControllerURL({ nodeControllerUrl: '', hostname: '2001:db8::1', southboundPort: 7443 }),
    'https://[2001:db8::1]:7443',
  )
})

test('deriveControllerURL returns "" when southbound is off and nothing is configured', () => {
  assert.equal(deriveControllerURL({ nodeControllerUrl: '', hostname: 'console.example.com', southboundPort: 0 }), '')
  assert.equal(deriveControllerURL({}), '')
})

test('relayPortFromAddress reads the trailing port, including bracketed IPv6 hosts', () => {
  assert.equal(relayPortFromAddress('10.0.0.2:7000'), 7000)
  assert.equal(relayPortFromAddress('edge1.example.com:7443'), 7443)
  assert.equal(relayPortFromAddress('[2001:db8::1]:7000'), 7000)
  assert.equal(relayPortFromAddress(''), null)
  assert.equal(relayPortFromAddress(undefined), null)
  assert.equal(relayPortFromAddress('no-port-here'), null)
  assert.equal(relayPortFromAddress('host:0'), null)
  assert.equal(relayPortFromAddress('host:70000'), null)
})

test('defaultNodeImage builds rpop:<version>, falling back to "dev"', () => {
  assert.equal(defaultNodeImage('1.4.0'), 'rpop:1.4.0')
  assert.equal(defaultNodeImage(''), 'rpop:dev')
  assert.equal(defaultNodeImage(undefined), 'rpop:dev')
})

test('nodeCommandLine contains every required flag, with defaults when relayListen is unset', () => {
  const command = nodeCommandLine({
    controllerURL: 'https://controller.example.com:7443', joinToken: 'tok-1', binPath: '/tmp/rpop',
  })
  assert.match(command, /^\/tmp\/rpop \\\n {2}-mode node/)
  assert.match(command, /-controller 'https:\/\/controller\.example\.com:7443'/)
  assert.match(command, /-join-token 'tok-1'/)
  assert.ok(command.includes(`-data-dir '${DEFAULT_HOST_DATA_DIR}'`))
  assert.ok(command.includes(`-log-dir '${DEFAULT_HOST_LOG_DIR}'`))
  assert.ok(!command.includes('-relay-listen'))
})

test('nodeCommandLine adds -relay-listen only when a relay listen address is given, and quotes it', () => {
  const command = nodeCommandLine({
    controllerURL: 'https://controller.example.com:7443', joinToken: 'tok-1', relayListen: '0.0.0.0:7000',
  })
  assert.ok(command.includes("-relay-listen '0.0.0.0:7000'"))
})

test('nodeCommandLine shell-escapes a join token containing a single quote', () => {
  const command = nodeCommandLine({ controllerURL: 'https://controller.example.com:7443', joinToken: "a'b" })
  assert.ok(command.includes(shellQuote("a'b")))
})

test('systemdInstallFromImageCommands and systemdInstallFromScpCommand cover both install paths', () => {
  const fromImage = systemdInstallFromImageCommands({ image: 'registry.example.com/rpop:1.2.3' })
  assert.match(fromImage, /docker create --name rpop-tmp 'registry\.example\.com\/rpop:1\.2\.3'/)
  assert.match(fromImage, /docker cp rpop-tmp:\/app\/rpop \/usr\/local\/bin\/rpop/)
  assert.match(fromImage, /chmod \+x \/usr\/local\/bin\/rpop/)

  const fromScp = systemdInstallFromScpCommand({ host: 'controller.example.com' })
  assert.match(fromScp, /scp <user>@controller\.example\.com:\/usr\/local\/bin\/rpop/)
})

test('systemdEnvFile contains every required key and includes RPOP_RELAY_LISTEN only when set', () => {
  const withoutRelay = systemdEnvFile({ controllerURL: 'https://controller.example.com:7443', joinToken: 'tok-1' })
  assert.match(withoutRelay, /^RPOP_MODE=node$/m)
  assert.match(withoutRelay, /^RPOP_CONTROLLER=https:\/\/controller\.example\.com:7443$/m)
  assert.match(withoutRelay, /^RPOP_JOIN_TOKEN=tok-1$/m)
  assert.match(withoutRelay, new RegExp(`^RPOP_DATA_DIR=${DEFAULT_HOST_DATA_DIR}$`, 'm'))
  assert.match(withoutRelay, new RegExp(`^RPOP_LOG_DIR=${DEFAULT_HOST_LOG_DIR}$`, 'm'))
  assert.ok(!withoutRelay.includes('RPOP_RELAY_LISTEN'))

  const withRelay = systemdEnvFile({ controllerURL: 'https://c:7443', joinToken: 't', relayListen: '0.0.0.0:7000' })
  assert.match(withRelay, /^RPOP_RELAY_LISTEN=0\.0\.0\.0:7000$/m)
})

test('systemdUnitFile keeps both data and log directories writable under ProtectSystem=strict', () => {
  const unit = systemdUnitFile({ dataDir: '/var/lib/rpop-node', logDir: '/var/log/rpop-node' })
  assert.match(unit, /ExecStart=\/usr\/local\/bin\/rpop/)
  assert.match(unit, /User=rpop/)
  assert.match(unit, /EnvironmentFile=\/etc\/rpop\/node\.env/)
  assert.match(unit, /AmbientCapabilities=CAP_NET_BIND_SERVICE/)
  assert.match(unit, /ProtectSystem=strict/)
  assert.match(unit, /ReadWritePaths=\/var\/lib\/rpop-node \/var\/log\/rpop-node/)
  assert.match(unit, /Restart=always/)
})

test('systemdRemoveTokenCommands removes the token line and restarts the service', () => {
  const commands = systemdRemoveTokenCommands()
  assert.match(commands, /sed -i '\/\^RPOP_JOIN_TOKEN=\/d' \/etc\/rpop\/node\.env/)
  assert.match(commands, /systemctl restart rpop-node/)
})

test('dockerRunCommandHostNetwork carries the image, controller URL, and join token, quoted', () => {
  const command = dockerRunCommandHostNetwork({
    image: 'rpop:1.2.3', controllerURL: 'https://controller.example.com:7443', joinToken: 'tok-1',
  })
  assert.match(command, /--network host/)
  assert.ok(command.includes(`-e RPOP_CONTROLLER=${shellQuote('https://controller.example.com:7443')}`))
  assert.ok(command.includes(`-e RPOP_JOIN_TOKEN=${shellQuote('tok-1')}`))
  assert.ok(command.trim().endsWith(shellQuote('rpop:1.2.3')))
  assert.ok(command.includes(`-e RPOP_DATA_DIR=${DEFAULT_CONTAINER_DATA_DIR}`))
  assert.ok(command.includes(`-e RPOP_LOG_DIR=${DEFAULT_CONTAINER_LOG_DIR}`))
  assert.ok(!command.includes('-p '))
})

test('dockerRunCommandBridge maps the relay port and sets RPOP_RELAY_LISTEN only when relayPort is known', () => {
  const withPort = dockerRunCommandBridge({
    image: 'rpop:1.2.3', controllerURL: 'https://c:7443', joinToken: 't', relayPort: 7000,
  })
  assert.ok(withPort.includes('-p 7000:7000'))
  assert.ok(withPort.includes('-e RPOP_RELAY_LISTEN=0.0.0.0:7000'))

  const withoutPort = dockerRunCommandBridge({ image: 'rpop:1.2.3', controllerURL: 'https://c:7443', joinToken: 't' })
  assert.ok(!withoutPort.includes('-p '))
  assert.ok(!withoutPort.includes('RPOP_RELAY_LISTEN'))
})

test('dockerComposeFile embeds the image and controller URL and reads the token from .env', () => {
  const compose = dockerComposeFile({ image: 'rpop:1.2.3', controllerURL: 'https://controller.example.com:7443' })
  assert.match(compose, /image: "rpop:1\.2\.3"/)
  assert.match(compose, /RPOP_CONTROLLER: "https:\/\/controller\.example\.com:7443"/)
  assert.match(compose, /RPOP_JOIN_TOKEN: "\$\{RPOP_JOIN_TOKEN\}"/)
  assert.ok(!compose.includes('tok-1'))
  assert.match(compose, new RegExp(`- rpop-node-data:${DEFAULT_CONTAINER_DATA_DIR}`))
})

test('dockerComposeFile double-quotes an image name that contains a double quote', () => {
  const compose = dockerComposeFile({ image: 'weird"image', controllerURL: 'https://c:7443' })
  assert.match(compose, /image: "weird\\"image"/)
})

test('dockerComposeEnvFile carries the join token and nothing else sensitive', () => {
  assert.equal(dockerComposeEnvFile({ joinToken: 'tok-1' }), 'RPOP_JOIN_TOKEN=tok-1\n')
})
