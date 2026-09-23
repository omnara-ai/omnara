import type { GitHubInstallations } from '@omnara/sdk'
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, vi } from 'vitest'

import type { FakeApi } from '@/test/fake-api'
import { fakeId, projectApp } from '@/test/fixtures'
import { renderProjectApp } from '@/test/project-app-render'
import { enableReactActEnvironment } from '@/test/react-act'
import { button } from '@/test/secret-editor'

export const orgId = fakeId('org'),
  projectId = fakeId('proj'),
  secretId = fakeId('sec')
export const app = projectApp({ app_type: 'github_pr' })
export const projectPath = `/api/v1/orgs/${orgId}/projects/${projectId}`
export const appPath = projectPath + '/apps/' + app.id
export const inspectPath = appPath + '/github-setup/installations'
export const verified: GitHubInstallations = {
  provider_app_id: '111',
  name: 'Team reviewer',
  slug: 'team-reviewer',
  install_url: 'https://github.com/apps/team-reviewer/installations/new?state=' + secretId,
  installations: [
    {
      id: '222',
      account: 'engineering',
      account_type: 'Organization',
      settings_url: 'https://github.com/organizations/engineering/settings/installations/222',
    },
  ],
}
export const reads = [
  { method: 'GET', path: appPath, respond: () => Response.json(app) },
  {
    method: 'GET',
    path: projectPath + '/secrets',
    respond: () => Response.json({ data: [], next_cursor: null }),
  },
]
let root: Root, restore: () => void
export let container: HTMLDivElement
beforeEach(() => {
  restore = enableReactActEnvironment()
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
})
afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  restore()
  window.history.replaceState(null, '', '/')
  vi.restoreAllMocks()
})
export function render(api: FakeApi, node: ReactNode) {
  return renderProjectApp(root, api, node)
}
export function click(name: string) {
  act(() => {
    button(name).click()
  })
}
export function select(id: string, value: string) {
  act(() => {
    const element = document.getElementById(id)
    if (!(element instanceof HTMLSelectElement)) throw new Error('Missing select ' + id)
    element.value = value
    element.dispatchEvent(new Event('change', { bubbles: true }))
  })
}
export function credential(id: string, name: string) {
  return {
    id,
    org_id: orgId,
    owner: { kind: 'project', project_id: projectId },
    name,
    kind: 'github_app_credentials',
    management_kind: 'tenant',
    metadata: {},
    current_version_number: 1,
    payload_keys: [],
    created_at: app.created_at,
    updated_at: app.updated_at,
  }
}
export function credentialPage(...secrets: ReturnType<typeof credential>[]) {
  return Response.json({
    data: secrets.map((secret) => ({
      secret,
      availability: { source: 'direct', project_id: projectId },
    })),
    next_cursor: null,
  })
}
