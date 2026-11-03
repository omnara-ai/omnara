import { Link, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'

import { BrandMark } from '@/components/brand/OmnaraMark'
import { Check, ChevronsUpDown, Plus, SettingsIcon } from '@/components/icons'
import { CreateOrgDialog } from '@/components/org/CreateOrgDialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from '@/components/ui/sidebar'
import { useActiveOrg } from '@/lib/use-active-org'

export function OrgSwitcher() {
  const navigate = useNavigate()
  const { orgs, activeOrg, setActiveOrgId } = useActiveOrg()
  const { setOpenMobile } = useSidebar()
  const [newOrgOpen, setNewOrgOpen] = useState(false)

  async function switchOrganization(id: string) {
    if (id === activeOrg.id) return
    setActiveOrgId(id)
    setOpenMobile(false)
    await navigate({ to: '/', replace: true })
  }

  return (
    <>
      <SidebarMenu>
        <SidebarMenuItem>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <SidebarMenuButton
                size="lg"
                className="data-[state=open]:bg-sidebar-accent data-[state=open]:text-sidebar-accent-foreground h-10 py-1"
              >
                <BrandMark className="aspect-square size-8" />
                <div className="grid flex-1 text-left text-sm leading-tight">
                  <span className="truncate font-semibold">{activeOrg.name}</span>
                  <span className="text-muted-foreground truncate text-xs capitalize">
                    {activeOrg.role}
                  </span>
                </div>
                <ChevronsUpDown className="ml-auto size-4" />
              </SidebarMenuButton>
            </DropdownMenuTrigger>
            <DropdownMenuContent
              className="w-(--radix-dropdown-menu-trigger-width) min-w-60 rounded-lg"
              align="start"
              side="bottom"
              sideOffset={4}
            >
              <DropdownMenuLabel className="text-muted-foreground text-xs">
                Organizations
              </DropdownMenuLabel>
              {orgs.map((org) => (
                <DropdownMenuItem
                  key={org.id}
                  className="gap-2"
                  onClick={() => {
                    void switchOrganization(org.id)
                  }}
                >
                  <span className="flex-1 truncate">{org.name}</span>
                  {org.id === activeOrg.id && <Check className="size-4 shrink-0" />}
                </DropdownMenuItem>
              ))}
              <DropdownMenuSeparator />
              <DropdownMenuItem asChild>
                <Link
                  to="/settings"
                  onClick={() => {
                    setOpenMobile(false)
                  }}
                >
                  <SettingsIcon />
                  Organization settings
                </Link>
              </DropdownMenuItem>
              <DropdownMenuItem
                onClick={() => {
                  setNewOrgOpen(true)
                }}
              >
                <Plus />
                New organization
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </SidebarMenuItem>
      </SidebarMenu>

      <CreateOrgDialog
        open={newOrgOpen}
        onOpenChange={(open) => {
          setNewOrgOpen(open)
          if (!open) setOpenMobile(false)
        }}
      />
    </>
  )
}
