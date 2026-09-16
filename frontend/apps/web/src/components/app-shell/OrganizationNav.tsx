import { Link, useRouterState } from '@tanstack/react-router'

import {
  BrainCircuit,
  ChartBar,
  CreditCard,
  Fingerprint,
  House,
  KeyRound,
  Server,
  SettingsIcon,
  Sparkles,
  Users,
} from '@/components/icons'
import {
  SidebarGroup,
  SidebarGroupContent,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
} from '@/components/ui/sidebar'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'
import { useWebConfig } from '@/lib/web-config'

const resources = [
  { to: '/' as const, label: 'Overview', icon: House },
  { to: '/members' as const, label: 'Members', icon: Users },
  { to: '/machines' as const, label: 'Machines', icon: Server },
  { to: '/models' as const, label: 'Models', icon: BrainCircuit },
  { to: '/usage' as const, label: 'Usage', icon: ChartBar },
  { to: '/apps' as const, label: 'Apps', icon: SettingsIcon },
  { to: '/secrets' as const, label: 'Secrets', icon: KeyRound },
  { to: '/skills' as const, label: 'Skills', icon: Sparkles },
  { to: '/user/api-tokens' as const, label: 'API Tokens', icon: Fingerprint },
]

export function OrganizationNav() {
  const { activeOrg } = useActiveOrg()
  const pathname = useRouterState({ select: (state) => state.location.pathname })
  const { data: webConfig } = useWebConfig()

  return (
    <SidebarGroup>
      <SidebarGroupContent>
        <SidebarMenu>
          {resources
            .filter(
              (resource) =>
                (resource.to !== '/apps' && resource.to !== '/usage') ||
                canManageOrg(activeOrg.role),
            )
            .map((resource) => (
              <SidebarMenuItem key={resource.to}>
                <SidebarMenuButton asChild isActive={pathname === resource.to}>
                  <Link to={resource.to}>
                    <resource.icon />
                    <span>{resource.label}</span>
                  </Link>
                </SidebarMenuButton>
              </SidebarMenuItem>
            ))}
          {webConfig?.billingHref && (
            <SidebarMenuItem>
              <SidebarMenuButton asChild>
                <a href={webConfig.billingHref}>
                  <CreditCard />
                  <span>Credits</span>
                </a>
              </SidebarMenuButton>
            </SidebarMenuItem>
          )}
        </SidebarMenu>
      </SidebarGroupContent>
    </SidebarGroup>
  )
}
