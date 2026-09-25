import { Routes, Route, Navigate } from 'react-router-dom'
import ShellView from '@/views/ShellView'
import CatalogPage from '@/views/CatalogPage'
import DemoPage from '@/views/DemoPage'
import TokensPage from '@/views/TokensPage'

export default function AppRouter() {
  return (
    <Routes>
      <Route path="/" element={<ShellView />}>
        <Route index element={<Navigate to="/catalog" replace />} />
        <Route path="catalog" element={<CatalogPage />} />
        <Route path="demo" element={<DemoPage />} />
        <Route path="tokens" element={<TokensPage />} />
      </Route>
      <Route path="*" element={<Navigate to="/catalog" replace />} />
    </Routes>
  )
}
