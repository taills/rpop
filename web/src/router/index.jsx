import { lazy, Suspense } from 'react'
import { Routes, Route, Navigate } from 'react-router-dom'
import ShellView from '@/views/ShellView'
import SitesPage from '@/views/SitesPage'
import LogsPage from '@/views/LogsPage'
import LogSettingsPage from '@/views/LogSettingsPage'
import SystemSettingsPage from '@/views/SystemSettingsPage'
import ProxiesPage from '@/views/ProxiesPage'
import NodesPage from '@/views/NodesPage'
import NodeDetailPage from '@/views/NodeDetailPage'
import TopologyPage from '@/views/TopologyPage'
import TracePage from '@/views/TracePage'

// The component catalog / page demos / token playground are development aids, not part of the production
// console; they are lazy-loaded (separate chunks) and only routed at all in dev, so a production build neither
// downloads nor bundles their code (see ShellView's menus, gated the same way).
const CatalogPage = lazy(() => import('@/views/CatalogPage'))
const DemoPage = lazy(() => import('@/views/DemoPage'))
const TokensPage = lazy(() => import('@/views/TokensPage'))

export default function AppRouter() {
  return (
    <Routes>
      <Route path="/" element={<ShellView />}>
        <Route index element={<Navigate to="/sites" replace />} />
        <Route path="sites" element={<SitesPage />} />
        <Route path="nodes" element={<NodesPage />} />
        <Route path="nodes/:id" element={<NodeDetailPage />} />
        <Route path="topology" element={<TopologyPage />} />
        <Route path="proxies" element={<ProxiesPage />} />
        <Route path="logs" element={<LogsPage />} />
        <Route path="trace/:trackId?" element={<TracePage />} />
        <Route path="log-settings" element={<LogSettingsPage />} />
        <Route path="system-settings" element={<SystemSettingsPage />} />
        {import.meta.env.DEV && (
          <>
            <Route path="catalog" element={<Suspense fallback={null}><CatalogPage /></Suspense>} />
            <Route path="demo" element={<Suspense fallback={null}><DemoPage /></Suspense>} />
            <Route path="tokens" element={<Suspense fallback={null}><TokensPage /></Suspense>} />
          </>
        )}
      </Route>
      <Route path="*" element={<Navigate to="/sites" replace />} />
    </Routes>
  )
}
