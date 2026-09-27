import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import AppRouter from './router'
import AuthGate from './components/AuthGate.jsx'
import UiToastHost from './components/ui/UiToastHost.jsx'
import './styles/fonts.css'
import './styles/tokens.css'
import './styles/base.css'
import './Rpop.css'
import './Admin.css'

// UiToastHost is mounted once here, as a sibling of AuthGate rather than inside it, so toast.* calls made from
// the login/setup screen (AuthGate's own error handling included) render too, not just the ones made once a
// session is authenticated. It portals into document.body (see UiToastHost.jsx), so its position in this tree
// only controls when it mounts, not where it renders.
createRoot(document.getElementById('app')).render(
  <BrowserRouter>
    <AuthGate>
      <AppRouter />
    </AuthGate>
    <UiToastHost />
  </BrowserRouter>,
)
