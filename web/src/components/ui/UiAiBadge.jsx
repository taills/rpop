import './UiAiBadge.css'
import UiIcon from './UiIcon'

export default function UiAiBadge({ children }) {
  return (
    <span className="ui-ai-badge">
      <UiIcon name="bot" size={12} />
      {children ?? 'AI'}
    </span>
  )
}
