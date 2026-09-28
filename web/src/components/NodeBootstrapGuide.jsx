import { useEffect, useMemo, useState } from 'react'
import { api } from '@/api.js'
import { UiAlert, UiField, UiInput, UiTabs } from '@/components/ui'
import NodeBootstrapCommandTab from './NodeBootstrapCommandTab.jsx'
import NodeBootstrapSystemdTab from './NodeBootstrapSystemdTab.jsx'
import NodeBootstrapDockerTab from './NodeBootstrapDockerTab.jsx'
import NodeBootstrapComposeTab from './NodeBootstrapComposeTab.jsx'
import { defaultNodeImage, deriveControllerURL, relayPortFromAddress } from '@/nodeBootstrap.js'
import './NodeBootstrapGuide.css'

const TABS = [
  { key: 'cli', label: '命令行' },
  { key: 'systemd', label: 'systemd' },
  { key: 'docker', label: 'docker run' },
  { key: 'compose', label: 'docker-compose' },
]

// NodeBootstrapGuide turns a freshly issued join token into copy-pasteable deployment instructions. It fetches
// the controller's own bootstrap info once when shown (GET /api/nodes/bootstrap-info: version, southbound
// listen state, and the nodeControllerUrl/nodeImage system settings), derives a starting "controller address"
// the operator can still edit, and regenerates every command/config below it live from that address and the
// active tab. See JoinTokenDialog, which renders this under the one-time token it already shows.
export default function NodeBootstrapGuide({ node, token, visible }) {
  const [info, setInfo] = useState(null)
  const [error, setError] = useState('')
  const [controllerURL, setControllerURL] = useState('')
  const [tab, setTab] = useState('cli')

  useEffect(() => {
    if (!visible) return
    let cancelled = false
    setInfo(null)
    setError('')
    api('/nodes/bootstrap-info').then((response) => {
      if (cancelled) return
      setInfo(response)
      setControllerURL(deriveControllerURL({
        nodeControllerUrl: response.nodeControllerUrl,
        hostname: window.location.hostname,
        southboundPort: response.southboundEnabled ? response.southboundPort : 0,
      }))
    }).catch((e) => { if (!cancelled) setError(e.message) })
    return () => { cancelled = true }
    // node?.id/token key a fresh fetch per issued token, in case system settings changed between two dialogs.
  }, [visible, node?.id, token])

  const relayPort = useMemo(() => relayPortFromAddress(node?.relayAddress), [node?.relayAddress])
  const image = (info?.nodeImage || '').trim() || defaultNodeImage(info?.version)

  if (error) return <UiAlert type="error" title="无法加载节点接入信息">{error}</UiAlert>
  if (!info) return <p className="ui-cell-dim">正在加载节点接入信息…</p>

  const tabProps = { controllerURL, token, relayPort, image }

  return (
    <div className="node-bootstrap-guide">
      <UiField label="控制器地址" hint="节点通过此地址接入控制器（southbound）；可按需修改，下方命令会同步更新">
        <UiInput value={controllerURL} onChange={setControllerURL} placeholder="https://controller.example.com:7443" />
      </UiField>
      {!info.southboundEnabled && (
        <UiAlert type="warn" title="远端节点无法接入">
          当前控制器未开启 southbound 监听，节点无法注册。请以 <code>controller</code> 模式运行控制器，或为 <code>all-in-one</code> 模式设置 <code>-southbound-addr</code>（环境变量 <code>RPOP_SOUTHBOUND_ADDR</code>）后重启控制器。
        </UiAlert>
      )}

      <UiTabs tabs={TABS} value={tab} onChange={setTab} />

      {tab === 'cli' && <NodeBootstrapCommandTab {...tabProps} />}
      {tab === 'systemd' && <NodeBootstrapSystemdTab {...tabProps} />}
      {tab === 'docker' && <NodeBootstrapDockerTab {...tabProps} />}
      {tab === 'compose' && <NodeBootstrapComposeTab {...tabProps} />}
    </div>
  )
}
