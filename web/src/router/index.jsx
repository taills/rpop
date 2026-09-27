import { Routes, Route, Navigate } from 'react-router-dom'
import ShellView from '@/views/ShellView'
import SitesPage from '@/views/SitesPage'
import LogsPage from '@/views/LogsPage'
import LogSettingsPage from '@/views/LogSettingsPage'
import SystemSettingsPage from '@/views/SystemSettingsPage'
import ProxiesPage from '@/views/ProxiesPage'
import CatalogPage from '@/views/CatalogPage'
import DemoPage from '@/views/DemoPage'
import TokensPage from '@/views/TokensPage'
import TracePage from '@/views/TracePage'

export default function AppRouter() {
  return (
    <Routes>
      <Route path="/" element={<ShellView />}>
        <Route index element={<Navigate to="/sites" replace />} />
        <Route path="sites" element={<SitesPage />} />
        <Route path="logs" element={<LogsPage />} />
        <Route path="log-settings" element={<LogSettingsPage />} />
        <Route path="system-settings" element={<SystemSettingsPage />} />
        <Route path="proxies" element={<ProxiesPage />} />
        <Route path="catalog" element={<CatalogPage />} />
        <Route path="demo" element={<DemoPage />} />
        <Route path="tokens" element={<TokensPage />} />
        <Route path="trace/:trackId?" element={<TracePage />} />
      </Route>
      <Route path="*" element={<Navigate to="/sites" replace />} />
    </Routes>
  )
}
