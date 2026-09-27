import { api } from '../api.js'
import '../Rpop.css'
import '../Admin.css'
import SystemSettings from '../components/SystemSettings.jsx'

export default function SystemSettingsPage() {
  return <div className="main"><SystemSettings api={api}/></div>
}
