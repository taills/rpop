import './UiCategorySwitcher.css'
import { cx } from '@/utils/cx'
import { useCategoryStore } from '@/stores/category'

export default function UiCategorySwitcher({ className = '' }) {
  const categoryStore = useCategoryStore()

  return (
    <div className={cx('ui-category-switcher', className)} role="group" aria-label="业务分类">
      {categoryStore.categories.map((c) => (
        <button
          key={c.id}
          type="button"
          className={cx(
            'ui-category-switcher__chip',
            c.id === categoryStore.categoryId && 'on',
            c.status !== 'ready' && 'is-planned',
          )}
          title={`${c.name} · ${c.desc}`}
          disabled={c.status !== 'ready'}
          onClick={() => categoryStore.setCategory(c.id)}
        >
          {c.name}
          {c.status !== 'ready' && <span className="ui-category-switcher__tag">规划中</span>}
        </button>
      ))}
    </div>
  )
}
