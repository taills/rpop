import './UiFilterBar.css'

export default function UiFilterBar({ children, right }) {
  return (
    <div className="ui-filter-bar">
      <div className="ui-filter-bar__main">{children}</div>
      {right != null && <div className="ui-filter-bar__right">{right}</div>}
    </div>
  )
}
