import { useAgentPanel } from '@omnara/react'
import type { AgentPanel as Panel } from '@omnara/sdk'
import { CatchBoundary, useParams, useRouterState } from '@tanstack/react-router'
import {
  Activity,
  lazy,
  type ReactNode,
  Suspense,
  useContext,
  useEffect,
  useRef,
  useState,
} from 'react'

import { ChevronDown, MessageCircleQuestion } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { ActiveOrgContext } from '@/lib/active-org-context'
import { containFileDrop } from '@/lib/file-drop-guard'

import { AgentPanelBootstrap } from './AgentPanelBootstrap'
import { AgentPanelHeader } from './AgentPanelHeader'
import { dashboardPageContext } from './page-context'

const LazyAgentPanelChat = lazy(async () => {
  const module = await import('./AgentPanelChat')
  return { default: module.AgentPanelChat }
})
const LazyAgentPanelSetup = lazy(async () => {
  const module = await import('./AgentPanelSetup')
  return { default: module.AgentPanelSetup }
})

export function AgentPanel({ children }: { children: ReactNode }) {
  const pathname = useRouterState({ select: (state) => state.location.pathname })
  const consentPage = pathname === '/device' || pathname === '/oauth/authorize'
  const query = useAgentPanel()
  const panel = query.data
  const [expanded, setExpanded] = useState(false)
  const [opened, setOpened] = useState(false)
  const [settings, setSettings] = useState(false)
  const surface = useRef<HTMLDialogElement>(null)
  const launcher = useRef<HTMLButtonElement>(null)
  const selection = panel?.selection

  function close() {
    setExpanded(false)
    launcher.current?.focus()
  }
  const showLauncher = !consentPage && (panel?.enabled ?? query.isError)
  const visible = expanded && showLauncher

  useEffect(() => {
    if (visible) surface.current?.focus()
  }, [visible])

  return (
    <>
      {children}
      {/* Floats over the page, above any pinned page actions, without reserving layout space. */}
      <div className="above-pinned-actions pointer-events-none fixed top-3 right-3 z-40 flex flex-col items-end justify-end gap-3 *:pointer-events-auto sm:top-16 sm:right-4">
        {/* A nonmodal dialog handles Escape without trapping focus. */}
        {/* eslint-disable-next-line jsx-a11y/no-noninteractive-element-interactions */}
        <dialog
          open={visible}
          ref={surface}
          tabIndex={-1}
          id="agent-panel"
          aria-label="Assistant"
          onDragOver={containFileDrop}
          onDrop={containFileDrop}
          className={
            visible
              ? 'bg-background animate-in fade-in-0 zoom-in-95 static m-0 flex max-h-[40rem] min-h-0 w-[calc(100vw-1.5rem)] flex-1 origin-bottom-right flex-col overflow-hidden rounded-2xl border p-0 shadow-xl duration-[var(--duration-panel)] motion-reduce:animate-none sm:w-[27.5rem]'
              : 'hidden'
          }
          onKeyDown={(event) => {
            if (event.key === 'Escape' && !event.defaultPrevented) {
              event.preventDefault()
              close()
            }
          }}
        >
          <AgentPanelHeader
            selection={selection}
            settings={settings}
            onClose={close}
            onSettings={() => {
              setSettings((value) => !value)
            }}
          />
          {query.isPending && (
            <p className="text-muted-foreground p-4 text-sm">Loading assistant…</p>
          )}
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
              settings={settings}
              allowSetup={!consentPage}
              onConnected={() => {
                setSettings(false)
              }}
              onClose={close}
            />
          )}
        </dialog>
        {showLauncher && (
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                ref={launcher}
                data-floating-launcher
                size="icon"
                className="size-11 rounded-full shadow-lg"
                aria-expanded={visible}
                aria-controls="agent-panel"
                onClick={() => {
                  setOpened(true)
                  setExpanded((value) => !value)
                }}
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
        )}
      </div>
    </>
  )
}

function PanelContents({
  panel,
  opened,
  visible,
  settings,
  allowSetup,
  onConnected,
  onClose,
}: {
  panel: Panel
  opened: boolean
  visible: boolean
  settings: boolean
  allowSetup: boolean
  onConnected: () => void
  onClose: () => void
}) {
  const activeOrg = useContext(ActiveOrgContext)
  const pathname = useRouterState({ select: (state) => state.location.pathname })
  const params = useParams({ strict: false })
  const selection = panel.selection
  const choosing = settings || selection == null
  if (!panel.enabled) return null
  return (
    <>
      {panel.state === 'unavailable' && (
        <p role="status" className="text-muted-foreground p-4 text-sm">
          Your selected agent is unavailable. It may have been removed, or your access may have
          changed. Choose another agent.
        </p>
      )}
      {allowSetup && panel.setup != null && (
        <AgentPanelBootstrap setup={panel.setup} revision={panel.revision} />
      )}
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
                  dashboardPageContext(pathname, params, activeOrg?.activeOrg, selection)
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
            <LazyAgentPanelSetup
              panel={panel}
              onConnected={onConnected}
              onClose={settings ? onClose : undefined}
            />
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
