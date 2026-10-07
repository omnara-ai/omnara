import { type LinkProps, useRouterState } from '@tanstack/react-router'
import type { ComponentType } from 'react'

import { NavExternalLink, type NavItem, NavSection } from '@/components/app-shell/NavSection'
import {
  Bot,
  Box,
  ChartBar,
  CreditCard,
  Fingerprint,
  House,
  KeyRound,
  Server,
  Sparkles,
  Users,
} from '@/components/icons'
import { useWebConfig } from '@/lib/web-config'

export function OrganizationNav() {
  const pathname = useRouterState({ select: (state) => state.location.pathname })
  const { data: webConfig } = useWebConfig()

  function item(
    to: LinkProps['to'] & string,
    label: string,
    icon: ComponentType<{ className?: string }>,
    emphasized = false,
  ): NavItem {
    return {
      id: to,
      to,
      label,
      icon,
      emphasized,
      isActive: pathname === to || (to !== '/' && pathname.startsWith(`${to}/`)),
    }
  }

  return (
    <>
      <NavSection
        label="Organization"
        items={[
          item('/', 'Overview', House, true),
          item('/agents', 'Agents', Bot, true),
          item('/usage', 'Usage', ChartBar, true),
        ]}
      >
        {webConfig?.billingHref && (
          <NavExternalLink href={webConfig.billingHref} label="Credits" icon={CreditCard} />
        )}
      </NavSection>
      <NavSection
        label="Resources"
        items={[
          item('/models', 'Models', Box),
          item('/machines', 'Machines', Server),
          item('/secrets', 'Secrets', KeyRound),
          item('/skills', 'Skills', Sparkles),
        ]}
      />
      <NavSection
        label="Access"
        items={[
          item('/members', 'Members', Users),
          item('/user/api-tokens', 'API Tokens', Fingerprint),
        ]}
      />
    </>
  )
}
