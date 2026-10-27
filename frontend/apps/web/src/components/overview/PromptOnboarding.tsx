import { type ReactNode, useEffect, useState } from 'react'

import { AuroraPanel } from '@/components/aurora/AuroraPanel'
import { AgentSystemDiagram } from '@/components/overview/AgentSystemDiagram'
import { CopyButton } from '@/components/overview/CodeBlock'
import { ClaudeLogo, CodexLogo, CursorLogo } from '@/components/overview/codingAppLogos'
import { agentSetupPrompt, skillUrl } from '@/components/overview/onboardingCli'
import { useProjectAgent } from '@/components/overview/useOnboarding'

export function PromptOnboarding({
  orgId,
  projectId,
  onAgentLaunched,
  onSetUpManually,
}: {
  orgId: string
  projectId: string
  onAgentLaunched: () => void
  onSetUpManually: () => void
}) {
  useWatchForLaunch(orgId, projectId, onAgentLaunched)
  return (
    <div className="flex flex-col gap-12 pb-8 sm:gap-14 sm:pb-12">
      <SetupPrompt />
      <div className="flex flex-col gap-8">
        <AgentSystemDiagram />
        <OnboardingModeLink onClick={onSetUpManually}>or set up manually</OnboardingModeLink>
      </div>
    </div>
  )
}

/**
 * Calls `onLaunched` once the coding agent launches an agent while the prompt is up. An
 * agent already there when the prompt opened doesn't count, so switching back to the
 * prompt after launching from the steps doesn't close onboarding.
 */
function useWatchForLaunch(orgId: string, projectId: string, onLaunched: () => void) {
  const agent = useProjectAgent({ orgId, projectId })
  const [initialAgentId] = useState(agent?.id)
  const launched = agent != null && agent.id !== initialAgentId
  useEffect(() => {
    if (launched) onLaunched()
  }, [launched, onLaunched])
}

export function OnboardingModeLink({
  onClick,
  children,
}: {
  onClick: () => void
  children: ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="text-muted-foreground hover:text-foreground self-center text-sm underline-offset-4 transition-colors hover:underline"
    >
      {children}
    </button>
  )
}

const encodedPrompt = encodeURIComponent(agentSetupPrompt)

// Native prompt links, matching the omnara.com hero.
const codingApps = [
  { name: 'Claude Code', Logo: ClaudeLogo, href: `claude://code/new?q=${encodedPrompt}` },
  { name: 'Codex', Logo: CodexLogo, href: `codex://threads/new?prompt=${encodedPrompt}` },
  {
    name: 'Cursor',
    Logo: CursorLogo,
    href: `cursor://anysphere.cursor-deeplink/prompt?text=${encodedPrompt}`,
  },
]

function SetupPrompt() {
  return (
    <div className="flex w-full max-w-2xl min-w-0 flex-col gap-3 self-center">
      <div className="flex items-center justify-between gap-3 pr-1.5">
        <span className="text-muted-foreground text-sm">
          Paste into your coding agent to set up your first Omnara agent
        </span>
        <div
          role="group"
          aria-label="Open the prompt in a desktop coding app"
          className="hidden items-center [@media(hover:hover)_and_(pointer:fine)]:flex"
        >
          {codingApps.map(({ name, Logo, href }) => (
            <a
              key={name}
              href={href}
              aria-label={`Open the prompt in ${name}`}
              title={`Open in ${name}`}
              className="text-muted-foreground hover:bg-foreground/[0.06] hover:text-foreground flex size-8 items-center justify-center rounded-md transition-colors"
            >
              <Logo aria-hidden="true" className="size-[18px] fill-current" />
            </a>
          ))}
        </div>
      </div>
      <AuroraPanel className="flex min-h-12 items-center gap-3 py-1.5 pr-1.5 pl-4 sm:pl-5">
        <p className="text-foreground/85 min-w-0 flex-1 font-mono text-[13.5px] leading-6 sm:truncate">
          Read <span className="text-primary">{skillUrl.replace(/^https:\/\/www\./, '')}</span> to
          set up my Omnara agent
        </p>
        <CopyButton text={agentSetupPrompt} label="prompt" />
      </AuroraPanel>
    </div>
  )
}
