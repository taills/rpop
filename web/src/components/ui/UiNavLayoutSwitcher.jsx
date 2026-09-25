import './UiNavLayoutSwitcher.css'
import { cx } from '@/utils/cx'
import { useNavStore } from '@/stores/nav'

export default function UiNavLayoutSwitcher() {
  const navStore = useNavStore()

  return (
    <div className="ui-nav-mode" role="group" aria-label="导航布局切换">
      {navStore.modes.map((m) => (
        <button
          key={m.id}
          type="button"
          className={cx('ui-nav-mode__chip', m.id === navStore.modeId && 'on')}
          title={`${m.name} · ${m.desc}`}
          onClick={() => navStore.setMode(m.id)}
        >
          <span className="ui-nav-mode__icon" aria-hidden="true">
            {/* 横版：上下横条 */}
            {m.id === 'horizontal' ? (
              <svg width="14" height="14" viewBox="0 0 14 14" fill="none">
                <rect x="1" y="2" width="12" height="2.2" rx="0.6" fill="currentColor" opacity="0.9" />
                <rect x="1" y="6" width="8" height="2.2" rx="0.6" fill="currentColor" opacity="0.45" />
                <rect x="1" y="10" width="10" height="2.2" rx="0.6" fill="currentColor" opacity="0.3" />
              </svg>
            ) : m.id === 'vertical' ? (
              /* 竖版：左右竖条 */
              <svg width="14" height="14" viewBox="0 0 14 14" fill="none">
                <rect x="2" y="1" width="2.2" height="12" rx="0.6" fill="currentColor" opacity="0.9" />
                <rect x="6" y="1" width="2.2" height="8" rx="0.6" fill="currentColor" opacity="0.45" />
                <rect x="10" y="1" width="2.2" height="10" rx="0.6" fill="currentColor" opacity="0.3" />
              </svg>
            ) : (
              /* 混排：顶横 + 左竖 */
              <svg width="14" height="14" viewBox="0 0 14 14" fill="none">
                <rect x="1" y="1" width="12" height="2.2" rx="0.6" fill="currentColor" opacity="0.9" />
                <rect x="1" y="5" width="2.2" height="8" rx="0.6" fill="currentColor" opacity="0.75" />
                <rect x="5" y="5" width="8" height="2.2" rx="0.6" fill="currentColor" opacity="0.4" />
                <rect x="5" y="9" width="6" height="2.2" rx="0.6" fill="currentColor" opacity="0.28" />
              </svg>
            )}
          </span>
          <span className="ui-nav-mode__name">{m.name}</span>
        </button>
      ))}
    </div>
  )
}
