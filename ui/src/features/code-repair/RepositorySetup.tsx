import { FormEvent, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, APIError } from '../../api'
import { RequestState } from '../../shared/components'

type Binding = { ID: string; ServiceName: string; Environment: string; RepositoryURL: string; BaseRef: string; AllowedPaths: string[]; Enabled: boolean; CredentialRef: string; ValidationProfile?: { image: string; id: string }; Automation?: { enabled: boolean; expires_at: string; campaign_id: string; max_investigations: number; max_model_requests: number } }
const splitPaths = (value: FormDataEntryValue | null) => String(value || '').split(',').map((s) => s.trim()).filter(Boolean)

export function RepositorySetup({ service = '', environment = '', onCreated }: { service?: string; environment?: string; onCreated?: () => void }) {
  const client = useQueryClient(), [error, setError] = useState('')
  const [automatic, setAutomatic] = useState(false)
  const create = useMutation({ mutationFn: (body: object) => api('/api/v1/repair/bindings', { method: 'POST', body: JSON.stringify(body) }), onSuccess: () => { client.invalidateQueries({ queryKey: ['repair', 'repositories'] }); onCreated?.() } })
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); setError('')
    const data = new FormData(event.currentTarget)
    try {
      const args: unknown = JSON.parse(String(data.get('arguments')))
      const checks: unknown = JSON.parse(String(data.get('checks') || '[]'))
      if (!Array.isArray(args) || !args.every((item) => typeof item === 'string') || !Array.isArray(checks)) throw new Error('Arguments must be a JSON array of strings; checks must be a JSON array.')
      create.mutate({ service_name: String(data.get('service')).trim(), environment: String(data.get('environment')).trim(),
        repository_url: String(data.get('repository_url')).trim(), base_ref: String(data.get('base_ref')).trim(), allowed_paths: splitPaths(data.get('allowed_paths')),
        credential_ref: String(data.get('credential_ref') || '').trim(), test_recipes: ['repository-tests'],
        validation_profile: { id: 'repository-tests', version: 'validation-v1', image: String(data.get('image')).trim(), root_files: splitPaths(data.get('root_files')),
          protected_paths: splitPaths(data.get('protected_paths')), checks, test: { executable: String(data.get('executable')).trim(), arguments: args, timeout_seconds: 120 },
          expected_test_name: String(data.get('expected_test_name')).trim(), expected_failure: String(data.get('expected_failure')).trim() },
        automation: automatic ? { enabled: true, expires_at: new Date(String(data.get('expires_at'))).toISOString(), campaign_id: String(data.get('campaign_id')).trim(),
          max_investigations: Number(data.get('max_investigations')), max_model_requests: Number(data.get('max_model_requests')) } : null })
    } catch (cause) { setError(cause instanceof Error ? cause.message : 'Invalid repository configuration.') }
  }
  return <form className="repair-setup" onSubmit={submit}>
    <h3>Register repository</h3><p className="muted">Map a service to its source repository. Validation uses a prebuilt sandbox image with the Triovexa test runner and the required language tools. Disable an existing binding before registering its replacement.</p>
    <label>Service<input name="service" defaultValue={service} required /></label>
    <label>Environment<input name="environment" defaultValue={environment} required /></label>
    <label>GitHub repository URL<input name="repository_url" placeholder="https://github.com/owner/repository" required /></label>
    <label>Base branch<input name="base_ref" defaultValue="main" required /></label>
    <label>Allowed source paths, comma separated<input name="allowed_paths" placeholder="src, tests, pyproject.toml" required /></label>
    <label>GitHub credential reference<input name="credential_ref" placeholder="env:REPAIR_GITHUB_READ_TOKEN" /><span className="muted">Optional for public repositories. The credential must exist on the server and repair runner; enter its reference, not the token.</span></label>
    <fieldset><legend>Repository validation</legend>
      <label>Prebuilt sandbox image<input name="image" placeholder="triovexa-repair-sandbox:python" required /></label>
      <label>Required root files, comma separated<input name="root_files" placeholder="pyproject.toml" required /></label>
      <label>Protected test paths, comma separated<input name="protected_paths" placeholder="tests" required /></label>
      <label>Test executable in container<input name="executable" placeholder="/usr/local/bin/python" required /></label>
      <label>Fixed test arguments (JSON)<textarea name="arguments" defaultValue={'["-m", "pytest", "tests", "-q"]'} required rows={3} /></label>
      <label>Formatter/linter checks (JSON)<textarea name="checks" defaultValue="[]" rows={3} /><span className="muted">Optional commands: executable, arguments, timeout_seconds, require_empty_output. Checks must work with a read-only checkout.</span></label>
      <label>Regression test name in failing output<input name="expected_test_name" placeholder="test_accepts_supported_schema" required /></label>
      <label>Regression failure marker<input name="expected_failure" placeholder="supported schema rejected" required /></label>
    </fieldset>
    <fieldset><legend>Investigation policy</legend>
      <label><input type="checkbox" checked={automatic} onChange={(event) => setAutomatic(event.target.checked)} /> Automatically investigate incoming alerts</label>
      <p className="muted">Investigations can start without a restart. This grant does not authorize automatic PR publication, merge or deployment.</p>
      {automatic && <>
        <label>Existing model campaign ID<input name="campaign_id" required /></label>
        <label>Grant expiry<input name="expires_at" type="datetime-local" required /></label>
        <label>Maximum investigations<input name="max_investigations" type="number" min={1} max={100} defaultValue={1} required /></label>
        <label>Maximum model requests per investigation<input name="max_model_requests" type="number" min={1} max={20} defaultValue={4} required /></label>
      </>}
    </fieldset>
    {error && <p role="alert">{error}</p>}{create.isError && <RequestState error={create.error} retry={() => create.reset()} />}
    {create.isSuccess && <p role="status">Repository registered.</p>}
    <button className="primary" disabled={create.isPending}>{create.isPending ? 'Saving…' : 'Register repository'}</button>
  </form>
}

export function RepositorySettings() {
  const client = useQueryClient()
  const result = useQuery({ queryKey: ['repair', 'repositories'], queryFn: () => api<{ items: Binding[] }>('/api/v1/repair/repositories'), retry: false })
  const operation = useMutation({ mutationFn: ({ id, action }: { id: string; action: string }) => api<{ status?: string }>(`/api/v1/repair/repositories/${id}/${action}`, { method: 'POST', body: '{}' }), onSuccess: () => client.invalidateQueries({ queryKey: ['repair', 'repositories'] }) })
  if (result.error instanceof APIError && [403, 404, 503].includes(result.error.status)) return null
  return <section className="repair-content"><h2>Source repositories</h2>
    {result.isError && <RequestState error={result.error} retry={() => result.refetch()} />}
    {result.data?.items.map((binding) => <section key={binding.ID} className="setting-row"><div><h3>{binding.ServiceName} · {binding.Environment}</h3><p>{binding.RepositoryURL}</p><p>{binding.Enabled ? (binding.Automation?.enabled ? 'Automatic investigation enabled' : 'Manual investigation') : 'Disabled'}</p>
      <details><summary>Configuration</summary><p>Branch: {binding.BaseRef} · Scope: {binding.AllowedPaths.join(', ')}</p><p>Sandbox: {binding.ValidationProfile?.image || 'Legacy Go fixture'} · Credential: {binding.CredentialRef || 'Public repository'}</p>
        {binding.Automation?.enabled && <p>Campaign: {binding.Automation.campaign_id} · Up to {binding.Automation.max_investigations} investigations, {binding.Automation.max_model_requests} model requests each · Expires {new Date(binding.Automation.expires_at).toLocaleString()}</p>}
      </details></div><div className="setting-action">
      {binding.Enabled && <><button disabled={operation.isPending} onClick={() => operation.mutate({ id: binding.ID, action: 'check' })}>Check repository access</button><button disabled={operation.isPending} onClick={() => operation.mutate({ id: binding.ID, action: 'disable' })}>Disable</button></>}
    </div></section>)}
    {operation.isError && <RequestState error={operation.error} retry={() => operation.reset()} />}
    {operation.data?.status === 'connected' && <p role="status">GitHub repository access verified.</p>}
    {result.isSuccess && <RepositorySetup />}
  </section>
}
