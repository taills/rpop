// nodeCard.js — pure helpers for the node card grid (NodesPage): the free-text search predicate and the
// anomaly-first metric ordering used to lay out each card's body. Kept framework-free so it is covered by
// node --test and stays independent of how NodeCard.jsx renders a metric (see docs/architecture/
// control-data-plane.md §5, "节点卡片网格" entry).
import { linksNeedAttention, logsNeedAttention, pathsNeedAttention } from './nodeHealth.js'

// nodeMatchesQuery is the node list's search-box predicate: case-insensitive substring match against the
// node's display name and its stable id, same rule NodesPage used inline before the table became a card grid.
export function nodeMatchesQuery(node, query) {
  const q = (query || '').trim().toLowerCase()
  if (!q) return true
  return `${node.name} ${node.id}`.toLowerCase().includes(q)
}

// Every metric a node card can show, keyed the same as the former table's columns, each with a pure
// "is this one anomalous for this node" predicate. Embedded nodes report a trivially-synced revision and have
// no certificate of their own, so those two never flag for them (NodeCard renders "内嵌节点" for cert instead).
const METRIC_ANOMALY = {
  revision: (node) => !node.embedded && !node.inSync,
  cert: (node) => !node.embedded && node.certGeneration === 0,
  links: (node) => linksNeedAttention(node.links),
  paths: (node) => pathsNeedAttention(node.paths),
  logs: (node) => logsNeedAttention(node.logs),
  protocol: (node) => node.protocolStatus === 'outdated',
  clockSkew: (node) => node.clockSkewStatus === 'warn',
}

const METRIC_ORDER = ['revision', 'cert', 'links', 'paths', 'logs', 'protocol', 'clockSkew']

// nodeCardMetrics returns every metric key with its anomaly flag, sorted anomalies-first. Array#sort is
// stable (ES2019+), so metrics that tie on anomaly keep METRIC_ORDER's relative order — a healthy node's card
// always lays out the same way, and a problem always rises to the top of the body instead of hiding below the
// fold.
export function nodeCardMetrics(node) {
  return METRIC_ORDER
    .map((key) => ({ key, anomaly: METRIC_ANOMALY[key](node) }))
    .sort((a, b) => Number(b.anomaly) - Number(a.anomaly))
}

// nodeCardHasAnomaly is a convenience for the card's border/header treatment: true if any metric is flagged.
export function nodeCardHasAnomaly(node) {
  return nodeCardMetrics(node).some((m) => m.anomaly)
}
