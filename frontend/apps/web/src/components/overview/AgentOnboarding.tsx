import {
  useCreateAgentConfig,
  useCreateAgentProfile,
  useOrgOverview,
  usePersonalAccessTokens,
} from '@omnara/react'
import type {
  Agent,
  AgentProfile,
  OrgOverviewResponse,
  PersonalAccessToken,
  VisibleProject,
} from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { type ReactNode, startTransition, useDeferredValue, useState } from 'react'

import { agentTemplates } from '@/components/agents/agentTemplates'
import { Monitor, Terminal } from '@/components/icons'
import { CodeBlock, CodeTabsBlock } from '@/components/overview/CodeBlock'
import { ChatGuide, ChatOptions, CreateChat, RunFooter } from '@/components/overview/OnboardingChat'
import {
  chatMessage,
  cliLoginCommand,
  cliSetupPrompt,
  cliTokenHost,
  isCliLoginToken,
  profileCreateSpec,
} from '@/components/overview/onboardingCli'
import { ProfileDraftStep } from '@/components/overview/OnboardingProfileDraft'
import {
  OnboardingStep,
  OnboardingSteps,
  type StepStatus,
} from '@/components/overview/OnboardingSteps'
import { useCreateChat } from '@/components/overview/useCreateChat'
import { useTemplateDefaults } from '@/components/overview/useTemplateDefaults'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { errorMessage } from '@/lib/submit-status'

const pollIntervalMs = 3000

type OnboardingTab = 'cli' | 'browser'

interface OnboardingProgress {
  profile?: AgentProfile
  agent?: Agent
}

function stepStatuses(completed: boolean[]): StepStatus[] {
  const activeIndex = completed.indexOf(false)
  return completed.map((done, index) => {
    if (done) return 'done'
    return index === activeIndex ? 'active' : 'upcoming'
  })
}

export function AgentOnboarding({
  orgId,
  project,
  overview,
}: {
  orgId: string
  project: VisibleProject
  overview: OrgOverviewResponse
}) {
  const [tab, setTab] = useState<OnboardingTab>('cli')
  const [created, setCreated] = useState<OnboardingProgress>({})

  const liveProfile =
    created.profile ??
    overview.recent_agent_profiles.find((candidate) => candidate.project_id === project.id)
  const liveAgents = overview.recent_agents.filter((agent) => agent.project_id === project.id)
  const liveAgent =
    created.agent ??
    liveAgents.find((candidate) => candidate.agent_profile_id === liveProfile?.id) ??
    liveAgents[0]

  useOrgOverview(orgId, { refetchInterval: liveAgent == null ? pollIntervalMs : false })
  const tokensQuery = usePersonalAccessTokens(50, {
    refetchInterval: tab === 'cli' && liveProfile == null ? pollIntervalMs : false,
  })

  const shownOverview = useDeferredValue(overview)
  const shownTokens = useDeferredValue(tokensQuery.data)
  const profile =
    created.profile ??
    shownOverview.recent_agent_profiles.find((candidate) => candidate.project_id === project.id)
  const shownAgents = shownOverview.recent_agents.filter((agent) => agent.project_id === project.id)
  const agent =
    created.agent ??
    shownAgents.find((candidate) => candidate.agent_profile_id === profile?.id) ??
    shownAgents[0]
  const cliToken = useInfiniteQueryItems({ data: shownTokens }).find(isCliLoginToken)

  return (
    <div className="flex w-full max-w-4xl flex-col gap-10 py-4">
      <Tabs
        value={tab}
        onValueChange={(value) => {
          setTab(value === 'browser' ? 'browser' : 'cli')
        }}
      >
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div className="flex flex-col gap-2">
            <h2 className="text-2xl font-semibold tracking-tight">Launch your first agent</h2>
            <p className="text-muted-foreground text-sm">
              Follow the steps to create an agent profile and start a chat.
            </p>
          </div>
          <TabsList aria-label="Setup method" className="h-10 self-start">
            <TabsTrigger value="cli" className="px-4">
              <Terminal />
              CLI
            </TabsTrigger>
            <TabsTrigger value="browser" className="px-4">
              <Monitor />
              Browser
            </TabsTrigger>
          </TabsList>
        </div>
        <TabsContent value="cli" className="pt-14">
          {!tokensQuery.isPending && (
            <CliSteps
              orgId={orgId}
              project={project}
              cliToken={cliToken}
              profile={profile}
              agent={agent}
              onProfileCreated={(createdProfile) => {
                setCreated((prev) => ({ ...prev, profile: createdProfile }))
              }}
              onAgentCreated={(createdAgent) => {
                setCreated((prev) => ({ ...prev, agent: createdAgent }))
              }}
            />
          )}
        </TabsContent>
        <TabsContent value="browser" className="pt-14">
          <BrowserSteps
            orgId={orgId}
            project={project}
            profile={profile}
            agent={agent}
            onProfileCreated={(createdProfile) => {
              startTransition(() => {
                setCreated((prev) => ({ ...prev, profile: createdProfile }))
              })
            }}
            onAgentCreated={(createdAgent) => {
              startTransition(() => {
                setCreated((prev) => ({ ...prev, agent: createdAgent }))
              })
            }}
          />
        </TabsContent>
      </Tabs>
    </div>
  )
}

function CliSteps({
  orgId,
  project,
  cliToken,
  profile,
  agent,
  onProfileCreated,
  onAgentCreated,
}: {
  orgId: string
  project: VisibleProject
  cliToken?: PersonalAccessToken
  profile?: AgentProfile
  agent?: Agent
  onProfileCreated: (profile: AgentProfile) => void
  onAgentCreated: (agent: Agent) => void
}) {
  const [profilePending, setProfilePending] = useState(false)
  const chatRun = useCreateChat(orgId, project, profile, onAgentCreated)
  const [login = 'upcoming', createProfile = 'upcoming', chat = 'upcoming'] = stepStatuses([
    cliToken != null || profile != null,
    profile != null,
    agent != null,
  ])
  return (
    <OnboardingSteps>
      <OnboardingStep
        index={1}
        title="Log in with the CLI"
        doneTitle="Logged in with the CLI"
        description="Run the command below to sign in and save an API token"
        status={login}
        nextStatus={createProfile}
        completion={cliToken ? cliTokenHost(cliToken) : 'CLI access is set up'}
      >
        {login !== 'done' && <CodeBlock content={cliLoginCommand} label="command" />}
      </OnboardingStep>
      <OnboardingStep
        index={2}
        title="Create an agent profile"
        doneTitle="Created an agent profile"
        description="Run the command or hand the prompt to your coding agent"
        status={createProfile}
        nextStatus={chat}
        pending={profilePending}
        completion={profile && <ProfileCreated project={project} profile={profile} />}
      >
        {!profile && (
          <CliProfileOptions
            orgId={orgId}
            projectId={project.id}
            onCreated={onProfileCreated}
            onPendingChange={setProfilePending}
          />
        )}
      </OnboardingStep>
      <OnboardingStep
        index={3}
        title="Chat with your agent"
        doneTitle="Started a chat"
        description="Launch an agent from the profile, then talk to it from the browser or your terminal"
        status={chat}
        pending={chatRun.pending}
        completion={agent && <ChatGuide project={project} agent={agent} />}
      >
        {profile ? (
          <ChatOptions
            orgId={orgId}
            project={project}
            profile={profile}
            run={
              agent
                ? undefined
                : {
                    pending: chatRun.pending,
                    error: chatRun.error,
                    onRun: () => {
                      void chatRun.launch(chatMessage)
                    },
                  }
            }
          />
        ) : null}
      </OnboardingStep>
    </OnboardingSteps>
  )
}

function CliProfileOptions({
  orgId,
  projectId,
  onCreated,
  onPendingChange,
}: {
  orgId: string
  projectId: string
  onCreated: (profile: AgentProfile) => void
  onPendingChange: (pending: boolean) => void
}) {
  const defaults = useTemplateDefaults(orgId, projectId)
  const createAgentConfig = useCreateAgentConfig(orgId, projectId)
  const createAgentProfile = useCreateAgentProfile(orgId, projectId)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<ReactNode>(null)
  if (!defaults.ready) return null
  const template = agentTemplates[0]
  if (!template) return null
  const spec = profileCreateSpec({
    orgId,
    projectId,
    name: template.name,
    config: defaults.build(template),
  })

  async function run() {
    setPending(true)
    onPendingChange(true)
    setError(null)
    try {
      const config = await createAgentConfig.mutateAsync({
        source: spec.json,
        source_format: 'json',
      })
      onCreated(await createAgentProfile.mutateAsync({ name: spec.name, config: config.id }))
    } catch (err) {
      setError(errorMessage(err, 'Could not create profile'))
    } finally {
      setPending(false)
      onPendingChange(false)
    }
  }

  return (
    <CodeTabsBlock
      label="How to create the profile"
      tabs={[
        { value: 'command', label: 'CLI', content: spec.command },
        {
          value: 'prompt',
          label: 'Prompt',
          content: { copy: cliSetupPrompt, segments: [cliSetupPrompt], language: 'text' },
          emphasis: true,
          footer: false,
        },
      ]}
      footer={
        <RunFooter
          pending={pending}
          error={error}
          onRun={() => {
            void run()
          }}
        />
      }
    />
  )
}

function BrowserSteps({
  orgId,
  project,
  profile,
  agent,
  onProfileCreated,
  onAgentCreated,
}: {
  orgId: string
  project: VisibleProject
  profile?: AgentProfile
  agent?: Agent
  onProfileCreated: (profile: AgentProfile) => void
  onAgentCreated: (agent: Agent) => void
}) {
  const [createProfile = 'upcoming', chat = 'upcoming'] = stepStatuses([
    profile != null,
    agent != null,
  ])
  return (
    <OnboardingSteps>
      <OnboardingStep
        index={1}
        title="Create an agent profile"
        doneTitle="Created an agent profile"
        description="Pick a template, optionally customize it, then create the profile"
        status={createProfile}
        nextStatus={chat}
        completion={profile && <ProfileCreated project={project} profile={profile} />}
      >
        {!profile && (
          <ProfileDraftStep orgId={orgId} project={project} onCreated={onProfileCreated} />
        )}
      </OnboardingStep>
      <OnboardingStep
        index={2}
        title="Create a chat"
        doneTitle="Created a chat"
        description="Start a chat with an agent running your profile"
        status={chat}
        completion={agent && <ChatGuide project={project} agent={agent} />}
      >
        {agent ? null : profile ? (
          <CreateChat
            orgId={orgId}
            project={project}
            profile={profile}
            onCreated={onAgentCreated}
          />
        ) : null}
        {agent && profile && <ChatOptions orgId={orgId} project={project} profile={profile} />}
      </OnboardingStep>
    </OnboardingSteps>
  )
}

function ProfileCreated({ project, profile }: { project: VisibleProject; profile: AgentProfile }) {
  return (
    <>
      <Link
        to="/projects/$projectId/agent-profiles/$profileId"
        params={{ projectId: project.id, profileId: profile.id }}
        className="underline-offset-2 hover:underline"
      >
        {profile.name}
      </Link>
    </>
  )
}
