import { Link, type LinkProps } from '@tanstack/react-router'
import type { ComponentType, ReactNode } from 'react'

import {
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
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

export function NavSection({
  label,
  items,
  children,
}: {
  label?: string
  items: NavItem[]
  children?: ReactNode
}) {
  return (
    <SidebarGroup className="px-2 py-1">
      {label && (
        <SidebarGroupLabel className="text-sidebar-foreground/55 h-8 px-2.5 text-sm font-normal">
          {label}
        </SidebarGroupLabel>
      )}
      <SidebarGroupContent>
        <SidebarMenu className="gap-1">
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
