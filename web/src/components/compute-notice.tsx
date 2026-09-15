import { useEditionFeatures } from '../lib/dashboard-edition'
import { Note } from './shared'

export function ComputeNotice() {
  const features = useEditionFeatures()
  if (!features.hostedFree) return null
  return (
    <Note>
      <strong>Hosted Free limits</strong>
      <p>
        One application with one small service and one replica. Files on its container disk are
        temporary. Persistent storage, multiple services, scheduled jobs and public TCP ports
        require your own server. Outbound connections are limited to HTTP and HTTPS.
      </p>
      <a href={features.computeURL} target="_blank" rel="noopener noreferrer">
        View compute options in a new tab
      </a>
    </Note>
  )
}
