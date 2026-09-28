// nodeCard.js — pure helpers for the node card grid (NodesPage): the free-text search predicate and the
// anomaly-first metric ordering used to lay out each card's body. Kept framework-free so it is covered by
// node --test and stays independent of how NodeCard.jsx renders a metric (see docs/architecture/
// control-data-plane.md §5, "节点卡片网格" entry).
import { NODE_ATTENTION_CHECKS, nodeHasApplyErrors, nodeHasRelayError, nodeNeedsAttention } from './nodeHealth.js'

// nodeMatchesQuery is the node list's search-box predicate: case-insensitive substring match against the
// node's display name and its stable id, same rule NodesPage used inline before the table became a card grid.
export function nodeMatchesQuery(node, query) {
  const q = (query || '').trim().toLowerCase()
  if (!q) return true
  return `${node.name} ${node.id}`.toLowerCase().includes(q)
}

// Every metric a node card can show, keyed the same as the former table's columns, each with a pure
// "is this one anomalous for this node" predicate. links/paths/logs/protocol/clockSkew reuse nodeHealth.js's
// NODE_ATTENTION_CHECKS directly, so a red grid cell here and the NodesPage banner's count can never disagree
// (see that map's doc comment). revision/cert always report false: a node that just had a new config published
// reports a briefly unsynced revision, and a node not yet issued a certificate reports generation 0 — both are
// normal, expected states rather than anomalies, so they keep their own warn/muted tag (see NodeCard.jsx) but
// never turn the cell red.
const METRIC_ANOMALY = {
  revision: () => false,
  cert: () => false,
  links: NODE_ATTENTION_CHECKS.links,
  paths: NODE_ATTENTION_CHECKS.paths,
  logs: NODE_ATTENTION_CHECKS.logs,
  protocol: NODE_ATTENTION_CHECKS.protocol,
  clockSkew: NODE_ATTENTION_CHECKS.clockSkew,
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

// nodeCardHasAnomaly is the single source for the card's border/header "需要关注" treatment. It is exactly
// nodeHealth.js's nodeNeedsAttention — the same predicate the NodesPage banner counts — so a card flagged here
// is always counted by the banner, and a node the banner counts always renders its card as flagged.
export function nodeCardHasAnomaly(node) {
  return nodeNeedsAttention(node)
}

// nodeCardErrorText condenses a node's relay-bind failure and per-site apply failures — the same two raw
// fields NodeDetailPage renders as separate "中继端口绑定失败"/"部分站点应用失败" alerts — into the one short
// line a card has room for. Truncation/tooltip is a display concern left to the caller (NodeCard.jsx uses a
// `title` attribute so the full text is never actually lost); this only decides what the line says.
export function nodeCardErrorText(node) {
  const parts = []
  if (nodeHasRelayError(node)) parts.push(`中继绑定失败：${node.relayError}`)
  if (nodeHasApplyErrors(node)) {
    const entries = Object.entries(node.errors)
    parts.push(entries.map(([site, message]) => `${site}：${message}`).join('；'))
  }
  return parts.join('；')
}
