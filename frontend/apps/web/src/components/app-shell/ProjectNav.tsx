import { useRouterState } from '@tanstack/react-router'

import { NavSection } from '@/components/app-shell/NavSection'
import { Bot, Box, ChartBar, House, KeyRound, Server, Sparkles } from '@/components/icons'

export function ProjectNav({ projectId }: { projectId: string }) {
  const pathname = useRouterState({ select: (state) => state.location.pathname })
  const root = `/projects/${projectId}`
  const params = { projectId }
  const within = (...segments: string[]) =>
    segments.some((segment) => pathname.startsWith(`${root}/${segment}`))

  return (
    <NavSection
      items={[
        {
          id: 'overview',
          label: 'Overview',
          icon: House,
          to: '/projects/$projectId',
          params,
          isActive: pathname === root,
          emphasized: true,
        },
        {
          id: 'agents',
          label: 'Agents',
          icon: Bot,
          to: '/projects/$projectId/agents',
          params,
          isActive: within('agents', 'agent-profiles'),
          emphasized: true,
        },
        {
          id: 'usage',
          label: 'Usage',
          icon: ChartBar,
          to: '/projects/$projectId/usage',
          params,
          isActive: within('usage'),
          emphasized: true,
        },
        {
          id: 'models',
          label: 'Models',
          icon: Box,
          to: '/projects/$projectId/models',
          params,
          isActive: within('models'),
        },
        {
          id: 'machines',
          label: 'Machines',
          icon: Server,
          to: '/projects/$projectId/machines',
          params,
          isActive: within('machines'),
        },
        {
          id: 'secrets',
          label: 'Secrets',
          icon: KeyRound,
          to: '/projects/$projectId/secrets',
          params,
          isActive: within('secrets'),
        },
        {
          id: 'skills',
          label: 'Skills',
          icon: Sparkles,
          to: '/projects/$projectId/skills',
          params,
          isActive: within('skills'),
        },
      ]}
    />
  )
}
