import './UiTypeSwitcher.css'
import { cx } from '@/utils/cx'
import { useTypeStore } from '@/stores/type'

export default function UiTypeSwitcher() {
  const typeStore = useTypeStore()

  return (
    <div className="ui-type-switcher" role="group" aria-label="字体档位切换">
      {typeStore.scales.map((s) => (
        <button
          key={s.id}
          type="button"
          className={cx('ui-type-switcher__chip', s.id === typeStore.scaleId && 'on')}
          title={`${s.name} · ${s.desc} · ×${s.ratio}`}
          style={{ fontSize: `${Math.round(12 * s.ratio)}px` }}
          onClick={() => typeStore.setScale(s.id)}
        >
          {s.name}
        </button>
      ))}
    </div>
  )
}
