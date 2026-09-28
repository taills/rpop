import './UiThemeSwitcher.css'
import { useEffect, useId, useLayoutEffect, useRef, useState } from 'react'
import { cx } from '@/utils/cx'
import { useThemeStore } from '@/stores/theme'
import { groupThemesByMode } from '@/themes'
import { clampDropdownLeft } from '@/utils/dropdownPosition'
import { moveActiveIndex } from './dropdownKeyboard'
import UiIcon from './UiIcon'

/** 触发按钮 + 分组下拉面板；替换掉旧版六个色块胶囊平铺的写法（见 docs/architecture/control-data-plane.md §5）。 */
export default function UiThemeSwitcher() {
  const themeStore = useThemeStore()
  const [open, setOpen] = useState(false)
  const [activeIndex, setActiveIndex] = useState(-1)
  const rootRef = useRef(null)
  const triggerRef = useRef(null)
  const panelRef = useRef(null)
  // useId() scopes every option/panel id to this instance so two switchers on the same page (e.g. a future
  // second mount, or a dev tool rendering the catalog alongside the real one) never collide on plain
  // `ui-theme-switcher-opt-${id}` strings, which aria-activedescendant/aria-controls both rely on staying unique.
  const uid = useId()
  const panelId = `${uid}-panel`
  const optionId = (themeId) => `${uid}-opt-${themeId}`

  const groups = groupThemesByMode(themeStore.themes)
  const flatThemes = groups.flatMap((g) => g.items)
  const currentTheme = themeStore.theme

  // 点击面板之外关闭；写法参照 UiSelect.jsx。
  useEffect(() => {
    function onClickAway(e) {
      if (!rootRef.current?.contains(e.target)) setOpen(false)
    }
    document.addEventListener('mousedown', onClickAway)
    return () => document.removeEventListener('mousedown', onClickAway)
  }, [])

  // CSS alone (`position: absolute; right: 0`) anchors the panel to the *trigger's own* right edge, which is
  // fine on a wide screen but not the page's own right edge — on a narrow screen where the trigger sits close
  // to the left (squeezed by neighboring header controls, e.g. "退出登录"), that made the panel run off the
  // viewport's left edge entirely (browser-verified at 320px; see docs/architecture/control-data-plane.md §5's
  // "UI 审查问题修复" entry and utils/dropdownPosition.js's tests). Recomputed with useLayoutEffect (before
  // paint, so there is no visible jump) and again on resize while open.
  useLayoutEffect(() => {
    if (!open) return
    function reposition() {
      const panel = panelRef.current
      const trigger = triggerRef.current
      const root = rootRef.current
      if (!panel || !trigger || !root) return
      const desiredLeft = clampDropdownLeft({
        triggerRight: trigger.getBoundingClientRect().right,
        panelWidth: panel.offsetWidth,
        viewportWidth: window.innerWidth,
      })
      // The panel's `left` is resolved by the browser relative to its nearest positioned ancestor (rootRef,
      // `.ui-theme-switcher`), not the viewport — but clampDropdownLeft works in viewport coordinates (it only
      // knows about getBoundingClientRect()/window.innerWidth). Convert back into the ancestor's own coordinate
      // space, or the panel ends up offset by however far that ancestor sits from the viewport's left edge.
      panel.style.left = `${desiredLeft - root.getBoundingClientRect().left}px`
    }
    reposition()
    window.addEventListener('resize', reposition)
    return () => window.removeEventListener('resize', reposition)
  }, [open])

  function openPanel() {
    setActiveIndex(flatThemes.findIndex((t) => t.id === themeStore.themeId))
    setOpen(true)
  }
  function closePanel() {
    setOpen(false)
  }
  function pick(id) {
    themeStore.setTheme(id)
    closePanel()
    triggerRef.current?.focus()
  }

  // 焦点全程留在触发按钮上（类似原生 <select>），用 aria-activedescendant 指向高亮的选项，
  // 这样 Esc 关闭时"把焦点还给触发按钮"天然成立，不需要在按钮和面板之间来回搬运 DOM 焦点。
  function onTriggerKeyDown(e) {
    if (!open) {
      if (e.key === 'Enter' || e.key === ' ' || e.key === 'ArrowDown') {
        e.preventDefault()
        openPanel()
      }
      return
    }
    if (e.key === 'Escape') {
      e.preventDefault()
      closePanel()
      return
    }
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      setActiveIndex((i) => moveActiveIndex(i, e.key === 'ArrowDown' ? 1 : -1, flatThemes.length))
      return
    }
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      const t = flatThemes[activeIndex]
      if (t) pick(t.id)
    }
  }

  const activeOptionId = activeIndex >= 0 && flatThemes[activeIndex] ? optionId(flatThemes[activeIndex].id) : undefined

  return (
    <div ref={rootRef} className={cx('ui-theme-switcher', open && 'open')}>
      <button
        ref={triggerRef}
        type="button"
        className="ui-theme-switcher__trigger"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={panelId}
        aria-activedescendant={activeOptionId}
        onClick={() => (open ? closePanel() : openPanel())}
        onKeyDown={onTriggerKeyDown}
      >
        <span className="ui-theme-switcher__swatches" aria-hidden="true">
          {currentTheme.swatches.map((c, i) => (
            <i key={i} style={{ background: c }} />
          ))}
        </span>
        <span className="ui-theme-switcher__name">{currentTheme.name}</span>
        <UiIcon name="chevronDown" size={14} className={cx('ui-theme-switcher__caret', open && 'open')} />
      </button>

      {open && (
        <div ref={panelRef} id={panelId} className="ui-theme-switcher__panel" role="listbox" aria-label="主题">
          {groups.map((group) => (
            <div key={group.mode} className="ui-theme-switcher__group" role="group" aria-label={group.label}>
              <div className="ui-theme-switcher__group-label" aria-hidden="true">{group.label}</div>
              {group.items.map((t) => {
                const flatIndex = flatThemes.indexOf(t)
                const selected = t.id === themeStore.themeId
                return (
                  <button
                    key={t.id}
                    id={optionId(t.id)}
                    type="button"
                    role="option"
                    aria-selected={selected}
                    tabIndex={-1}
                    className={cx('ui-theme-switcher__option', selected && 'on', flatIndex === activeIndex && 'is-active')}
                    title={t.desc}
                    onClick={() => pick(t.id)}
                    onMouseEnter={() => setActiveIndex(flatIndex)}
                  >
                    <span className="ui-theme-switcher__swatches" aria-hidden="true">
                      {t.swatches.map((c, i) => (
                        <i key={i} style={{ background: c }} />
                      ))}
                    </span>
                    <span className="ui-theme-switcher__option-text">
                      <span className="ui-theme-switcher__option-name">{t.name}</span>
                      <span className="ui-theme-switcher__option-desc">{t.desc}</span>
                    </span>
                    {selected && <UiIcon name="check" size={14} className="ui-theme-switcher__check" />}
                  </button>
                )
              })}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
