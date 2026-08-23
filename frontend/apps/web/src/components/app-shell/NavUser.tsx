import { useMe } from '@omnara/react'
import { sessionLogout } from '@omnara/sdk/browser'
import { useState } from 'react'

import { ChevronsUpDown, CircleHelp, LogOut, Monitor, Moon, Sun } from '@/components/icons'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { SidebarMenu, SidebarMenuButton, SidebarMenuItem } from '@/components/ui/sidebar'
import type { ThemePreference } from '@/lib/theme'
import { getThemePreference, setThemePreference } from '@/lib/theme'

// Logout redirects on success, so there is no success state.
type LogoutStatus = { kind: 'idle' } | { kind: 'pending' } | { kind: 'error'; message: string }

function Identity({ name, email, initials }: { name: string; email: string; initials: string }) {
  return (
    <>
      <span className="bg-secondary text-secondary-foreground flex aspect-square size-8 items-center justify-center rounded-lg text-xs font-medium">
        {initials}
      </span>
      <div className="grid flex-1 text-left text-sm leading-tight">
        <span className="truncate font-medium">{name}</span>
        <span className="text-muted-foreground truncate text-xs">{email}</span>
      </div>
    </>
  )
}

const themeOptions = [
  { value: 'light', label: 'Light', icon: Sun },
  { value: 'dark', label: 'Dark', icon: Moon },
  { value: 'system', label: 'System', icon: Monitor },
] as const

function ThemeMenuItems() {
  const [theme, setTheme] = useState<ThemePreference>(getThemePreference)
  return (
    <div className="flex items-center gap-2 px-2 py-1.5 text-sm">
      <span className="text-muted-foreground">Theme</span>
      <DropdownMenuRadioGroup
        value={theme}
        className="bg-muted ml-auto flex rounded-md p-0.5"
        aria-label="Theme preference"
        onValueChange={(value) => {
          const preference = value as ThemePreference
          setThemePreference(preference)
          setTheme(preference)
        }}
      >
        {themeOptions.map(({ value, label, icon: Icon }) => (
          <DropdownMenuRadioItem
            key={value}
            value={value}
            className={`size-7 justify-center p-0 [&>span]:hidden ${
              theme === value
                ? 'bg-background text-foreground shadow-sm'
                : 'text-muted-foreground hover:text-foreground'
            }`}
            aria-label={`${label} theme`}
            onSelect={(event) => {
              event.preventDefault()
            }}
          >
            <Icon className="size-4" />
          </DropdownMenuRadioItem>
        ))}
      </DropdownMenuRadioGroup>
    </div>
  )
}

export function NavUser() {
  const { data: me } = useMe()
  const [logoutStatus, setLogoutStatus] = useState<LogoutStatus>({ kind: 'idle' })

  async function handleLogout() {
    setLogoutStatus({ kind: 'pending' })
    try {
      await sessionLogout()
      window.location.href = '/login'
    } catch {
      setLogoutStatus({ kind: 'error', message: 'Logout failed' })
    }
  }

  const name = me.user.display_name || 'You'
  const email = me.user.email || 'No email'
  const initials = (me.user.display_name || me.user.email || 'U').trim().slice(0, 2).toUpperCase()

  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <SidebarMenuButton
              size="lg"
              className="data-[state=open]:bg-sidebar-accent data-[state=open]:text-sidebar-accent-foreground"
            >
              <Identity name={name} email={email} initials={initials} />
              <ChevronsUpDown className="ml-auto size-4" />
            </SidebarMenuButton>
          </DropdownMenuTrigger>
          <DropdownMenuContent
            className="w-(--radix-dropdown-menu-trigger-width) min-w-60 rounded-lg"
            side="top"
            align="end"
            sideOffset={4}
          >
            <DropdownMenuLabel className="p-0 font-normal">
              <div className="flex items-center gap-2 px-1 py-1.5">
                <Identity name={name} email={email} initials={initials} />
              </div>
            </DropdownMenuLabel>
            <DropdownMenuSeparator />
            <ThemeMenuItems />
            <DropdownMenuSeparator />
            <DropdownMenuItem
              asChild
              className="hover:bg-primary/15 focus:bg-primary/15 [&_svg]:!text-foreground"
            >
              <a href="https://docs.omnara.com/support" target="_blank" rel="noreferrer">
                <CircleHelp />
                <span className="translate-y-px">Need help?</span>
              </a>
            </DropdownMenuItem>
            <DropdownMenuItem
              variant="destructive"
              disabled={logoutStatus.kind === 'pending'}
              onClick={() => {
                void handleLogout()
              }}
            >
              <LogOut />
              Log out
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
        {logoutStatus.kind === 'error' && (
          <p className="text-destructive px-2 pt-1 text-xs">{logoutStatus.message}</p>
        )}
      </SidebarMenuItem>
    </SidebarMenu>
  )
}
