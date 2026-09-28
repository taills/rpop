import '../SharedPortHints.css'

// SharedPortHints renders the advisory (never blocking) hints sharedPortHints.js's siteSharedPortHints predicts
// for the site's current listen address/hostnames/placement — see that module's doc comment for exactly what
// each `kind`/severity mirrors on the server. Rendered inline in the "监听" section, next to the fields it
// concerns; the backend's own rejection (shown separately, in the form's top error banner) is what actually
// gates saving — these are only ever a heads-up before that round trip.
export default function SharedPortHints({ hints }) {
  if (!hints.length) return null
  return <ul className="wide shared-port-hints">
    {hints.map((hint, index) => <li key={index} className={`shared-port-hint is-${hint.severity}`}>{hint.text}</li>)}
  </ul>
}
