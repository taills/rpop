import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import AppRouter from './router'
import AuthGate from './components/AuthGate.jsx'
import './styles/fonts.css'
import './styles/tokens.css'
import './styles/base.css'
import './Rpop.css'
import './Admin.css'
import './Sidebar.css'

createRoot(document.getElementById('app')).render(
  <BrowserRouter>
    <AuthGate>
      <AppRouter />
    </AuthGate>
  </BrowserRouter>,
)
