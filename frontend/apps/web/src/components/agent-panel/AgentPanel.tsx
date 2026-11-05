import { useAgentPanel, useCreateAssistantAgent, useUpdateAgentPanel } from '@omnara/react'
import type { AgentPanel as Panel } from '@omnara/sdk'
import { CatchBoundary, useParams, useRouterState } from '@tanstack/react-router'
import { lazy, type ReactNode, Suspense, useContext, useEffect, useRef, useState } from 'react'

import { Bot } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { ActiveOrgContext } from '@/lib/active-org-context'
import type { CssVariables } from '@/lib/css'
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
  const style: CssVariables = {
    '--app-height': showLauncher ? 'calc(100svh - 3rem)' : '100svh',
  }

  useEffect(() => {
    if (visible) surface.current?.focus()
  }, [visible])

  return (
    <div className="h-svh" style={style}>
      <div className="h-(--app-height) overflow-auto">{children}</div>
      {showLauncher && (
        <div className="bg-background flex h-12 items-center justify-end border-t px-4">
          <Button
            ref={launcher}
            className="rounded-full shadow-sm"
            aria-expanded={visible}
            aria-controls="agent-panel"
            onClick={() => {
              setOpened(true)
              setExpanded((value) => !value)
            }}
          >
            <Bot className="size-4" />
            Assistant
          </Button>
        </div>
      )}
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
            ? 'bg-background fixed top-auto right-3 bottom-14 left-auto z-50 m-0 flex h-[min(640px,75dvh,calc(100dvh-5rem))] w-[min(440px,calc(100vw-24px))] flex-col overflow-hidden rounded-2xl border p-0 shadow-xl'
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
            settings={settings}
            allowSetup={!consentPage}
            onConnected={() => {
              setSettings(false)
            }}
            onClose={close}
          />
        )}
      </dialog>
    </div>
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
  const create = useCreateAssistantAgent()
  const update = useUpdateAgentPanel()
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
        <CatchBoundary getResetKey={() => selection.agent_id} errorComponent={PanelContentError}>
          <Suspense
            fallback={<p className="text-muted-foreground p-4 text-sm">Loading assistant…</p>}
          >
            <LazyAgentPanelChat
              key={selection.agent_id}
              selection={selection}
              archived={panel.state === 'archived'}
              hidden={!visible || choosing}
              getContext={() =>
                dashboardPageContext(pathname, params, activeOrg?.activeOrg, selection)
              }
            />
          </Suspense>
        </CatchBoundary>
      )}
      {choosing && opened && (
        <CatchBoundary
          getResetKey={() => selection?.agent_id ?? 'setup'}
          errorComponent={PanelContentError}
        >
          <Suspense fallback={<p className="text-muted-foreground p-4 text-sm">Loading setup…</p>}>
            <LazyAgentPanelSetup
              panel={panel}
              actions={{ create, update }}
              onConnected={onConnected}
            />
          </Suspense>
        </CatchBoundary>
      )}
      {settings && (
        <footer className="flex shrink-0 gap-2 border-t p-3">
          {selection != null && (
            <Button
              size="sm"
              variant="outline"
              disabled={update.isPending}
              onClick={() => {
                update.mutate({ agent_id: null })
              }}
            >
              Disconnect
            </Button>
          )}
          <Button
            size="sm"
            variant="ghost"
            disabled={update.isPending}
            onClick={() => {
              update.mutate({ enabled: false }, { onSuccess: onClose })
            }}
          >
            Turn off panel
          </Button>
        </footer>
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
