import { FAILOVER_RANGE, parseFailoverError } from '../failoverForm.js'

// activeProbeValue/activeProbeFromValue map the tri-state override (null = follow the node-wide default, true,
// false) to and from the single <select> value FailoverFields renders it as.
function activeProbeValue(activeProbe) {
  if (activeProbe === true) return 'on'
  if (activeProbe === false) return 'off'
  return ''
}
function activeProbeFromValue(value) {
  if (value === 'on') return true
  if (value === 'off') return false
  return null
}

// FailoverFields edits one upstream's optional D19/D30 degradation overrides (store.Upstream.Failover): a blank
// field keeps the node's global default, so this block only ever narrows or widens the tuning for an upstream
// whose paths need different treatment than the rest. Serialization and validation are pure functions in
// failoverForm.js, covered by failoverForm.test.js; this component only renders and edits that shape.
export default function FailoverFields({ upstream, upstreamIndex, error, setUpstream }) {
  const failover = upstream.failover || {}
  const located = parseFailoverError(error)
  const rowError = located && located.upstreamIndex === upstreamIndex ? error : ''
  const setFailover = (patch) => setUpstream({ failover: { ...failover, ...patch } })
  const numberField = (key) => ({
    value: failover[key] ?? '',
    onChange: (event) => setFailover({ [key]: event.target.value === '' ? '' : Number(event.target.value) }),
  })
  return <>
    <p className="form-note">留空表示沿用节点的全局默认值；仅在该上游的候选路径需要不同的建连预算或冷却节奏时才填写。</p>
    {rowError && <div className="path-error">{rowError}</div>}
    <label>建连超时（毫秒）
      <input type="number" min={FAILOVER_RANGE.dialTimeoutMs.min} max={FAILOVER_RANGE.dialTimeoutMs.max} placeholder="10000（默认）" {...numberField('dialTimeoutMs')}/>
      <span>{FAILOVER_RANGE.dialTimeoutMs.min} - {FAILOVER_RANGE.dialTimeoutMs.max}</span>
    </label>
    <label>最小冷却时间（毫秒）
      <input type="number" min={FAILOVER_RANGE.minCooldownMs.min} max={FAILOVER_RANGE.minCooldownMs.max} placeholder="1000（默认）" {...numberField('minCooldownMs')}/>
    </label>
    <label>最大冷却时间（毫秒）
      <input type="number" min={FAILOVER_RANGE.maxCooldownMs.min} max={FAILOVER_RANGE.maxCooldownMs.max} placeholder="60000（默认）" {...numberField('maxCooldownMs')}/>
    </label>
    <label>主动探测（D19）
      <select value={activeProbeValue(failover.activeProbe)} onChange={(event) => setFailover({ activeProbe: activeProbeFromValue(event.target.value) })}>
        <option value="">跟随全局默认</option>
        <option value="on">开启</option>
        <option value="off">关闭</option>
      </select>
      <span>路径冷却到期时主动探测一次，成功则提前恢复健康</span>
    </label>
  </>
}
