import './UiTextarea.css'

export default function UiTextarea({ value = '', placeholder = '', rows = 3, disabled = false, onChange }) {
  return (
    <textarea
      className="ui-textarea"
      value={value}
      placeholder={placeholder}
      rows={rows}
      disabled={disabled}
      onChange={(e) => onChange?.(e.target.value)}
    />
  )
}
