const scopedSections = ['agents', 'usage', 'models', 'machines', 'secrets', 'skills'] as const
type ScopedSection = (typeof scopedSections)[number]

export const projectPaths = {
  agents: '/projects/$projectId/agents',
  usage: '/projects/$projectId/usage',
  models: '/projects/$projectId/models',
  machines: '/projects/$projectId/machines',
  secrets: '/projects/$projectId/secrets',
  skills: '/projects/$projectId/skills',
} as const satisfies Record<ScopedSection, string>

export const organizationPaths = {
  agents: '/agents',
  usage: '/usage',
  models: '/models',
  machines: '/machines',
  secrets: '/secrets',
  skills: '/skills',
} as const satisfies Record<ScopedSection, string>

/** The section a path belongs to, so switching scope can stay in the same section. */
export function currentSection(pathname: string): ScopedSection | undefined {
  const segment = pathname.replace(/^\/projects\/[^/]+/, '').split('/')[1]
  // Agent profile pages belong to the Agents section, as they do in the sidebar.
  if (segment === 'agent-profiles') return 'agents'
  return scopedSections.find((section) => section === segment)
}
