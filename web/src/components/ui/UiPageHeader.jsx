import './UiPageHeader.css'

export default function UiPageHeader({ title, eyebrow = '', sub = '', titleExtra, below, actions }) {
  return (
    <div className="ui-page-header">
      <div className="ui-page-header__main">
        {eyebrow && <div className="ui-eyebrow">{eyebrow}</div>}
        <h1 className="ui-title">
          {title}
          {titleExtra}
        </h1>
        {sub && <p className="ui-sub">{sub}</p>}
        {below}
      </div>
      {actions != null && <div className="ui-header-actions">{actions}</div>}
    </div>
  )
}
