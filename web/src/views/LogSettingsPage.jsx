import { api } from '../api.js'
import '../Rpop.css'
import '../Admin.css'
import LogSettings from '../components/LogSettings.jsx'

export default function LogSettingsPage() {
  return <div className="main"><LogSettings api={api}/></div>
}
