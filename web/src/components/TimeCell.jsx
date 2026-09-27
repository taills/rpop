import './TimeCell.css'
import { describeTime } from '@/nodeHealth'

// TimeCell renders an optional RFC3339 timestamp field as a relative label with the absolute local time
// underneath (control-data-plane.md §5's convention: an empty string means "not applicable", never the zero
// time literal), falling back to an em dash when the field is empty. Shared by the nodes/topology health
// tables so every timestamp column looks the same.
export default function TimeCell({ value }) {
  const described = describeTime(value)
  if (!described) return <span className="ui-cell-dim">—</span>
  return (
    <span className="time-cell">
      <span>{described.relative}</span>
      <small className="ui-cell-dim time-cell__abs">{described.absolute}</small>
    </span>
  )
}
