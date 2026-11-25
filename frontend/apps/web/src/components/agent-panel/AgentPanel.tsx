import { useAgentPanel, useMe } from '@omnara/react'
import type { AgentPanel as Panel } from '@omnara/sdk'
import { CatchBoundary, useParams, useRouterState } from '@tanstack/react-router'
import {
  Activity,
  lazy,
  type ReactNode,
  type RefObject,
  Suspense,
  useContext,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from 'react'
import { createPortal } from 'react-dom'

import { ChevronDown, MessageCircleQuestion } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { ActiveOrgContext } from '@/lib/active-org-context'
import { containFileDrop } from '@/lib/file-drop-guard'
import { cn } from '@/lib/utils'

import { AgentPanelBootstrap } from './AgentPanelBootstrap'
import { AgentPanelHeader } from './AgentPanelHeader'
import { AssistantContext, createAskQueue, useAutomaticAssistant } from './assistant-context'
import { AssistantCursor } from './AssistantCursor'
import type { PagePointer as Pointer } from './page-actions'
import { dashboardPageContext } from './page-context'
import { pageSnapshot } from './page-snapshot'
import { PagePointer } from './PagePointer'

const LazyAgentPanelChat = lazy(async () => {
  const module = await import('./AgentPanelChat')
  return { default: module.AgentPanelChat }
})
const LazyAgentPanelSetup = lazy(async () => {
  const module = await import('./AgentPanelSetup')
  return { default: module.AgentPanelSetup }
})

export function AgentPanel({ children }: { children: ReactNode }) {
  const query = useAgentPanel()
  const pathname = useRouterState({ select: (state) => state.location.pathname })
  const consentPage = pathname === '/device' || pathname === '/oauth/authorize'
  const [openRequests, setOpenRequests] = useState(0)
  const [questions] = useState(createAskQueue)
  const [dock, setDock] = useState<HTMLElement | null>(null)
  const [launcherHidden, hideLauncher] = useState(false)
  const [pointer, setPointer] = useState<Pointer | null>(null)
  const [followRequests, setFollowRequests] = useState(0)
  const [asideRequests, setAsideRequests] = useState(0)
  function open() {
    setOpenRequests((count) => count + 1)
  }
  function point(next: Pointer | null) {
    setPointer(next)
    const floating = document.getElementById(floatingPanelID)
    if (next != null && floating != null && overlaps(next.element, floating)) {
      setAsideRequests((count) => count + 1)
    }
  }
  function ask(text: string) {
    open()
    questions.ask(text)
  }
  const bootstrap = useAutomaticAssistant(
    consentPage ? undefined : query.data?.setup,
    query.data?.revision ?? 0,
    open,
  )
  return (
    <AssistantContext
      value={{
        bootstrap,
        open,
        ask,
        registerSender: questions.register,
        setDock,
        hideLauncher,
        point,
        follow: () => {
          setFollowRequests((count) => count + 1)
        },
      }}
    >
      {children}
      <PanelSurface
        query={query}
        hidden={consentPage || launcherHidden}
        requests={{ open: openRequests, follow: followRequests, aside: asideRequests }}
        dock={consentPage ? null : dock}
      />
      <AssistantCursor />
      <PagePointer
        pointer={pointer}
        onDismiss={() => {
          setPointer(null)
        }}
      />
    </AssistantContext>
  )
}

const floatingPanelID = 'agent-panel'

function overlaps(element: Element, other: Element): boolean {
  const a = element.getBoundingClientRect()
  const b = other.getBoundingClientRect()
  return a.left < b.right && b.left < a.right && a.top < b.bottom && b.top < a.bottom
}

interface PanelRequests {
  open: number
  /** Open once the conversation is no longer docked in a page: the assistant moved the user. */
  follow: number
  /** Collapse so the page underneath is clear. */
  aside: number
}

function usePanelState(requests: PanelRequests, docked: boolean) {
  const [expanded, setExpanded] = useState(false)
  const [opened, setOpened] = useState(false)
  const [settings, setSettings] = useState(false)
  const [handled, setHandled] = useState(requests)
  if (handled.open !== requests.open || (handled.follow !== requests.follow && !docked)) {
    setHandled({ ...handled, open: requests.open, follow: requests.follow })
    setExpanded(true)
    setOpened(true)
    setSettings(false)
  }
  if (handled.aside !== requests.aside) {
    setHandled((current) => ({ ...current, aside: requests.aside }))
    setExpanded(false)
  }
  if (docked && (!opened || expanded)) {
    setOpened(true)
    setExpanded(false)
  }
  return { expanded, setExpanded, opened, setOpened, settings, setSettings }
}

// One element holds the conversation for the life of the page. Moving it between the
// corner panel and a page's dock keeps the draft, scroll position and stream.
function createHost() {
  const host = document.createElement('div')
  host.setAttribute('data-assistant-surface', '')
  host.className = 'flex min-h-0 flex-1 flex-col'
  return host
}

function PanelSurface({
  query,
  hidden,
  requests,
  dock,
}: {
  query: ReturnType<typeof useAgentPanel>
  hidden: boolean
  requests: PanelRequests
  dock: HTMLElement | null
}) {
  const { data: me } = useMe()
  const panel = query.data
  const docked = dock != null
  const { expanded, setExpanded, opened, setOpened, settings, setSettings } = usePanelState(
    requests,
    docked,
  )
  const [host] = useState(createHost)
  const floating = useRef<HTMLDialogElement>(null)
  const launcher = useRef<HTMLButtonElement>(null)

  function close() {
    setExpanded(false)
    launcher.current?.focus()
  }
  const chatting = panel?.selection != null && !settings
  const showLauncher = !hidden && !docked && me.orgs.length > 0 && (panel != null || query.isError)
  const visible = expanded && showLauncher

  useLayoutEffect(() => {
    const home = dock ?? floating.current
    home?.append(host)
    return () => {
      host.remove()
    }
  }, [dock, host])

  useEffect(() => {
    if (visible) floating.current?.focus()
  }, [visible])

  return (
    <>
      {/* Floats over the page, above pinned actions, without reserving layout space. */}
      <div
        data-assistant-surface
        className="above-pinned-actions pointer-events-none fixed top-3 right-3 z-40 flex flex-col items-end justify-end gap-3 *:pointer-events-auto sm:top-16 sm:right-4"
      >
        {/* A nonmodal dialog handles Escape without trapping focus. */}
        {/* eslint-disable-next-line jsx-a11y/no-noninteractive-element-interactions */}
        <dialog
          open={visible}
          ref={floating}
          tabIndex={-1}
          id={floatingPanelID}
          aria-label="Assistant"
          onDragOver={containFileDrop}
          onDrop={containFileDrop}
          className={
            visible
              ? cn(
                  'bg-background animate-in fade-in-0 zoom-in-95 static m-0 flex max-h-[40rem] min-h-0 w-[calc(100vw-1.5rem)] origin-bottom-right flex-col overflow-hidden rounded-2xl border p-0 shadow-xl duration-[var(--duration-panel)] outline-none motion-reduce:animate-none sm:w-[27.5rem]',
                  // A conversation fills the height; setup is only as tall as its form.
                  chatting && 'flex-1',
                )
              : 'hidden'
          }
          onKeyDown={(event) => {
            if (event.key === 'Escape' && !event.defaultPrevented) {
              event.preventDefault()
              close()
            }
          }}
        />
        {showLauncher && (
          <PanelLauncher
            ref={launcher}
            visible={visible}
            onToggle={() => {
              setOpened(true)
              setExpanded((value) => !value)
            }}
          />
        )}
      </div>
      {createPortal(
        <PanelBody
          query={query}
          opened={opened}
          visible={docked || visible}
          docked={docked}
          settings={settings}
          onClose={close}
          onSettings={setSettings}
        />,
        host,
      )}
    </>
  )
}

function PanelLauncher({
  ref,
  visible,
  onToggle,
}: {
  ref: RefObject<HTMLButtonElement | null>
  visible: boolean
  onToggle: () => void
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          ref={ref}
          data-floating-launcher
          size="icon"
          className="size-11 rounded-full shadow-lg"
          aria-expanded={visible}
          aria-controls={floatingPanelID}
          onClick={onToggle}
        >
          {visible ? (
            <ChevronDown className="size-5" />
          ) : (
            <MessageCircleQuestion className="size-5" />
          )}
          <span className="sr-only">Assistant</span>
        </Button>
      </TooltipTrigger>
      <TooltipContent side="left">Assistant</TooltipContent>
    </Tooltip>
  )
}

function PanelBody({
  query,
  opened,
  visible,
  docked,
  settings,
  onClose,
  onSettings,
}: {
  query: ReturnType<typeof useAgentPanel>
  opened: boolean
  visible: boolean
  docked: boolean
  settings: boolean
  onClose: () => void
  onSettings: (settings: boolean) => void
}) {
  const panel = query.data
  return (
    <>
      <AgentPanelHeader
        selection={panel?.selection}
        settings={settings}
        docked={docked}
        onClose={onClose}
        onSettings={() => {
          onSettings(!settings)
        }}
      />
      {query.isPending && <p className="text-muted-foreground p-4 text-sm">Loading assistant…</p>}
      {panel == null && query.error != null && (
        <div className="p-4">
          <p role="alert" className="text-destructive text-sm">
            Could not load your assistant.
          </p>
          <Button variant="outline" onClick={() => void query.refetch()}>
            Try again
          </Button>
        </div>
      )}
      {panel != null && (
        <PanelContents
          panel={panel}
          opened={opened}
          visible={visible}
          docked={docked}
          settings={settings}
          onConnected={() => {
            onSettings(false)
          }}
        />
      )}
    </>
  )
}

function PanelContents({
  panel,
  opened,
  visible,
  docked,
  settings,
  onConnected,
}: {
  panel: Panel
  opened: boolean
  visible: boolean
  docked: boolean
  settings: boolean
  onConnected: () => void
}) {
  const activeOrg = useContext(ActiveOrgContext)
  const pathname = useRouterState({ select: (state) => state.location.pathname })
  const route = useRouterState({ select: (state) => state.matches.at(-1)?.fullPath })
  const params = useParams({ strict: false })
  const selection = panel.selection
  const choosing = settings || selection == null
  return (
    <>
      {panel.state === 'unavailable' && (
        <p role="status" className="text-muted-foreground p-4 text-sm">
          Your selected agent is unavailable. It may have been removed, or your access may have
          changed. Choose another agent.
        </p>
      )}
      <AgentPanelBootstrap />
      {selection != null && opened && (
        <Activity mode={visible && !choosing ? 'visible' : 'hidden'}>
          <CatchBoundary getResetKey={() => selection.agent_id} errorComponent={PanelContentError}>
            <Suspense
              fallback={<p className="text-muted-foreground p-4 text-sm">Loading assistant…</p>}
            >
              <LazyAgentPanelChat
                key={selection.agent_id}
                selection={selection}
                archived={panel.state === 'archived'}
                getContext={() =>
                  dashboardPageContext({
                    pathname,
                    route,
                    params,
                    snapshot: pageSnapshot,
                    docked,
                    viewedOrg: activeOrg?.activeOrg,
                    selection,
                  })
                }
              />
            </Suspense>
          </CatchBoundary>
        </Activity>
      )}
      {choosing && opened && (
        <CatchBoundary
          getResetKey={() => selection?.agent_id ?? 'setup'}
          errorComponent={PanelContentError}
        >
          <Suspense fallback={<p className="text-muted-foreground p-4 text-sm">Loading setup…</p>}>
            <LazyAgentPanelSetup panel={panel} onConnected={onConnected} settings={settings} />
          </Suspense>
        </CatchBoundary>
      )}
    </>
  )
}

function PanelContentError() {
  return (
    <div className="p-4">
      <p role="alert" className="text-destructive text-sm">
        Could not load your assistant.
      </p>
      <Button
        variant="outline"
        onClick={() => {
          window.location.reload()
        }}
      >
        Reload page
      </Button>
    </div>
  )
}
