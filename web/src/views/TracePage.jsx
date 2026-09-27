import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import {
  UiPageHeader,
  UiCard,
  UiField,
  UiInput,
  UiButton,
  UiSeg,
  UiAlert,
  UiEmpty,
  UiSkeleton,
  UiDescriptions,
  UiTimeline,
  UiStatusDot,
} from '@/components/ui'
import { useToast } from '@/stores/toast'
import { api } from '@/api'
import { groupHops, hopDurationBars, isValidTraceId, sortTunnelEvents, tunnelSectionState, withRelativeTiming } from '@/trace'
import './TracePage.css'

const ROLE_LABEL = { entry: '入口', relay: '中继', exit: '出口' }
const ROLE_TONE = { entry: 'brand', relay: 'warn', exit: 'success' }
const STAGE_LABEL = { arrived: '到达', established: '建立', ended: '结束' }

function millis(value) {
  return `${Number(value || 0).toFixed(1)} ms`
}

function statusTone(status) {
  if (status >= 500) return 'danger'
  if (status >= 400) return 'warn'
  return 'success'
}

// formatClock shows millisecond precision — this timeline is usually sub-second end to end, so a plain
// toLocaleTimeString would collapse every hop onto the same second.
function formatClock(timestamp) {
  const date = new Date(timestamp)
  if (Number.isNaN(date.getTime())) return String(timestamp)
  return `${date.toLocaleTimeString(undefined, { hour12: false })}.${String(date.getMilliseconds()).padStart(3, '0')}`
}

function eventTone(event) {
  if (event.error) return 'danger'
  if (event.stage === 'arrived') return 'brand'
  return 'success'
}

// HopDurationChart is the "each hop's duration" mini-Gantt beside the vertical timeline: one row per hop,
// bar offset/width already computed as percentages of the tunnel's total observed span (see hopDurationBars in
// trace.js). Hand-rolled with CSS, no charting dependency.
function HopDurationChart({ bars }) {
  return (
    <div className="trace-gantt" role="img" aria-label="每跳耗时占比">
      {bars.map((bar) => (
        <div className="trace-gantt__row" key={bar.nodeId}>
          <div className="trace-gantt__label">
            <UiStatusDot tone={ROLE_TONE[bar.role] || 'neutral'}>{ROLE_LABEL[bar.role] || bar.role || '未知角色'}</UiStatusDot>
            <span className="trace-gantt__node" title={bar.nodeId}>{bar.nodeId}</span>
          </div>
          <div className="trace-gantt__track">
            <div className="trace-gantt__bar" style={{ left: `${bar.offsetPercent}%`, width: `${bar.widthPercent}%` }} />
          </div>
          <div className="trace-gantt__duration">{millis(bar.durationMs)}</div>
        </div>
      ))}
    </div>
  )
}

function eventItem(event, hop) {
  return {
    tone: eventTone(event),
    title: `${ROLE_LABEL[hop.role] || hop.role || '未知角色'} · ${hop.nodeId} · ${STAGE_LABEL[event.stage] || event.stage}`,
    time: `${formatClock(event.timestamp)} · +${event.relativeMs} ms`,
    desc: (
      <>
        {event.peer && <span>对端 {event.peer}　</span>}
        {(event.bytesIn || event.bytesOut) ? <span>{event.bytesIn || 0}B ⇢ {event.bytesOut || 0}B　</span> : null}
        <span>距上一事件 {millis(event.deltaMs)}</span>
        {event.error && <div className="trace-item-error">错误：{event.error}</div>}
      </>
    ),
  }
}

// useTraceQuery drives the two independent lookups the page supports: a track ID (the access log record, then
// its tunnel if it has one) or a bare tunnel ID (skip straight to the tunnel timeline). Kept as one hook so
// TracePage itself only has to render the resulting state.
function useTraceQuery(trackId, tunnelParam) {
  const [record, setRecord] = useState(null)
  const [recordError, setRecordError] = useState('')
  const [recordStatus, setRecordStatus] = useState(0)
  const [recordLoading, setRecordLoading] = useState(false)
  const [events, setEvents] = useState(null)
  const [eventsError, setEventsError] = useState('')
  const [eventsLoading, setEventsLoading] = useState(false)
  const requestRef = useRef(0)

  const loadTunnel = useCallback(async (tunnelId, requestId) => {
    setEventsLoading(true); setEventsError('')
    try {
      const list = await api(`/logging/tunnels/${encodeURIComponent(tunnelId)}`)
      if (requestRef.current === requestId) setEvents(list || [])
    } catch (e) {
      if (requestRef.current === requestId) setEventsError(e.message)
    } finally {
      if (requestRef.current === requestId) setEventsLoading(false)
    }
  }, [])

  const loadTrack = useCallback(async (id, requestId) => {
    setRecordLoading(true); setRecordError(''); setRecordStatus(0); setRecord(null); setEvents(null); setEventsError('')
    try {
      const found = await api(`/logging/trace/${encodeURIComponent(id)}`)
      if (requestRef.current !== requestId) return
      setRecord(found)
      if (found.tunnelId) await loadTunnel(found.tunnelId, requestId)
      else setEvents([])
    } catch (e) {
      if (requestRef.current === requestId) { setRecordError(e.message); setRecordStatus(e.status || 0) }
    } finally {
      if (requestRef.current === requestId) setRecordLoading(false)
    }
  }, [loadTunnel])

  useEffect(() => {
    const requestId = ++requestRef.current
    setRecord(null); setRecordError(''); setRecordStatus(0); setEvents(null); setEventsError('')
    if (trackId) {
      if (!isValidTraceId(trackId)) { setRecordError('Track ID 必须是合法的 UUID'); return }
      loadTrack(trackId, requestId)
    } else if (tunnelParam) {
      if (!isValidTraceId(tunnelParam)) { setEventsError('Tunnel ID 必须是合法的 UUID'); return }
      loadTunnel(tunnelParam, requestId)
    }
  }, [trackId, tunnelParam, loadTrack, loadTunnel])

  return { record, recordError, recordStatus, recordLoading, events, eventsError, eventsLoading }
}

export default function TracePage() {
  const { trackId } = useParams()
  const [searchParams] = useSearchParams()
  const navigate = useNavigate()
  const { toast } = useToast()
  const tunnelParam = searchParams.get('tunnel') || ''

  const [formMode, setFormMode] = useState(trackId || !tunnelParam ? 'track' : 'tunnel')
  const [trackInput, setTrackInput] = useState(trackId || '')
  const [tunnelInput, setTunnelInput] = useState(tunnelParam)
  const [inputError, setInputError] = useState('')

  // Keeps the search form in sync with the URL when it changes without this component remounting — e.g.
  // clicking another record's trackId link in LogDetailDrawer while already on this page.
  useEffect(() => {
    if (trackId) { setFormMode('track'); setTrackInput(trackId) }
    else if (tunnelParam) { setFormMode('tunnel'); setTunnelInput(tunnelParam) }
  }, [trackId, tunnelParam])

  // The design doc's "/trace?trackId=" prefill convention and this step's "/trace/:trackId" convention both
  // resolve to the same canonical URL, so callers can use either without the page splitting behavior on it.
  useEffect(() => {
    const prefill = searchParams.get('trackId')
    if (!trackId && prefill) navigate(`/trace/${prefill}`, { replace: true })
  }, [trackId, searchParams, navigate])

  const { record, recordError, recordStatus, recordLoading, events, eventsError, eventsLoading } = useTraceQuery(trackId, tunnelParam)

  function submit(event) {
    event.preventDefault()
    const value = (formMode === 'track' ? trackInput : tunnelInput).trim()
    if (!isValidTraceId(value)) { setInputError(`${formMode === 'track' ? 'Track ID' : 'Tunnel ID'} 必须是合法的 UUID`); return }
    setInputError('')
    navigate(formMode === 'track' ? `/trace/${value}` : `/trace?tunnel=${value}`)
  }

  function copy(value) {
    navigator.clipboard?.writeText(value).then(() => toast.success('已复制')).catch(() => toast.error('复制失败'))
  }

  const timeline = useMemo(() => {
    if (!events || !events.length) return { hops: [], bars: [], items: [] }
    const hops = groupHops(withRelativeTiming(sortTunnelEvents(events)))
    const items = hops.flatMap((hop) => hop.events.map((event) => eventItem(event, hop)))
    return { hops, bars: hopDurationBars(hops), items }
  }, [events])

  const hasQuery = Boolean(trackId || tunnelParam)
  const sectionState = tunnelSectionState({ trackId, recordLoading, recordError, record, eventsLoading, eventsError, events })

  return (
    <div className="ui-page">
      <UiPageHeader
        eyebrow="控制台 · 追踪"
        title="请求追踪时间线"
        sub="按入口节点生成的 Rpop-Track-Id，或请求使用的 Rpop-Tunnel-Id，查询它经过的全部节点与耗时（D22）。"
      />

      <UiCard title="查询" icon="search" flush>
        <form className="pad trace-search" onSubmit={submit}>
          <UiSeg
            value={formMode}
            onChange={(mode) => { setFormMode(mode); setInputError('') }}
            options={[{ label: '按 Track ID', value: 'track' }, { label: '按 Tunnel ID', value: 'tunnel' }]}
          />
          <UiField
            label={formMode === 'track' ? 'Track ID' : 'Tunnel ID'}
            hint={formMode === 'track' ? '每个请求的 Rpop-Track-Id（访问日志记录自带）' : '跨节点隧道的 Rpop-Tunnel-Id（仅开启访问日志的隧道才有）'}
            error={inputError}
          >
            <UiInput
              value={formMode === 'track' ? trackInput : tunnelInput}
              onChange={formMode === 'track' ? setTrackInput : setTunnelInput}
              placeholder="例如 0190f3d1-9e2b-7c3a-8b1a-1234567890ab"
            />
          </UiField>
          <UiButton type="submit" variant="primary" icon="search">查询</UiButton>
        </form>
      </UiCard>

      {!hasQuery && <UiEmpty icon="search" title="输入一个 Track ID 或 Tunnel ID 开始查询" desc="Track ID 可以在访问日志详情里找到，点击即可直接跳转到这里。" />}

      {trackId && (
        <UiCard title="访问日志摘要" icon="file" className="mt">
          {recordLoading && <UiSkeleton type="block" height="140px" />}
          {!recordLoading && recordError && (
            recordStatus === 404
              ? <UiEmpty icon="search" title="未找到匹配的访问日志记录" desc="该 Track ID 可能不存在，或所在的日志适配器暂时无法查询。" />
              : <UiAlert type="error" title="查询失败">{recordError}</UiAlert>
          )}
          {!recordLoading && !recordError && record && (
            <UiDescriptions
              column="2"
              items={[
                { label: '时间', key: 'time', value: new Date(record.timestamp).toLocaleString(), mono: true },
                { label: '站点', key: 'site', value: record.siteId || '—', mono: true },
                { label: '方法', key: 'method', value: record.method },
                { label: 'Host / Path', key: 'path', value: `${record.host || ''}${record.path || ''}` || '—' },
                { label: '状态码', key: 'status', value: <UiStatusDot tone={statusTone(record.status)}>{record.status}</UiStatusDot> },
                { label: '完整耗时', key: 'duration', value: millis(record.responseMillis) },
                { label: '入口节点', key: 'entry', value: record.reportedBy || '—', mono: true },
                { label: '上游', key: 'upstream', value: record.upstream || '—' },
                {
                  label: 'Track ID', key: 'trackId', value: (
                    <span className="trace-id"><span className="ui-mono">{record.trackId}</span><UiButton size="sm" variant="ghost" icon="copy" onClick={() => copy(record.trackId)} /></span>
                  ),
                },
                {
                  label: 'Tunnel ID', key: 'tunnelId', value: record.tunnelId ? (
                    <span className="trace-id"><span className="ui-mono">{record.tunnelId}</span><UiButton size="sm" variant="ghost" icon="copy" onClick={() => copy(record.tunnelId)} /></span>
                  ) : '—（直连出口，未经过隧道）',
                },
              ]}
            />
          )}
        </UiCard>
      )}

      {sectionState !== 'hidden' && (
        <UiCard title="隧道时间线" icon="clock" className="mt">
          <p className="trace-skew-note">跨节点时间以各节点本地上报时的本地时钟为准，节点间可能存在轻微偏差（时钟偏差标注见阶段 7）。</p>
          {sectionState === 'loading' && <UiSkeleton type="block" height="160px" />}
          {sectionState === 'error' && <UiAlert type="error" title="查询失败">{eventsError}</UiAlert>}
          {sectionState === 'no-tunnel' && (
            <UiEmpty icon="link" title="该请求未经过跨节点隧道" desc="上游是直连出口，没有隧道事件可展示。" />
          )}
          {sectionState === 'empty' && (
            <UiEmpty icon="clock" title="尚未查询到隧道事件" desc="隧道事件由节点异步回传，可能仍在路上；也可能这个 Tunnel ID 不存在。" />
          )}
          {sectionState === 'ready' && (
            <>
              <HopDurationChart bars={timeline.bars} />
              <UiTimeline items={timeline.items} />
            </>
          )}
        </UiCard>
      )}
    </div>
  )
}
