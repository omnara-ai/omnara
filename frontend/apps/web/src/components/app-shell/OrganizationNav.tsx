import { fetchWebConfig } from '@omnara/sdk/browser'
import { useQuery } from '@tanstack/react-query'
import { Link, useRouterState } from '@tanstack/react-router'

import {
  BrainCircuit,
  CreditCard,
  Fingerprint,
  House,
  KeyRound,
  Server,
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

const resources = [
  { to: '/' as const, label: 'Overview', icon: House },
  { to: '/members' as const, label: 'Members', icon: Users },
  { to: '/machines' as const, label: 'Machines', icon: Server },
  { to: '/models' as const, label: 'Models', icon: BrainCircuit },
  { to: '/secrets' as const, label: 'Secrets', icon: KeyRound },
  { to: '/skills' as const, label: 'Skills', icon: Sparkles },
  { to: '/user/api-tokens' as const, label: 'API Tokens', icon: Fingerprint },
]

export function OrganizationNav() {
  const pathname = useRouterState({ select: (state) => state.location.pathname })
  const { activeOrg } = useActiveOrg()
  const webConfigQuery = useQuery({ queryKey: ['web-config'], queryFn: fetchWebConfig })
  const billingURL = webConfigQuery.data?.billingURL

  return (
    <SidebarGroup>
      <SidebarGroupContent>
        <SidebarMenu>
          {resources.map((resource) => (
            <SidebarMenuItem key={resource.to}>
              <SidebarMenuButton asChild isActive={pathname === resource.to}>
                <Link to={resource.to}>
                  <resource.icon />
                  <span>{resource.label}</span>
                </Link>
              </SidebarMenuButton>
            </SidebarMenuItem>
          ))}
          {billingURL && canManageOrg(activeOrg.role) && (
            <SidebarMenuItem>
              <SidebarMenuButton asChild>
                <a href={`${billingURL}?org=${activeOrg.id}`}>
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
