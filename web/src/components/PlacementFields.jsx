import '../Placement.css'
import { isPlacementError, togglePlacementNode } from '../siteForm.js'

function placementBadges(node) {
  if (node.embedded) return [{ text: '内嵌', tone: '' }]
  if (!node.online) return [{ text: '离线', tone: 'warn' }]
  return []
}

// PlacementFields edits config.nodes (D5): the data-plane nodes a site is served from. An empty selection is the
// server's existing default — the embedded node "local" — kept for configs saved before this field existed (see
// control.siteNodes). PathsEditor recomputes its own hop exclusions from this same live `nodeIds` in real time
// (see hopNodeOptions in pathsForm.js), so toggling a node here immediately narrows that upstream's candidate hops.
export default function PlacementFields({ nodeIds, nodes, error, onChange }) {
  const placementError = isPlacementError(error) ? error : ''
  return <div className="wide placement-fields">
    <p className="form-note">选择该站点对外提供服务的节点；留空表示仅使用内嵌节点 local（兼容未设置此项的旧配置）。选中多个节点会让站点同时在这些节点上监听。</p>
    <div className="placement-list">
      {nodes.map(node => <label key={node.id} className="placement-node">
        <input type="checkbox" checked={nodeIds.includes(node.id)} onChange={() => onChange(togglePlacementNode(nodeIds, node.id))}/>
        <span>{node.name || node.id}</span>
        {placementBadges(node).map(badge => <span key={badge.text} className={`placement-badge ${badge.tone}`}>{badge.text}</span>)}
      </label>)}
      {!nodes.length && <span className="form-note">暂无可选节点，请先在“节点”页面注册。</span>}
    </div>
    {placementError && <div className="placement-error" role="alert">{placementError}</div>}
  </div>
}
