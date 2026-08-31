import type { ComponentProps, CSSProperties } from 'react'
import { createContext, use, useEffect, useState } from 'react'

import { TooltipProvider } from '@/components/ui/tooltip'
import { useIsMobile } from '@/hooks/use-mobile'
import { cn } from '@/lib/utils'

const SIDEBAR_COOKIE_NAME = 'sidebar_state'
const SIDEBAR_COOKIE_MAX_AGE = 60 * 60 * 24 * 7
const SIDEBAR_WIDTH = '16rem'
const SIDEBAR_WIDTH_ICON = '3rem'
const SIDEBAR_KEYBOARD_SHORTCUT = 'b'

function commitDesktopSidebarOpen(
  open: boolean,
  onOpenChange: ((open: boolean) => void) | undefined,
  setInternalOpen: (open: boolean) => void,
) {
  if (onOpenChange) onOpenChange(open)
  else setInternalOpen(open)
  document.cookie = `${SIDEBAR_COOKIE_NAME}=${String(open)}; path=/; max-age=${SIDEBAR_COOKIE_MAX_AGE}`
}

interface SidebarContextProps {
  state: 'expanded' | 'collapsed'
  open: boolean
  setOpen: (open: boolean) => void
  openMobile: boolean
  setOpenMobile: (open: boolean) => void
  isMobile: boolean
  toggleSidebar: () => void
}

const SidebarContext = createContext<SidebarContextProps | null>(null)

function useSidebar() {
  const context = use(SidebarContext)
  if (!context) {
    throw new Error('useSidebar must be used within a SidebarProvider.')
  }
  return context
}

function SidebarProvider({
  defaultOpen = true,
  open: openProp,
  onOpenChange: setOpenProp,
  keyboardShortcut = true,
  className,
  style,
  children,
  ...props
}: ComponentProps<'div'> & {
  defaultOpen?: boolean
  open?: boolean
  onOpenChange?: (open: boolean) => void
  keyboardShortcut?: boolean
}) {
  const isMobile = useIsMobile()
  const [openMobile, setOpenMobile] = useState(false)
  const [internalOpen, setInternalOpen] = useState(defaultOpen)
  const open = openProp ?? internalOpen

  const setOpen = (nextOpen: boolean) => {
    commitDesktopSidebarOpen(nextOpen, setOpenProp, setInternalOpen)
  }

  const toggleSidebar = () => {
    if (isMobile) {
      setOpenMobile((value) => !value)
    } else {
      commitDesktopSidebarOpen(!open, setOpenProp, setInternalOpen)
    }
  }

  useEffect(() => {
    if (!keyboardShortcut) return

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === SIDEBAR_KEYBOARD_SHORTCUT && (event.metaKey || event.ctrlKey)) {
        event.preventDefault()
        if (isMobile) setOpenMobile((value) => !value)
        else commitDesktopSidebarOpen(!open, setOpenProp, setInternalOpen)
      }
    }
    window.addEventListener('keydown', handleKeyDown)
    return () => {
      window.removeEventListener('keydown', handleKeyDown)
    }
  }, [isMobile, keyboardShortcut, open, setOpenProp])

  const state = open ? 'expanded' : 'collapsed'

  const contextValue: SidebarContextProps = {
    state,
    open,
    setOpen,
    isMobile,
    openMobile,
    setOpenMobile,
    toggleSidebar,
  }

  return (
    <SidebarContext.Provider value={contextValue}>
      <TooltipProvider delayDuration={0}>
        <div
          data-slot="sidebar-wrapper"
          style={
            {
              '--sidebar-width': SIDEBAR_WIDTH,
              '--sidebar-width-icon': SIDEBAR_WIDTH_ICON,
              ...style,
            } as CSSProperties
          }
          className={cn(
            'group/sidebar-wrapper has-data-[variant=inset]:bg-sidebar flex min-h-svh w-full',
            className,
          )}
          {...props}
        >
          {children}
        </div>
      </TooltipProvider>
    </SidebarContext.Provider>
  )
}

export { SidebarProvider, useSidebar }
