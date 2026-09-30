import { Link, type LinkProps } from '@tanstack/react-router'
import type { ComponentType, ReactNode } from 'react'

import {
  SidebarGroup,
  SidebarGroupContent,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
} from '@/components/ui/sidebar'

type NavIcon = ComponentType<{ className?: string }>

export interface NavItem {
  id: string
  label: string
  icon: NavIcon
  to: LinkProps['to']
  params?: LinkProps['params']
  isActive: boolean
  emphasized?: boolean
}

export function NavSection({ items, children }: { items: NavItem[]; children?: ReactNode }) {
  return (
    <SidebarGroup>
      <SidebarGroupContent>
        <SidebarMenu>
          {items.map((item) => (
            <SidebarMenuItem key={item.id}>
              <SidebarMenuButton
                asChild
                isActive={item.isActive}
                className={item.emphasized ? 'font-medium' : undefined}
              >
                <Link to={item.to} params={item.params}>
                  <item.icon />
                  <span>{item.label}</span>
                </Link>
              </SidebarMenuButton>
            </SidebarMenuItem>
          ))}
          {children}
        </SidebarMenu>
      </SidebarGroupContent>
    </SidebarGroup>
  )
}

export function NavExternalLink({
  href,
  label,
  icon: Icon,
}: {
  href: string
  label: string
  icon: NavIcon
}) {
  return (
    <SidebarMenuItem>
      <SidebarMenuButton asChild>
        <a href={href}>
          <Icon />
          <span>{label}</span>
        </a>
      </SidebarMenuButton>
    </SidebarMenuItem>
  )
}
