import { UiAlert } from '@/components/ui'

// GuideStep numbers one step of a deployment recipe (see NodeBootstrapGuide's four tabs); children are usually
// a mix of prose and CopyableCode blocks.
export function GuideStep({ n, title, children }) {
  return (
    <section className="node-bootstrap-step">
      <h4 className="node-bootstrap-step__title"><span className="node-bootstrap-step__num">{n}</span>{title}</h4>
      <div className="node-bootstrap-step__body">{children}</div>
    </section>
  )
}

// COMMON_TROUBLESHOOTING covers the three failure modes every deployment method shares; each tab appends its
// own note about removing the join token once registration succeeds (how to do that differs per method).
export const COMMON_TROUBLESHOOTING = [
  'token 已过期或已被使用：在“节点管理”页为该节点重新生成 join token（会使旧 token 立即失效），再用新 token 重新执行注册。',
  '节点连不上控制器：确认控制器主机的防火墙放行了 southbound 端口，且上方“控制器地址”中的主机名/端口能从节点所在网络访问到。',
  '证书或 CA 不匹配：join token 里固定了生成它那一刻的控制器 CA；如果控制器的 CA 后来变了（例如控制器重装），旧 token 会被拒绝，需要重新生成一个新的。',
]

// GuideOutcome renders the "success looks like" note and the troubleshooting list every tab ends with.
export function GuideOutcome({ tokenNote }) {
  return (
    <>
      <UiAlert type="success" title="成功的样子">
        注册成功后几秒内，该节点会在“节点管理”列表中显示为在线，且证书代数（certGeneration）从 0 变为 1。
      </UiAlert>
      <div className="node-bootstrap-troubleshoot">
        <h5>常见问题</h5>
        <ul>
          {COMMON_TROUBLESHOOTING.map((item) => <li key={item}>{item}</li>)}
          <li>{tokenNote}</li>
        </ul>
      </div>
    </>
  )
}
