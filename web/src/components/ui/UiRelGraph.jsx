import './UiRelGraph.css'

export default function UiRelGraph({ nodes = [], edges = [], height = '280px' }) {
  function nodeById(id) {
    return nodes.find((n) => n.id === id) || { x: 0, y: 0 }
  }

  return (
    <div className="ui-rel-graph" style={{ height }}>
      <svg className="ui-rel-graph__svg" viewBox="0 0 640 280" preserveAspectRatio="xMidYMid meet">
        <defs>
          <marker
            id="ui-rel-arrow"
            viewBox="0 0 10 10"
            refX="8"
            refY="5"
            markerWidth="6"
            markerHeight="6"
            orient="auto-start-reverse"
          >
            <path d="M0 1 L8 5 L0 9" fill="none" stroke="var(--accent)" strokeWidth="1.2" />
          </marker>
        </defs>
        {edges.map((e, i) => (
          <line
            key={i}
            x1={nodeById(e.from).x}
            y1={nodeById(e.from).y}
            x2={nodeById(e.to).x}
            y2={nodeById(e.to).y}
            stroke="var(--accent)"
            strokeWidth="1.2"
            strokeOpacity="0.45"
            markerEnd="url(#ui-rel-arrow)"
          />
        ))}
        {nodes.map((n) => (
          <g key={n.id}>
            <circle
              cx={n.x}
              cy={n.y}
              r={n.r || 22}
              fill={n.fill || 'var(--soft-blue)'}
              stroke={n.stroke || 'var(--accent)'}
              strokeWidth="1.5"
            />
            <text x={n.x} y={n.y + 4} textAnchor="middle" fontSize="11" fill="var(--text-primary)" fontWeight="600">
              {n.short}
            </text>
            <text x={n.x} y={n.y + 38} textAnchor="middle" fontSize="10" fill="var(--text-secondary)">
              {n.label}
            </text>
          </g>
        ))}
      </svg>
    </div>
  )
}
