import { useParams, useRouterState } from '@tanstack/react-router'
import type { ComponentType } from 'react'

import { NavExternalLink, type NavItem, NavSection } from '@/components/app-shell/NavSection'
import {
  currentSection,
  organizationPaths,
  projectPaths,
  type ScopedSection,
} from '@/components/app-shell/scoped-sections'
import {
  ArrowLeft,
  BoltIcon,
  Bot,
  Box,
  Brain,
  ChartBar,
  CreditCard,
  Fingerprint,
  House,
  KeyRound,
  Server,
  Sparkles,
  Users,
} from '@/components/icons'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'
import { useWebConfig } from '@/lib/web-config'

/**
 * The sidebar's sections. Inside a project the scoped items point at the project's pages
 * instead of the organization's, but every section and item stays in place, so the
 * organization-wide links (credits, members, API tokens) never disappear.
 */
export function SidebarNav() {
  const pathname = useRouterState({ select: (state) => state.location.pathname })
  const projectId = useParams({ strict: false, select: (params) => params.projectId })
  const { data: webConfig } = useWebConfig()
  const { activeOrg } = useActiveOrg()
  const section = currentSection(pathname)

  function scoped(
    id: ScopedSection,
    label: string,
    icon: ComponentType<{ className?: string }>,
    emphasized = false,
  ): NavItem {
    return {
      id,
      label,
      icon,
      emphasized,
      ...(projectId
        ? { to: projectPaths[id], params: { projectId } }
        : { to: organizationPaths[id] }),
      isActive: section === id,
    }
  }

  function organization(
    to: '/members' | '/api-tokens',
    label: string,
    icon: ComponentType<{ className?: string }>,
  ): NavItem {
    return { id: to, to, label, icon, isActive: pathname === to || pathname.startsWith(`${to}/`) }
  }

  const overview: NavItem = projectId
    ? {
        id: 'overview',
        to: '/projects/$projectId',
        params: { projectId },
        isActive: pathname === `/projects/${projectId}`,
        label: 'Overview',
        icon: House,
        emphasized: true,
      }
    : {
        id: 'overview',
        to: '/',
        isActive: pathname === '/',
        label: 'Overview',
        icon: House,
        emphasized: true,
      }

  return (
    <>
      {projectId && (
        <NavSection
          items={[
            {
              id: 'back',
              // Back to the organization's version of this page, so the section stays put.
              to: section ? organizationPaths[section] : '/',
              label: 'Back to organization',
              icon: ArrowLeft,
              isActive: false,
            },
          ]}
        />
      )}
      <NavSection
        label={projectId ? 'Project' : 'Organization'}
        items={[
          overview,
          scoped('agents', 'Agents', Bot, true),
          scoped('integrations', 'Integrations', BoltIcon, true),
          scoped('usage', 'Usage', ChartBar, true),
        ]}
      >
        {/* Only set when billing is configured and the viewer can manage the org. */}
        {webConfig?.billingHref && (
          <NavExternalLink href={webConfig.billingHref} label="Credits" icon={CreditCard} />
        )}
      </NavSection>
      <NavSection
        label="Resources"
        items={[
          scoped('models', 'Models', Box),
          scoped('machines', 'Machines', Server),
          scoped('secrets', 'Secrets', KeyRound),
          scoped('skills', 'Skills', Sparkles),
          scoped('memory', 'Memory', Brain),
        ]}
      />
      <NavSection
        label="Access"
        items={[
          organization('/members', 'Members', Users),
          ...(canManageOrg(activeOrg.role)
            ? [organization('/api-tokens', 'API Tokens', Fingerprint)]
            : []),
        ]}
      />
    </>
  )
}
