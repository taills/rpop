import CopyableCode from './CopyableCode.jsx'
import { GuideOutcome, GuideStep } from './NodeBootstrapParts.jsx'
import { nodeCommandLine } from '@/nodeBootstrap.js'

// NodeBootstrapCommandTab is the simplest recipe: run the binary directly, once, in the foreground. Good for a
// quick try-out; systemd (see NodeBootstrapSystemdTab) is the recipe for running it unattended.
export default function NodeBootstrapCommandTab({ controllerURL, token, relayPort }) {
  const command = nodeCommandLine({ controllerURL, joinToken: token })
  const withRelayListen = relayPort
    ? nodeCommandLine({ controllerURL, joinToken: token, relayListen: `0.0.0.0:${relayPort}` })
    : ''

  return (
    <div>
      <GuideStep n={1} title="运行节点">
        <p>在已安装 rpop 二进制、能访问控制器的机器上执行：</p>
        <CopyableCode label="启动命令" code={command} />
        {relayPort > 0 && (
          <>
            <p>
              该节点配置了中继地址（端口 {relayPort}）。如果这台机器用于对外公布的地址（NAT、端口转发、多网卡）和它实际监听的地址不同，需要额外加上 <code>-relay-listen</code> 指定本机实际监听的地址；否则可以省略：
            </p>
            <CopyableCode label="带 -relay-listen 的启动命令" code={withRelayListen} />
          </>
        )}
        <p>首次启动会用 <code>-join-token</code> 完成注册，并把节点身份写入 <code>-data-dir</code>；之后重启无需再带 <code>-join-token</code>（会被忽略）。</p>
      </GuideStep>
      <GuideOutcome tokenNote="命令行方式的 token 只出现在这一次的启动参数里，不会被保存到磁盘，无需额外清理；如果把这条命令写进了脚本或历史记录，注册成功后请自行删除。" />
    </div>
  )
}
