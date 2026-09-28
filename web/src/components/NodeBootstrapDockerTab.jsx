import CopyableCode from './CopyableCode.jsx'
import { GuideOutcome, GuideStep } from './NodeBootstrapParts.jsx'
import { dockerRunCommandBridge, dockerRunCommandHostNetwork } from '@/nodeBootstrap.js'

// NodeBootstrapDockerTab is the single-container recipe: --network host is recommended (every port the node
// ever binds is already reachable, no per-site port mapping to keep in sync), with the bridge-network
// alternative underneath for hosts that cannot use host networking (notably Docker Desktop on macOS/Windows,
// which does not support it at all).
export default function NodeBootstrapDockerTab({ controllerURL, token, relayPort, image }) {
  const hostNetworkCommand = dockerRunCommandHostNetwork({ image, controllerURL, joinToken: token })
  const bridgeCommand = dockerRunCommandBridge({ image, controllerURL, joinToken: token, relayPort })

  return (
    <div>
      <GuideStep n={1} title="运行容器（推荐：host 网络）">
        <p><code>--network host</code> 下容器与宿主机共享网络栈，中继端口和之后在此节点上运行的每个站点端口都不用单独映射：</p>
        <CopyableCode label="docker run（host 网络）" code={hostNetworkCommand} />
      </GuideStep>

      <GuideStep n={2} title="不使用 host 网络时">
        <p>
          需要显式映射端口：下面命令映射了该节点的中继端口{relayPort > 0 ? `（${relayPort}）` : ''}；之后在此节点上每新增一个站点，都要为它的监听端口追加一组 <code>-p &lt;端口&gt;:&lt;端口&gt;</code>。Docker Desktop（macOS/Windows）不支持 <code>--network host</code>，必须使用这种方式。
        </p>
        <CopyableCode label="docker run（桥接网络 + 端口映射）" code={bridgeCommand} />
      </GuideStep>

      <GuideStep n={3} title="健康检查">
        <p>镜像自带的 <code>HEALTHCHECK</code> 在 node 模式下检查节点是否已完成注册（本地身份文件是否齐全），不需要额外配置或关闭。</p>
      </GuideStep>

      <GuideOutcome tokenNote="注册成功后，token 已经没有用处；建议用不带 -e RPOP_JOIN_TOKEN 的同一条命令重建容器（docker stop/rm 后重新 docker run）——节点身份已保存在挂载的数据卷里，重建不影响它。" />
    </div>
  )
}
