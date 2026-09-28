import { api } from '../api.js'
import LogViewer from '../components/LogViewer.jsx'

export default function LogsPage() {
  return <LogViewer api={api}/>
}
