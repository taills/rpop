import './TokensPage.css'
import { cx } from '@/utils/cx'
import {
  UiPageHeader,
  UiButton,
  UiCard,
  UiCategorySwitcher,
  UiNavLayoutSwitcher,
  UiTypeSwitcher,
  UiTag,
  UiDivider,
  UiStats,
  UiStatCard,
  UiAiBadge,
  UiStatusDot,
  UiProgress,
} from '@/components/ui'
import { useThemeStore } from '@/stores/theme'
import { useNavStore } from '@/stores/nav'
import { useTypeStore } from '@/stores/type'
import { useCategoryStore } from '@/stores/category'

export default function TokensPage() {
  const themeStore = useThemeStore()
  const navStore = useNavStore()
  const typeStore = useTypeStore()
  const categoryStore = useCategoryStore()

  const tokens = [
    { key: '--bg-canvas' },
    { key: '--bg-card' },
    { key: '--border-primary' },
    { key: '--text-primary' },
    { key: '--text-secondary' },
    { key: '--accent' },
    { key: '--accent-blue-600' },
    { key: '--accent-purple' },
    { key: '--accent-green' },
    { key: '--accent-orange' },
    { key: '--accent-red' },
    { key: '--tag-ai' },
  ]

  function resolve(key) {
    if (typeof window === 'undefined') return ''
    return getComputedStyle(document.documentElement).getPropertyValue(key).trim() || '—'
  }

  return (
    <div className="ui-page">
      <UiPageHeader
        eyebrow="Towere UI Kit"
        title="Token · 主题 · 分类"
        sub="组件只读语义变量；主题 / 导航 / 字体档位 / 业务分类可一键切换"
        actions={<UiButton variant="primary" size="sm" onClick={() => themeStore.nextTheme()}>切换主题</UiButton>}
      />

      <div id="token-category" className="scroll-anchor" />
      <UiCard title="业务分类（可扩展）" className="mt" icon="layers">
        <div className="ui-cell-dim mb8">
          组件库本身是通用的；分类用于按业务场景组织演示与文档。
          当前默认 <code>admin 管理后台</code>。新增分类见文档「业务分类」。{' '}
          <UiCategorySwitcher className="ml8" />
        </div>
        <div className="nav-mode-grid">
          {categoryStore.categories.map((c) => (
            <div key={c.id} className={cx('nav-mode-card', c.id === categoryStore.categoryId && 'on')}>
              <div className="nav-mode-card__name">
                {c.name}
                {' '}
                <code>{c.id}</code>
                {c.status !== 'ready' && <span className="ui-cell-dim">规划中</span>}
              </div>
              <div className="nav-mode-card__desc">{c.desc}</div>
              {c.modules?.length > 0 && (
                <div className="cat-modules">
                  {c.modules.map((m) => <UiTag key={m.key} tone="type">{m.label}</UiTag>)}
                </div>
              )}
              <UiButton
                size="sm"
                variant="outline"
                disabled={c.status !== 'ready'}
                onClick={() => categoryStore.setCategory(c.id)}
              >
                {c.status === 'ready' ? '使用此分类' : '尚未开放'}
              </UiButton>
            </div>
          ))}
        </div>
      </UiCard>

      <div id="token-nav" className="scroll-anchor" />
      <UiCard title="导航布局（一键切换）" className="mt" icon="layout">
        <div className="ui-cell-dim mb8">
          顶栏右侧 <UiNavLayoutSwitcher /> 可在三种壳层间切换；偏好写入 <code>localStorage</code> 与 <code>data-nav</code>。
        </div>
        <div className="nav-mode-grid">
          {navStore.modes.map((m) => (
            <div key={m.id} className={cx('nav-mode-card', m.id === navStore.modeId && 'on')}>
              <div className="nav-mode-card__name">{m.name} <code>{m.id}</code></div>
              <div className="nav-mode-card__desc">{m.desc}</div>
              <div className="nav-mode-card__diagram" aria-hidden="true">
                {m.id === 'horizontal' ? (
                  <div className="dgm dgm-h">
                    <div className="dgm-bar long" /><div className="dgm-bar mid" />
                  </div>
                ) : m.id === 'vertical' ? (
                  <div className="dgm dgm-v">
                    <div className="dgm-side" /><div className="dgm-main" />
                  </div>
                ) : (
                  <div className="dgm dgm-mix">
                    <div className="dgm-top" />
                    <div className="dgm-row"><div className="dgm-side short" /><div className="dgm-main" /></div>
                  </div>
                )}
              </div>
              <UiButton size="sm" variant="outline" onClick={() => navStore.setMode(m.id)}>应用此布局</UiButton>
            </div>
          ))}
        </div>
      </UiCard>

      <div id="token-type" className="scroll-anchor" />
      <UiCard title="字体档位（一键切换）" className="mt" icon="form">
        <div className="ui-cell-dim mb8">
          顶栏 <UiTypeSwitcher /> 切换 <code>data-type-scale</code>；组件字号只读 <code>--fs-*</code> 语义 token，
          当前倍率 ×{typeStore.ratio}（{typeStore.scale.name}）。
        </div>
        <div className="nav-mode-grid">
          {typeStore.scales.map((s) => (
            <div key={s.id} className={cx('nav-mode-card', s.id === typeStore.scaleId && 'on')}>
              <div className="nav-mode-card__name">{s.name} <code>{s.id}</code> ×{s.ratio}</div>
              <div className="nav-mode-card__desc">{s.desc}</div>
              <div className="type-preview" style={{ fontSize: `${Math.round(13 * s.ratio)}px` }}>
                <div className="type-preview__title" style={{ fontSize: `${Math.round(22 * s.ratio)}px` }}>页头标题 22</div>
                <div className="type-preview__body">正文 13 · 表格/按钮读 --fs-sm</div>
                <div className="type-preview__num din" style={{ fontSize: `${Math.round(28 * s.ratio)}px` }}>1,284</div>
              </div>
              <UiButton size="sm" variant="outline" onClick={() => typeStore.setScale(s.id)}>应用此档位</UiButton>
            </div>
          ))}
        </div>

        <UiDivider label="组件 → 字号 Token 对照（必须遵守）" />
        <div className="type-map">
          {typeStore.roleMap.map((r) => (
            <div key={r.token} className="type-map__row">
              <div className="type-map__token ui-mono">{r.token}</div>
              <div className="type-map__base din">{r.base}</div>
              <div className="type-map__now din">{typeStore.resolveToken(r.token) || r.base}</div>
              <div className="type-map__usage">{r.usage}</div>
            </div>
          ))}
        </div>
      </UiCard>

      <div id="token-theme" className="scroll-anchor" />
      <UiCard title="主题画廊（点击切换）" icon="palette" className="mt">
        <div className="theme-grid">
          {themeStore.themes.map((t) => (
            <button
              key={t.id}
              type="button"
              className={cx('theme-card', t.id === themeStore.themeId && 'on')}
              onClick={() => themeStore.setTheme(t.id)}
            >
              <div className="theme-card__swatch">
                {t.swatches.map((c, i) => <span key={i} style={{ background: c }} />)}
              </div>
              <div className="theme-card__name">{t.name}</div>
              <div className="theme-card__meta">{t.mode === 'dark' ? '深色' : '浅色'} · {t.id}</div>
              <div className="theme-card__desc">{t.desc}</div>
            </button>
          ))}
        </div>
      </UiCard>

      <UiCard title="当前主题语义 Token" className="mt">
        <div className="token-grid">
          {tokens.map((tok) => (
            <div key={tok.key} className="token-item">
              <span className="token-item__swatch" style={{ background: `var(${tok.key})` }} />
              <div>
                <div className="token-item__key">{tok.key}</div>
                <div className="token-item__val ui-mono">{resolve(tok.key)}</div>
              </div>
            </div>
          ))}
        </div>
      </UiCard>

      <UiCard title="实时预览（同一组件换主题）" className="mt">
        <UiStats>
          <UiStatCard label="主题" value={themeStore.theme.name} tone="accent" />
          <UiStatCard label="模式" value={themeStore.theme.mode === 'dark' ? '深色' : '浅色'} />
          <UiStatCard label="强调色" value="" tone="ai" hint="见下方色块" />
          <UiStatCard label="组件" value="43+" hint="统一 Token" />
        </UiStats>
        <div className="row">
          <UiButton variant="primary">Primary</UiButton>
          <UiButton variant="accent">Accent</UiButton>
          <UiButton variant="outline">Outline</UiButton>
          <UiButton variant="danger">Danger</UiButton>
          <UiTag tone="risk-high">高危</UiTag>
          <UiAiBadge />
          <UiStatusDot tone="success">运行中</UiStatusDot>
        </div>
        <div className="mt">
          <UiProgress value={72} sub={themeStore.theme.name} />
        </div>
      </UiCard>

      <UiCard title="如何接入到你的原型项目" className="mt">
        <ol className="steps">
          <li>复制 <code>src/components/ui</code>、<code>src/styles</code>、<code>src/themes</code>、<code>src/stores/theme.js</code></li>
          <li>在入口 <code>import</code> fonts/tokens/base.css，并 <code>app.use(UiKit)</code></li>
          <li>页面根节点用 <code>ui-page</code>，组件只通过 props / slot 使用</li>
          <li>在顶栏放 <code>UiThemeSwitcher</code>，或调用 <code>useThemeStore().setTheme(id)</code></li>
        </ol>
      </UiCard>
    </div>
  )
}
