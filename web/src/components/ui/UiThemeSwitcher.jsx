import './UiThemeSwitcher.css'
import { cx } from '@/utils/cx'
import { useThemeStore } from '@/stores/theme'

export default function UiThemeSwitcher() {
  const themeStore = useThemeStore()

  return (
    <div className="ui-theme-switcher" role="group" aria-label="主题切换">
      {themeStore.themes.map((t) => (
        <button
          key={t.id}
          type="button"
          className={cx('ui-theme-switcher__chip', t.id === themeStore.themeId && 'on')}
          title={`${t.name} · ${t.desc}`}
          onClick={() => themeStore.setTheme(t.id)}
        >
          <span className="ui-theme-switcher__swatches" aria-hidden="true">
            {t.swatches.map((c, i) => (
              <i key={i} style={{ background: c }} />
            ))}
          </span>
          <span className="ui-theme-switcher__name">{t.name}</span>
        </button>
      ))}
    </div>
  )
}
