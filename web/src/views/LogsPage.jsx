import { api } from '../api.js'
import '../Rpop.css'
import LogViewer from '../components/LogViewer.jsx'

export default function LogsPage() {
  return <div className="main"><LogViewer api={api}/></div>
}
