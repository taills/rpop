import { api } from '../api.js'
import SystemSettings from '../components/SystemSettings.jsx'

export default function SystemSettingsPage() {
  return <SystemSettings api={api}/>
}
