import type { HarnessDispatchProfile } from '@/services/orchestrationTypes'
import { parsePaimos6Orchestrator } from '@/v6/orchestrator'
import { setupDisplayLabelError } from '@/v6/orchestratorSetup'

type RecordValue = Record<string, unknown>
function record(value: unknown): RecordValue {
  if (!value || typeof value !== 'object' || Array.isArray(value))
    throw new Error('invalid setup response')
  return value as RecordValue
}
function exact(value: RecordValue, fields: string[]) {
  if (Object.keys(value).length !== fields.length || fields.some((key) => !(key in value)))
    throw new Error('invalid setup response')
}
export function parseRootConfig(value: unknown) {
  const raw = record(value)
  exact(raw, ['schema_version', 'revision', 'orchestrator', 'updated_at'])
  const target = raw.orchestrator === null ? null : record(raw.orchestrator)
  const redacted = parsePaimos6Orchestrator({
    ...raw,
    orchestrator: target === null ? null : { display_label: target.display_label },
  })
  if (target) {
    exact(target, ['project_id', 'project_key', 'project_agent_id', 'key', 'display_label'])
    if (
      !Number.isSafeInteger(target.project_id) ||
      Number(target.project_id) <= 0 ||
      !Number.isSafeInteger(target.project_agent_id) ||
      Number(target.project_agent_id) <= 0 ||
      typeof target.project_key !== 'string' ||
      !/^[A-Z][A-Z0-9]{2,9}$/.test(target.project_key) ||
      typeof target.key !== 'string' ||
      !/^[a-z][a-z0-9_-]{0,31}$/.test(target.key) ||
      setupDisplayLabelError(String(target.display_label))
    )
      throw new Error('invalid setup response')
  }
  return { revision: redacted.revision, label: redacted.orchestrator?.display_label ?? null }
}
export function parseDispatchProfiles(value: unknown): HarnessDispatchProfile[] {
  const raw = record(value)
  exact(raw, ['dispatch_profiles'])
  if (!Array.isArray(raw.dispatch_profiles) || raw.dispatch_profiles.length > 200)
    throw new Error('invalid dispatch catalog')
  const seen = new Set<string>()
  return raw.dispatch_profiles.map((candidate) => {
    const profile = record(candidate)
    exact(profile, [
      'id',
      'version',
      'harness',
      'model',
      'effort',
      'machine_source',
      'account_source',
      'workspace_mode',
      ...('family' in profile ? ['family'] : []),
      ...('tier' in profile ? ['tier'] : []),
    ])
    if (
      ['id', 'version'].some(
        (key) =>
          typeof profile[key] !== 'string' ||
          !/^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/.test(String(profile[key])),
      ) ||
      typeof profile.model !== 'string' ||
      !/^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$/.test(profile.model) ||
      !['codex', 'claude', 'pi', 'cursor'].includes(String(profile.harness)) ||
      !['low', 'medium', 'high', 'xhigh', 'max', 'default'].includes(String(profile.effort)) ||
      (profile.effort === 'default' && profile.harness !== 'cursor') ||
      ((profile.version === '2' || 'family' in profile || 'tier' in profile) &&
        (!['openai', 'anthropic', 'xai', 'cursor'].includes(String(profile.family)) ||
          !['fast', 'standard', 'strong', 'frontier'].includes(String(profile.tier)))) ||
      profile.machine_source !== 'authenticated_reporter' ||
      profile.account_source !== 'local_probe' ||
      !['exclusive', 'shared'].includes(String(profile.workspace_mode))
    )
      throw new Error('invalid dispatch catalog')
    const key = `${profile.id}@${profile.version}`
    if (seen.has(key)) throw new Error('ambiguous dispatch catalog')
    seen.add(key)
    return profile as HarnessDispatchProfile
  })
}
function quote(value: string) {
  return `'${value.replace(/'/g, `'"'"'`)}'`
}
export function workerStartCommand(input: {
  instance: string
  deployment: string
  project: string
  agent: string
  profile: HarnessDispatchProfile | null
  ticket: string
  shape: string
}): string | null {
  if (
    ![input.instance, input.deployment].every((v) => /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(v)) ||
    input.deployment === 'default' ||
    !/^[A-Z][A-Z0-9]{2,9}$/.test(input.project) ||
    !/^[a-z][a-z0-9_-]{0,31}$/.test(input.agent) ||
    !input.profile ||
    (input.ticket &&
      (!new RegExp(`^${input.project}-[1-9][0-9]*$`).test(input.ticket) ||
        !['ship', 'scout'].includes(input.shape)))
  )
    return null
  return `paimos --instance ${quote(input.instance)} worker start --expect-deployment-instance ${quote(input.deployment)} --project ${quote(input.project)} --agent ${quote(input.agent)} --profile ${quote(`${input.profile.id}@${input.profile.version}`)}${input.ticket ? ` --ticket ${quote(input.ticket)} --work-shape ${quote(input.shape)}` : ''} --guided`
}
