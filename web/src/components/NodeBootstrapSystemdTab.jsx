import CopyableCode from './CopyableCode.jsx'
import { GuideOutcome, GuideStep } from './NodeBootstrapParts.jsx'
import {
  DEFAULT_SYSTEMD_ENV_PATH, hostFromURL, systemdEnableCommands, systemdEnvFile, systemdEnvFileSetupCommands,
  systemdInstallFromImageCommands, systemdInstallFromScpCommand, systemdLogsCommand, systemdRemoveTokenCommands,
  systemdSetupCommands, systemdStatusCommand, systemdUnitFile,
} from '@/nodeBootstrap.js'

const UNIT_PATH = '/etc/systemd/system/rpop-node.service'

// NodeBootstrapSystemdTab is the recipe for running a node unattended on a Linux host: a dedicated system user,
// a hardened unit, and the join token kept only in a 0600 env file that gets its token line removed once the
// node has registered (see step 7 and GuideOutcome's tokenNote below).
export default function NodeBootstrapSystemdTab({ controllerURL, token, relayPort, image }) {
  const relayListen = relayPort ? `0.0.0.0:${relayPort}` : ''

  return (
    <div>
      <GuideStep n={1} title="安装与控制器版本相同的 rpop 二进制">
        <p>方式 a：从节点镜像中取出二进制（推荐，天然与控制器同版本）：</p>
        <CopyableCode label="从镜像安装" code={systemdInstallFromImageCommands({ image })} />
        <p>方式 b：如果控制器本机也在原生运行 rpop，直接从控制器主机拷贝：</p>
        <CopyableCode label="从控制器主机 scp" code={systemdInstallFromScpCommand({ host: hostFromURL(controllerURL) })} />
      </GuideStep>

      <GuideStep n={2} title="创建系统用户和数据/日志目录">
        <CopyableCode label="useradd / mkdir / chown" code={systemdSetupCommands()} />
      </GuideStep>

      <GuideStep n={3} title={`写入 ${DEFAULT_SYSTEMD_ENV_PATH}`}>
        <CopyableCode label="创建目录并收紧权限" code={systemdEnvFileSetupCommands()} />
        <p>文件内容（token 只在首次启动时用到）：</p>
        <CopyableCode label={DEFAULT_SYSTEMD_ENV_PATH} code={systemdEnvFile({ controllerURL, joinToken: token })} />
        {relayPort > 0 && (
          <p>
            该节点配置了中继地址（端口 {relayPort}）。仅当这台主机对外公布的地址和它实际监听的地址不同（NAT、端口转发）时，再追加一行 <code>{`RPOP_RELAY_LISTEN=${relayListen}`}</code>。
          </p>
        )}
      </GuideStep>

      <GuideStep n={4} title={`写入 ${UNIT_PATH}`}>
        <p>已按 systemd 加固（<code>ProtectSystem=strict</code> 等），并保留了对数据/日志目录的写权限，以及绑定 80/443 等特权端口所需的 <code>CAP_NET_BIND_SERVICE</code>：</p>
        <CopyableCode label={UNIT_PATH} code={systemdUnitFile()} />
      </GuideStep>

      <GuideStep n={5} title="启用并启动服务">
        <CopyableCode label="daemon-reload / enable --now" code={systemdEnableCommands()} />
      </GuideStep>

      <GuideStep n={6} title="查看状态与日志">
        <CopyableCode label="服务状态" code={systemdStatusCommand()} />
        <CopyableCode label="实时日志" code={systemdLogsCommand()} />
      </GuideStep>

      <GuideStep n={7} title="注册成功后，从 env 文件中删除 token">
        <CopyableCode label="删除 token 并重启" code={systemdRemoveTokenCommands()} />
      </GuideStep>

      <GuideOutcome tokenNote={`注册成功后请执行第 7 步：token 只在首次注册时需要，留在 ${DEFAULT_SYSTEMD_ENV_PATH} 里没有用处，删掉后更安全。`} />
    </div>
  )
}
