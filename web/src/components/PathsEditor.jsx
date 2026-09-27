import {
  addHop, addPath, availableHopNodes, hopFromSelection, hopSelection, moveHop, movePath,
  parsePathsError, pathLabel, removeHop, removePath, setHop,
} from '../pathsForm.js'

function HopRow({ hop, hopIndex, hopCount, nodes, proxies, onChange, onMove, onRemove }) {
  return <div className="hop-row">
    <select aria-label={`第 ${hopIndex + 1} 跳`} value={hopSelection(hop)} onChange={event => onChange(hopFromSelection(event.target.value))}>
      <option value="">选择节点或代理…</option>
      <optgroup label="节点">{nodes.map(node => <option key={node.id} value={`node:${node.id}`}>{node.name || node.id}</option>)}</optgroup>
      <optgroup label="具名代理">{proxies.map(proxy => <option key={proxy.id} value={`proxy:${proxy.id}`}>{proxy.name || proxy.id}</option>)}</optgroup>
    </select>
    <button type="button" className="icon-btn" title="上移" disabled={hopIndex === 0} onClick={() => onMove(-1)}>↑</button>
    <button type="button" className="icon-btn" title="下移" disabled={hopIndex === hopCount - 1} onClick={() => onMove(1)}>↓</button>
    <button type="button" className="icon-btn danger" title="删除该跳" onClick={onRemove}>×</button>
  </div>
}

function PathRow({ path, index, pathCount, nodes, proxies, error, onMove, onRemove, onAddHop, onHopChange, onHopMove, onHopRemove }) {
  return <div className="path-row">
    <div className="path-row-head">
      <span className="path-no">#{index + 1}</span>
      <code className="path-summary">{pathLabel(path.via, { nodes, proxies })}</code>
      <span className="path-actions">
        <button type="button" className="icon-btn" title="上移路径" disabled={index === 0} onClick={() => onMove(-1)}>↑</button>
        <button type="button" className="icon-btn" title="下移路径" disabled={index === pathCount - 1} onClick={() => onMove(1)}>↓</button>
        <button type="button" className="icon-btn danger" title="删除路径" onClick={onRemove}>⌫</button>
      </span>
    </div>
    {error && <div className="path-error">{error}</div>}
    <div className="path-hops">
      {path.via.map((hop, hopIndex) => <HopRow key={hopIndex} hop={hop} hopIndex={hopIndex} hopCount={path.via.length} nodes={nodes} proxies={proxies}
        onChange={next => onHopChange(hopIndex, next)} onMove={delta => onHopMove(hopIndex, delta)} onRemove={() => onHopRemove(hopIndex)}/>)}
      <button type="button" className="link-btn" onClick={onAddHop}>＋ 添加跳</button>
    </div>
  </div>
}

// PathsEditor edits an upstream's ordered candidate paths (store.Upstream.Paths): each is dialed in order and the
// first that connects is used; each path is itself an ordered node/proxy hop sequence (store.Hop). Reordering
// uses up/down buttons rather than drag-and-drop, matching the rest of the site editor. All mutations go through
// pathsForm.js's pure helpers so the list logic stays covered by pathsForm.test.js.
export default function PathsEditor({ upstream, upstreamIndex, catalog, placementIds, error, setUpstream }) {
  const paths = upstream.paths || []
  const nodes = availableHopNodes(catalog.nodes || [], placementIds || [])
  const proxies = catalog.proxies || []
  const located = parsePathsError(error)
  const setPaths = next => setUpstream({ paths: next })

  return <div className="wide path-list">
    <p className="form-note">按优先级从上到下依次尝试；建连失败（尚未发出任何请求字节）时自动切换下一条，某条路径为空表示直连。每条路径最多 8 跳，最多 8 条候选路径。</p>
    {paths.map((path, index) => {
      const rowLocated = located && located.upstreamIndex === upstreamIndex && located.pathIndex === index ? located : null
      const rowError = rowLocated ? (rowLocated.hopIndex >= 0 ? `第 ${rowLocated.hopIndex + 1} 跳：${error}` : error) : ''
      return <PathRow key={index} path={path} index={index} pathCount={paths.length} nodes={nodes} proxies={proxies} error={rowError}
        onMove={delta => setPaths(movePath(paths, index, delta))}
        onRemove={() => setPaths(removePath(paths, index))}
        onAddHop={() => setPaths(addHop(paths, index))}
        onHopChange={(hopIndex, hop) => setPaths(setHop(paths, index, hopIndex, hop))}
        onHopMove={(hopIndex, delta) => setPaths(moveHop(paths, index, hopIndex, delta))}
        onHopRemove={hopIndex => setPaths(removeHop(paths, index, hopIndex))}/>
    })}
    {!paths.length && <p className="form-note">还没有候选路径，添加第一条后可继续添加节点或代理跳。</p>}
    <button type="button" className="secondary add-path" onClick={() => setPaths(addPath(paths))}>＋ 添加候选路径</button>
  </div>
}
