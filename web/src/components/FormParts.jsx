import { isExpired } from '../siteForm.js'

export function FormSection({ title, description, children }) {
  return <>
    <div className="form-section-title wide"><strong>{title}</strong>{description && <span>{description}</span>}</div>
    {children}
  </>
}

// OptionToggle shows its settings only while the checkbox is on.
export function OptionToggle({ checked, onChange, label, hint, children }) {
  return <>
    <label className="check"><input type="checkbox" checked={checked} onChange={event => onChange(event.target.checked)}/> {label} {hint && <span>{hint}</span>}</label>
    {checked && children && <div className="wide toggle-panel form-grid">{children}</div>}
  </>
}

export function certificateLabel(certificate, detail) {
  return `${certificate.name}${detail ? ` · ${detail}` : ''}${isExpired(certificate) ? ' · 已过期' : ''}`
}
