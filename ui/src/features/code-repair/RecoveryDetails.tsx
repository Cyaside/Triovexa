export type RepairDeployment = {
  DeploymentID: string; RevisionSHA: string; Phase: string; VerificationStatus: string
  Contract?: { profile: { kind: string; alert_name: string; http?: { minimum_requests: number; max_error_ratio: number; window_seconds: number } } }
  Baseline?: { http?: { healthy: boolean; requests: number; errors: number }; queue_backlog?: number; jobs_processed?: number }
}

export function RecoveryDetails({ deployments }: { deployments: RepairDeployment[] }) {
  if (!deployments.length) return null
  return <section className="repair-section"><h3>Deployment and recovery</h3>{deployments.map((deployment) => {
    const profile = deployment.Contract?.profile
    const http = profile?.http
    const before = deployment.Baseline?.http
    return <div key={deployment.DeploymentID}>
      <dl className="repair-facts">
        <div><dt>Deployment</dt><dd><code>{deployment.DeploymentID}</code></dd></div>
        <div><dt>Revision</dt><dd><code>{deployment.RevisionSHA.slice(0, 12)}</code></dd></div>
        <div><dt>Phase</dt><dd>{deployment.Phase.replaceAll('_', ' ')}</dd></div>
        <div><dt>Verification</dt><dd>{deployment.VerificationStatus.replaceAll('_', ' ')}</dd></div>
        <div><dt>Recovery profile</dt><dd>{profile?.kind === 'http' ? 'HTTP service' : profile ? 'Redis queue worker' : 'Historical queue contract'}</dd></div>
        {profile && <div><dt>Alert</dt><dd>{profile.alert_name}</dd></div>}
      </dl>
      {http && <p className="muted">Recovery requires healthy instances, at least {http.minimum_requests} requests per {http.window_seconds}-second window, HTTP 5xx ratio at most {(http.max_error_ratio * 100).toFixed(2)}%, and the deployed merge revision.</p>}
      {before && <p className="muted">Baseline: {before.healthy ? 'healthy' : 'unhealthy'} · {before.requests.toFixed(1)} requests · {before.errors.toFixed(1)} server errors.</p>}
    </div>
  })}</section>
}
