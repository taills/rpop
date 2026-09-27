import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

// This test guards a real regression: UiToastHost.jsx existed and toast.js worked, but nothing in the app tree
// ever rendered <UiToastHost/>, so every toast.success/error call silently had nowhere to show up in
// production. It walks the actual source tree rather than importing main.jsx (node --test has no JSX
// transform), so it is a static assertion on the shipped source rather than a rendered-DOM check.

const srcDir = path.dirname(fileURLToPath(import.meta.url))

// listJsxFiles recurses through src/, skipping the handful of dev-only pages that intentionally aren't part of
// the production bundle (see router/index.jsx: they're only routed under import.meta.env.DEV).
const devOnlyFiles = new Set(['CatalogPage.jsx', 'DemoPage.jsx', 'TokensPage.jsx'])
function listJsxFiles(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const full = path.join(dir, entry.name)
    if (entry.isDirectory()) return listJsxFiles(full)
    if (!entry.name.endsWith('.jsx') || devOnlyFiles.has(entry.name)) return []
    return [full]
  })
}

test('main.jsx mounts <UiToastHost/> so toasts render outside of dev-only demo pages', () => {
  const mainSource = readFileSync(path.join(srcDir, 'main.jsx'), 'utf8')
  assert.match(mainSource, /<UiToastHost\s*\/>/, 'main.jsx must render <UiToastHost/> at the app root')
  assert.match(mainSource, /import UiToastHost from ['"].*UiToastHost(\.jsx)?['"]/, 'main.jsx must import UiToastHost')
})

test('production source mounts <UiToastHost/> exactly once (no duplicate hosts, none lost)', () => {
  const mountsByFile = listJsxFiles(srcDir)
    .map((file) => [file, (readFileSync(file, 'utf8').match(/<UiToastHost\b/g) || []).length])
    .filter(([, count]) => count > 0)

  const total = mountsByFile.reduce((sum, [, count]) => sum + count, 0)
  assert.equal(total, 1, `expected exactly one <UiToastHost/> mount in production source, found: ${JSON.stringify(mountsByFile)}`)
  assert.equal(mountsByFile[0][0], path.join(srcDir, 'main.jsx'))
})
